//go:build linux
// +build linux

package netcfg

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vishvananda/netlink"
)

// LinuxConfigurator implements NetworkConfigurator for Linux using netlink
// LinuxConfigurator 使用 netlink 实现 Linux 的 NetworkConfigurator
type LinuxConfigurator struct{}

var qmapMuxCreateMu sync.Mutex

var (
	netlinkLinkByName = netlink.LinkByName
	netlinkAddrList   = netlink.AddrList
	netlinkAddrDel    = netlink.AddrDel
	netlinkRouteList  = netlink.RouteList
	netlinkRouteDel   = netlink.RouteDel
)

// sysClassNetRoot is the base of the sysfs netdev tree. A package variable
// (not a hardcoded literal) so tests can point it at a fake tree instead of
// the real kernel.
var sysClassNetRoot = "/sys/class/net"

func NewLinuxConfigurator() *LinuxConfigurator {
	return &LinuxConfigurator{}
}

func (l *LinuxConfigurator) SetIPAddress(ifname string, ip net.IP, prefixLen int) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}

	ipNet := &net.IPNet{
		IP:   ip,
		Mask: net.CIDRMask(prefixLen, 32),
	}

	addr := &netlink.Addr{IPNet: ipNet}

	if err := netlink.AddrAdd(link, addr); err != nil {
		if !strings.Contains(err.Error(), "exists") {
			return fmt.Errorf("failed to add address: %w", err)
		}
	}
	return nil
}

func (l *LinuxConfigurator) SetIPv6Address(ifname string, ip net.IP, prefixLen int) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}

	ipNet := &net.IPNet{
		IP:   ip,
		Mask: net.CIDRMask(prefixLen, 128),
	}

	addr := &netlink.Addr{IPNet: ipNet}

	if err := netlink.AddrAdd(link, addr); err != nil {
		if !strings.Contains(err.Error(), "exists") {
			return fmt.Errorf("failed to add IPv6 address: %w", err)
		}
	}
	return nil
}

func (l *LinuxConfigurator) FlushAddresses(ifname string) error {
	link, err := netlinkLinkByName(ifname)
	if err != nil {
		return fmt.Errorf("netcfg: lookup interface %s for address flush: %w", ifname, err)
	}

	addrs, err := netlinkAddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("netcfg: list addresses on %s: %w", ifname, err)
	}

	var result error
	for _, addr := range addrs {
		addr := addr
		if err := netlinkAddrDel(link, &addr); err != nil {
			result = errors.Join(result, fmt.Errorf("netcfg: delete address from %s: %w", ifname, err))
		}
	}
	return result
}

func (l *LinuxConfigurator) AddDefaultRoute(ifname string, gateway net.IP) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}

	var dst *net.IPNet
	if gateway.To4() != nil {
		dst = &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	} else {
		dst = &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	}

	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst,
		Gw:        gateway,
		Priority:  5000, // High metric to avoid overriding system default route / 高跃点数避免覆盖系统默认路由
	}

	if err := netlink.RouteAdd(route); err != nil {
		if !strings.Contains(err.Error(), "exists") {
			return fmt.Errorf("failed to add default route: %w", err)
		}
	}
	return nil
}

func (l *LinuxConfigurator) AddDefaultRouteDirect(ifname string, ipv6 bool) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}

	var dst *net.IPNet
	if ipv6 {
		dst = &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	} else {
		dst = &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	}

	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst,
		Priority:  512, // High metric / 高跃点数
	}

	if err := netlink.RouteAdd(route); err != nil {
		if !strings.Contains(err.Error(), "exists") {
			return fmt.Errorf("failed to add default route: %w", err)
		}
	}
	return nil
}

func (l *LinuxConfigurator) FlushRoutes(ifname string) error {
	link, err := netlinkLinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}

	routes, err := netlinkRouteList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("failed to list routes: %w", err)
	}

	var result error
	for _, route := range routes {
		route := route
		if err := netlinkRouteDel(&route); err != nil {
			result = errors.Join(result, fmt.Errorf("netcfg: delete route from %s: %w", ifname, err))
		}
	}
	return result
}

func (l *LinuxConfigurator) BringUp(ifname string) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}
	return netlink.LinkSetUp(link)
}

func (l *LinuxConfigurator) BringDown(ifname string) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return nil // Interface gone
	}
	return netlink.LinkSetDown(link)
}

func (l *LinuxConfigurator) SetMTU(ifname string, mtu int) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}
	return netlink.LinkSetMTU(link, mtu)
}

func (l *LinuxConfigurator) GetCurrentIP(ifname string) (net.IP, error) {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", ifname, err)
	}

	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("failed to list addresses: %w", err)
	}

	if len(addrs) == 0 {
		return nil, nil
	}
	return addrs[0].IP, nil
}

func (l *LinuxConfigurator) IsUp(ifname string) (bool, error) {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return false, fmt.Errorf("interface %s not found: %w", ifname, err)
	}
	return link.Attrs().Flags&net.FlagUp != 0, nil
}

const resolvConfPath = "/etc/resolv.conf"

