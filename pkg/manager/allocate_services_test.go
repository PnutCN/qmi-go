package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

func TestAllocateServicesUsesCallerContextForClientIDAllocation(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{EnableIPv4: true}
	m.client = &qmi.Client{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	wdsCalls := 0
	nasCalls := 0
	m.newWDSService = func(ctx context.Context, _ *qmi.Client) (*qmi.WDSService, error) {
		wdsCalls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("WDS allocation context has no deadline")
		}
		return nil, context.DeadlineExceeded
	}
	m.newNASService = func(context.Context, *qmi.Client) (*qmi.NASService, error) {
		nasCalls++
		return &qmi.NASService{}, nil
	}

	err := m.allocateServices(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("allocateServices() err=%v, want context.DeadlineExceeded", err)
	}
	if wdsCalls != 1 {
		t.Fatalf("WDS allocations=%d want 1", wdsCalls)
	}
	if nasCalls != 0 {
		t.Fatalf("NAS allocations=%d want 0 after WDS context cancellation", nasCalls)
	}
}

func TestAllocateServicesSkipsWMSAndWDAWhenDisabledButKeepsVOICE(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: ""},
		EnableIPv4:      false,
		EnableIPv6:      false,
		DisableWMSInd:   true,
		DisableVOICEInd: true,
	}
	m.client = &qmi.Client{}

	m.newNASService = func(context.Context, *qmi.Client) (*qmi.NASService, error) { return &qmi.NASService{}, nil }
	m.newDMSService = func(context.Context, *qmi.Client) (*qmi.DMSService, error) { return &qmi.DMSService{}, nil }
	m.newUIMService = func(context.Context, *qmi.Client) (*qmi.UIMService, error) { return &qmi.UIMService{}, nil }
	m.registerNASIndications = func(context.Context, qmi.NASIndicationRegistration) error { return nil }
	m.registerUIMIndications = func(context.Context) (uint32, error) { return 0, nil }

	wdaCalls := 0
	wmsCalls := 0
	voiceCalls := 0
	m.newWDAService = func(context.Context, *qmi.Client) (*qmi.WDAService, error) {
		wdaCalls++
		return &qmi.WDAService{}, nil
	}
	m.newWMSService = func(context.Context, *qmi.Client) (*qmi.WMSService, error) {
		wmsCalls++
		return &qmi.WMSService{}, nil
	}
	m.newVOICEService = func(context.Context, *qmi.Client) (*qmi.VOICEService, error) {
		voiceCalls++
		return &qmi.VOICEService{}, nil
	}

	if err := m.allocateServices(context.Background()); err != nil {
		t.Fatalf("allocateServices() unexpected error: %v", err)
	}
	if wdaCalls != 0 {
		t.Fatalf("WDA allocations=%d want 0 without data interface/family", wdaCalls)
	}
	if wmsCalls != 0 {
		t.Fatalf("WMS allocations=%d want 0 when WMS indications are disabled", wmsCalls)
	}
	if voiceCalls != 1 {
		t.Fatalf("VOICE allocations=%d want 1", voiceCalls)
	}
}

func TestAllocateServicesLazyDataPlaneSkipsWDSAndWDAButKeepsVOICE(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwan0"},
		EnableIPv4:      true,
		EnableIPv6:      false,
		DisableWMSInd:   true,
		DisableVOICEInd: true,
		DataPlanePolicy: DataPlanePolicyLazy,
	}
	m.client = &qmi.Client{}

	wdsCalls := 0
	wdaCalls := 0
	voiceCalls := 0
	m.newWDSService = func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
		wdsCalls++
		return &qmi.WDSService{}, nil
	}
	m.newWDAService = func(context.Context, *qmi.Client) (*qmi.WDAService, error) {
		wdaCalls++
		return &qmi.WDAService{}, nil
	}
	m.newNASService = func(context.Context, *qmi.Client) (*qmi.NASService, error) { return &qmi.NASService{}, nil }
	m.newDMSService = func(context.Context, *qmi.Client) (*qmi.DMSService, error) { return &qmi.DMSService{}, nil }
	m.newUIMService = func(context.Context, *qmi.Client) (*qmi.UIMService, error) { return &qmi.UIMService{}, nil }
	m.registerNASIndications = func(context.Context, qmi.NASIndicationRegistration) error { return nil }
	m.registerUIMIndications = func(context.Context) (uint32, error) { return 0, nil }
	m.newVOICEService = func(context.Context, *qmi.Client) (*qmi.VOICEService, error) {
		voiceCalls++
		return &qmi.VOICEService{}, nil
	}

	if err := m.allocateServices(context.Background()); err != nil {
		t.Fatalf("allocateServices() error = %v", err)
	}
	if wdsCalls != 0 || wdaCalls != 0 {
		t.Fatalf("data-plane allocations WDS=%d WDA=%d want 0/0", wdsCalls, wdaCalls)
	}
	if voiceCalls != 1 {
		t.Fatalf("VOICE allocations=%d want 1", voiceCalls)
	}
}

