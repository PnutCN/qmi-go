package qmi

import "testing"

func TestDecodeEFSPNASCII(t *testing.T) {
	data := []byte{0x00, 'C', 'M', 'C', 'C', 0xFF, 0xFF, 0xFF}
	got, err := decodeEFSPN(data)
	if err != nil {
		t.Fatalf("decodeEFSPN() error = %v", err)
	}
	if got != "CMCC" {
		t.Fatalf("decodeEFSPN() = %q, want %q", got, "CMCC")
	}
}

func TestDecodeEFSPNUCS2(t *testing.T) {
	data := []byte{0x00, 0x80, 0x4E, 0x2D, 0x56, 0xFD, 0x79, 0xFB, 0x52, 0xA8, 0xFF, 0xFF}
	got, err := decodeEFSPN(data)
	if err != nil {
		t.Fatalf("decodeEFSPN() error = %v", err)
	}
	if got != "中国移动" {
		t.Fatalf("decodeEFSPN() = %q, want %q", got, "中国移动")
	}
}

func TestDecodeEFSPNCompressedUCS281(t *testing.T) {
	data := []byte{0x00, 0x81, 0x03, 0x9C, 0xAD, 0x01, 0x02, 0xFF}
	got, err := decodeEFSPN(data)
	if err != nil {
		t.Fatalf("decodeEFSPN() error = %v", err)
	}
	if got != "中£$" {
		t.Fatalf("decodeEFSPN() = %q, want %q", got, "中£$")
	}
}

func TestDecodeEFSPNCompressedUCS282(t *testing.T) {
	data := []byte{0x00, 0x82, 0x03, 0x4E, 0x00, 0xAD, 0x01, 0x02, 0xFF}
	got, err := decodeEFSPN(data)
	if err != nil {
		t.Fatalf("decodeEFSPN() error = %v", err)
	}
	if got != "中£$" {
		t.Fatalf("decodeEFSPN() = %q, want %q", got, "中£$")
	}
}

func TestDecodeEFSPNEmptyPadding(t *testing.T) {
	data := []byte{0x00, 0xFF, 0xFF, 0x00}
	if got, err := decodeEFSPN(data); err == nil {
		t.Fatalf("decodeEFSPN() = %q, want error", got)
	}
}

func TestDecodeEFSPNPreservesSelectorSpaces(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"ascii", []byte{0x00, ' ', 'O', '2', ' ', 0xff, 0xff}, " O2 "},
		{"ucs2", []byte{0x00, 0x80, 0x00, 0x20, 0x4e, 0x2d, 0x00, 0x20, 0xff, 0xff}, " 中 "},
		{"spaces", []byte{0x00, ' ', ' ', 0xff}, "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeEFSPN(tc.raw)
			if err != nil || got != tc.want {
				t.Fatalf("decodeEFSPN() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDecodeEFSPNEncodingSpecificPadding(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"ucs2-zero-low-byte", []byte{0, 0x80, 0x4e, 0x00}, "一"},
		{"ucs2-wide-space", []byte{0, 0x80, 0x30, 0x00}, "　"},
		{"ucs2-padded", []byte{0, 0x80, 0x4e, 0x00, 0xff, 0xff, 0xff}, "一"},
		{"gsm-at", []byte{0, 0x00, 0xff}, "@"},
		{"gsm-not-ascii", []byte{0, 0x24, 0x40, 0xff}, "¤¡"},
		{"compressed-effective-ff", []byte{0, 0x81, 1, 0x9c, 0xff}, "\u4e7f"},
		{"compressed-gsm-escape", []byte{0, 0x81, 2, 0, 0x1b, 0x3c}, "["},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeEFSPN(tc.raw)
			if err != nil || got != tc.want {
				t.Fatalf("decodeEFSPN() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
