package main

import (
	"strings"
	"testing"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

func TestFormatCellLocationInfoFormatsRawNR5GMeasurements(t *testing.T) {
	output := formatCellLocationInfo(&qmi.CellLocationInfo{NR5G: &qmi.NR5GCellLocationInfo{
		RSRP: -950,
		RSRQ: -110,
		SNR:  123,
	}})
	if !strings.Contains(output, "RSRP: -95.0 dBm") || !strings.Contains(output, "RSRQ: -11.0 dB") || !strings.Contains(output, "SNR: 12.3 dB") {
		t.Fatalf("unexpected NR5G cell output: %q", output)
	}
}