func TestAllocateServicesReturnsErrorWhenCoreServiceAllocationFails(t *testing.T) {
	tests := []struct {
		name string
		hook func(*Manager, error)
		want string
	}{
		{
			name: "NAS",
			hook: func(m *Manager, err error) {
				m.newNASService = func(context.Context, *qmi.Client) (*qmi.NASService, error) { return nil, err }
			},
			want: "failed to allocate NAS client",
		},
		{
			name: "DMS",
			hook: func(m *Manager, err error) {
				m.newDMSService = func(context.Context, *qmi.Client) (*qmi.DMSService, error) { return nil, err }
			},
			want: "failed to allocate DMS client",
		},
		{
			name: "UIM",
			hook: func(m *Manager, err error) {
				m.newUIMService = func(context.Context, *qmi.Client) (*qmi.UIMService, error) { return nil, err }
			},
			want: "failed to allocate UIM client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newRecoveryTestManager()
			m.cfg = Config{DisableWMSInd: true, DisableVOICEInd: true}
			m.client = &qmi.Client{}
			m.newNASService = func(context.Context, *qmi.Client) (*qmi.NASService, error) {
				return &qmi.NASService{}, nil
			}
			m.newDMSService = func(context.Context, *qmi.Client) (*qmi.DMSService, error) {
				return &qmi.DMSService{}, nil
			}
			m.newUIMService = func(context.Context, *qmi.Client) (*qmi.UIMService, error) {
				return &qmi.UIMService{}, nil
			}
			m.newVOICEService = func(context.Context, *qmi.Client) (*qmi.VOICEService, error) {
				return &qmi.VOICEService{}, nil
			}
			m.registerNASIndications = func(context.Context, qmi.NASIndicationRegistration) error {
				return nil
			}
			m.registerUIMIndications = func(context.Context) (uint32, error) {
				return 0, nil
			}
			coreErr := qmi.ErrServiceNotSupported
			tt.hook(m, coreErr)
			err := m.allocateServices(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("allocateServices() error=%v, want %q", err, tt.want)
			}
			if !errors.Is(err, qmi.ErrServiceNotSupported) {
				t.Fatalf("allocateServices() error=%v, want to wrap ErrServiceNotSupported", err)
			}
		})
	}
}

func TestAllocateServicesContinuesWhenAuxiliaryServiceAllocationFails(t *testing.T) {
	m := newRecoveryTestManager()
	m.client = &qmi.Client{}
	m.newNASService = func(context.Context, *qmi.Client) (*qmi.NASService, error) {
		return &qmi.NASService{}, nil
	}
	m.newDMSService = func(context.Context, *qmi.Client) (*qmi.DMSService, error) {
		return &qmi.DMSService{}, nil
	}
	m.newUIMService = func(context.Context, *qmi.Client) (*qmi.UIMService, error) {
		return &qmi.UIMService{}, nil
	}
	m.registerNASIndications = func(context.Context, qmi.NASIndicationRegistration) error {
		return nil
	}
	m.registerUIMIndications = func(context.Context) (uint32, error) {
		return 0, nil
	}
	m.newWMSService = func(context.Context, *qmi.Client) (*qmi.WMSService, error) {
		return nil, fmt.Errorf("WMS unavailable")
	}
	m.newVOICEService = func(context.Context, *qmi.Client) (*qmi.VOICEService, error) {
		return nil, fmt.Errorf("VOICE unavailable")
	}

	if err := m.allocateServices(context.Background()); err != nil {
		t.Fatalf("allocateServices() error=%v, want nil for auxiliary failures", err)
	}
}

