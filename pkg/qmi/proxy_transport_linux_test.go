//go:build linux

package qmi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestProxySocketAddressNormalizesCommonAbstractSocketNames(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "default", in: "", want: "\x00qmi-proxy"},
		{name: "plain", in: "qmi-proxy", want: "\x00qmi-proxy"},
		{name: "at prefix", in: "@qmi-proxy", want: "\x00qmi-proxy"},
		{name: "nul prefix", in: "\x00qmi-proxy", want: "\x00qmi-proxy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proxySocketAddress(tt.in); got != tt.want {
				t.Fatalf("proxySocketAddress(%q)=%q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestOpenProxyTransportRetriesUntilContextDeadline(t *testing.T) {
	proxyExecutable := filepath.Join(t.TempDir(), "qmi-proxy")
	if err := os.WriteFile(proxyExecutable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldDial := dialProxyHook
	oldStart := startProxyProcessHook
	oldRetryDelay := proxyRetryDelay
	t.Cleanup(func() {
		dialProxyHook = oldDial
		startProxyProcessHook = oldStart
		proxyRetryDelay = oldRetryDelay
	})

	attempts := 0
	starts := 0
	dialProxyHook = func(context.Context, string) (qmiTransport, error) {
		attempts++
		return nil, errors.New("proxy socket not ready")
	}
	startProxyProcessHook = func(string) error {
		starts++
		return nil
	}
	proxyRetryDelay = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := openProxyTransport(ctx, ClientOptions{
		ProxyPath:       "@qmi-proxy",
		ProxyExecutable: proxyExecutable,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("openProxyTransport() error=%v, want context deadline exceeded", err)
	}
	if starts != 1 {
		t.Fatalf("start attempts=%d, want 1", starts)
	}
	if attempts < 3 {
		t.Fatalf("dial attempts=%d, want at least 3 retries before deadline", attempts)
	}
}

func TestOpenProxyTransportRetriesUntilProxyIsReady(t *testing.T) {
	proxyExecutable := filepath.Join(t.TempDir(), "qmi-proxy")
	if err := os.WriteFile(proxyExecutable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldDial := dialProxyHook
	oldStart := startProxyProcessHook
	oldRetryDelay := proxyRetryDelay
	t.Cleanup(func() {
		dialProxyHook = oldDial
		startProxyProcessHook = oldStart
		proxyRetryDelay = oldRetryDelay
	})

	attempts := 0
	starts := 0
	var serverConn net.Conn
	dialProxyHook = func(context.Context, string) (qmiTransport, error) {
		attempts++
		if attempts < 4 {
			return nil, errors.New("proxy socket not ready")
		}
		clientConn, conn := net.Pipe()
		serverConn = conn
		return clientConn, nil
	}
	startProxyProcessHook = func(string) error {
		starts++
		return nil
	}
	proxyRetryDelay = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conn, err := openProxyTransport(ctx, ClientOptions{
		ProxyPath:       "\x00qmi-proxy",
		ProxyExecutable: proxyExecutable,
	})
	if err != nil {
		t.Fatalf("openProxyTransport() error=%v", err)
	}
	defer conn.Close()
	if serverConn != nil {
		defer serverConn.Close()
	}
	if starts != 1 {
		t.Fatalf("start attempts=%d, want 1", starts)
	}
	if attempts != 4 {
		t.Fatalf("dial attempts=%d, want 4", attempts)
	}
}

func TestOpenProxyTransportDoesNotForkDuringProxyReadinessWindow(t *testing.T) {
	proxyExecutable := filepath.Join(t.TempDir(), "qmi-proxy")
	if err := os.WriteFile(proxyExecutable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldDial := dialProxyHook
	oldStart := startProxyProcessHook
	oldRetryDelay := proxyRetryDelay
	t.Cleanup(func() {
		dialProxyHook = oldDial
		startProxyProcessHook = oldStart
		proxyRetryDelay = oldRetryDelay
	})

	const callers = 6
	var mu sync.Mutex
	firstDials := 0
	starts := 0
	proxyUp := false
	var conns []net.Conn
	firstRoundDone := make(chan struct{})
	firstRoundOnce := sync.Once{}
	startsObserved := make(chan struct{})
	startsObservedOnce := sync.Once{}

	dialProxyHook = func(ctx context.Context, _ string) (qmiTransport, error) {
		mu.Lock()
		firstDials++
		call := firstDials
		if call == callers {
			firstRoundOnce.Do(func() { close(firstRoundDone) })
		}
		up := proxyUp
		mu.Unlock()

		if call <= callers {
			select {
			case <-firstRoundDone:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return nil, errors.New("proxy socket not ready")
		}
		if !up {
			return nil, errors.New("proxy socket not ready")
		}
		clientConn, serverConn := net.Pipe()
		mu.Lock()
		conns = append(conns, clientConn, serverConn)
		mu.Unlock()
		return clientConn, nil
	}

	startProxyProcessHook = func(string) error {
		mu.Lock()
		starts++
		if starts == callers {
			startsObservedOnce.Do(func() { close(startsObserved) })
		}
		mu.Unlock()
		return nil
	}

	// Simulate a real daemon: starting the process returns first, and the
	// abstract socket becomes reachable shortly afterwards. If all callers
	// fork before then, the implementation has no readiness coordination.
	go func() {
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-startsObserved:
		case <-timer.C:
		}
		mu.Lock()
		proxyUp = true
		mu.Unlock()
	}()
	proxyRetryDelay = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			conn, err := openProxyTransport(ctx, ClientOptions{
				ProxyPath:       "@qmi-proxy",
				ProxyExecutable: proxyExecutable,
			})
			errs[idx] = err
			if conn != nil {
				_ = conn.Close()
			}
		}(i)
	}
	wg.Wait()

	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d failed: %v", i, err)
		}
	}
	mu.Lock()
	gotStarts := starts
	mu.Unlock()
	if gotStarts != 1 {
		t.Fatalf("qmi-proxy started %d times during readiness window, want 1", gotStarts)
	}
}

func TestStartProxyProcessReapsExitedChild(t *testing.T) {
	tempDir := t.TempDir()
	proxyExecutable := filepath.Join(tempDir, "qmi-proxy")
	pidFile := filepath.Join(tempDir, "pid")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$$\" > %s\nexit 0\n", pidFile)
	if err := os.WriteFile(proxyExecutable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := startProxyProcess(proxyExecutable); err != nil {
		t.Fatalf("startProxyProcess() error = %v", err)
	}

	deadline := time.Now().Add(time.Second)
	pid := 0
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("proxy child did not publish its pid")
	}
	t.Cleanup(func() {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	})

	for time.Now().Before(deadline) {
		state, exists := proxyProcessState(pid)
		if !exists {
			return
		}
		if state == 'Z' {
			t.Fatalf("proxy child %d became a zombie", pid)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("proxy child %d was not reaped before timeout", pid)
}

func proxyProcessState(pid int) (byte, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return 0, false
	}
	if err != nil {
		return 0, true
	}
	line := string(data)
	endCommand := strings.LastIndex(line, ") ")
	if endCommand < 0 || endCommand+2 >= len(line) {
		return 0, true
	}
	return line[endCommand+2], true
}
