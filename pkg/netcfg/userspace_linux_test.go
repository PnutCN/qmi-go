//go:build linux

package netcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDisableIPv6AutoconfigurationWritesEverySetting(t *testing.T) {
	root := t.TempDir()
	ifdir := filepath.Join(root, "qmimux0")
	if err := os.MkdirAll(ifdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"accept_ra", "accept_ra_defrtr", "autoconf"} {
		if err := os.WriteFile(filepath.Join(ifdir, name), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := disableIPv6AutoconfigurationAt(root, "qmimux0"); err != nil {
		t.Fatalf("disableIPv6AutoconfigurationAt: %v", err)
	}
	for _, name := range []string{"accept_ra", "accept_ra_defrtr", "autoconf"} {
		got, err := os.ReadFile(filepath.Join(ifdir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "0\n" {
			t.Fatalf("%s = %q, want %q", name, got, "0\n")
		}
	}
}

func TestDisableIPv6AutoconfigurationToleratesMissingSettings(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "qmimux0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := disableIPv6AutoconfigurationAt(root, "qmimux0"); err != nil {
		t.Fatalf("missing sysctls must be tolerated, got %v", err)
	}
}

func TestPrepareUserspaceOnlyRejectsUnsafeInterfaceNames(t *testing.T) {
	for _, name := range []string{"", "../etc", "a/b"} {
		if err := PrepareUserspaceOnly(name); err == nil {
			t.Fatalf("PrepareUserspaceOnly(%q) = nil, want error", name)
		}
	}
}
