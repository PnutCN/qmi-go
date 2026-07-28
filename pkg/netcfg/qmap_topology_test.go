//go:build linux

package netcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverQMAPTopologyAdoptsRenamedMaster(t *testing.T) {
	root := t.TempDir()
	writeQMAPTopologyFile(t, root, "wwp0s20u1i4_q/qmi/add_mux", "")
	writeQMAPTopologyFile(t, root, "wwp0s20u1i4_q/qmi/del_mux", "")
	writeQMAPTopologyFile(t, root, "wwp0s20u1i4_q/qmi/raw_ip", "Y")
	writeQMAPTopologyFile(t, root, "wwp0s20u1i4/qmi/mux_id", "0x81")

	got, err := discoverQMAPTopologyAt(root, "wwp0s20u1i4")
	if err != nil {
		t.Fatalf("discoverQMAPTopologyAt() error = %v", err)
	}
	if got.MasterInterface != "wwp0s20u1i4_q" {
		t.Fatalf("MasterInterface = %q, want renamed physical master", got.MasterInterface)
	}
	if got.MuxInterfaces[1] != "wwp0s20u1i4" {
		t.Fatalf("MuxInterfaces = %v, want default mux 1 at stable name", got.MuxInterfaces)
	}
}

func TestDiscoverQMAPTopologyFollowsSysfsInterfaceSymlinks(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	writeQMAPTopologyFile(t, target, "wwan0_q/qmi/add_mux", "")
	writeQMAPTopologyFile(t, target, "wwan0_q/qmi/del_mux", "")
	writeQMAPTopologyFile(t, target, "wwan0_q/qmi/raw_ip", "Y")
	writeQMAPTopologyFile(t, target, "wwan0/qmi/mux_id", "0x81")
	for _, name := range []string{"wwan0_q", "wwan0"} {
		if err := os.Symlink(filepath.Join(target, name), filepath.Join(root, name)); err != nil {
			t.Fatalf("Symlink(%q): %v", name, err)
		}
	}

	got, err := discoverQMAPTopologyAt(root, "wwan0")
	if err != nil {
		t.Fatalf("discoverQMAPTopologyAt() error = %v", err)
	}
	if got.MasterInterface != "wwan0_q" || got.MuxInterfaces[1] != "wwan0" {
		t.Fatalf("topology = %+v", got)
	}
}

func TestDiscoverQMAPTopologyKeepsNativeLayout(t *testing.T) {
	root := t.TempDir()

	got, err := discoverQMAPTopologyAt(root, "wwan0")
	if err != nil {
		t.Fatalf("discoverQMAPTopologyAt() error = %v", err)
	}
	if got.MasterInterface != "wwan0" || len(got.MuxInterfaces) != 0 {
		t.Fatalf("topology = %+v, want native configured interface", got)
	}
}

func TestDiscoverQMAPTopologyRejectsAmbiguousMasters(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"wwan0_q", "wwan1_q"} {
		writeQMAPTopologyFile(t, root, name+"/qmi/add_mux", "")
		writeQMAPTopologyFile(t, root, name+"/qmi/del_mux", "")
		writeQMAPTopologyFile(t, root, name+"/qmi/raw_ip", "Y")
	}

	if _, err := discoverQMAPTopologyAt(root, "wwan0"); err == nil {
		t.Fatal("expected ambiguous QMAP masters to be rejected")
	}
}

func writeQMAPTopologyFile(t *testing.T, root, relativePath, content string) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
