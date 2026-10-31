package manager

import (
	"errors"
	"fmt"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

// dualStackDialResult 是一次双栈拨号的结果。
type dualStackDialResult struct {
	handleV4, handleV6 uint32
	// v4Err / v6Err 是各自的失败原因（没拨或拨通时为 nil），给日志用。
	v4Err, v6Err error
	// err 非 nil 表示这次连接失败：开了的族一个都没拨通。
	err error
}

// dialDualStack 按配置拨 IPv4 / IPv6：开了的族**任一拨通就算连上**，全部失败才算失败。
//
// 从前 IPv4 一失败就整体返回、IPv6 根本不拨，而 IPv6 失败只告警、继续用 IPv4 —— 不对称。
// 2026-10-31 实测：T-Mobile US（漫游 home-routed）只给 IPv6，IPv4 呼叫回 call end type=6 code=51
// （3GPP ESM #51 PDN type IPv6 only allowed），v4v6 配置下连拨 23 次一次也没拨 IPv6。
//
// 先拨 IPv4 的顺序不变（双栈网络上与从前完全一样）；dialV6 为 nil 表示没有 IPv6 的 WDS 客户端。
func dialDualStack(enableV4, enableV6 bool, dialV4, dialV6 func() (uint32, error)) dualStackDialResult {
	var r dualStackDialResult
	if !enableV4 && !enableV6 {
		r.err = errors.New("IPv4 与 IPv6 都没有启用，没有可拨的数据呼叫")
		return r
	}
	if enableV4 {
		r.handleV4, r.v4Err = dialV4()
	}
	canV6 := enableV6 && dialV6 != nil
	if canV6 {
		r.handleV6, r.v6Err = dialV6()
	}
	switch {
	case enableV4 && r.v4Err == nil, canV6 && r.v6Err == nil:
		return r
	case enableV4 && canV6:
		r.err = fmt.Errorf("IPv4 与 IPv6 数据呼叫都失败：%w", errors.Join(r.v4Err, r.v6Err))
	case enableV4:
		r.err = r.v4Err
	default:
		r.err = r.v6Err
		if r.err == nil {
			r.err = errors.New("没有可用的 IPv6 WDS 客户端")
		}
	}
	return r
}

// ipFamily 是健康检查要查哪个 WDS 客户端的分组业务状态。
type ipFamily int

const (
	familyNone ipFamily = iota
	familyV4
	familyV6
)

// packetStatusFamily 选健康检查查哪个族：**实际连着的那个**。
//
// 从前只查 IPv4 客户端。v4v6 配置下 IPv4 被拒、只连上 IPv6（dialDualStack 的回退）时，IPv4
// 客户端上没有会话、永远报 disconnected，健康检查就判「Connection lost!」→ 重拨 → IPv6 回 NoEffect
// （会话其实还在）→ 无限循环（2026-10-31 vopive 5489）。IPv4 连着或都没连时与从前相同，查 IPv4。
func packetStatusFamily(handleV4, handleV6 uint32, hasV4, hasV6 bool) ipFamily {
	switch {
	case handleV4 != 0 && hasV4:
		return familyV4
	case handleV6 != 0 && hasV6:
		return familyV6
	case hasV4:
		return familyV4
	case hasV6:
		return familyV6
	default:
		return familyNone
	}
}

// dialRecoveringNoEffect 拨一次；回 NoEffect（这个族在模组上已经有会话 —— 断开通知迟到、或上一轮没收尾）
// 时先 stop 再拨一次。别的错误原样返回。
func dialRecoveringNoEffect(dial func() (uint32, error), stop func() error) (uint32, error) {
	h, err := dial()
	var qmiErr *qmi.QMIError
	if err == nil || !errors.As(err, &qmiErr) || qmiErr.ErrorCode != qmi.QMIErrNoEffect {
		return h, err
	}
	if stopErr := stop(); stopErr != nil {
		return 0, fmt.Errorf("%w（停掉残留会话也失败：%v）", err, stopErr)
	}
	return dial()
}
