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

func TestBuildSendAPDUTLVsOmitsChannelIDForBasicChannel(t *testing.T) {
	tlvs := buildSendAPDUTLVs(1, 0, []byte{0x80, 0xC2, 0x00, 0x00})

	for _, tlv := range tlvs {
		if tlv.Type == 0x10 {
			t.Fatal("basic channel must omit the optional Channel ID TLV")
		}
	}
}

func TestBuildSendAPDUTLVsKeepsChannelIDForLogicalChannel(t *testing.T) {
	tlvs := buildSendAPDUTLVs(1, 2, []byte{0x00, 0xA4, 0x04, 0x00})

	for _, tlv := range tlvs {
		if tlv.Type != 0x10 {
			continue
		}
		if len(tlv.Value) != 1 || tlv.Value[0] != 2 {
			t.Fatalf("Channel ID TLV = %v, want [2]", tlv.Value)
		}
		return
	}
	t.Fatal("logical channel must include the Channel ID TLV")
}
