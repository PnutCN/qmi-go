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

	masters := make([]string, 0, 1)
	muxes := make(map[uint8]string)
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(root, entry.Name()))
		if err != nil || !info.IsDir() {
			continue
		}
		name := entry.Name()
		if hasQMAPControlTriad(root, name) {
			masters = append(masters, name)
		}
		if muxID, ok := readQMAPMuxID(root, name); ok {
			if existing, exists := muxes[muxID]; exists && existing != name {
				return QMAPTopology{}, fmt.Errorf("QMAP topology: mux ID %d belongs to both %s and %s", muxID, existing, name)
			}
			muxes[muxID] = name
		}
	}

	switch len(masters) {
	case 0:
		return QMAPTopology{MasterInterface: configuredMaster, MuxInterfaces: muxes}, nil
	case 1:
		return QMAPTopology{MasterInterface: masters[0], MuxInterfaces: muxes}, nil
	default:
		return QMAPTopology{}, fmt.Errorf("QMAP topology: ambiguous physical masters %s", strings.Join(masters, ", "))
	}
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
