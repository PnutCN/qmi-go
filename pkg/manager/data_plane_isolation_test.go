package manager

import (
	"testing"

	"github.com/iniwex5/qmi-go/pkg/netcfg"
	"github.com/iniwex5/qmi-go/pkg/qmi"
)

// newCoexistTestManager creates a manager with a published QMAP snapshot for
// the default-data and isolation tests.
func newCoexistTestManager(t *testing.T, defaultIface string, defaultMuxID uint8) *Manager {
	t.Helper()
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		DataPlane:       DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: defaultMuxID},
	}
	m.dataPlane.snapshot = DataPlaneSnapshot{
		Generation:       1,
		Mode:             DataPlaneModeQMAP,
		DefaultInterface: defaultIface,
		DefaultMuxID:     defaultMuxID,
	}
	m.dataPlane.masterInterface = "wwp0s20u1i4"
	return m
}

// TestDefaultDataPlaneTargetReadsSnapshotNotConfig pins the core invariant:
// consumers use the published topology rather than a construction-time value.
func TestDefaultDataPlaneTargetReadsSnapshotNotConfig(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	snapshot, master := m.defaultDataPlaneTarget()
	if snapshot.DefaultMuxID != 1 || snapshot.DefaultInterface != "qmimux0" {
		t.Fatalf("snapshot = %+v, want mux 1 on qmimux0", snapshot)
	}
	if master != "wwp0s20u1i4" {
		t.Fatalf("master = %q, want the physical master", master)
	}
}

// TestDefaultConnectionDiscoversEndpointInsteadOfHardcodingFour protects
// devices whose data endpoint interface number is not 4.
func TestDefaultConnectionDiscoversEndpointInsteadOfHardcodingFour(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	askedFor := ""
	m.pdnOps = pdnOps{
		discoverEndpoint: func(ifname string) (uint32, error) {
			askedFor = ifname
			return 8, nil
		},
	}

	binding, err := m.defaultMuxBinding()
	if err != nil {
		t.Fatalf("defaultMuxBinding() error = %v", err)
	}
	if binding.EpIfID != 8 {
		t.Fatalf("EpIfID = %d, want discovered 8 (not hardcoded 4)", binding.EpIfID)
	}
	if binding.MuxID != 1 {
		t.Fatalf("MuxID = %d, want 1 from the published snapshot", binding.MuxID)
	}
	if askedFor != "wwp0s20u1i4" {
		t.Fatalf("discoverEndpoint argument = %q, want the physical master", askedFor)
	}
}

// TestDefaultMuxBindingFailsWhenEndpointUndiscoverable ensures QMAP dialing
// cannot silently proceed without a valid data endpoint binding.
func TestDefaultMuxBindingFailsWhenEndpointUndiscoverable(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.pdnOps = pdnOps{
		discoverEndpoint: func(string) (uint32, error) { return 0, netcfg.ErrDataEndpointUnavailable },
	}
	if _, err := m.defaultMuxBinding(); err == nil {
		t.Fatal("expected an error when the data endpoint cannot be discovered")
	}
}

// TestDisconnectNeverBringsDownPhysicalMaster is the key QMAP isolation
// invariant: the physical master carries every mux and must remain up.
func TestDisconnectNeverBringsDownPhysicalMaster(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	var flushed, downed []string
	m.netcfgOps = netcfgOps{
		flushAddresses: func(name string) error { flushed = append(flushed, name); return nil },
		flushRoutes:    func(string) error { return nil },
		bringDown:      func(name string) error { downed = append(downed, name); return nil },
	}

	m.teardownDefaultDataInterface()

	for _, name := range downed {
		if name == "wwp0s20u1i4" {
			t.Fatalf("BringDown called on physical master %q", name)
		}
	}
	if len(downed) != 1 || downed[0] != "qmimux0" {
		t.Fatalf("downed = %v, want only qmimux0", downed)
	}
	if len(flushed) != 1 || flushed[0] != "qmimux0" {
		t.Fatalf("flushed = %v, want only qmimux0", flushed)
	}
}

// TestDisconnectUsesPhysicalInterfaceWhenNative preserves the valid Native
// behavior: there is no sibling mux, so the physical interface is the target.
func TestDisconnectUsesPhysicalInterfaceWhenNative(t *testing.T) {
	m := newCoexistTestManager(t, "qmimux0", 1)
	m.dataPlane.snapshot = DataPlaneSnapshot{
		Generation:       2,
		Mode:             DataPlaneModeNative,
		DefaultInterface: "wwp0s20u1i4",
	}
	var downed []string
	m.netcfgOps = netcfgOps{
		flushAddresses: func(string) error { return nil },
		flushRoutes:    func(string) error { return nil },
		bringDown:      func(name string) error { downed = append(downed, name); return nil },
	}

	m.teardownDefaultDataInterface()

	if len(downed) != 1 || downed[0] != "wwp0s20u1i4" {
		t.Fatalf("downed = %v, want the Native physical interface", downed)
	}
}

var _ = qmi.MuxBinding{}
