package manager

import (
	"context"
	"errors"
	"github.com/iniwex5/qmi-go/pkg/netcfg"
	"github.com/iniwex5/qmi-go/pkg/qmi"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestDefaultDataBearerRealWDSAuthenticationTLVs(t *testing.T) {
	for _, auth := range []uint8{0, 1, 2, 3} {
		t.Run(string(rune('0'+auth)), func(t *testing.T) {
			client := newUIMReadinessTestClient(t)
			seen := false
			serveUIMReadinessTestRequests(t, client, func(req *qmi.Packet) (*qmi.Packet, error) {
				if req.MessageID == qmi.WDSStartNetworkInterface {
					seen = true
					for typ, want := range map[uint8]string{0x14: "fixture.apn", 0x17: "fixture-user", 0x18: "fixture-password"} {
						tlv := qmi.FindTLV(req.TLVs, typ)
						if tlv == nil || string(tlv.Value) != want {
							t.Error("完整同源认证TLV丢失")
						}
					}
					tlv := qmi.FindTLV(req.TLVs, 0x16)
					if tlv == nil || len(tlv.Value) != 1 || tlv.Value[0] != auth {
						t.Error("认证位图/NONE未显式发出")
					}
					return &qmi.Packet{TLVs: []qmi.TLV{{Type: 2, Value: []byte{0, 0, 0, 0}}, {Type: 1, Value: []byte{42, 0, 0, 0}}}}, nil
				}
				return &qmi.Packet{TLVs: []qmi.TLV{{Type: 2, Value: []byte{0, 0, 0, 0}}}}, nil
			})
			wds := &qmi.WDSService{}
			setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("client"), reflect.ValueOf(client))
			setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("clientID"), reflect.ValueOf(uint8(1)))
			m := &Manager{coreReady: true, state: StateDisconnected, log: NewNopLogger()}
			m.defaultDataDial = func(ctx context.Context) error {
				_, err := m.startDefaultDataCall(ctx, wds, qmi.IpFamilyV4)
				return err
			}
			if err := m.ConnectDefaultDataBearer(context.Background(), "fixture.apn", "fixture-user", "fixture-password", auth, true, false, 1400, 0, func() bool { return true }, nil); err != nil || !seen {
				t.Fatal("真实WDS建链失败", err)
			}
		})
	}
}

func TestDefaultDataBearerAtomicRollbackCancellationAndNoHotChange(t *testing.T) {
	m := &Manager{coreReady: true, state: StateDisconnected, log: NewNopLogger(), cfg: Config{APN: "old", Username: "old-user", Password: "old-pass", AuthType: 2, EnableIPv4: true, DataMTUv4: 1400}}
	m.defaultDataDial = func(context.Context) error { return errors.New("fixture dial failed") }
	if err := m.ConnectDefaultDataBearer(context.Background(), "new", "new-user", "new-pass", 1, false, true, 0, 1280, nil, nil); err == nil {
		t.Fatal("拨号失败未回滚")
	}
	if m.cfg.APN != "old" || m.cfg.Password != "old-pass" || m.cfg.AuthType != 2 || !m.cfg.EnableIPv4 || m.cfg.EnableIPv6 || m.cfg.DataMTUv4 != 1400 {
		t.Fatal("认证/协议/MTU没有整组回滚")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.ConnectDefaultDataBearer(ctx, "new", "", "", 0, true, false, 0, 0, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("调用方取消未阻止修改")
	}
	m.state = StateConnected
	if err := m.ConnectDefaultDataBearer(context.Background(), "new", "", "", 0, true, false, 0, 0, nil, nil); !errors.Is(err, ErrDataCallActive) {
		t.Fatal("活动连接被热替换")
	}
}

func TestDefaultDataBearerGuardBlocksAutonomousRedial(t *testing.T) {
	m := &Manager{coreReady: true, state: StateDisconnected, log: NewNopLogger(), desiredConnection: true, defaultDataGuard: func() bool { return false }}
	if err := m.doConnectLocked(); !errors.Is(err, context.Canceled) {
		t.Fatal("换卡后自动重连绕过同卡闸", err)
	}
}

func TestDefaultDataBearerMTUResolution(t *testing.T) {
	for _, tc := range []struct {
		n4, n6, c4, c6 int
		v4, v6         bool
		want           int
	}{{1500, 0, 1400, 0, true, false, 1500}, {0, 1450, 0, 1280, false, true, 1450}, {1500, 0, 1400, 1280, true, true, 1280}, {0, 0, 1400, 0, true, false, 1400}} {
		got, err := defaultDataMTU(tc.n4, tc.n6, tc.c4, tc.c6, tc.v4, tc.v6)
		if err != nil || got != tc.want {
			t.Fatal("MTU网络优先/回落错误", got, err)
		}
	}
	if _, err := defaultDataMTU(576, 0, 0, 1280, true, true); err == nil {
		t.Fatal("双栈MTU冲突未报告")
	}
}

func TestDefaultDataBearerLiveVerificationRunsOutsideMutexBeforeAndAfterWDS(t *testing.T) {
	for _, failAt := range []int32{0, 1, 2} {
		t.Run(map[int32]string{0: "same-card", 1: "stale-cache-before-start", 2: "card-change-during-start"}[failAt], func(t *testing.T) {
			client := newUIMReadinessTestClient(t)
			var starts, stops atomic.Int32
			serveUIMReadinessTestRequests(t, client, func(req *qmi.Packet) (*qmi.Packet, error) {
				tlvs := []qmi.TLV{{Type: 2, Value: []byte{0, 0, 0, 0}}}
				if req.MessageID == qmi.WDSStartNetworkInterface {
					starts.Add(1)
					tlvs = append(tlvs, qmi.TLV{Type: 1, Value: []byte{42, 0, 0, 0}})
				}
				if req.MessageID == qmi.WDSStopNetworkInterface {
					stops.Add(1)
				}
				return &qmi.Packet{TLVs: tlvs}, nil
			})
			wds := &qmi.WDSService{}
			setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("client"), reflect.ValueOf(client))
			setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("clientID"), reflect.ValueOf(uint8(1)))
			m := &Manager{log: NewNopLogger(), defaultDataExplicitAuth: true, cfg: Config{APN: "fixture.apn"}}
			var checks atomic.Int32
			m.defaultDataGuard = func() bool { return true } // Worker缓存尚未更新。
			m.defaultDataVerify = func(context.Context) error {
				// 真实GetICCID也会取manager锁，持锁调用会在此永久等待。
				m.mu.Lock()
				m.mu.Unlock()
				if n := checks.Add(1); n == failAt {
					return errors.New("fixture private identity must not leak")
				}
				return nil
			}
			handle, err := m.startDefaultDataCall(context.Background(), wds, qmi.IpFamilyV4)
			if failAt == 0 {
				if err != nil || handle != 42 || checks.Load() != 2 || starts.Load() != 1 {
					t.Fatal("真实建链前后未核验", err)
				}
			} else if err == nil || handle != 0 || starts.Load() != failAt-1 || stops.Load() != failAt-1 || err.Error() != "默认数据实时卡身份复核失败" {
				t.Fatal("物理换卡仍发出旧认证或未清理已发句柄")
			}
		})
	}
}

