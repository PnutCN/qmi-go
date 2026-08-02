package manager

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/iniwex5/qmi-go/pkg/netcfg"
	"github.com/iniwex5/qmi-go/pkg/qmi"
)

type cleanupConfigurator struct {
	reconciledMaster string
	reconciledKeep   []uint8
	events           *[]string
}

func (*cleanupConfigurator) SetIPAddress(string, net.IP, int) error   { return nil }
func (*cleanupConfigurator) SetIPv6Address(string, net.IP, int) error { return nil }
func (*cleanupConfigurator) FlushAddresses(string) error              { return nil }
func (*cleanupConfigurator) AddDefaultRoute(string, net.IP) error     { return nil }
func (*cleanupConfigurator) AddDefaultRouteDirect(string, bool) error { return nil }
func (*cleanupConfigurator) FlushRoutes(string) error                 { return nil }
func (*cleanupConfigurator) BringUp(string) error                     { return nil }
func (*cleanupConfigurator) BringDown(string) error                   { return nil }
func (*cleanupConfigurator) SetMTU(string, int) error                 { return nil }
func (*cleanupConfigurator) GetCurrentIP(string) (net.IP, error)      { return nil, nil }
func (*cleanupConfigurator) IsUp(string) (bool, error)                { return false, nil }
func (*cleanupConfigurator) UpdateDNS(string, string) error           { return nil }
func (*cleanupConfigurator) RestoreDNS() error                        { return nil }
func (*cleanupConfigurator) AddQMAPMux(string, uint8) (string, error) { return "", nil }
func (*cleanupConfigurator) DelQMAPMux(string, uint8) error           { return nil }
func (*cleanupConfigurator) GetQMAPMuxIface(string, uint8) string     { return "" }
func (*cleanupConfigurator) EnableRawIP(string) error                 { return nil }
func (c *cleanupConfigurator) ReconcileResidualMux(master string, keep []uint8) ([]uint8, error) {
	c.reconciledMaster = master
	c.reconciledKeep = append([]uint8(nil), keep...)
	if c.events != nil {
		*c.events = append(*c.events, "reconcile-residual-mux")
	}
	return nil, nil
}

func TestCleanupDoesNotUseFixedSleepWhenNoTasks(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.Timeouts.Stop = time.Second

	start := time.Now()
	m.cleanup()
	elapsed := time.Since(start)

	if elapsed >= 90*time.Millisecond {
		t.Fatalf("cleanup() elapsed = %s, want no fixed 100ms sleep", elapsed)
	}
}

func TestCleanupDeletesDefaultMuxFromResolvedQMAPMaster(t *testing.T) {
	original := netcfg.GetConfigurator()
	cfg := &cleanupConfigurator{}
	netcfg.SetConfigurator(cfg)
	t.Cleanup(func() { netcfg.SetConfigurator(original) })

	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:    ModemDevice{NetInterface: "wwan0"},
		DataPlane: DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1},
		Timeouts:  TimeoutConfig{Stop: time.Second},
	}
	m.dataPlane.snapshot = DataPlaneSnapshot{
		Generation:       1,
		Mode:             DataPlaneModeQMAP,
		DefaultInterface: "qmimux7",
		DefaultMuxID:     1,
	}
	m.muxIface = "qmimux7"
	m.masterIface = "wwan0_q_q"

	m.cleanup()

	if cfg.reconciledMaster != "wwan0_q_q" {
		t.Fatalf("ReconcileResidualMux master = %q, want resolved QMAP master", cfg.reconciledMaster)
	}
	if cfg.reconciledKeep != nil {
		t.Fatalf("ReconcileResidualMux keep = %v, want nil to remove every QMAP mux", cfg.reconciledKeep)
	}
}

func TestCleanupReconcilesAllResidualMuxesAfterSecondaryPDNs(t *testing.T) {
	original := netcfg.GetConfigurator()
	events := make([]string, 0, 5)
	cfg := &cleanupConfigurator{events: &events}
	netcfg.SetConfigurator(cfg)
	t.Cleanup(func() { netcfg.SetConfigurator(original) })

	m := newRecoveryTestManager()
	m.cfg = Config{
		Device:    ModemDevice{NetInterface: "wwan0"},
		DataPlane: DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: 1},
		Timeouts:  TimeoutConfig{Stop: time.Second},
	}
	m.dataPlane.snapshot = DataPlaneSnapshot{
		Generation:       1,
		Mode:             DataPlaneModeQMAP,
		DefaultInterface: "qmimux7",
		DefaultMuxID:     1,
	}
	m.dataPlane.sessions = map[uint64]*managedPDNSession{
		1: {
			manager:  m,
			snapshot: PDNSnapshot{ID: 1, Generation: 1, InterfaceName: "qmimux8", Handle: 1},
			master:   "wwan0_q_q",
			muxID:    2,
		},
	}
	m.pdnOps = pdnOps{
		bringDown: func(string) error { return nil },
		stop: func(context.Context, *qmi.WDSService, uint32) error {
			events = append(events, "secondary-pdn-stop")
			return nil
		},
		releaseWDS: func(*qmi.WDSService) error { return nil },
		deleteMux: func(string, uint8) error {
			events = append(events, "secondary-pdn-mux-delete")
			return nil
		},
	}
	m.muxIface = "qmimux7"
	m.masterIface = "wwan0_q_q"

	m.cleanup()

	if cfg.reconciledMaster != "wwan0_q_q" {
		t.Fatalf("ReconcileResidualMux master = %q, want resolved QMAP master", cfg.reconciledMaster)
	}
	if cfg.reconciledKeep != nil {
		t.Fatalf("ReconcileResidualMux keep = %v, want nil to remove every QMAP mux", cfg.reconciledKeep)
	}
	if len(events) < 3 || events[len(events)-1] != "reconcile-residual-mux" {
		t.Fatalf("cleanup events = %v, want secondary PDN cleanup before residual mux cleanup", events)
	}
}

func TestRunCleanupTasksWaitsForCompletion(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	done := make(chan []cleanupTaskResult, 1)

	go func() {
		done <- runCleanupTasks(context.Background(), NewNopLogger(), []cleanupTask{{
			name: "slow",
			run: func(context.Context) error {
				close(started)
				<-release
				return nil
			},
		}})
	}()

	select {
	case <-started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cleanup task did not start")
	}

	select {
	case <-done:
		t.Fatal("runCleanupTasks returned before task completed")
	default:
	}

	close(release)

	select {
	case results := <-done:
		if len(results) != 1 || results[0].name != "slow" || results[0].err != nil {
			t.Fatalf("cleanup task results = %#v, want one successful slow task", results)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("runCleanupTasks did not return after task completed")
	}
}

func TestRunCleanupTasksStopsWaitingAtContextDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	results := runCleanupTasks(ctx, NewNopLogger(), []cleanupTask{{
		name: "blocked",
		run: func(context.Context) error {
			<-release
			return nil
		},
	}})
	elapsed := time.Since(start)

	if elapsed >= 90*time.Millisecond {
		t.Fatalf("runCleanupTasks elapsed = %s, want deadline-bounded wait", elapsed)
	}
	if len(results) != 1 || results[0].name != "blocked" || results[0].err == nil {
		t.Fatalf("cleanup task results = %#v, want blocked task with deadline error", results)
	}
}
