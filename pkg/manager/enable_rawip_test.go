package manager

import (
	"context"
	"testing"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

func TestDataFormatTargetForMuxIsQMAPWhenMuxed(t *testing.T) {
	got := dataFormatTargetForMux(1)
	if got.UlDataAggregation != qmi.DataAggregationQMAP || got.DlDataAggregation != qmi.DataAggregationQMAP {
		t.Fatalf("target = %+v, want QMAP (%#x) on both directions for MuxID=1", got, qmi.DataAggregationQMAP)
	}
	if got.LinkProtocol != qmi.LinkProtocolIP {
		t.Fatalf("LinkProtocol = %#x, want IP", got.LinkProtocol)
	}
}

func TestDataFormatTargetForMuxIsDisabledWhenNative(t *testing.T) {
	got := dataFormatTargetForMux(0)
	if got.UlDataAggregation != uint32(qmi.DataFormatUlDataAggDisabled) || got.DlDataAggregation != uint32(qmi.DataFormatDlDataAggDisabled) {
		t.Fatalf("target = %+v, want Disabled aggregation for MuxID=0", got)
	}
}

func TestDataFormatMatchesComparesAllThreeFields(t *testing.T) {
	target := dataFormatTargetForMux(1)

	atTarget := qmi.DataFormat{LinkProtocol: qmi.LinkProtocolIP, UlDataAggregation: qmi.DataAggregationQMAP, DlDataAggregation: qmi.DataAggregationQMAP}
	if !dataFormatMatches(atTarget, target) {
		t.Fatal("dataFormatMatches: identical format should match")
	}

	wrongAgg := qmi.DataFormat{LinkProtocol: qmi.LinkProtocolIP, UlDataAggregation: uint32(qmi.DataFormatUlDataAggDisabled), DlDataAggregation: uint32(qmi.DataFormatDlDataAggDisabled)}
	if dataFormatMatches(wrongAgg, target) {
		t.Fatal("dataFormatMatches: link protocol matching alone must not be enough — this is exactly the bug that let the device drift to QMAP undetected")
	}

	wrongProtocol := qmi.DataFormat{LinkProtocol: 0, UlDataAggregation: qmi.DataAggregationQMAP, DlDataAggregation: qmi.DataAggregationQMAP}
	if dataFormatMatches(wrongProtocol, target) {
		t.Fatal("dataFormatMatches: link protocol mismatch must not match")
	}
}

// newManagerForDataFormatTest builds a *Manager whose enableRawIP body runs
// for real (unlike most tests in this package, which bypass it entirely via
// enableRawIPHook) but whose two WDA calls are captured via
// getDataFormatFn/setDataFormatFn instead of touching a real modem.
//
// The kernel raw_ip sysfs check inside enableRawIP cannot be faked this way
// (it stats a real /sys/class/net path, and a nonexistent interface makes
// kernelEnabled permanently false on Linux, not "treated as done" — the
// combined kernelEnabled&&modemEnabled skip can therefore never be exercised
// through this seam). These tests only assert on the CONTENT written to
// SetDataFormat, which is meaningful regardless of the kernel branch; the
// "already at target, don't write" behavior is covered directly by
// TestDataFormatMatchesComparesAllThreeFields above instead.
func newManagerForDataFormatTest(t *testing.T, muxID uint8, current qmi.DataFormat, setCalls *[]qmi.DataFormat) *Manager {
	t.Helper()
	m := newRecoveryTestManager()
	m.cfg = Config{
		Device: ModemDevice{NetInterface: "nonexistent-test-iface"},
		MuxID:  muxID,
	}
	m.wda = &qmi.WDAService{}
	m.getDataFormatFn = func(ctx context.Context) (*qmi.DataFormat, error) {
		got := current
		return &got, nil
	}
	m.setDataFormatFn = func(ctx context.Context, f qmi.DataFormat) error {
		*setCalls = append(*setCalls, f)
		return nil
	}
	return m
}

func TestEnableRawIPWritesQMAPTargetWhenMuxed(t *testing.T) {
	var setCalls []qmi.DataFormat
	m := newManagerForDataFormatTest(t, 1, qmi.DataFormat{
		LinkProtocol:      qmi.LinkProtocolIP,
		UlDataAggregation: uint32(qmi.DataFormatUlDataAggDisabled),
		DlDataAggregation: uint32(qmi.DataFormatDlDataAggDisabled),
	}, &setCalls)

	if err := m.enableRawIP(context.Background()); err != nil {
		t.Fatalf("enableRawIP: %v", err)
	}
	if len(setCalls) != 1 {
		t.Fatalf("SetDataFormat calls = %d, want 1", len(setCalls))
	}
	got := setCalls[0]
	if got.UlDataAggregation != qmi.DataAggregationQMAP || got.DlDataAggregation != qmi.DataAggregationQMAP {
		t.Fatalf("SetDataFormat aggregation = ul=%#x dl=%#x, want QMAP (%#x) on both", got.UlDataAggregation, got.DlDataAggregation, qmi.DataAggregationQMAP)
	}
}

func TestEnableRawIPWritesDisabledTargetWhenNative(t *testing.T) {
	var setCalls []qmi.DataFormat
	m := newManagerForDataFormatTest(t, 0, qmi.DataFormat{
		LinkProtocol:      qmi.LinkProtocolIP,
		UlDataAggregation: qmi.DataAggregationQMAP,
		DlDataAggregation: qmi.DataAggregationQMAP,
	}, &setCalls)

	if err := m.enableRawIP(context.Background()); err != nil {
		t.Fatalf("enableRawIP: %v", err)
	}
	if len(setCalls) != 1 {
		t.Fatalf("SetDataFormat calls = %d, want 1", len(setCalls))
	}
	got := setCalls[0]
	if got.UlDataAggregation != uint32(qmi.DataFormatUlDataAggDisabled) || got.DlDataAggregation != uint32(qmi.DataFormatDlDataAggDisabled) {
		t.Fatalf("SetDataFormat aggregation = ul=%#x dl=%#x, want Disabled (device is left over in QMAP from a prior muxed session, MuxID=0 must push it back down)", got.UlDataAggregation, got.DlDataAggregation)
	}
}