func TestDefaultDataBearerIPv6RotationChecksLeaseAndExplicitNone(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired-lease", true: "explicit-none"}[current], func(t *testing.T) {
			client := newUIMReadinessTestClient(t)
			seen := false
			explicitNone := false
			serveUIMReadinessTestRequests(t, client, func(req *qmi.Packet) (*qmi.Packet, error) {
				if req.MessageID == qmi.WDSStartNetworkInterface {
					seen = true
					auth := qmi.FindTLV(req.TLVs, 0x16)
					explicitNone = auth != nil && len(auth.Value) == 1 && auth.Value[0] == 0
					return &qmi.Packet{TLVs: []qmi.TLV{{Type: 2, Value: []byte{0, 0, 0, 0}}, {Type: 1, Value: []byte{42, 0, 0, 0}}}}, nil
				}
				return &qmi.Packet{TLVs: []qmi.TLV{{Type: 2, Value: []byte{0, 0, 0, 0}}}}, nil
			})
			wds := &qmi.WDSService{}
			setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("client"), reflect.ValueOf(client))
			setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("clientID"), reflect.ValueOf(uint8(1)))
			m := &Manager{log: NewNopLogger(), wdsV6: wds, cfg: Config{APN: "fixture", Username: "fixture-user", Password: "fixture-password", AuthType: 0, EnableIPv6: true}, defaultDataExplicitAuth: true, defaultDataGuard: func() bool { return current }}
			m.redialIPv6(context.Background())
			if !current && (seen || m.handleV6 != 0) {
				t.Fatal("过期同卡租约仍向真实WDS发送旧凭据并建立IPv6承载")
			}
			if current && (!seen || !explicitNone) {
				t.Fatal("IPv6轮换绕过显式NONE入口")
			}
		})
	}
}

func TestDefaultDataBearerMTUApplicationFailureMustReturnError(t *testing.T) {
	original := netcfg.GetConfigurator()
	netcfg.SetConfigurator(&cleanupConfigurator{})
	t.Cleanup(func() { netcfg.SetConfigurator(original) })
	client := newUIMReadinessTestClient(t)
	serveUIMReadinessTestRequests(t, client, func(req *qmi.Packet) (*qmi.Packet, error) {
		tlvs := []qmi.TLV{{Type: 2, Value: []byte{0, 0, 0, 0}}}
		if req.MessageID == qmi.WDSGetRuntimeSettings {
			tlvs = append(tlvs, qmi.TLV{Type: qmi.TLVWDSIPv4Address, Value: []byte{5, 0, 0, 10}}, qmi.TLV{Type: qmi.TLVWDSMtu, Value: []byte{0xdc, 5, 0, 0}})
		}
		return &qmi.Packet{TLVs: tlvs}, nil
	})
	wds := &qmi.WDSService{}
	setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("client"), reflect.ValueOf(client))
	setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("clientID"), reflect.ValueOf(uint8(1)))
	m := newRecoveryTestManager()
	m.cfg = Config{Device: ModemDevice{NetInterface: "fixture0"}, NoRoute: true, NoDNS: true, EnableIPv4: true, DataMTUv4: 1400}
	m.wds = wds
	want := errors.New("fixture MTU rejected")
	m.netcfgOps.setMTU = func(string, int) error { return want }
	if err := m.configureNetwork(); err == nil {
		t.Fatal("MTU实际应用失败仍对上层报告整组成功，无法触发回滚")
	}
	m.coreReady, m.state = true, StateDisconnected
	m.cfg.APN, m.cfg.Username, m.cfg.Password, m.cfg.AuthType = "old", "old-user", "old-pass", 2
	m.defaultDataDial = func(context.Context) error { return m.configureNetwork() }
	if err := m.ConnectDefaultDataBearer(context.Background(), "new", "new-user", "new-pass", 1, true, false, 1400, 0, nil, nil); err == nil {
		t.Fatal("MTU应用失败仍提交完整记录")
	}
	if m.cfg.APN != "old" || m.cfg.Username != "old-user" || m.cfg.Password != "old-pass" || m.cfg.AuthType != 2 || m.cfg.DataMTUv4 != 1400 || !m.cfg.EnableIPv4 {
		t.Fatal("真实MTU失败没有整组回滚")
	}
}
