package qmi

import "testing"

func TestDispatchUIMIndications(t *testing.T) {
	c := &Client{eventCh: make(chan Event, 4)}
	cases := []struct {
		msgID uint16
		want  EventType
	}{
		{msgID: UIMStatusChangeInd, want: EventSimStatusChanged},
		{msgID: UIMSessionClosedInd, want: EventUIMSessionClosed},
		{msgID: UIMRefreshInd, want: EventUIMRefresh},
		{msgID: UIMSlotStatusInd, want: EventUIMSlotStatus},
	}

	for _, tc := range cases {
		c.dispatchIndication(&Packet{ServiceType: ServiceUIM, MessageID: tc.msgID, IsIndication: true})
		evt := <-c.eventCh
		if evt.Type != tc.want {
			t.Fatalf("UIM msg 0x%04X dispatched as %v, want %v", tc.msgID, evt.Type, tc.want)
		}
	}
}

// hasTLV reports whether a TLV of the given type is present.
func hasTLV(tlvs []TLV, typ byte) bool {
	for _, t := range tlvs {
		if t.Type == typ {
			return true
		}
	}
	return false
}

// The Channel ID TLV (0x10) is OPTIONAL in QMI_UIM_SEND_APDU. Omitting it selects
// the basic channel; sending it with value 0 is not the same thing — the modem
// rejects the request outright.
//
// Observed on a Quectel EC25 (2026-08-02) while sending an ENVELOPE
// (SMS-PP DOWNLOAD, ETSI TS 131.111 §7.1.1), which is a card-level command and
// must go over the basic channel:
//
//	service=0x000b msg=0x003b result=0x0001 error=0x0030 (QMI_ERR_INVALID_ARGUMENT)
//
// The bug stayed hidden because every existing caller (eSIM profile management,
// SIM auth) opens a logical channel first, so channel was always > 0.
func TestSendAPDUOmitsChannelTLVForBasicChannel(t *testing.T) {
	tlvs := buildSendAPDUTLVs(1, 0, []byte{0x80, 0xC2, 0x00, 0x00, 0x02, 0xAA, 0xBB})

	if hasTLV(tlvs, 0x10) {
		t.Fatal("channel 0 means the basic channel: TLV 0x10 must be omitted, not sent as 0")
	}
	// The APDU and slot TLVs must still be there.
	if !hasTLV(tlvs, 0x02) || !hasTLV(tlvs, 0x01) {
		t.Fatalf("dropped a required TLV: %+v", tlvs)
	}
}

// A logical channel must still be announced — otherwise every eSIM/SIM-auth APDU
// silently lands on the basic channel and talks to the wrong application.
func TestSendAPDUKeepsChannelTLVForLogicalChannel(t *testing.T) {
	tlvs := buildSendAPDUTLVs(1, 2, []byte{0x00, 0xA4, 0x04, 0x00})

	for _, tlv := range tlvs {
		if tlv.Type == 0x10 {
			if len(tlv.Value) != 1 || tlv.Value[0] != 2 {
				t.Fatalf("channel TLV carries %v, want [2]", tlv.Value)
			}
			return
		}
	}
	t.Fatal("logical channel 2 was not announced: TLV 0x10 is missing")
}

// The APDU TLV is a 16-bit little-endian length followed by the command.
func TestSendAPDUEncodesLengthPrefix(t *testing.T) {
	cmd := make([]byte, 300) // longer than 255 to catch a byte-sized length
	tlvs := buildSendAPDUTLVs(1, 0, cmd)

	for _, tlv := range tlvs {
		if tlv.Type != 0x02 {
			continue
		}
		if len(tlv.Value) != 2+len(cmd) {
			t.Fatalf("APDU TLV is %d bytes, want %d", len(tlv.Value), 2+len(cmd))
		}
		if tlv.Value[0] != 0x2C || tlv.Value[1] != 0x01 { // 300 = 0x012C little-endian
			t.Fatalf("length prefix is %02x%02x, want 2c01", tlv.Value[0], tlv.Value[1])
		}
		return
	}
	t.Fatal("APDU TLV missing")
}
