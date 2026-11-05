package manager

import (
	"context"
	"errors"
	"github.com/iniwex5/qmi-go/pkg/qmi"
	"time"
)

var ErrDataCallActive = errors.New("默认数据承载仍在运行，不能热替换配置")

// ConnectDefaultDataBearer只在无活动承载时整组提交，保留独立API以供旧适配器检测能力。
// 参数只包含本次建链所需值，不触及SIM身份或其他IMS PDN。
func (m *Manager) ConnectDefaultDataBearer(ctx context.Context, apn, username, password string, auth uint8, v4, v6 bool, mtu4, mtu6 int, stillCurrent func() bool, verifyCurrent func(context.Context) error) error {
	if m == nil {
		return errors.New("默认数据核心未就绪")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if auth > 3 || !v4 && !v6 || mtu4 < 0 || mtu4 > 65535 || mtu6 < 0 || mtu6 > 65535 {
		return ErrInvalidDataConfig
	}
	m.dataConfigMu.Lock()
	defer m.dataConfigMu.Unlock()
	m.dataOpMu.Lock()
	defer m.dataOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if !m.coreReady || m.state == StateStopping {
		m.mu.Unlock()
		return errors.New("默认数据核心未就绪")
	}
	if m.state == StateConnected || m.state == StateConnecting || m.handleV4 != 0 || m.handleV6 != 0 {
		m.mu.Unlock()
		return ErrDataCallActive
	}
	oldAPN, oldUser, oldPassword, oldAuth := m.cfg.APN, m.cfg.Username, m.cfg.Password, m.cfg.AuthType
	oldGuard := m.defaultDataGuard
	oldVerify := m.defaultDataVerify
	oldExplicit := m.defaultDataExplicitAuth
	old4, old6, oldMTU4, oldMTU6, oldDesired := m.cfg.EnableIPv4, m.cfg.EnableIPv6, m.cfg.DataMTUv4, m.cfg.DataMTUv6, m.desiredConnection
	m.cfg.APN, m.cfg.Username, m.cfg.Password, m.cfg.AuthType = apn, username, password, auth
	m.cfg.EnableIPv4, m.cfg.EnableIPv6, m.cfg.DataMTUv4, m.cfg.DataMTUv6 = v4, v6, mtu4, mtu6
	m.desiredConnection = true
	m.defaultDataGuard = stillCurrent
	m.defaultDataVerify = verifyCurrent
	m.defaultDataExplicitAuth = true
	m.mu.Unlock()
	var err error
	if m.defaultDataDial != nil {
		err = m.defaultDataDial(ctx)
	} else {
		err = m.doConnectLockedContext(ctx)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		return nil
	}
	// 失败创建的句柄必须清完，旧口令、地址族及MTU也必须一并回滚。
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = m.doDisconnectWithContextLocked(cleanup)
	cancel()
	m.mu.Lock()
	m.cfg.APN, m.cfg.Username, m.cfg.Password, m.cfg.AuthType = oldAPN, oldUser, oldPassword, oldAuth
	m.cfg.EnableIPv4, m.cfg.EnableIPv6, m.cfg.DataMTUv4, m.cfg.DataMTUv6 = old4, old6, oldMTU4, oldMTU6
	m.desiredConnection = oldDesired
	m.defaultDataGuard = oldGuard
	m.defaultDataVerify = oldVerify
	m.defaultDataExplicitAuth = oldExplicit
	m.mu.Unlock()
	return err
}

func (m *Manager) startDefaultDataCall(ctx context.Context, wds *qmi.WDSService, family uint8) (uint32, error) {
	if err := m.verifyDefaultDataCurrent(ctx); err != nil {
		return 0, err
	}
	var handle uint32
	var err error
	if m.defaultDataExplicitAuth {
		handle, err = wds.StartNetworkInterfaceWithAuthentication(ctx, m.cfg.APN, m.cfg.Username, m.cfg.Password, m.cfg.AuthType, family)
	} else {
		handle, err = wds.StartNetworkInterface(ctx, m.cfg.APN, m.cfg.Username, m.cfg.Password, m.cfg.AuthType, family)
	}
	if err == nil {
		if verifyErr := m.verifyDefaultDataCurrent(ctx); verifyErr != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = wds.StopNetworkInterface(cleanup, handle)
			cancel()
			return 0, verifyErr
		}
	}
	return handle, err
}

func (m *Manager) verifyDefaultDataCurrent(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	guard, verify := m.defaultDataGuard, m.defaultDataVerify
	m.mu.RUnlock()
	if guard != nil && !guard() {
		return context.Canceled
	}
	if verify != nil {
		if err := verify(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("默认数据实时卡身份复核失败")
		}
	}
	if guard != nil && !guard() {
		return context.Canceled
	}
	return nil
}

func (m *Manager) cleanupFailedDefaultDataCall(err error) {
	if err == nil || !m.defaultDataExplicitAuth || m.handleV4 == 0 && m.handleV6 == 0 {
		return
	}
	// 首次整组调用、后续自主重连和轮换均清同一承载，不能留下已失效租约的半成功句柄。
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = m.doDisconnectWithContextLocked(cleanup)
	cancel()
}

// defaultDataMTU不把IPv4配置MTU套给IPv6；同接口只能执行两族共同允许的上限。
func defaultDataMTU(network4, network6 int, fallback4, fallback6 int, v4, v6 bool) (int, error) {
	result := 0
	for _, item := range []struct {
		active   bool
		network  int
		fallback int
	}{{v4, network4, fallback4}, {v6, network6, fallback6}} {
		if !item.active {
			continue
		}
		value := item.fallback
		if item.network > 0 {
			if item.network > 65535 {
				return 0, errors.New("网络MTU越界")
			}
			value = int(item.network)
		}
		if value > 0 && (result == 0 || value < result) {
			result = value
		}
	}
	if v6 && result > 0 && result < 1280 {
		return 0, errors.New("数据MTU不满足IPv6最小值")
	}
	return result, nil
}
