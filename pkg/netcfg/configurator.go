package netcfg

import (
	"net"
	"sync"
)

// NetworkConfigurator defines the interface for OS-specific network operations
// NetworkConfigurator 定义了特定于操作系统的网络操作接口
type NetworkConfigurator interface {
	// SetIPAddress configures an IPv4 address on the interface
	// SetIPAddress 在接口上配置 IPv4 地址
	SetIPAddress(ifname string, ip net.IP, prefixLen int) error

	// SetIPv6Address configures an IPv6 address on the interface
	// SetIPv6Address 在接口上配置 IPv6 地址
	SetIPv6Address(ifname string, ip net.IP, prefixLen int) error

	// FlushAddresses removes all IP addresses from the interface
	// FlushAddresses 移除接口上的所有 IP 地址
	FlushAddresses(ifname string) error

	// AddDefaultRoute adds a default route via the given gateway
	// AddDefaultRoute 添加通过给定网关的默认路由
	AddDefaultRoute(ifname string, gateway net.IP) error

	// AddDefaultRouteDirect adds a default route directly to the interface
	// AddDefaultRouteDirect 直接向接口添加默认路由
	AddDefaultRouteDirect(ifname string, ipv6 bool) error

	// FlushRoutes removes all routes for the interface
	// FlushRoutes 移除接口的所有路由
	FlushRoutes(ifname string) error

	// BringUp brings the interface up
	// BringUp 启动接口
	BringUp(ifname string) error

	// BringDown brings the interface down
	// BringDown 关闭接口
	BringDown(ifname string) error

	// SetMTU sets the MTU for the interface
	// SetMTU 设置接口的 MTU
	SetMTU(ifname string, mtu int) error

	// GetCurrentIP returns the current IPv4 address of the interface
	// GetCurrentIP 返回接口当前的 IPv4 地址
	GetCurrentIP(ifname string) (net.IP, error)

	// IsUp checks if the interface is up
	// IsUp 检查接口是否已启动
	IsUp(ifname string) (bool, error)

	// UpdateDNS updates the system DNS configuration
	// UpdateDNS 更新系统 DNS 配置
	UpdateDNS(dns1, dns2 string) error

	// RestoreDNS restores the system DNS configuration
	// RestoreDNS 恢复系统 DNS 配置
	RestoreDNS() error

	// AddQMAPMux 创建 QMAP 多路复用虚拟网卡，返回虚拟网卡名
	AddQMAPMux(masterIface string, muxID uint8) (string, error)

	// DelQMAPMux 销毁 QMAP 虚拟网卡
	DelQMAPMux(masterIface string, muxID uint8) error

	// GetQMAPMuxIface 根据 MuxID 查询虚拟网卡名
	GetQMAPMuxIface(masterIface string, muxID uint8) string

	// EnableRawIP 在网卡上开启 Raw IP 模式
	EnableRawIP(ifname string) error

	// ReconcileResidualMux 删除 masterIface 下不在 keepMuxIDs 中的所有 QMAP
	// mux（清理上次进程异常退出遗留的状态），返回被删除的 mux_id 列表。
	ReconcileResidualMux(masterIface string, keepMuxIDs []uint8) ([]uint8, error)
}

// 进程级单例。锁保护的是懒初始化那一步:GetConfigurator 在 nil 时会**写**
// currentConfigurator,而它同时被多条线并发调用 —— Manager.cleanup 就是把
// FlushAddresses / ReconcileResidualMux 等清理任务并发跑的(runCleanupTasks),
// 两个任务同时首次取配置器,一个读到 nil 的同时另一个正在写。-race 稳定复现。
//
// 不用 sync.Once:SetConfigurator 允许运行时替换(测试注入 mock),Once 之后
// 就再也换不回来了。用普通 Mutex 而非 RWMutex 是因为读路径里本身带着写,
// RWMutex 得做锁升级,而这个函数的调用频率(每次网络配置操作)完全不值当。
var (
	configuratorMu      sync.Mutex
	currentConfigurator NetworkConfigurator
)

// SetConfigurator sets the active network configurator
// SetConfigurator 设置活动的网络配置器
func SetConfigurator(c NetworkConfigurator) {
	configuratorMu.Lock()
	defer configuratorMu.Unlock()
	currentConfigurator = c
}

// GetConfigurator returns the active network configurator
// GetConfigurator 返回活动的网络配置器
func GetConfigurator() NetworkConfigurator {
	configuratorMu.Lock()
	defer configuratorMu.Unlock()
	if currentConfigurator == nil {
		// Auto-detect platform implementation / 自动检测平台实现
		// 在锁内构造是安全的:GetPlatformConfigurator 只是 NewXConfigurator(),
		// 不会绕回来再取一次配置器。
		currentConfigurator = GetPlatformConfigurator()
	}
	return currentConfigurator
}
