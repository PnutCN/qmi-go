//go:build linux

package qmi

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
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

// TestOpenProxyTransportStartsProxyOnlyOnceUnderConcurrency 锁住防惊群。
//
// 没有这条保护时，N 台设备并发初始化会各自 dial 失败、各自 fork 一个 qmi-proxy，
// 而 abstract socket 只有一个能 bind 成功 —— 其余立刻退出。配合当年 Release()
// 不收尸，就在宿主上堆出一批僵尸；实测僵尸到 7 个之后新连接开始
// "context deadline exceeded"，六台模组里三台一起掉线。
func TestOpenProxyTransportStartsProxyOnlyOnceUnderConcurrency(t *testing.T) {
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

	var mu sync.Mutex
	starts := 0
	proxyUp := false
	var conns []net.Conn

	dialProxyHook = func(context.Context, string) (qmiTransport, error) {
		mu.Lock()
		defer mu.Unlock()
		if !proxyUp {
			return nil, errors.New("proxy socket not ready")
		}
		clientConn, serverConn := net.Pipe()
		conns = append(conns, clientConn, serverConn)
		return clientConn, nil
	}
	startProxyProcessHook = func(string) error {
		mu.Lock()
		defer mu.Unlock()
		starts++
		proxyUp = true // 拉起之后后续 dial 都能成功
		return nil
	}
	proxyRetryDelay = time.Millisecond

	// Act —— 模拟六台设备同时初始化
	const callers = 6
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
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
		for _, c := range conns {
			_ = c.Close()
		}
	})

	// Assert
	for i, err := range errs {
		if err != nil {
			t.Errorf("第 %d 个调用者失败: %v", i, err)
		}
	}
	mu.Lock()
	got := starts
	mu.Unlock()
	if got != 1 {
		t.Fatalf("qmi-proxy 被拉起 %d 次，应恰好 1 次 —— 多余的那些会 bind 失败退出并变成僵尸", got)
	}
}
