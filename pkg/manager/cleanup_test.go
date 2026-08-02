package manager

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/iniwex5/qmi-go/pkg/netcfg"
)

type cleanupConfigurator struct{ deletedMaster string }

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
func (c *cleanupConfigurator) DelQMAPMux(master string, _ uint8) error {
	c.deletedMaster = master
	return nil
}
func (*cleanupConfigurator) GetQMAPMuxIface(string, uint8) string                  { return "" }
func (*cleanupConfigurator) EnableRawIP(string) error                              { return nil }
func (*cleanupConfigurator) ReconcileResidualMux(string, []uint8) ([]uint8, error) { return nil, nil }

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

	if cfg.deletedMaster != "wwan0_q_q" {
		t.Fatalf("DelQMAPMux master = %q, want resolved QMAP master", cfg.deletedMaster)
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