func (l *LinuxConfigurator) UpdateDNS(dns1, dns2 string) error {
	var lines []string
	if data, err := os.ReadFile(resolvConfPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				lines = append(lines, line)
			}
		}
	}

	if dns1 != "" {
		lines = append(lines, "nameserver "+dns1)
	}
	if dns2 != "" && dns2 != dns1 {
		lines = append(lines, "nameserver "+dns2)
	}

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(resolvConfPath, []byte(content), 0644)
}

func (l *LinuxConfigurator) RestoreDNS() error {
	var lines []string
	if data, err := os.ReadFile(resolvConfPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				lines = append(lines, line)
			}
		}
	}
	content := strings.Join(lines, "\n")
	return os.WriteFile(resolvConfPath, []byte(content), 0644)
}

// ============================================================================
// QMAP 多路复用 sysfs 操作 (Linux 专属)
// ============================================================================

// AddQMAPMux 向内核 qmi_wwan 驱动申请创建一个 MUX ID 对应的虚拟网卡
// 等同于: echo {muxID} > /sys/class/net/{masterIface}/qmi/add_mux
// 返回值为创建出的虚拟网卡名，例如 "qmimux0"
func (l *LinuxConfigurator) AddQMAPMux(masterIface string, muxID uint8) (string, error) {
	// qmi_wwan allocates qmimux names globally and by creation order, not by
	// mux ID. Serialize creation in this process so the before/after snapshot
	// below cannot mistake another worker's newly created mux for this one.
	qmapMuxCreateMu.Lock()
	defer qmapMuxCreateMu.Unlock()

	addMuxPath := filepath.Join(sysClassNetRoot, masterIface, "qmi/add_mux")

	// 检查 sysfs 节点是否存在
	if _, err := os.Stat(addMuxPath); os.IsNotExist(err) {
		return "", fmt.Errorf("sysfs 节点 %s 不存在，内核驱动可能不支持 QMAP", addMuxPath)
	}

	before, err := qmapMuxInterfaces()
	if err != nil {
		return "", fmt.Errorf("读取现有 QMAP 网卡失败: %w", err)
	}

	// 写入 MuxID 触发内核创建虚拟网卡。
	data := fmt.Sprintf("%d\n", muxID)
	if err := os.WriteFile(addMuxPath, []byte(data), 0200); err != nil {
		// 如果已经存在，可能返回 "device or resource busy" 之类的错误
		// 尝试检查对应的虚拟网卡是否已存在
		ifname := l.GetQMAPMuxIface(masterIface, muxID)
		if ifname != "" {
			return ifname, nil // 虚拟网卡已存在，直接使用
		}
		return "", fmt.Errorf("写入 %s 失败: %w", addMuxPath, err)
	}

	if ifname, err := waitForNewQMAPMuxInterface(before, 2*time.Second); err == nil {
		return ifname, nil
	}

	// Some drivers expose a per-interface mux_id attribute. Keep that path as
	// a fallback, but never use muxID-1 as the primary identity: on kernels
	// without the attribute, mux 2 can legitimately be named qmimux0.
	ifname := l.GetQMAPMuxIface(masterIface, muxID)
	if ifname == "" {
		return "", fmt.Errorf("MuxID %d 的虚拟网卡创建后未找到", muxID)
	}

	return ifname, nil
}

func qmapMuxInterfaces() (map[string]struct{}, error) {
	entries, err := os.ReadDir(sysClassNetRoot)
	if err != nil {
		return nil, err
	}
	interfaces := make(map[string]struct{})
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "qmimux") {
			interfaces[entry.Name()] = struct{}{}
		}
	}
	return interfaces, nil
}

