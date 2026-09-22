package manager

import (
	"net"
	"testing"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

func TestSettingsReturnsIndependentRuntimeSnapshot(t *testing.T) {
	m := &Manager{settings: &qmi.RuntimeSettings{
		IPv4DNS1: net.IPv4(192, 0, 2, 53),
		IPv6DNS1: net.ParseIP("2001:db8::53"),
	}}

	got := m.Settings()
	if got == nil {
		t.Fatal("Settings() returned nil")
	}
	got.IPv4DNS1[0] = 203
	got.IPv6DNS1[0] = 0xff

	again := m.Settings()
	if again == nil || again.IPv4DNS1.String() != "192.0.2.53" || again.IPv6DNS1.String() != "2001:db8::53" {
		t.Fatalf("Settings() exposed mutable internal data: %+v", again)
	}
}
