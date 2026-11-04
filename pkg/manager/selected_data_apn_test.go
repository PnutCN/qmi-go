package manager

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/iniwex5/qmi-go/pkg/qmi"
)

func TestSelectedDataAPNUsesOwnedDefaultClientAndChecksConnectionTwice(t *testing.T) {
	for _, scenario := range []string{"v4", "v6", "disconnect", "changed", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			m := newRecoveryTestManager()
			client := newUIMReadinessTestClient(t)
			wds := &qmi.WDSService{}
			setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("client"), reflect.ValueOf(client))
			setUnexportedField(t, reflect.ValueOf(wds).Elem().FieldByName("clientID"), reflect.ValueOf(uint8(7)))
			m.state = StateConnected
			m.cfg.APN = "stored-old-card.apn"
			if scenario == "v6" {
				m.wdsV6, m.handleV6 = wds, 10
			} else {
				m.wds, m.handleV4 = wds, 20
			}
			statuses := 0
			serveUIMReadinessTestRequests(t, client, func(req *qmi.Packet) (*qmi.Packet, error) {
				if req.ClientID != 7 {
					t.Fatal("used a different WDS client")
				}
				tlvs := []qmi.TLV{{Type: 0x02, Value: []byte{0, 0, 0, 0}}}
				switch req.MessageID {
				case qmi.WDSGetPktSrvcStatus:
					statuses++
					status := qmi.StatusConnected
					if scenario == "disconnect" && statuses == 2 {
						status = qmi.StatusDisconnected
					}
					tlvs = append(tlvs, qmi.TLV{Type: 0x01, Value: []byte{byte(status)}})
				case qmi.WDSGetRuntimeSettings:
					if scenario != "missing" {
						tlvs = append(tlvs, qmi.TLV{Type: 0x14, Value: []byte("actual.apn")})
					}
					if scenario == "changed" {
						m.mu.Lock()
						m.handleV4++
						m.mu.Unlock()
					}
				default:
					t.Fatalf("read-only APN lookup sent 0x%x", req.MessageID)
				}
				return &qmi.Packet{TLVs: tlvs}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			apn, err := m.ReadSelectedDataAPN(ctx)
			valid := scenario == "v4" || scenario == "v6"
			if (err == nil) != valid {
				t.Fatalf("scenario %s: %v", scenario, err)
			}
			if valid && (apn != "actual.apn" || statuses != 2) {
				t.Fatal("used configured APN or failed to recheck active call")
			}
		})
	}
}

func TestSelectedDataAPNRejectsUnownedOrDisconnectedSettings(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.APN = "stored-old-card.apn"
	if _, err := m.ReadSelectedDataAPN(context.Background()); err == nil {
		t.Fatal("disconnected config became live APN")
	}
	m.state = StateConnected
	if _, err := m.ReadSelectedDataAPN(context.Background()); err == nil {
		t.Fatal("unowned cached settings became live APN")
	}
	m.dataOpMu.Lock()
	defer m.dataOpMu.Unlock()
	if _, err := m.ReadSelectedDataAPN(context.Background()); err == nil {
		t.Fatal("lookup waited on a mutating data operation")
	}
}
