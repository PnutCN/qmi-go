package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

// IMS PDN 的一次性探测。
//
// # 与 OpenPDN 的分工
//
// OpenPDN 建的是一条**能承载 IP 流量**的次要 PDN：要 QMAP mux、要网卡、要把
// 接口拉起来。代价是它要求数据面处于 QMAP 模式，而切到 QMAP 会改变主数据面的
// 网卡布局 —— 对只想读一次 P-CSCF 的调用方来说太重了。
//
// 这里做的是另一件事：**只要设置，不要流量**。租一条自己的 WDS、发起数据呼叫、
// 读运行时设置（含网络下发的 P-CSCF）、立刻停掉。全程不碰 mux、不碰网卡、
// 不动共享数据面。
//
// 典型用途是回答「这张卡的运营商 IMS 核心网可达吗」—— 模组自己没启用 IMS 时
// （没匹配到运营商 MBN），这是唯一能问出答案的办法。
//
// # 为什么必须用 profile index 而不是 APN 字符串
//
// P-CSCF 由网络在 PDN 建立时经 PCO 下发，而「请不请求 PCO」是 WDS profile 里
// 的位，由运营商 MBN 配好。用 `apn=ims` 现拼参数建起来的 PDN **拿不到 P-CSCF**
// （2026-08-07 在 EC20 上实测确认），指向模组已有的 IMS profile 才拿得到。
// 所以 apnHint 只用于找 profile，找到之后发起请求时 APN 一律留空。

// imsProbeStopTimeout 是停止探测呼叫单独的超时。
//
// **必须与探测本身的 ctx 分开。** 共用的话，探测超时那一刻 ctx 已经死了，
// 停止动作会立刻失败 —— 于是最需要停的那种情况反而停不掉，留下一条挂着的
// 数据呼叫。
const imsProbeStopTimeout = 10 * time.Second

var (
	// ErrIMSProfileNotFound means the modem has no profile that can be selected
	// as the IMS profile for this probe.
	//
	// 这不是故障：没匹配到运营商 MBN 的卡（回退到 ROW_Generic_3GPP）本来就
	// 可能没有 IMS profile。调用方应据此报告「这张卡没有 IMS 配置」，
	// 而不是报错。
	ErrIMSProfileNotFound = errors.New("qmi manager: no IMS profile on modem")
	// ErrIMSProbeActivePDN keeps the first production version conservative until
	// the target modem proves that a temporary WDS call is harmless beside live
	// manager-owned data calls.
	ErrIMSProbeActivePDN = errors.New("qmi manager: IMS probe requires no active PDN")
)

// imsProbeOps isolates the modem call sequence from lifecycle and cleanup
// coordination. The hooks keep manager tests transport-free while the default
// implementation still uses the real WDS service methods.
type imsProbeOps struct {
	leaseWDS        func(context.Context, *qmi.Client) (*qmi.WDSService, error)
	discoverProfile func(context.Context, *qmi.WDSService, uint8, string) (uint8, bool, error)
	start           func(context.Context, *qmi.WDSService, string, uint8) (uint32, error)
	settings        func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error)
	stop            func(context.Context, *qmi.WDSService, uint32) error
	releaseWDS      func(*qmi.WDSService) error
}

func (m *Manager) resolvedIMSProbeOps() imsProbeOps {
	ops := m.imsProbeOps
	if ops.leaseWDS == nil {
		ops.leaseWDS = func(ctx context.Context, client *qmi.Client) (*qmi.WDSService, error) {
			if m.newWDSService != nil {
				return m.newWDSService(ctx, client)
			}
			return qmi.NewWDSServiceWithContext(ctx, client)
		}
	}
	if ops.discoverProfile == nil {
		ops.discoverProfile = func(ctx context.Context, wds *qmi.WDSService, profileType uint8, apnHint string) (uint8, bool, error) {
			return wds.DiscoverIMSProfileIndex(ctx, profileType, apnHint)
		}
	}
	if ops.start == nil {
		ops.start = func(ctx context.Context, wds *qmi.WDSService, apn string, ipFamily uint8) (uint32, error) {
			return wds.StartNetworkInterface(ctx, apn, "", "", 0, ipFamily)
		}
	}
	if ops.settings == nil {
		ops.settings = func(ctx context.Context, wds *qmi.WDSService, ipFamily uint8) (*qmi.RuntimeSettings, error) {
			return wds.GetRuntimeSettings(ctx, ipFamily)
		}
	}
	if ops.stop == nil {
		ops.stop = func(ctx context.Context, wds *qmi.WDSService, handle uint32) error {
			return wds.StopNetworkInterface(ctx, handle)
		}
	}
	if ops.releaseWDS == nil {
		ops.releaseWDS = m.closeTemporaryWDSService
	}
	return ops
}

