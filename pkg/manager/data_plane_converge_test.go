package manager

import (
	"context"
	"sync"
	"testing"

	"github.com/iniwex5/qmi-go/pkg/netcfg"
	"github.com/iniwex5/qmi-go/pkg/qmi"
)

func TestConvergeDataPlanePublishesAfterMasterMutation(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		MuxID:           1,
	}
	// Keep this test at topology ownership: no real QMI client allocation is
	// needed when a WDA service has already been established.
	m.wda = &qmi.WDAService{}
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux: func(master string, muxID uint8) (string, error) {
			if master != "wwp0s20u1i4" || muxID != 1 {
				t.Fatalf("unexpected topology input: master=%q mux=%d", master, muxID)
			}
			return "qmimux0", nil
		},
	}

	got, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
		Mode:         DataPlaneModeQMAP,
		DefaultMuxID: 1,
	})
	if err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if got.DefaultInterface != "qmimux0" {
		t.Fatalf("DefaultInterface = %q, want returned kernel mux interface", got.DefaultInterface)
	}
	if m.dataPlane.masterInterface != "wwp0s20u1i4" {
		t.Fatalf("internal master = %q, want unchanged physical name", m.dataPlane.masterInterface)
	}
	if got.Generation == 0 {
		t.Fatal("Generation = 0, want published generation")
	}
}

func TestConvergeDataPlaneCoalescesConcurrentSameSpec(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		MuxID:           1,
	}
	m.wda = &qmi.WDAService{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var mu sync.Mutex
	mutations := 0
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux: func(string, uint8) (string, error) {
			mu.Lock()
			mutations++
			mu.Unlock()
			enteredOnce.Do(func() { close(entered) })
			<-release
			return "qmimux0", nil
		},
	}

	type result struct {
		snapshot DataPlaneSnapshot
		err      error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			<-start
			snapshot, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
				Mode: DataPlaneModeQMAP, DefaultMuxID: 1,
			})
			results <- result{snapshot: snapshot, err: err}
		}()
	}
	close(start)
	<-entered
	close(release)

	var generation uint64
	for range 8 {
		got := <-results
		if got.err != nil {
			t.Fatalf("ConvergeDataPlane() error = %v", got.err)
		}
		if generation == 0 {
			generation = got.snapshot.Generation
		} else if got.snapshot.Generation != generation {
			t.Fatalf("generation = %d, want %d for every caller", got.snapshot.Generation, generation)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if mutations != 1 {
		t.Fatalf("topology mutations = %d, want 1", mutations)
	}
}

func TestConvergeDataPlaneAdoptsExistingRenamedTopology(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		MuxID:           1,
	}
	m.wda = &qmi.WDAService{}
	mutations := 0
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{
				MasterInterface: "wwp0s20u1i4_q",
				MuxInterfaces:   map[uint8]string{1: "wwp0s20u1i4"},
			}, nil
		},
		enableRawIP: func(string) error {
			mutations++
			return nil
		},
		addQMAPMux: func(string, uint8) (string, error) {
			mutations++
			return "", nil
		},
	}

	got, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{
		Mode: DataPlaneModeQMAP, DefaultMuxID: 1,
	})
	if err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if got.DefaultInterface != "wwp0s20u1i4" || m.dataPlane.masterInterface != "wwp0s20u1i4_q" {
		t.Fatalf("snapshot=%+v internal master=%q", got, m.dataPlane.masterInterface)
	}
	if mutations != 0 {
		t.Fatalf("topology mutations = %d, want no mutation after adoption", mutations)
	}
}

func TestEnsureDataPlaneTopologyReusesConvergedQMAP(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:          ModemDevice{NetInterface: "wwp0s20u1i4"},
		DataPlanePolicy: DataPlanePolicyLazy,
		MuxID:           1,
	}
	m.wda = &qmi.WDAService{}
	mutations := 0
	m.dataPlaneOps = dataPlaneOps{
		discoverQMAPTopology: func(string) (netcfg.QMAPTopology, error) {
			return netcfg.QMAPTopology{MasterInterface: "wwp0s20u1i4", MuxInterfaces: map[uint8]string{}}, nil
		},
		enableRawIP: func(string) error { return nil },
		addQMAPMux: func(string, uint8) (string, error) {
			mutations++
			return "qmimux0", nil
		},
	}

	if _, err := m.ConvergeDataPlane(context.Background(), DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1}); err != nil {
		t.Fatalf("ConvergeDataPlane() error = %v", err)
	}
	if err := m.EnsureDataPlaneTopology(context.Background()); err != nil {
		t.Fatalf("EnsureDataPlaneTopology() error = %v", err)
	}
	if mutations != 1 {
		t.Fatalf("topology mutations = %d, want one shared convergence", mutations)
	}
}
