package qmi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestClientLogfUsesInjectedLogger(t *testing.T) {
	var gotLevel ClientLogLevel
	var gotMessage string
	c := &Client{
		opts: ClientOptions{
			Logf: func(level ClientLogLevel, format string, args ...any) {
				gotLevel = level
				gotMessage = fmt.Sprintf(format, args...)
			},
		},
	}

	c.logf(ClientLogLevelDebug, "hello %s", "qmi")

	if gotLevel != ClientLogLevelDebug {
		t.Fatalf("level=%v want %v", gotLevel, ClientLogLevelDebug)
	}
	if gotMessage != "hello qmi" {
		t.Fatalf("message=%q want %q", gotMessage, "hello qmi")
	}
}

func TestDispatchIndicationClassifiesNASEventReportSeparately(t *testing.T) {
	c := &Client{
		eventCh:            make(chan Event, 1),
		indicationInCh:     nil,
		closeCh:            make(chan struct{}),
		transactions:       make(map[uint32]*transactionEntry),
		recentTransactions: make(map[uint32]recentTransaction),
		clientIDs:          make(map[uint16]uint8),
	}

	c.dispatchIndication(&Packet{
		ServiceType:  ServiceNAS,
		MessageID:    NASEventReportInd,
		IsIndication: true,
	})

	got := <-c.eventCh
	if got.Type != EventNASEventReport {
		t.Fatalf("event type=%v want EventNASEventReport", got.Type)
	}
	if got.ServiceID != ServiceNAS || got.MessageID != NASEventReportInd {
		t.Fatalf("raw ids service=0x%02x msg=0x%04x", got.ServiceID, got.MessageID)
	}
}

func TestDispatchIndicationClassifiesCellLocationInfo(t *testing.T) {
	c := &Client{
		eventCh:            make(chan Event, 1),
		closeCh:            make(chan struct{}),
		transactions:       make(map[uint32]*transactionEntry),
		recentTransactions: make(map[uint32]recentTransaction),
		clientIDs:          make(map[uint16]uint8),
	}
	c.dispatchIndication(&Packet{ServiceType: ServiceNAS, MessageID: NASGetCellLocationInfo, IsIndication: true})
	if got := <-c.eventCh; got.Type != EventNASCellLocationInfoChanged {
		t.Fatalf("event type=%v want EventNASCellLocationInfoChanged", got.Type)
	}
}

func TestModemResetIndicationNotDroppedWhenQueueFull(t *testing.T) {
	c := &Client{
		opts:              ClientOptions{ReadDeadline: 5 * time.Millisecond},
		eventCh:           make(chan Event, 4),
		indicationInCh:    make(chan Event, 1),
		coalescedSignalCh: make(chan struct{}, 1),
		closeCh:           make(chan struct{}),
		coalesced: coalescedEventStore{
			events: make(map[string]Event),
		},
	}

	// Fill indication queue first so enqueueIndication must use coalesced fallback.
	c.indicationInCh <- Event{Type: EventUnknown}
	c.enqueueIndication(Event{
		Type:      EventModemReset,
		ServiceID: ServiceControl,
		MessageID: CTLRevokeClientIDInd,
	})

	done := make(chan struct{})
	c.wg.Add(1)
	go func() {
		c.indicationLoop()
		close(done)
	}()
	defer func() {
		close(c.closeCh)
		<-done
	}()

	deadline := time.After(1 * time.Second)
	for {
		select {
		case evt := <-c.eventCh:
			if evt.Type == EventModemReset {
				return
			}
		case <-deadline:
			t.Fatal("expected EventModemReset to be delivered")
		}
	}
}

func TestSendRequestWithCanceledContextDoesNotQueueWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &Client{
		opts:         DefaultClientOptions(),
		writeCh:      make(chan writeRequest, 1),
		closeCh:      make(chan struct{}),
		transactions: make(map[uint32]*transactionEntry),
	}

	_, err := c.SendRequest(ctx, ServiceUIM, 1, UIMReadRecord, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := len(c.writeCh); got != 0 {
		t.Fatalf("SendRequest queued %d write(s) after context cancellation", got)
	}
	if got := len(c.transactions); got != 0 {
		t.Fatalf("SendRequest left %d transaction(s) after context cancellation", got)
	}
}

func TestCompletedTimedOutTransactionIsRememberedForLateResponse(t *testing.T) {
	c := &Client{
		opts:               DefaultClientOptions(),
		writeCh:            make(chan writeRequest, 1),
		closeCh:            make(chan struct{}),
		transactions:       make(map[uint32]*transactionEntry),
		recentTransactions: make(map[uint32]recentTransaction),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := c.SendRequest(ctx, ServiceUIM, 1, UIMReadRecord, nil)
		errCh <- err
	}()

	wr := <-c.writeCh
	wr.result <- nil

	err := <-errCh
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}

	key := uint32(ServiceUIM)<<16 | 1
	if !c.isRecentTransaction(key, ServiceUIM, UIMReadRecord) {
		t.Fatalf("timed out UIMReadRecord transaction was not retained for late response matching")
	}
	if got := len(c.transactions); got != 0 {
		t.Fatalf("timed out request left %d active transaction(s)", got)
	}
}

func TestReadLoopDropsLateResponseAfterCanceledTransaction(t *testing.T) {
	conn, peer := net.Pipe()
	late := make(chan struct{}, 1)
	c := &Client{
		conn: conn,
		opts: ClientOptions{ReadDeadline: 5 * time.Millisecond, Logf: func(_ ClientLogLevel, format string, _ ...any) {
			if strings.Contains(format, "received late response") {
				late <- struct{}{}
			}
		}},
		eventCh:            make(chan Event, 1),
		writeCh:            make(chan writeRequest, 1),
		closeCh:            make(chan struct{}),
		transactions:       make(map[uint32]*transactionEntry),
		recentTransactions: make(map[uint32]recentTransaction),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := c.SendRequest(ctx, ServiceUIM, 1, UIMReadRecord, nil)
		result <- err
	}()
	wr := <-c.writeCh
	request, err := UnmarshalPacket(wr.data)
	if err != nil {
		t.Fatal(err)
	}
	wr.result <- nil
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("调用方取消未结束事务", err)
	}
	c.wg.Add(1)
	go c.readLoop()
	t.Cleanup(func() {
		close(c.closeCh)
		conn.Close()
		peer.Close()
		c.wg.Wait()
	})
	// 走真实帧读取/事务分派，迟到的普通响应不得被提升为SIM状态或其他异步事件。
	packet := (&Packet{ServiceType: ServiceUIM, ClientID: 1, TransactionID: request.TransactionID, MessageID: UIMReadRecord}).Marshal()
	packet[QmuxHeaderSize] = 0x02
	if _, err := peer.Write(packet); err != nil {
		t.Fatal(err)
	}
	select {
	case <-late:
	case <-time.After(time.Second):
		t.Fatal("真实readLoop未识别已取消事务的迟到响应")
	}
	select {
	case <-c.eventCh:
		t.Fatal("迟到应答被错误分派成异步事件")
	default:
	}
	key := uint32(ServiceUIM)<<16 | uint32(request.TransactionID)
	if c.unmatchedResponses.Load() != 1 || c.isRecentTransaction(key, ServiceUIM, UIMReadRecord) {
		t.Fatal("迟到响应没有消费已取消事务记录")
	}
}
