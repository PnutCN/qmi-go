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

// TestWaitForProxyStartRetriesWhenOwnerTimedOutButProxyIsUp 锁住这一点:
// **owner 的截止时间不该判等待者的死。**
//
// 各设备的 QMI 客户端创建各有各的超时,先抢到归属的那个 ctx 可能只剩几十毫秒,
// 于是它在 proxy 还没 bind 好时就放弃了。若把它的 error 原样抄给所有等待者,
// 一台设备的短超时会把并发初始化的其余几台一起判死 —— 而 proxy 其实已经起来了。
// 这正是"六台里三台一起掉线"的形态。
func TestWaitForProxyStartRetriesWhenOwnerTimedOutButProxyIsUp(t *testing.T) {
	oldDial := dialProxyHook
	t.Cleanup(func() { dialProxyHook = oldDial })

	var conns []net.Conn
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	// proxy 此刻**已经就绪** —— owner 只是没赶上自己的截止时间。
	dialProxyHook = func(context.Context, string) (qmiTransport, error) {
		clientConn, serverConn := net.Pipe()
		conns = append(conns, clientConn, serverConn)
		return clientConn, nil
	}

	attempt := &proxyStartAttempt{done: make(chan struct{})}
	attempt.err = fmt.Errorf("connect qmi-proxy after starting: %w", context.DeadlineExceeded)
	close(attempt.done)

	// 等待者自己的 ctx 还很充裕。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := waitForProxyStart(ctx, "@qmi-proxy", attempt)
	if err != nil {
		t.Fatalf("等待者应当自救成功，却继承了 owner 的超时: %v", err)
	}
	if conn == nil {
		t.Fatal("成功时必须返回连接")
	}
	_ = conn.Close()
}

// TestWaitForProxyStartReportsBothCausesWhenProxyIsAlsoDown 是上一条的反面:
// proxy 真的没起来时,等待者自救也失败 —— 此时两个原因都要带上,否则现场
// 分不清是"没拉起来"还是"拉起来了没赶上"。
func TestWaitForProxyStartReportsBothCausesWhenProxyIsAlsoDown(t *testing.T) {
	oldDial := dialProxyHook
	t.Cleanup(func() { dialProxyHook = oldDial })

	dialErr := errors.New("proxy socket not ready")
	dialProxyHook = func(context.Context, string) (qmiTransport, error) { return nil, dialErr }

	ownerErr := errors.New("start qmi-proxy failed")
	attempt := &proxyStartAttempt{done: make(chan struct{})}
	attempt.err = ownerErr
	close(attempt.done)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := waitForProxyStart(ctx, "@qmi-proxy", attempt)
	if err == nil {
		t.Fatal("proxy 没起来时必须失败")
	}
	if !errors.Is(err, ownerErr) {
		t.Errorf("错误里缺 owner 的原因: %v", err)
	}
	if !errors.Is(err, dialErr) {
		t.Errorf("错误里缺等待者自己 dial 的原因: %v", err)
	}
}
