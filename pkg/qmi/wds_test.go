package qmi

import (
	"encoding/binary"
	"errors"
	"testing"
)

func wdsTLVUint32(tlvType uint8, v uint32) TLV {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func wdsTLVUint64(tlvType uint8, v uint64) TLV {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, v)
	return TLV{Type: tlvType, Value: buf}
}

func TestBuildProfileSettingsTLVsIncludesZeroValuedEnumsWhenRequested(t *testing.T) {
	tlvs := buildProfileSettingsTLVs(WDSProfileSettings{
		Name:              "internet",
		APN:               "cmnet",
		Username:          "user",
		Password:          "pass",
		PDPType:           WDSPDPTypeIPv4,
		HasPDPType:        true,
		Authentication:    WDSAuthNone,
		HasAuthentication: true,
	})

	if len(tlvs) != 6 {
		t.Fatalf("expected 6 TLVs, got %d", len(tlvs))
	}
	if tlvs[0].Type != 0x10 || string(tlvs[0].Value) != "internet" {
		t.Fatalf("unexpected profile name TLV: %+v", tlvs[0])
	}
	if tlvs[1].Type != 0x11 || len(tlvs[1].Value) != 1 || tlvs[1].Value[0] != WDSPDPTypeIPv4 {
		t.Fatalf("unexpected PDP type TLV: %+v", tlvs[1])
	}
	if tlvs[5].Type != 0x1D || len(tlvs[5].Value) != 1 || tlvs[5].Value[0] != WDSAuthNone {
		t.Fatalf("unexpected auth TLV: %+v", tlvs[5])
	}
}

func TestParseChannelRatesResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: []byte{0x20, 0x03, 0x00, 0x00, 0x40, 0x06, 0x00, 0x00, 0x80, 0x0C, 0x00, 0x00, 0x00, 0x19, 0x00, 0x00}},
		},
	}
	rates, err := parseChannelRatesResponse(resp)
	if err != nil {
		t.Fatalf("parseChannelRatesResponse returned error: %v", err)
	}
	if rates.TxRateBPS != 800 || rates.RxRateBPS != 1600 || rates.MaxTxRateBPS != 3200 || rates.MaxRxRateBPS != 6400 {
		t.Fatalf("unexpected rates: %+v", rates)
	}
}

func TestParsePacketStatisticsResponseOutOfCallKeepsLastCallCounters(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			qmiErrorResultTLV(QMIErrOutOfCall),
			wdsTLVUint64(0x1B, 1234),
			wdsTLVUint64(0x1C, 5678),
		},
	}
	stats, err := parsePacketStatisticsResponse(resp)
	var outOfCall *OutOfCallError
	if !errors.As(err, &outOfCall) {
		t.Fatalf("expected OutOfCallError, got %v", err)
	}
	if !stats.HasLastCallTxBytesOK || stats.LastCallTxBytesOK != 1234 {
		t.Fatalf("unexpected last call tx bytes: %+v", stats)
	}
	if !stats.HasLastCallRxBytesOK || stats.LastCallRxBytesOK != 5678 {
		t.Fatalf("unexpected last call rx bytes: %+v", stats)
	}
}

func TestParseAutoconnectSettingsResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: []byte{WDSAutoconnectEnabled}},
			{Type: 0x10, Value: []byte{WDSAutoconnectRoamingHomeOnly}},
		},
	}
	settings, err := parseAutoconnectSettingsResponse(resp)
	if err != nil {
		t.Fatalf("parseAutoconnectSettingsResponse returned error: %v", err)
	}
	if !settings.HasStatus || settings.Status != WDSAutoconnectEnabled {
		t.Fatalf("unexpected autoconnect status: %+v", settings)
	}
	if !settings.HasRoaming || settings.Roaming != WDSAutoconnectRoamingHomeOnly {
		t.Fatalf("unexpected roaming status: %+v", settings)
	}
}

func TestParseDataBearerTechnologyResponseOutOfCallKeepsLast(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			qmiErrorResultTLV(QMIErrOutOfCall),
			{Type: 0x10, Value: []byte{0x0A}},
		},
	}
	info, err := parseDataBearerTechnologyResponse(resp)
	var outOfCall *OutOfCallError
	if !errors.As(err, &outOfCall) {
		t.Fatalf("expected OutOfCallError, got %v", err)
	}
	if !info.HasLast || info.Last != DataBearerTechnology(0x0A) {
		t.Fatalf("unexpected last bearer info: %+v", info)
	}
}

func TestParseCurrentBearerTechnologyResponse(t *testing.T) {
	current := make([]byte, 9)
	current[0] = WDSNetworkType3GPP
	binary.LittleEndian.PutUint32(current[1:5], 0x11223344)
	binary.LittleEndian.PutUint32(current[5:9], 0x55667788)

	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: current},
		},
	}
	info, err := parseCurrentBearerTechnologyResponse(resp)
	if err != nil {
		t.Fatalf("parseCurrentBearerTechnologyResponse returned error: %v", err)
	}
	if !info.HasCurrent {
		t.Fatalf("expected current bearer info, got %+v", info)
	}
	if info.Current.NetworkType != WDSNetworkType3GPP || info.Current.RATMask != 0x11223344 || info.Current.SOMask != 0x55667788 {
		t.Fatalf("unexpected current bearer info: %+v", info.Current)
	}
}

func TestParseCreateProfileResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			{Type: 0x01, Value: []byte{WDSProfileType3GPP, 0x07}},
		},
	}
	profile, err := parseCreateProfileResponse(resp, "iot")
	if err != nil {
		t.Fatalf("parseCreateProfileResponse returned error: %v", err)
	}
	if profile.Type != WDSProfileType3GPP || profile.Index != 0x07 || profile.Name != "iot" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
}

func TestParsePacketStatisticsResponse(t *testing.T) {
	resp := &Packet{
		TLVs: []TLV{
			successResultTLV(),
			wdsTLVUint32(0x10, 10),
			wdsTLVUint32(0x11, 20),
			wdsTLVUint64(0x19, 300),
			wdsTLVUint64(0x1A, 400),
			wdsTLVUint32(0x1D, 5),
			wdsTLVUint32(0x1E, 6),
		},
	}
	stats, err := parsePacketStatisticsResponse(resp)
	if err != nil {
		t.Fatalf("parsePacketStatisticsResponse returned error: %v", err)
	}
	if stats.TxPacketsOK != 10 || stats.RxPacketsOK != 20 || stats.TxBytesOK != 300 || stats.RxBytesOK != 400 {
		t.Fatalf("unexpected counters: %+v", stats)
	}
	if stats.TxPacketsDropped != 5 || stats.RxPacketsDropped != 6 {
		t.Fatalf("unexpected dropped counters: %+v", stats)
	}
	if stats.PresentMask != (WDSPacketStatsTxPacketsOK | WDSPacketStatsRxPacketsOK | WDSPacketStatsTxBytesOK | WDSPacketStatsRxBytesOK | WDSPacketStatsTxPacketsDropped | WDSPacketStatsRxPacketsDropped) {
		t.Fatalf("unexpected present mask: 0x%X", stats.PresentMask)
	}
}

func TestParseRuntimeSettingsPCSCFAndIMCN(t *testing.T) {
	resp := &Packet{TLVs: []TLV{
		successResultTLV(),
		{Type: TLVWDSPCSCFUsingPCO, Value: []byte{0x01}},
		{Type: TLVWDSPCSCFServerAddrList, Value: []byte{
			0x02,
			0x01, 0x02, 0x03, 0x0A, // 10.3.2.1
			0x05, 0x00, 0x00, 0x0A, // 10.0.0.5
		}},
		{Type: TLVWDSPCSCFDomainList, Value: append(
			[]byte{0x01, 0x0B, 0x00}, []byte("pcscf.ims.x")...)},
		{Type: TLVWDSIMCNFlag, Value: []byte{0x01}},
	}}

	s := parseRuntimeSettings(resp)

	if !s.PCSCFUsingPCO {
		t.Fatal("PCSCFUsingPCO should be true")
	}
	if len(s.PCSCFv4) != 2 {
		t.Fatalf("expected 2 P-CSCF addresses, got %d", len(s.PCSCFv4))
	}
	if got := s.PCSCFv4[0].String(); got != "10.3.2.1" {
		t.Fatalf("first P-CSCF = %s, want 10.3.2.1", got)
	}
	if got := s.PCSCFv4[1].String(); got != "10.0.0.5" {
		t.Fatalf("second P-CSCF = %s, want 10.0.0.5", got)
	}
	if len(s.PCSCFDomains) != 1 || s.PCSCFDomains[0] != "pcscf.ims.x" {
		t.Fatalf("unexpected P-CSCF domains: %#v", s.PCSCFDomains)
	}
	if !s.IMCN {
		t.Fatal("IMCN should be true")
	}
}

func TestParseRuntimeSettingsTruncatedPCSCFListIsIgnored(t *testing.T) {
	resp := &Packet{TLVs: []TLV{
		successResultTLV(),
		// count says 2 but only one address follows
		{Type: TLVWDSPCSCFServerAddrList, Value: []byte{0x02, 0x01, 0x02, 0x03, 0x0A}},
	}}

	s := parseRuntimeSettings(resp)

	if len(s.PCSCFv4) != 1 {
		t.Fatalf("expected the one complete address to survive, got %d", len(s.PCSCFv4))
	}
}

func TestParseRuntimeSettingsStillParsesIPv4AndMTU(t *testing.T) {
	resp := &Packet{TLVs: []TLV{
		successResultTLV(),
		{Type: TLVWDSIPv4Address, Value: []byte{0x01, 0x02, 0x03, 0x0A}},
		wdsTLVUint32(TLVWDSMtu, 1420),
	}}

	s := parseRuntimeSettings(resp)

	if got := s.IPv4Address.String(); got != "10.3.2.1" {
		t.Fatalf("IPv4Address = %s, want 10.3.2.1", got)
	}
	if s.MTU != 1420 {
		t.Fatalf("MTU = %d, want 1420", s.MTU)
	}
}

func TestParsePacketServiceStatusIndication(t *testing.T) {
	packet := &Packet{
		TLVs: []TLV{
			{Type: 0x01, Value: []byte{byte(StatusAuthenticating)}},
		},
	}

	status, err := ParsePacketServiceStatusIndication(packet)
	if err != nil {
		t.Fatalf("ParsePacketServiceStatusIndication returned error: %v", err)
	}
	if status != StatusAuthenticating {
		t.Fatalf("unexpected packet service status indication: %v", status)
	}
}
