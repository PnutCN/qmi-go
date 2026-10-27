package qmi

import (
	"encoding/hex"
	"testing"
)

// 2026-10-27 真机（KPN 承载的 Speedtest Travel profile）：EF_PNN 长度字节声明 16、实际 15 字节。
func TestDecodePNNRecordToleratesOverlongLength(t *testing.T) {
	data, _ := hex.DecodeString("4310805378B94CA697E77410551EB697D9FFFF")
	rec, ok := decodePNNRecord(1, data)
	if !ok || rec.FullName != "Speedtest Travel" {
		t.Fatalf("FullName = %q ok=%v, want Speedtest Travel", rec.FullName, ok)
	}
}