// TestAllocateServicesAllocatesIMSAAndIMSWhenSupported 锁住这次行为反转:
// 以前 allocateServices 结尾无条件 `m.ims = nil; m.imsa = nil`,IMSA 的
// 注册状态查询因此恒返回 ErrServiceNotReady。现在按能力检查分配。
func TestAllocateServicesAllocatesIMSAAndIMSWhenSupported(t *testing.T) {
	m := newRecoveryTestManager()
	m.client = &qmi.Client{}
	m.newNASService = func(context.Context, *qmi.Client) (*qmi.NASService, error) { return &qmi.NASService{}, nil }
	m.newDMSService = func(context.Context, *qmi.Client) (*qmi.DMSService, error) { return &qmi.DMSService{}, nil }
	m.newUIMService = func(context.Context, *qmi.Client) (*qmi.UIMService, error) { return &qmi.UIMService{}, nil }
	m.registerNASIndications = func(context.Context, qmi.NASIndicationRegistration) error { return nil }
	m.registerUIMIndications = func(context.Context) (uint32, error) { return 0, nil }
	// 关掉 WMS:分配后紧跟 recoverWMSStateWithContext,会拿零值 client 发真请求。
	m.cfg = Config{DisableWMSInd: true}
	m.newVOICEService = func(context.Context, *qmi.Client) (*qmi.VOICEService, error) { return &qmi.VOICEService{}, nil }
	m.registerVOICEIndications = func(context.Context, qmi.VoiceIndicationRegistration) error { return nil }

	imsaCalls := 0
	imsCalls := 0
	indCfg := qmi.IMSAIndicationRegistration{}
	m.newIMSAService = func(context.Context, *qmi.Client) (*qmi.IMSAService, error) {
		imsaCalls++
		return &qmi.IMSAService{}, nil
	}
	m.newIMSService = func(context.Context, *qmi.Client) (*qmi.IMSService, error) {
		imsCalls++
		return &qmi.IMSService{}, nil
	}
	m.registerIMSAIndications = func(_ context.Context, cfg qmi.IMSAIndicationRegistration) error {
		indCfg = cfg
		return nil
	}

	if err := m.allocateServices(context.Background()); err != nil {
		t.Fatalf("allocateServices() error = %v", err)
	}
	if imsaCalls != 1 || imsCalls != 1 {
		t.Fatalf("IMSA allocations=%d IMS allocations=%d want 1/1", imsaCalls, imsCalls)
	}
	if m.imsa == nil || m.ims == nil {
		t.Fatalf("imsa=%v ims=%v want both non-nil", m.imsa, m.ims)
	}
	if !indCfg.RegistrationStatusChanged || !indCfg.ServicesStatusChanged {
		t.Fatalf("IMSA indication cfg = %+v, want both change flags set", indCfg)
	}
}

