package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/iniwex5/qmi-go/pkg/netcfg"
)

// DataPlaneMode identifies the link-layer topology used for a data plane.
type DataPlaneMode string

const (
	DataPlaneModeNative DataPlaneMode = "native"
	DataPlaneModeQMAP   DataPlaneMode = "qmap"
)

// DataPlaneSpec describes the manager-owned default data-plane topology.
type DataPlaneSpec struct {
	Mode         DataPlaneMode
	DefaultMuxID uint8
}

// DataPlaneSnapshot is the stable result of topology convergence. The
// physical QMAP master intentionally remains private to Manager.
type DataPlaneSnapshot struct {
	Generation       uint64
	Mode             DataPlaneMode
	DefaultInterface string
	DefaultMuxID     uint8
}

func dataPlaneSpecFromConfig(cfg Config) DataPlaneSpec {
	if cfg.MuxID == 0 {
		return DataPlaneSpec{Mode: DataPlaneModeNative}
	}
	return DataPlaneSpec{Mode: DataPlaneModeQMAP, DefaultMuxID: cfg.MuxID}
}

type dataPlaneController struct {
	mu              sync.Mutex
	snapshot        DataPlaneSnapshot
	masterInterface string
	nextSessionID   uint64
	sessions        map[uint64]*managedPDNSession
	reservedMuxes   map[uint8]uint64
}

type dataPlaneOps struct {
	discoverQMAPTopology func(configuredMaster string) (netcfg.QMAPTopology, error)
	enableRawIP          func(string) error
	addQMAPMux           func(master string, muxID uint8) (string, error)
}

func defaultDataPlaneOps() dataPlaneOps {
	return dataPlaneOps{
		discoverQMAPTopology: netcfg.DiscoverQMAPTopology,
		enableRawIP:          netcfg.EnableRawIP,
		addQMAPMux:           netcfg.AddQMAPMux,
	}
}

func (m *Manager) resolvedDataPlaneOps() dataPlaneOps {
	ops := m.dataPlaneOps
	defaults := defaultDataPlaneOps()
	if ops.enableRawIP == nil {
		ops.enableRawIP = defaults.enableRawIP
	}
	if ops.discoverQMAPTopology == nil {
		ops.discoverQMAPTopology = defaults.discoverQMAPTopology
	}
	if ops.addQMAPMux == nil {
		ops.addQMAPMux = defaults.addQMAPMux
	}
	return ops
}

// ConvergeDataPlane serializes QMAP discovery and mutation, then publishes a
// stable default-interface snapshot. It never exposes the physical master,
// whose name can change during QMAP setup.
func (m *Manager) ConvergeDataPlane(ctx context.Context, spec DataPlaneSpec) (DataPlaneSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if spec.Mode != DataPlaneModeNative && spec.Mode != DataPlaneModeQMAP {
		return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: unsupported data-plane mode %q", spec.Mode)
	}
	if spec.Mode == DataPlaneModeQMAP && spec.DefaultMuxID == 0 {
		return DataPlaneSnapshot{}, errors.New("qmi manager: QMAP requires a nonzero default mux ID")
	}

	m.dataPlane.mu.Lock()
	defer m.dataPlane.mu.Unlock()

	if snapshot := m.dataPlane.snapshot; snapshot.Generation != 0 &&
		snapshot.Mode == spec.Mode && snapshot.DefaultMuxID == spec.DefaultMuxID {
		return snapshot, nil
	}
	if err := m.ensureDataPlaneServices(ctx); err != nil {
		return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: allocate data-plane services: %w", err)
	}

	originalMaster := m.cfg.Device.NetInterface
	if originalMaster == "" {
		return DataPlaneSnapshot{}, errors.New("qmi manager: missing data-plane interface")
	}
	if spec.Mode == DataPlaneModeNative {
		snapshot := DataPlaneSnapshot{
			Generation:       m.dataPlane.snapshot.Generation + 1,
			Mode:             spec.Mode,
			DefaultInterface: originalMaster,
			DefaultMuxID:     spec.DefaultMuxID,
		}
		m.dataPlane.masterInterface = originalMaster
		m.dataPlane.snapshot = snapshot
		return snapshot, nil
	}

	master := m.dataPlane.masterInterface
	if master == "" {
		topology, err := m.resolvedDataPlaneOps().discoverQMAPTopology(originalMaster)
		if err != nil {
			return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: discover QMAP topology: %w", err)
		}
		master = topology.MasterInterface
		if defaultIface := topology.MuxInterfaces[spec.DefaultMuxID]; defaultIface != "" {
			m.dataPlane.masterInterface = master
			snapshot := DataPlaneSnapshot{
				Generation:       m.dataPlane.snapshot.Generation + 1,
				Mode:             spec.Mode,
				DefaultInterface: defaultIface,
				DefaultMuxID:     spec.DefaultMuxID,
			}
			m.dataPlane.snapshot = snapshot
			m.mu.Lock()
			m.masterIface = master
			m.muxIface = defaultIface
			m.mu.Unlock()
			return snapshot, nil
		}
	}
	ops := m.resolvedDataPlaneOps()
	if err := ops.enableRawIP(master); err != nil {
		m.log.WithError(err).Warn("开启 Raw IP 模式失败")
	}
	defaultIface, err := ops.addQMAPMux(master, spec.DefaultMuxID)
	if err != nil {
		return DataPlaneSnapshot{}, fmt.Errorf("qmi manager: create default data mux: %w", err)
	}

	m.dataPlane.masterInterface = master
	snapshot := DataPlaneSnapshot{
		Generation:       m.dataPlane.snapshot.Generation + 1,
		Mode:             spec.Mode,
		DefaultInterface: defaultIface,
		DefaultMuxID:     spec.DefaultMuxID,
	}
	m.dataPlane.snapshot = snapshot

	// Transitional internal state for the existing default dial path. No
	// external caller receives either of these mutable names through this API.
	m.mu.Lock()
	m.masterIface = master
	m.muxIface = defaultIface
	m.mu.Unlock()

	return snapshot, nil
}
