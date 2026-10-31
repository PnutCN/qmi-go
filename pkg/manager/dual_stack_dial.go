package manager

import (
	"errors"
	"fmt"
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