// TestAllocateServicesKeepsIMSAClientWhenIndicationRegistrationFails 说明指示注册
// 失败不能连累客户端:主动 GetIMSRegistrationStatus 仍然可用,退化成轮询好过
// 完全没有 IMS 状态。
func TestAllocateServicesKeepsIMSAClientWhenIndicationRegistrationFails(t *testing.T) {
	m := newRecoveryTestManager()
	m.client = &qmi.Client{}
	m.newNASService = func(context.Context, *qmi.Client) (*qmi.NASService, error) { return &qmi.NASService{}, nil }
	m.newDMSService = func(context.Context, *qmi.Client) (*qmi.DMSService, error) { return &qmi.DMSService{}, nil }
	m.newUIMService = func(context.Context, *qmi.Client) (*qmi.UIMService, error) { return &qmi.UIMService{}, nil }
	m.registerNASIndications = func(context.Context, qmi.NASIndicationRegistration) error { return nil }
	m.registerUIMIndications = func(context.Context) (uint32, error) { return 0, nil }
	m.newVOICEService = func(context.Context, *qmi.Client) (*qmi.VOICEService, error) { return &qmi.VOICEService{}, nil }
	m.cfg = Config{DisableWMSInd: true, DisableVOICEInd: true}
	m.registerIMSAIndications = func(context.Context, qmi.IMSAIndicationRegistration) error {
		return fmt.Errorf("indication register refused")
	}

	if err := m.allocateServices(context.Background()); err != nil {
		t.Fatalf("allocateServices() error=%v, want nil", err)
	}
	if m.imsa == nil {
		t.Fatal("imsa = nil, want client kept despite indication registration failure")
	}
}

// TestAllocateServicesContinuesWhenIMSAAllocationFails 保证 IMSA 分配失败是
// 非致命的 —— 不支持 IMS 的模组(实测 EC20 上的 cdc-wdm0/3/5)不应拖垮启动。
func TestAllocateServicesContinuesWhenIMSAAllocationFails(t *testing.T) {
	m := newRecoveryTestManager()
	m.client = &qmi.Client{}
	m.cfg = Config{DisableWMSInd: true, DisableVOICEInd: true}
	m.newNASService = func(context.Context, *qmi.Client) (*qmi.NASService, error) { return &qmi.NASService{}, nil }
	m.newDMSService = func(context.Context, *qmi.Client) (*qmi.DMSService, error) { return &qmi.DMSService{}, nil }
	m.newUIMService = func(context.Context, *qmi.Client) (*qmi.UIMService, error) { return &qmi.UIMService{}, nil }
	m.registerNASIndications = func(context.Context, qmi.NASIndicationRegistration) error { return nil }
	m.registerUIMIndications = func(context.Context) (uint32, error) { return 0, nil }
	m.newVOICEService = func(context.Context, *qmi.Client) (*qmi.VOICEService, error) { return &qmi.VOICEService{}, nil }
	m.newIMSAService = func(context.Context, *qmi.Client) (*qmi.IMSAService, error) {
		return nil, fmt.Errorf("QMI protocol error (31): 'InvalidServiceType'")
	}
	m.newIMSService = func(context.Context, *qmi.Client) (*qmi.IMSService, error) {
		return nil, fmt.Errorf("QMI protocol error (31): 'InvalidServiceType'")
	}

	if err := m.allocateServices(context.Background()); err != nil {
		t.Fatalf("allocateServices() error=%v, want nil for IMSA/IMS failures", err)
	}
	if m.imsa != nil || m.ims != nil {
		t.Fatalf("imsa=%v ims=%v want both nil after allocation failure", m.imsa, m.ims)
	}
}

func TestEnsureDataPlaneServicesAllocatesLazyServices(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwan0"},
		EnableIPv4:      true,
		DataPlanePolicy: DataPlanePolicyLazy,
	}
	m.client = &qmi.Client{}

	wdsCalls := 0
	wdaCalls := 0
	rawIPCalls := 0
	m.newWDSService = func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
		wdsCalls++
		return &qmi.WDSService{}, nil
	}
	m.newWDAService = func(context.Context, *qmi.Client) (*qmi.WDAService, error) {
		wdaCalls++
		return &qmi.WDAService{}, nil
	}
	m.enableRawIPHook = func(context.Context) error {
		rawIPCalls++
		return nil
	}

	if err := m.ensureDataPlaneServices(context.Background()); err != nil {
		t.Fatalf("ensureDataPlaneServices() error = %v", err)
	}
	if wdsCalls != 1 || wdaCalls != 1 {
		t.Fatalf("data-plane allocations WDS=%d WDA=%d want 1/1", wdsCalls, wdaCalls)
	}
	if rawIPCalls != 1 {
		t.Fatalf("RawIP calls=%d want 1", rawIPCalls)
	}
}
