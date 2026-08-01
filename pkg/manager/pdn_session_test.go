package manager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

func TestOpenPDNRollsBackInReverseOrderWhenSettingsFail(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{
		Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1,
	}
	m.dataPlane.masterInterface = "wwan0"
	var events []string
	m.pdnOps = pdnOps{
		bringUpMaster: func(string) error { return nil },
		addMux: func(string, uint8) (string, error) {
			events = append(events, "add_mux")
			return "qmimux1", nil
		},
		deleteMux: func(string, uint8) error {
			events = append(events, "del_mux")
			return nil
		},
		leaseWDS: func(context.Context, *qmi.Client) (*qmi.WDSService, error) {
			events = append(events, "lease_wds")
			return &qmi.WDSService{}, nil
		},
		bind: func(context.Context, *qmi.WDSService, qmi.MuxBinding) error {
			events = append(events, "bind")
			return nil
		},
		start: func(context.Context, *qmi.WDSService, PDNRequest) (uint32, error) {
			events = append(events, "start")
			return 42, nil
		},
		settings: func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error) {
			events = append(events, "settings")
			return nil, errors.New("settings failed")
		},
		stop: func(context.Context, *qmi.WDSService, uint32) error {
			events = append(events, "stop")
			return nil
		},
		releaseWDS: func(*qmi.WDSService) error {
			events = append(events, "release_wds")
			return nil
		},
		bringUp: func(string) error { return nil },
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if err == nil {
		t.Fatal("expected settings failure")
	}
	want := []string{"add_mux", "lease_wds", "bind", "start", "settings", "stop", "release_wds", "del_mux"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestStalePDNSessionCloseCannotDeleteNewGenerationMux(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	deleted := false
	m.pdnOps = successfulPDNOps(func(_ string, muxID uint8) error {
		if muxID == 2 {
			deleted = true
		}
		return nil
	})

	old, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	m.dataPlane.mu.Lock()
	m.dataPlane.snapshot.Generation++
	m.dataPlane.mu.Unlock()

	if err := old.Close(context.Background()); !errors.Is(err, ErrStalePDNSession) {
		t.Fatalf("Close() error = %v, want ErrStalePDNSession", err)
	}
	if deleted {
		t.Fatal("stale session deleted its mux after a new generation was published")
	}
}

func TestPDNSessionCloseLeavesDefaultMuxAndSharedClientUntouched(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	var deleted []uint8
	m.pdnOps = successfulPDNOps(func(_ string, muxID uint8) error {
		deleted = append(deleted, muxID)
		return nil
	})

	session, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !reflect.DeepEqual(deleted, []uint8{2}) {
		t.Fatalf("deleted muxes = %v, want only IMS mux 2", deleted)
	}
}

func TestOpenPDNBringsPhysicalMasterUpBeforeCreatingMux(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	var events []string
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	m.pdnOps.bringUpMaster = func(iface string) error {
		events = append(events, "master:"+iface)
		return nil
	}
	m.pdnOps.addMux = func(string, uint8) (string, error) {
		events = append(events, "add_mux")
		return "qmimux1", nil
	}

	if _, err := m.OpenPDN(context.Background(), PDNRequest{APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV4}); err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if !reflect.DeepEqual(events, []string{"master:wwan0", "add_mux"}) {
		t.Fatalf("events = %v", events)
	}
}

func TestOpenPDNPassesProfileIndexToStart(t *testing.T) {
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	var got PDNRequest
	m.pdnOps.start = func(_ context.Context, _ *qmi.WDSService, req PDNRequest) (uint32, error) {
		got = req
		return 42, nil
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{
		APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV6, ProfileIndex: 7,
	})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if got.ProfileIndex != 7 {
		t.Fatalf("start request ProfileIndex = %d, want 7", got.ProfileIndex)
	}
}

func TestOpenPDNPassesCallTypeToStart(t *testing.T) {
	embedded := qmi.WDSCallTypeEmbedded
	m := newRecoveryTestManager()
	m.dataPlane.snapshot = DataPlaneSnapshot{Generation: 1, Mode: DataPlaneModeQMAP, DefaultInterface: "wwan0", DefaultMuxID: 1}
	m.dataPlane.masterInterface = "wwan0"
	m.pdnOps = successfulPDNOps(func(string, uint8) error { return nil })
	var got PDNRequest
	m.pdnOps.start = func(_ context.Context, _ *qmi.WDSService, req PDNRequest) (uint32, error) {
		got = req
		return 42, nil
	}

	_, err := m.OpenPDN(context.Background(), PDNRequest{
		APN: "ims", MuxID: 2, IPFamily: qmi.IpFamilyV6, CallType: &embedded,
	})
	if err != nil {
		t.Fatalf("OpenPDN() error = %v", err)
	}
	if got.CallType == nil || *got.CallType != qmi.WDSCallTypeEmbedded {
		t.Fatalf("start request CallType = %v, want embedded", got.CallType)
	}
}

func TestDefaultStartAppliesCallTypeToWDSService(t *testing.T) {
	embedded := qmi.WDSCallTypeEmbedded
	ops := defaultPDNOps()
	client := newUIMReadinessTestClient(t)
	serveUIMReadinessTestRequests(t, client, func(req *qmi.Packet) (*qmi.Packet, error) {
		switch req.MessageID {
		case qmi.WDSSetClientIPFamilyPref:
			return &qmi.Packet{TLVs: []qmi.TLV{{Type: 0x02, Value: []byte{0x00, 0x00, 0x00, 0x00}}}}, nil
		case qmi.WDSStartNetworkInterface:
			if tlv := qmi.FindTLV(req.TLVs, 0x31); tlv == nil || len(tlv.Value) != 1 || tlv.Value[0] != 2 {
				t.Fatalf("profile TLV = %v, want [2]", tlv)
			}
			if tlv := qmi.FindTLV(req.TLVs, 0x35); tlv == nil || len(tlv.Value) != 1 || tlv.Value[0] != qmi.WDSCallTypeEmbedded {
				t.Fatalf("call type TLV = %v, want [%d]", tlv, qmi.WDSCallTypeEmbedded)
			}
			return &qmi.Packet{TLVs: []qmi.TLV{
				{Type: 0x02, Value: []byte{0x00, 0x00, 0x00, 0x00}},
				{Type: 0x01, Value: []byte{0x2a, 0x00, 0x00, 0x00}},
			}}, nil
		default:
			t.Fatalf("unexpected QMI message 0x%04x", req.MessageID)
			return nil, nil
		}
	})

	wds := &qmi.WDSService{}
	setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("client"), reflect.ValueOf(client))
	setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("clientID"), reflect.ValueOf(uint8(1)))

	handle, err := ops.start(context.Background(), wds, PDNRequest{
		APN: "ims", IPFamily: qmi.IpFamilyV6, ProfileIndex: 2, CallType: &embedded,
	})
	if err != nil {
		t.Fatalf("default start error = %v", err)
	}
	if handle != 42 {
		t.Fatalf("default start handle = %d, want 42", handle)
	}
	if !wds.HasCallType || wds.CallType != qmi.WDSCallTypeEmbedded {
		t.Fatalf("service call type = (%d, %v), want (%d, true)", wds.CallType, wds.HasCallType, qmi.WDSCallTypeEmbedded)
	}
	if wds.ProfileIndex != 2 {
		t.Fatalf("service profile index = %d, want 2", wds.ProfileIndex)
	}
}

func successfulPDNOps(deleteMux func(string, uint8) error) pdnOps {
	return pdnOps{
		bringUpMaster: func(string) error { return nil },
		addMux:        func(string, uint8) (string, error) { return "qmimux1", nil },
		deleteMux:     deleteMux,
		leaseWDS:      func(context.Context, *qmi.Client) (*qmi.WDSService, error) { return &qmi.WDSService{}, nil },
		bind:          func(context.Context, *qmi.WDSService, qmi.MuxBinding) error { return nil },
		start:         func(context.Context, *qmi.WDSService, PDNRequest) (uint32, error) { return 42, nil },
		settings: func(context.Context, *qmi.WDSService, uint8) (*qmi.RuntimeSettings, error) {
			return &qmi.RuntimeSettings{}, nil
		},
		bringUp:    func(string) error { return nil },
		bringDown:  func(string) error { return nil },
		stop:       func(context.Context, *qmi.WDSService, uint32) error { return nil },
		releaseWDS: func(*qmi.WDSService) error { return nil },
	}
}
