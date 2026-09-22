package qmi

import "testing"

func TestHomePLMNFromIMSIAndLengthPreservesEFADWidth(t *testing.T) {
	for _, tc := range []struct {
		name string
		imsi string
		len  int
		mnc  string
	}{
		{name: "two digit", imsi: "234336575868434", len: 2, mnc: "33"},
		{name: "three digit", imsi: "234336575868434", len: 3, mnc: "336"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mcc, mnc, err := homePLMNFromIMSIAndLength(tc.imsi, tc.len)
			if err != nil {
				t.Fatalf("homePLMNFromIMSIAndLength() error = %v", err)
			}
			if mcc != "234" || mnc != tc.mnc {
				t.Fatalf("home PLMN = %s/%s, want 234/%s", mcc, mnc, tc.mnc)
			}
		})
	}
}
