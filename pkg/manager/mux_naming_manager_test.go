package manager

import "testing"

func fakeMuxNetcfgOps(t *testing.T) (muxNetcfgOps, *[]string, *[]string) {
	t.Helper()
	var renameCalls []string // "from->to" pairs
	var addMuxCalls []string // "masterIface:muxID" pairs
	ops := muxNetcfgOps{
		bringDown: func(ifname string) error { return nil },
		renameInterface: func(from, to string) error {
			renameCalls = append(renameCalls, from+"->"+to)
			return nil
		},
		addQMAPMux: func(masterIface string, muxID uint8) (string, error) {
			addMuxCalls = append(addMuxCalls, masterIface)
			// Mimic the kernel's actual behaviour: the new mux netdev never
			// happens to be named after the master.
			return "qmimux0", nil
		},
		deriveMuxNames: func(original string) (string, string, error) {
			return original + "_q", original, nil
		},
	}
	return ops, &renameCalls, &addMuxCalls
}

func TestPrepareMuxedInterfacesRenamesMasterAndKeepsDataOnOriginalName(t *testing.T) {
	ops, renameCalls, _ := fakeMuxNetcfgOps(t)

	result := prepareMuxedInterfaces(ops, NewNopLogger(), "wwp0s20u1i4", "wwp0s20u1i4", 1)

	if result.masterIface != "wwp0s20u1i4_q" {
		t.Fatalf("masterIface = %q, want the renamed short master name", result.masterIface)
	}
	if !result.masterRenamed {
		t.Fatal("masterRenamed = false, want true on first mux")
	}
	if result.dataIface != "wwp0s20u1i4" {
		t.Fatalf("dataIface = %q, want the original name preserved", result.dataIface)
	}
	if len(*renameCalls) != 2 {
		t.Fatalf("rename calls = %v, want exactly 2 (master away, mux back)", *renameCalls)
	}
	if (*renameCalls)[0] != "wwp0s20u1i4->wwp0s20u1i4_q" {
		t.Fatalf("first rename = %q, want master moving off its original name first", (*renameCalls)[0])
	}
	if (*renameCalls)[1] != "qmimux0->wwp0s20u1i4" {
		t.Fatalf("second rename = %q, want the kernel-assigned mux netdev taking the original name", (*renameCalls)[1])
	}
}

func TestPrepareMuxedInterfacesSkipsRenameOnSubsequentCalls(t *testing.T) {
	ops, renameCalls, addMuxCalls := fakeMuxNetcfgOps(t)

	// currentMaster already differs from originalMaster: this device was
	// muxed by an earlier call (or an earlier process lifetime) and the
	// master rename must not be attempted a second time.
	result := prepareMuxedInterfaces(ops, NewNopLogger(), "wwp0s20u1i4", "wwp0s20u1i4_q", 1)

	if result.masterRenamed {
		t.Fatal("masterRenamed = true, want false — the master was already renamed")
	}
	if result.masterIface != "wwp0s20u1i4_q" {
		t.Fatalf("masterIface = %q, want unchanged wwp0s20u1i4_q", result.masterIface)
	}
	// Only the mux-creation rename (kernel name -> original) should fire,
	// not the master rename.
	if len(*renameCalls) != 1 {
		t.Fatalf("rename calls = %v, want exactly 1 (mux back to original name only)", *renameCalls)
	}
	if (*addMuxCalls)[0] != "wwp0s20u1i4_q" {
		t.Fatalf("AddQMAPMux called with master=%q, want the already-renamed short name", (*addMuxCalls)[0])
	}
}

func TestPrepareMuxedInterfacesReportsNoDataIfaceWhenAddMuxFails(t *testing.T) {
	ops, _, _ := fakeMuxNetcfgOps(t)
	ops.addQMAPMux = func(masterIface string, muxID uint8) (string, error) {
		return "", errAddMuxFailedForTest
	}

	result := prepareMuxedInterfaces(ops, NewNopLogger(), "wwp0s20u1i4", "wwp0s20u1i4", 1)

	if result.dataIface != "" {
		t.Fatalf("dataIface = %q, want empty when AddQMAPMux fails", result.dataIface)
	}
	// The master rename must still have gone through — it is independent of
	// whether the mux itself could be created.
	if !result.masterRenamed || result.masterIface != "wwp0s20u1i4_q" {
		t.Fatalf("master rename outcome = %+v, want it to have proceeded despite the AddQMAPMux failure", result)
	}
}

type staticError string

func (e staticError) Error() string { return string(e) }

const errAddMuxFailedForTest = staticError("add_mux failed")

func TestCurrentMasterInterfaceFallsBackToConfiguredNameUntilMuxed(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{Device: ModemDevice{NetInterface: "wwan0"}}

	if got := m.CurrentMasterInterface(); got != "wwan0" {
		t.Fatalf("CurrentMasterInterface() = %q before muxing, want configured name wwan0", got)
	}

	m.mu.Lock()
	m.masterIface = "wwan0_q"
	m.mu.Unlock()

	if got := m.CurrentMasterInterface(); got != "wwan0_q" {
		t.Fatalf("CurrentMasterInterface() = %q after muxing, want the renamed name", got)
	}
}
