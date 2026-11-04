package qmi

import (
	"context"
	"encoding/binary"
	"testing"
)

func TestReadActiveAPNQueriesOnlyCurrentSettingsAndRequiresNameTLV(t *testing.T) {
	for _, scenario := range []string{"normal", "empty", "missing", "duplicate", "control", "error"} {
		t.Run(scenario, func(t *testing.T) {
			client := newUIMUnitTestClient()
			stop := serveUIMUnitTestRequests(t, client, func(req *Packet) *Packet {
				if req.MessageID != WDSGetRuntimeSettings || req.ClientID != 7 {
					t.Fatalf("unexpected request: %+v", req)
				}
				mask := FindTLV(req.TLVs, 0x10)
				if mask == nil || len(mask.Value) != 4 || binary.LittleEndian.Uint32(mask.Value) != RuntimeMaskAPNName {
					t.Fatal("requested more than the APN")
				}
				if scenario == "error" {
					return &Packet{TLVs: []TLV{qmiErrorResultTLV(QMIErrOutOfCall)}}
				}
				tlvs := []TLV{successResultTLV()}
				value := []byte(" Iijmio.jp ")
				if scenario == "empty" {
					value = nil
				}
				if scenario == "control" {
					value = []byte("name\x00")
				}
				if scenario != "missing" {
					tlvs = append(tlvs, TLV{Type: 0x14, Value: value})
				}
				if scenario == "duplicate" {
					tlvs = append(tlvs, TLV{Type: 0x14, Value: value})
				}
				return &Packet{TLVs: tlvs}
			})
			defer stop()
			apn, err := (&WDSService{client: client, clientID: 7}).ReadActiveAPN(context.Background())
			valid := scenario == "normal" || scenario == "empty"
			if (err == nil) != valid {
				t.Fatalf("scenario=%s err=%v", scenario, err)
			}
			if scenario == "normal" && apn != " Iijmio.jp " {
				t.Fatal("APN was trimmed or rewritten")
			}
		})
	}
}
