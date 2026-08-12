package manager

import (
	"context"
	"testing"
	"time"
)

func TestIMSCardAccessBarrierBlocksBackgroundCardAccess(t *testing.T) {
	m := New(Config{}, nil)
	readStarted := make(chan struct{})
	readRelease := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		_, _ = withCardAccessValue(m, context.Background(), func() (struct{}, error) {
			close(readStarted)
			<-readRelease
			return struct{}{}, nil
		})
		close(readDone)
	}()

	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("background card read did not start")
	}

	barrierReady := make(chan func(), 1)
	barrierErr := make(chan error, 1)
	go func() {
		release, err := m.BeginIMSCardAccessBarrier(context.Background())
		if err != nil {
			barrierErr <- err
			return
		}
		barrierReady <- release
	}()

	select {
	case err := <-barrierErr:
		t.Fatalf("BeginIMSCardAccessBarrier() error = %v", err)
	case <-barrierReady:
		t.Fatal("IMS card barrier acquired while a background card read was active")
	case <-time.After(50 * time.Millisecond):
	}

	close(readRelease)
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("background card read did not finish")
	}

	var release func()
	select {
	case release = <-barrierReady:
	case err := <-barrierErr:
		t.Fatalf("BeginIMSCardAccessBarrier() error = %v", err)
	case <-time.After(time.Second):
		t.Fatal("IMS card barrier did not acquire after the active read finished")
	}

	secondReadStarted := make(chan struct{})
	go func() {
		_, _ = withCardAccessValue(m, context.Background(), func() (struct{}, error) {
			close(secondReadStarted)
			return struct{}{}, nil
		})
	}()
	select {
	case <-secondReadStarted:
		t.Fatal("background card read entered while IMS card barrier was active")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-secondReadStarted:
	case <-time.After(time.Second):
		t.Fatal("background card read did not resume after IMS card barrier release")
	}
}
