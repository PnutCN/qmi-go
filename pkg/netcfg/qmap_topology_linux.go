//go:build linux

package netcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverQMAPTopology reads the Linux qmi_wwan sysfs topology.
func (l *LinuxConfigurator) DiscoverQMAPTopology(configuredMaster string) (QMAPTopology, error) {
	return discoverQMAPTopologyAt(sysClassNetRoot, configuredMaster)
}

func discoverQMAPTopologyAt(root, configuredMaster string) (QMAPTopology, error) {
	if strings.TrimSpace(configuredMaster) == "" {
		return QMAPTopology{}, fmt.Errorf("QMAP topology: missing configured master interface")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return QMAPTopology{MasterInterface: configuredMaster, MuxInterfaces: map[uint8]string{}}, nil
		}
		return QMAPTopology{}, fmt.Errorf("QMAP topology: scan %s: %w", root, err)
	}

	// Fast path: the configured interface is itself still the QMAP master.
	// This is the common case, and it matters on a host with more than one
	// physical QMI device present -- with every QMI backend now declaring
	// QMAP (not just VoLTE devices), that is not an edge case. Each device's
	// own configured name resolves its own master directly here, without
	// ever having to tell it apart from another device's master/mux set.
	if hasQMAPControlTriad(root, configuredMaster) {
		return QMAPTopology{
			MasterInterface: configuredMaster,
			MuxInterfaces:   muxesBelongingTo(root, entries, configuredMaster),
		}, nil
	}

	// Slow path: the configured interface may have been renamed away to make
	// room for a mux reusing its old name (see
	// TestDiscoverQMAPTopologyAdoptsRenamedMaster). Every other QMAP master
	// candidate on the host is a possibility; with more than one, there is no
	// signal left to pick the right one, so that legitimately stays an error
	// rather than a guess.
	masters := make([]string, 0, 1)
	for _, entry := range entries {
		name := entry.Name()
		if name != configuredMaster && hasQMAPControlTriad(root, name) {
			masters = append(masters, name)
		}
	}
	switch len(masters) {
	case 0:
		return QMAPTopology{MasterInterface: configuredMaster, MuxInterfaces: map[uint8]string{}}, nil
	case 1:
		return QMAPTopology{
			MasterInterface: masters[0],
			MuxInterfaces:   muxesBelongingTo(root, entries, masters[0]),
		}, nil
	default:
		return QMAPTopology{}, fmt.Errorf("QMAP topology: ambiguous physical masters %s", strings.Join(masters, ", "))
	}
}

// muxesBelongingTo scans an already-listed directory for QMAP mux netdevs
// that the kernel reports as children of master, via the "lower_<master>"
// file qmi_wwan exposes under each mux's own netdev directory -- the same
// signal ReconcileResidualMux and GetQMAPMuxIface use to tell one physical
// device's muxes from another's. Without this, a second physical QMI device
// on the host would leak its mux_id entries into this device's topology.
func muxesBelongingTo(root string, entries []os.DirEntry, master string) map[uint8]string {
	muxes := make(map[uint8]string)
	for _, entry := range entries {
		name := entry.Name()
		if name == master || !muxBelongsToMaster(root, name, master) {
			continue
		}
		if muxID, ok := readQMAPMuxID(root, name); ok {
			muxes[muxID] = name
		}
	}
	return muxes
}

// muxBelongsToMaster reports whether the kernel's qmi_wwan driver reports
// muxIface as a child of master, via the "lower_<master>" file it exposes
// under /sys/class/net/<muxIface>/.
func muxBelongsToMaster(root, muxIface, master string) bool {
	_, err := os.Stat(filepath.Join(root, muxIface, "lower_"+master))
	return err == nil
}

func hasQMAPControlTriad(root, ifname string) bool {
	for _, name := range []string{"add_mux", "del_mux", "raw_ip"} {
		if _, err := os.Stat(filepath.Join(root, ifname, "qmi", name)); err != nil {
			return false
		}
	}
	return true
}

func readQMAPMuxID(root, ifname string) (uint8, bool) {
	for _, relativePath := range []string{"qmi/mux_id", "qmap/mux_id"} {
		data, err := os.ReadFile(filepath.Join(root, ifname, relativePath))
		if err != nil {
			continue
		}
		id, err := parseMuxIDAttr(strings.TrimSpace(string(data)))
		if err != nil {
			return 0, false
		}
		// qmi_wwan may set its high mux flag in this sysfs value.
		return id & 0x7f, true
	}
	return 0, false
}