func waitForNewQMAPMuxInterface(before map[string]struct{}, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		after, err := qmapMuxInterfaces()
		if err != nil {
			return "", err
		}
		if ifname, ok := newQMAPMuxInterface(before, after); ok {
			return ifname, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("等待新 QMAP 网卡超时")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func newQMAPMuxInterface(before, after map[string]struct{}) (string, bool) {
	added := make([]string, 0, 1)
	for ifname := range after {
		if _, exists := before[ifname]; !exists {
			added = append(added, ifname)
		}
	}
	if len(added) != 1 {
		return "", false
	}
	sort.Strings(added)
	return added[0], true
}

// DelQMAPMux 销毁指定 MuxID 对应的虚拟网卡
// 等同于: echo {muxID} > /sys/class/net/{masterIface}/qmi/del_mux
func (l *LinuxConfigurator) DelQMAPMux(masterIface string, muxID uint8) error {
	delMuxPath := filepath.Join(sysClassNetRoot, masterIface, "qmi/del_mux")

	if _, err := os.Stat(delMuxPath); os.IsNotExist(err) {
		return nil // 节点不存在就认为无需清理
	}

	data := fmt.Sprintf("%d\n", muxID)
	if err := os.WriteFile(delMuxPath, []byte(data), 0200); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", delMuxPath, err)
	}
	return nil
}

// GetQMAPMuxIface 根据 MuxID 推导虚拟网卡接口名
// qmi_wwan 驱动的命名规则: qmimux{muxID - 1}，即 MuxID=1 对应 qmimux0
// GetQMAPMuxIface 根据 MuxID 反查其虚拟网卡名。qmimux 编号按内核创建顺序全局
// 递增，与 MuxID 值无关——"MuxID=1 对应 qmimux0" 这类假设已在实测中被证伪
// (删除 mux_id=2 后新建的 mux_id=3 复用了刚释放的 qmimux0)，因此这里只信
// /sys/class/net/qmimuxN/qmap/mux_id 这一个真实来源，没有回退猜测；找不到就
// 如实返回空串，调用方据此判断"确实不存在"而不是被一个猜错的名字误导。
func (l *LinuxConfigurator) GetQMAPMuxIface(masterIface string, muxID uint8) string {
	return getQMAPMuxIfaceAt(sysClassNetRoot, masterIface, muxID)
}

// getQMAPMuxIfaceAt is GetQMAPMuxIface's testable core: root is injectable so
// tests can point it at a fake sysfs tree instead of the real kernel.
//
// masterIface scopes the search via muxBelongsToMaster (the same "lower_*"
// signal ReconcileResidualMux and DiscoverQMAPTopology use). Without it, a
// second physical QMI device on the host sharing the same mux_id -- a real
// case, since mux 1 is the default data connection on every device -- could
// make this return the wrong device's netdev name.
func getQMAPMuxIfaceAt(root, masterIface string, muxID uint8) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "qmimux") {
			continue
		}
		if !muxBelongsToMaster(root, name, masterIface) {
			continue
		}
		if id, ok := readQMAPMuxID(root, name); ok && id == muxID {
			return name
		}
	}

	return ""
}

// ReconcileResidualMux deletes every QMAP mux under masterIface whose mux_id
// is not in keepMuxIDs. Call this once, early in device bootstrap, before
// any add_mux for this session — a mux left allocated by a crashed previous
// process otherwise makes the next add_mux for the same mux_id fail with
// EINVAL ("mux_id already present"), and there is no way to distinguish that
// from a mux this session itself still needs without enumerating by mux_id
// (never by netdev name — qmimux numbering is global creation order,
// unrelated to mux_id; see GetQMAPMuxIface's doc comment). Returns the
// mux_ids it deleted.
func (l *LinuxConfigurator) ReconcileResidualMux(masterIface string, keepMuxIDs []uint8) ([]uint8, error) {
	keep := make(map[uint8]bool, len(keepMuxIDs))
	for _, id := range keepMuxIDs {
		keep[id] = true
	}

	entries, err := os.ReadDir(sysClassNetRoot)
	if err != nil {
		return nil, fmt.Errorf("netcfg: 扫描 %s 失败: %w", sysClassNetRoot, err)
	}

	var deleted []uint8
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "qmimux") {
			continue
		}
		if !muxBelongsToMaster(sysClassNetRoot, name, masterIface) {
			continue // 属于别的主设备
		}
		muxIDPath := filepath.Join(sysClassNetRoot, name, "qmap/mux_id")
		data, err := os.ReadFile(muxIDPath)
		if err != nil {
			continue
		}
		id, err := parseMuxIDAttr(strings.TrimSpace(string(data)))
		if err != nil || keep[id] {
			continue
		}
		if err := l.DelQMAPMux(masterIface, id); err != nil {
			return deleted, fmt.Errorf("netcfg: 清理残留 mux_id=%d 失败: %w", id, err)
		}
		deleted = append(deleted, id)
	}
	return deleted, nil
}

// parseMuxIDAttr parses qmap/mux_id, which the kernel reports as either
// "0xN" or a plain decimal string depending on driver version (see
// GetQMAPMuxIface's own dual-format comparison a few lines above).
func parseMuxIDAttr(s string) (uint8, error) {
	base := 10
	if strings.HasPrefix(s, "0x") {
		s = s[2:]
		base = 16
	}
	v, err := strconv.ParseUint(s, base, 8)
	if err != nil {
		return 0, err
	}
	return uint8(v), nil
}

// EnableRawIP 在物理网卡上开启 Raw IP 模式（QMAP 前置条件）
// 等同于: echo Y > /sys/class/net/{ifname}/qmi/raw_ip
func (l *LinuxConfigurator) EnableRawIP(ifname string) error {
	rawIPPath := filepath.Join(sysClassNetRoot, ifname, "qmi/raw_ip")

	if _, err := os.Stat(rawIPPath); os.IsNotExist(err) {
		return nil // 内核驱动不支持，跳过
	}

	// 检查是否已经开启
	if content, err := os.ReadFile(rawIPPath); err == nil {
		s := strings.TrimSpace(string(content))
		if s == "Y" || s == "y" || s == "1" {
			return nil // 已开启
		}
	}

	// 开启前需要先关闭网卡
	_ = l.BringDown(ifname)

	if err := os.WriteFile(rawIPPath, []byte("Y\n"), 0644); err != nil {
		return fmt.Errorf("开启 raw_ip 失败: %w", err)
	}

	return nil
}
