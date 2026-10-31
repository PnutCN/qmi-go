package manager

import (
	"errors"
	"testing"
)

// 双栈拨号：IPv4 被拒而 IPv6 可用时要继续拨 IPv6（2026-10-31）。
//
// T-Mobile US（漫游时 home-routed）只给 IPv6：IPv4 呼叫回 QMI call end type=6 code=51
// （3GPP ESM #51 PDN type IPv6 only allowed）。从前 IPv4 一失败就整体返回错误、IPv6 根本不拨，
// 而 IPv6 失败却只告警、继续用 IPv4 —— 不对称，纯 IPv6 网络上 v4v6 永远连不上（vopive 5489 连拨 23 次）。

var errV4Rejected = errors.New("start network failed: call end type=6 code=51")
var errV6Rejected = errors.New("start network failed: v6")

func dialer(handle uint32, err error, calls *int) func() (uint32, error) {
	return func() (uint32, error) {
		*calls++
		return handle, err
	}
}

func TestDialDualStackFallsBackToIPv6WhenIPv4Rejected(t *testing.T) {
	var c4, c6 int
	r := dialDualStack(true, true, dialer(0, errV4Rejected, &c4), dialer(0x66, nil, &c6))

	if r.err != nil {
		t.Fatalf("IPv6 拨通就不该整体失败：%v", r.err)
	}
	if c6 != 1 || r.handleV6 != 0x66 || r.handleV4 != 0 {
		t.Fatalf("应继续拨 IPv6：c6=%d r=%+v", c6, r)
	}
	if !errors.Is(r.v4Err, errV4Rejected) {
		t.Fatalf("IPv4 的失败原因要留着给日志：%v", r.v4Err)
	}
}

func TestDialDualStackFailsWhenBothRejected(t *testing.T) {
	var c4, c6 int
	r := dialDualStack(true, true, dialer(0, errV4Rejected, &c4), dialer(0, errV6Rejected, &c6))

	if r.err == nil || !errors.Is(r.err, errV4Rejected) || !errors.Is(r.err, errV6Rejected) {
		t.Fatalf("两个都失败才算失败，且两个原因都要带上：%v", r.err)
	}
}

func TestDialDualStackIPv4OnlyFailureIsFatal(t *testing.T) {
	var c4, c6 int
	r := dialDualStack(true, false, dialer(0, errV4Rejected, &c4), dialer(0x66, nil, &c6))

	if r.err == nil || c6 != 0 {
		t.Fatalf("只开 IPv4 时 IPv4 失败就是失败，且不能去拨 IPv6：err=%v c6=%d", r.err, c6)
	}
}

func TestDialDualStackKeepsIPv4WhenIPv6Fails(t *testing.T) {
	// 原有行为不变：IPv4 通、IPv6 失败 → 只用 IPv4。
	var c4, c6 int
	r := dialDualStack(true, true, dialer(0x44, nil, &c4), dialer(0, errV6Rejected, &c6))

	if r.err != nil || r.handleV4 != 0x44 || r.handleV6 != 0 || !errors.Is(r.v6Err, errV6Rejected) {
		t.Fatalf("r=%+v", r)
	}
}

func TestDialDualStackIPv6Only(t *testing.T) {
	var c4, c6 int
	r := dialDualStack(false, true, dialer(0x44, nil, &c4), dialer(0x66, nil, &c6))

	if r.err != nil || c4 != 0 || r.handleV6 != 0x66 {
		t.Fatalf("只开 IPv6 时不拨 IPv4：c4=%d r=%+v", c4, r)
	}
}

func TestDialDualStackNothingEnabled(t *testing.T) {
	var c4, c6 int
	r := dialDualStack(false, false, dialer(0x44, nil, &c4), dialer(0x66, nil, &c6))

	if r.err == nil || c4+c6 != 0 {
		t.Fatalf("什么都没开时应报错而不是假装连上：c4=%d c6=%d err=%v", c4, c6, r.err)
	}
}
