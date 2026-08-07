package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

// IMS PDN 的一次性探测。
//
// # 与 OpenPDN 的分工
//
// OpenPDN 建的是一条**能承载 IP 流量**的次要 PDN：要 QMAP mux、要网卡、要
// 把接口拉起来。代价是它要求数据面处于 QMAP 模式，而切到 QMAP 会改变主数据面
// 的网卡布局 —— 对只想读一次 P-CSCF 的调用方来说太重了。
//
// 这里做的是另一件事：**只要设置，不要流量**。租一条自己的 WDS、发起数据呼叫、
// 读运行时设置（含网络下发的 P-CSCF）、立刻停掉。全程不碰 mux、不碰网卡、
// 不动共享数据面，因此对正在工作的数据连接零影响。
//
// 典型用途是回答"这张卡的运营商 IMS 核心网可达吗" —— 模组自己没启用 IMS 时
// （没匹配到运营商 MBN），这是唯一能问出答案的办法。
//
// # 为什么必须用 profile index 而不是 APN 字符串
//
// P-CSCF 由网络在 PDN 建立时经 PCO 下发，而"请不请求 PCO"是 WDS profile 里的
// 位，由运营商 MBN 配好。用 `apn=ims` 现拼参数建起来的 PDN 拿不到 P-CSCF
// （2026-08-07 在 EC20 上实测确认），指向模组已有的 IMS profile 才拿得到。

const (
	// imsProbeStopTimeout 是停止探测呼叫单独的超时。
	//
	// **必须与探测本身的 ctx 分开。** 共用的话，探测超时那一刻 ctx 已经死了，
	// 停止动作会立刻失败 —— 于是最需要停的那种情况反而停不掉，留下一条挂着的
	// 数据呼叫。
	imsProbeStopTimeout = 10 * time.Second
)

// ErrIMSProfileNotFound 表示模组里没有可用于 IMS 的 profile。
//
// 这不是故障：没匹配到运营商 MBN 的卡（回退到 ROW_Generic_3GPP）本来就可能
// 没有 IMS profile。调用方应据此报告"这张卡没有 IMS 配置"，而不是报错。
var ErrIMSProfileNotFound = errors.New("qmi manager: no IMS profile on modem")

// ProbeIMSPDNSettings 起一条临时的 IMS 数据呼叫，读回运行时设置后立刻停掉。
//
// apnHint 是找 IMS profile 时的 APN 兜底匹配值（首选判据是 profile 的 IMCN
// 标志，见 WDSService.DiscoverIMSProfileIndex）。ipFamily 传 4 或 6。
//
// 返回的 RuntimeSettings 里 PCSCFv4 / PCSCFv6 / PCSCFDomains 就是这次探测的
// 目标；PCSCFUsingPCO 说明网络是否真的通过 PCO 下发了发现信息。
func (m *Manager) ProbeIMSPDNSettings(ctx context.Context, apnHint string, ipFamily uint8) (*qmi.RuntimeSettings, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	if client == nil {
		return nil, errors.New("qmi manager: no QMI client for IMS probe")
	}

	// 租一条**自己的** WDS：共享的 m.wds 在 DataPlanePolicyLazy 下可能根本没分配,
	// 而探测不该因为"用户没开数据网"就做不了。
	wds, err := qmi.NewWDSServiceWithContext(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("allocate WDS for IMS probe: %w", err)
	}
	defer wds.Close()

	idx, found, err := wds.DiscoverIMSProfileIndex(ctx, qmi.WDSProfileType3GPP, apnHint)
	if err != nil {
		return nil, fmt.Errorf("discover IMS profile: %w", err)
	}
	if !found {
		return nil, ErrIMSProfileNotFound
	}

	wds.ProfileIndex = idx
	// APN 留空：profile 里已经有了。这里再传一遍反而可能覆盖掉 profile 里
	// 那些我们看不见但重要的位（PCO 请求就是其中之一）。
	handle, err := wds.StartNetworkInterface(ctx, "", "", "", 0, ipFamily)
	if err != nil {
		return nil, fmt.Errorf("start IMS PDN (profile %d): %w", idx, err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), imsProbeStopTimeout)
		defer cancel()
		if err := wds.StopNetworkInterface(stopCtx, handle); err != nil {
			m.log.WithError(err).Warn("Failed to stop IMS probe PDN")
		}
	}()

	settings, err := wds.GetRuntimeSettings(ctx, ipFamily)
	if err != nil {
		return nil, fmt.Errorf("read IMS PDN settings: %w", err)
	}
	return settings, nil
}