// ProbeIMSPDNSettings starts a temporary modem-side IMS data call, reads its
// runtime settings, and tears the call down immediately. It deliberately does
// not create a QMAP mux or configure a Linux netdev, so it is distinct from
// OpenPDN. The modem still sees a real packet-data call; this method only avoids
// changing the host data-plane topology.
//
// apnHint is used only as the fallback selector for profile discovery. Once a
// profile is selected, the start request leaves APN empty so the modem keeps
// the profile's carrier-provisioned settings, including PCO behavior.
func (m *Manager) ProbeIMSPDNSettings(ctx context.Context, apnHint string, ipFamily uint8) (settings *qmi.RuntimeSettings, err error) {
	if m == nil {
		return nil, ErrServiceNotReady("qmi-core")
	}
	if ipFamily != qmi.IpFamilyV4 && ipFamily != qmi.IpFamilyV6 {
		return nil, fmt.Errorf("qmi manager: unsupported IMS probe IP family %d", ipFamily)
	}

	probeCtx, client, releaseScope, err := m.beginIMSProbe(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseScope()

	ops := m.resolvedIMSProbeOps()
	wds, err := ops.leaseWDS(probeCtx, client)
	if err != nil {
		return nil, fmt.Errorf("allocate WDS for IMS probe: %w", err)
	}
	if wds == nil {
		return nil, errors.New("allocate WDS for IMS probe: nil WDS service")
	}

	var (
		handle      uint32
		started     bool
		releaseCard func()
	)
	defer func() {
		var cleanupErrs []error
		if started {
			stopCtx, cancel := context.WithTimeout(context.Background(), imsProbeStopTimeout)
			cleanupErrs = append(cleanupErrs, ops.stop(stopCtx, wds, handle))
			cancel()
		}
		if releaseCard != nil {
			releaseCard()
		}
		cleanupErrs = append(cleanupErrs, ops.releaseWDS(wds))
		allErrs := append([]error{err}, cleanupErrs...)
		err = errors.Join(allErrs...)
	}()

	profileIndex, found, err := ops.discoverProfile(probeCtx, wds, qmi.WDSProfileType3GPP, apnHint)
	if err != nil {
		return nil, fmt.Errorf("discover IMS profile: %w", err)
	}
	if !found {
		return nil, ErrIMSProfileNotFound
	}

	// DMS/UIM card operations are gated while an IMS PDN is being brought up.
	// Profile discovery itself is a WDS profile query and does not need the
	// quiet window, so keep the window as short as the actual call lifecycle.
	releaseCard, err = m.BeginCardIOQuietWindow(probeCtx)
	if err != nil {
		return nil, fmt.Errorf("acquire IMS probe card quiet window: %w", err)
	}

	wds.ProfileIndex = profileIndex
	handle, err = ops.start(probeCtx, wds, "", ipFamily)
	if err != nil {
		return nil, fmt.Errorf("start IMS PDN (profile %d): %w", profileIndex, err)
	}
	started = true

	settings, err = ops.settings(probeCtx, wds, ipFamily)
	if err != nil {
		return nil, fmt.Errorf("read IMS PDN settings: %w", err)
	}
	if settings == nil {
		return nil, errors.New("read IMS PDN settings: empty result")
	}
	if m.log != nil {
		m.log.WithField("profile_index", profileIndex).
			WithField("pcscf_v4_count", len(settings.PCSCFv4)).
			WithField("pcscf_v6_count", len(settings.PCSCFv6)).
			WithField("pcscf_domain_count", len(settings.PCSCFDomains)).
			WithField("imcn", settings.IMCN).
			Info("IMS PDN probe completed")
	}
	return settings, nil
}

func (m *Manager) beginIMSProbe(ctx context.Context) (context.Context, *qmi.Client, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	if m.imsProbeBlocked {
		m.mu.Unlock()
		return nil, nil, nil, ErrServiceNotReady("qmi-core")
	}
	if m.state == StateStopping {
		m.mu.Unlock()
		return nil, nil, nil, ErrManagerStopping
	}
	if !m.coreReady || m.client == nil {
		m.mu.Unlock()
		return nil, nil, nil, ErrServiceNotReady("qmi-core")
	}
	if m.imsProbeSlot == nil {
		m.imsProbeSlot = make(chan struct{}, 1)
		m.imsProbeSlot <- struct{}{}
	}
	slot := m.imsProbeSlot
	lifecycle := m.ctx
	m.mu.Unlock()

	baseCtx, cancelBase := context.WithCancel(ctx)
	stopLifecycle := func() {}
	if lifecycle != nil {
		stop := context.AfterFunc(lifecycle, cancelBase)
		stopLifecycle = func() { stop() }
	}
	probeTimeout := m.cfg.Timeouts.Dial
	if probeTimeout <= 0 {
		probeTimeout = defaultTimeouts.Dial
	}
	probeCtx, cancelTimeout := contextWithMaxTimeout(baseCtx, probeTimeout)
	cancelAll := func() {
		stopLifecycle()
		cancelTimeout()
		cancelBase()
	}

	select {
	case <-probeCtx.Done():
		cancelAll()
		return nil, nil, nil, probeCtx.Err()
	case <-slot:
	}

	// Follow the same dataPlane -> manager lock order as OpenPDN and cleanup.
	// This makes the active-PDN policy a coherent snapshot rather than reading
	// the two registries independently.
	m.dataPlane.mu.Lock()
	m.mu.Lock()
	client := m.client
	activePDN := m.handleV4 != 0 || m.handleV6 != 0 || len(m.dataPlane.sessions) != 0
	blocked := m.imsProbeBlocked
	stopping := m.state == StateStopping
	ready := m.coreReady && client != nil
	if blocked || stopping || !ready || activePDN {
		m.mu.Unlock()
		m.dataPlane.mu.Unlock()
		slot <- struct{}{}
		cancelAll()
		if stopping {
			return nil, nil, nil, ErrManagerStopping
		}
		if activePDN {
			return nil, nil, nil, ErrIMSProbeActivePDN
		}
		return nil, nil, nil, ErrServiceNotReady("qmi-core")
	}
	m.imsProbeWG.Add(1)
	m.imsProbeCancel = cancelAll
	m.mu.Unlock()
	m.dataPlane.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			m.imsProbeCancel = nil
			m.mu.Unlock()
			cancelAll()
			m.imsProbeWG.Done()
			slot <- struct{}{}
		})
	}
	return probeCtx, client, release, nil
}

// blockIMSProbes prevents queued probes from starting and cancels the active
// one before cleanup clears the manager's shared QMI services.
func (m *Manager) blockIMSProbes() {
	m.mu.Lock()
	m.imsProbeBlocked = true
	cancel := m.imsProbeCancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.imsProbeWG.Wait()
}
