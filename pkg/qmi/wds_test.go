package qmi

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"reflect"
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

func TestStartNetworkInterfaceProfileIndex(t *testing.T) {
	tests := []struct {
		name         string
		profileIndex uint8
		wantProfile  []byte
	}{
		{name: "zero omits profile TLV", profileIndex: 0, wantProfile: nil},
		{name: "nonzero includes profile TLV", profileIndex: 7, wantProfile: []byte{7}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newUIMUnitTestClient()
			stop := serveUIMUnitTestRequests(t, client, func(req *Packet) *Packet {
				switch req.MessageID {
				case WDSSetClientIPFamilyPref:
					return &Packet{TLVs: []TLV{successResultTLV()}}
				case WDSStartNetworkInterface:
					tlv := FindTLV(req.TLVs, 0x31)
					if tt.wantProfile == nil {
						if legacy := FindTLV(req.TLVs, 0x30); legacy != nil {
							t.Fatalf("legacy profile TLV = %+v, want absent", legacy)
						}
						if tlv != nil {
							t.Fatalf("3GPP profile TLV = %+v, want absent", tlv)
						}
					} else if tlv == nil || !sameBytes(tlv.Value, tt.wantProfile) {
						t.Fatalf("3GPP profile TLV = %+v, want %v", tlv, tt.wantProfile)
					} else if legacy := FindTLV(req.TLVs, 0x30); legacy != nil {
						t.Fatalf("legacy profile TLV = %+v, want absent", legacy)
					}
					return &Packet{TLVs: []TLV{successResultTLV(), wdsTLVUint32(0x01, 42)}}
				default:
					t.Fatalf("unexpected message ID 0x%04x", req.MessageID)
					return nil
				}
			})
			defer stop()

			wds := &WDSService{client: client, clientID: 1, ProfileIndex: tt.profileIndex}
			if _, err := wds.StartNetworkInterface(context.Background(), "ims", "", "", 0, IpFamilyV6); err != nil {
				t.Fatalf("StartNetworkInterface() error = %v", err)
			}
		})
	}
}

// TestParseRuntimeSettingsPCSCFIPv6List pins the decoding of TLV 0x2E against
// the exact bytes an EM9190 returned on a China Unicom IMS bearer, and against
// the malformed shapes a walker over attacker- or firmware-supplied lengths
// has to survive.
//
// This replaces an earlier test that asserted 0x2E must be ignored. That
// assertion was written without hardware evidence; a dump of every TLV the
// modem returns then showed 0x2E carrying exactly the P-CSCF addresses the
// same bearer reports via AT+CGCONTRDP, so ignoring it was throwing away the
// only source of an IPv6 P-CSCF that QMI has.
func TestParseRuntimeSettingsPCSCFIPv6List(t *testing.T) {
	ip := func(s string) []byte { return net.ParseIP(s).To16() }

	tests := []struct {
		name  string
		value []byte
		want  []string
	}{
		{
			name: "two addresses, as measured on an EM9190",
			value: concatBytes([]byte{0x02},
				ip("2408:8141:e001:2000::50"), ip("2408:8141:e001:2000::60")),
			want: []string{"2408:8141:e001:2000::50", "2408:8141:e001:2000::60"},
		},
		{
			name:  "empty list: the shape sent when the network delivered no P-CSCF",
			value: []byte{0x00},
			want:  nil,
		},
		{
			name:  "count overstates the entries actually present",
			value: concatBytes([]byte{0x03}, ip("2408:8141:e001:2000::50")),
			want:  []string{"2408:8141:e001:2000::50"},
		},
		{
			name:  "trailing entry is truncated mid-address",
			value: concatBytes([]byte{0x02}, ip("2408:8141:e001:2000::50"), []byte{0x24, 0x08}),
			want:  []string{"2408:8141:e001:2000::50"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := parseRuntimeSettings(&Packet{TLVs: []TLV{
				successResultTLV(),
				{Type: TLVWDSPCSCFServerAddrListV6, Value: tt.value},
			}})
			var got []string
			for _, addr := range settings.PCSCFv6 {
				got = append(got, addr.String())
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("PCSCFv6 = %v, want %v", got, tt.want)
			}
			if len(settings.PCSCFv4) != 0 {
				t.Fatalf("IPv6 list must not leak into PCSCFv4: %v", settings.PCSCFv4)
			}
		})
	}
}

// The two address lists are separate TLVs and must stay in separate fields:
// a bearer reporting one must never populate the other.
func TestParseRuntimeSettingsKeepsPCSCFFamiliesSeparate(t *testing.T) {
	settings := parseRuntimeSettings(&Packet{TLVs: []TLV{
		successResultTLV(),
		{Type: TLVWDSPCSCFServerAddrList, Value: []byte{0x01, 0x04, 0x03, 0x02, 0x0A}},
		{Type: TLVWDSPCSCFServerAddrListV6, Value: append([]byte{0x01}, net.ParseIP("2408:8141:e001:2000::50").To16()...)},
	}})
	if len(settings.PCSCFv4) != 1 || settings.PCSCFv4[0].String() != "10.2.3.4" {
		t.Fatalf("PCSCFv4 = %v, want [10.2.3.4]", settings.PCSCFv4)
	}
	if len(settings.PCSCFv6) != 1 || settings.PCSCFv6[0].String() != "2408:8141:e001:2000::50" {
		t.Fatalf("PCSCFv6 = %v, want [2408:8141:e001:2000::50]", settings.PCSCFv6)
	}
}

func concatBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// TestParseRuntimeSettingsPCSCFDomainListTruncation pins the safety of the
// TLVWDSPCSCFDomainList walker against malformed input: an overstated entry
// count, a string length that runs past the end of the TLV body, and a
// truncated length prefix must all be ignored without panicking or looping,
// keeping only the complete entries that were actually readable. It also
// covers the well-formed multi-entry happy path.
func TestParseRuntimeSettingsPCSCFDomainListTruncation(t *testing.T) {
	// lengthPrefixed builds one well-formed entry: a little-endian uint16
	// byte length followed by the raw string bytes.
	lengthPrefixed := func(s string) []byte {
		b := make([]byte, 2, 2+len(s))
		binary.LittleEndian.PutUint16(b, uint16(len(s)))
		return append(b, s...)
	}

	tests := []struct {
		name        string
		value       []byte
		wantDomains []string
	}{
		{
			name: "count overstates entries: only one complete FQDN present",
			// count=3 but only one length-prefixed entry actually follows
			value:       append([]byte{0x03}, lengthPrefixed("abc")...),
			wantDomains: []string{"abc"},
		},
		{
			name: "declared string length runs past end of TLV body",
			// count=1, length prefix says 20, only 5 bytes follow
			value:       append([]byte{0x01, 0x14, 0x00}, []byte("hello")...),
			wantDomains: nil,
		},
		{
			name: "truncated length prefix",
			// count=1, a single trailing byte where 2 are needed for the length
			value:       []byte{0x01, 0xAB},
			wantDomains: nil,
		},
		{
			name: "well-formed multi-entry list",
			value: append(
				append([]byte{0x02}, lengthPrefixed("a.com")...),
				lengthPrefixed("b.example.org")...,
			),
			wantDomains: []string{"a.com", "b.example.org"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &Packet{TLVs: []TLV{
				successResultTLV(),
				{Type: TLVWDSPCSCFDomainList, Value: tt.value},
			}}

			s := parseRuntimeSettings(resp)

			if len(s.PCSCFDomains) != len(tt.wantDomains) {
				t.Fatalf("got %d domains %#v, want %d domains %#v", len(s.PCSCFDomains), s.PCSCFDomains, len(tt.wantDomains), tt.wantDomains)
			}
			for i := range tt.wantDomains {
				if s.PCSCFDomains[i] != tt.wantDomains[i] {
					t.Fatalf("domain[%d] = %q, want %q", i, s.PCSCFDomains[i], tt.wantDomains[i])
				}
			}
		})
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

// TestParseRuntimeSettingsPCSCFUsingPCOAndIMCNZeroValues covers the false
// paths for the two boolean flags, which were previously only exercised with
// 0x01: an explicit 0x00 byte must parse as false, and the flags must also
// default to false when their TLVs are absent entirely.
func TestParseRuntimeSettingsPCSCFUsingPCOAndIMCNZeroValues(t *testing.T) {
	t.Run("explicit zero byte parses as false", func(t *testing.T) {
		resp := &Packet{TLVs: []TLV{
			successResultTLV(),
			{Type: TLVWDSPCSCFUsingPCO, Value: []byte{0x00}},
			{Type: TLVWDSIMCNFlag, Value: []byte{0x00}},
		}}

		s := parseRuntimeSettings(resp)

		if s.PCSCFUsingPCO {
			t.Fatal("PCSCFUsingPCO should be false when TLV byte is 0x00")
		}
		if s.IMCN {
			t.Fatal("IMCN should be false when TLV byte is 0x00")
		}
	})

	t.Run("absent TLVs default to false", func(t *testing.T) {
		resp := &Packet{TLVs: []TLV{
			successResultTLV(),
		}}

		s := parseRuntimeSettings(resp)

		if s.PCSCFUsingPCO {
			t.Fatal("PCSCFUsingPCO should be false when TLV is absent")
		}
		if s.IMCN {
			t.Fatal("IMCN should be false when TLV is absent")
		}
	})
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

// Call end reason codes are only meaningful together with their type: 241
// under the internal type means the modem holds a matching call, while the
// same number under another type is unrelated. These predicates must key on
// both, or a 3GPP reason would be misread as a local modem condition.
func TestCallEndReasonPredicatesKeyOnTypeAndCode(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reason       *CallEndReason
		wantInUse    bool
		wantIPFamily bool
	}{
		{"nil", nil, false, false},
		{"interface in use", &CallEndReason{Type: CallEndReasonTypeInternal, Code: CallEndReasonInternalInterfaceInUseConfigMatch}, true, false},
		{"ipv4 disallowed", &CallEndReason{Type: CallEndReasonTypeInternal, Code: CallEndReasonInternalPDNIPv4CallDisallowed}, false, true},
		{"ipv6 disallowed", &CallEndReason{Type: CallEndReasonTypeInternal, Code: CallEndReasonInternalPDNIPv6CallDisallowed}, false, true},
		// Measured on an EM9190: the same IPv4-on-an-IPv6-IMS-APN request that
		// an EC25 refuses with 208 comes back as 231 here.
		{"ip version mismatch", &CallEndReason{Type: CallEndReasonTypeInternal, Code: CallEndReasonInternalIPVersionMismatch}, false, true},
		{"same codes under 3gpp type", &CallEndReason{Type: 6, Code: CallEndReasonInternalInterfaceInUseConfigMatch}, false, false},
		{"3gpp regular deactivation", &CallEndReason{Type: 6, Code: 36}, false, false},
		{"unknown internal code", &CallEndReason{Type: CallEndReasonTypeInternal, Code: 1}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.reason.IsInterfaceInUseConfigMatch(); got != tc.wantInUse {
				t.Fatalf("IsInterfaceInUseConfigMatch() = %v, want %v", got, tc.wantInUse)
			}
			if got := tc.reason.IsIPFamilyDisallowed(); got != tc.wantIPFamily {
				t.Fatalf("IsIPFamilyDisallowed() = %v, want %v", got, tc.wantIPFamily)
			}
		})
	}
}
