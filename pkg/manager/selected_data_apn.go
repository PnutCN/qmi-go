package manager

import (
	"context"
	"fmt"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

// ReadSelectedDataAPN 只认当前默认数据连接；IMS、临时彩信 PDN 不在候选中。
// 不能把配置的 APN 当成实测值：空配置可由网络选择，非空配置也可能尚未拨通。
func (m *Manager) ReadSelectedDataAPN(ctx context.Context) (string, error) {
	if m == nil {
		return "", fmt.Errorf("selected APN: manager unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !m.dataOpMu.TryLock() {
		return "", fmt.Errorf("selected APN: default data operation in progress")
	}
	defer m.dataOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.RLock()
	state, v4, v6, h4, h6 := m.state, m.wds, m.wdsV6, m.handleV4, m.handleV6
	m.mu.RUnlock()
	if state != StateConnected {
		return "", fmt.Errorf("selected APN: no connected default data call")
	}
	var selected *qmi.WDSService
	if h4 != 0 && v4 != nil {
		selected = v4
	} else if h6 != 0 && v6 != nil {
		selected = v6
	}
	if selected == nil {
		return "", fmt.Errorf("selected APN: no owned default data handle")
	}
	status, err := selected.GetPacketServiceStatus(ctx)
	if err != nil || status != qmi.StatusConnected {
		return "", fmt.Errorf("selected APN: default data call inactive")
	}
	apn, err := selected.ReadActiveAPN(ctx)
	if err != nil {
		return "", err
	}
	status, err = selected.GetPacketServiceStatus(ctx)
	if err != nil || status != qmi.StatusConnected {
		return "", fmt.Errorf("selected APN: default data call changed during read")
	}
	m.mu.RLock()
	stable := m.state == StateConnected && m.wds == v4 && m.wdsV6 == v6 && m.handleV4 == h4 && m.handleV6 == h6
	m.mu.RUnlock()
	if !stable {
		return "", fmt.Errorf("selected APN: default data identity changed during read")
	}
	return apn, nil
}
