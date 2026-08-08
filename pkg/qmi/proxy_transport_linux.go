//go:build linux

package qmi

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	dialProxyHook         = dialProxy
	startProxyProcessHook = startProxyProcess
	proxyRetryDelay       = 100 * time.Millisecond

	// proxyStartMu 串行化「拉起 qmi-proxy」这一步，防止并发初始化时惊群式
	// 重复 fork。见 openProxyTransport 里的说明。
	proxyStartMu sync.Mutex
)

func openProxyTransport(ctx context.Context, opts ClientOptions) (qmiTransport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	proxyPath := opts.ProxyPath
	if proxyPath == "" {
		proxyPath = defaultProxyPath
	}
	proxyExecutable := opts.ProxyExecutable
	if proxyExecutable == "" {
		proxyExecutable = defaultProxyExecutable
	}

	conn, firstErr := dialProxyHook(ctx, proxyPath)
	if firstErr == nil {
		return conn, nil
	}

	if proxyExecutable == "" {
		return nil, fmt.Errorf("connect qmi-proxy %q: %w", proxyPath, firstErr)
	}
	if _, err := os.Stat(proxyExecutable); err != nil {
		return nil, fmt.Errorf("connect qmi-proxy %q failed: %w; proxy executable %s is unavailable: %v", proxyPath, firstErr, proxyExecutable, err)
	}
	// **同一时刻只允许一个调用者去拉起 proxy。**
	//
	// 没有这把锁时，N 台设备并发初始化会各自 dial 失败、各自 fork 一个 qmi-proxy，
	// 而 abstract socket 只有一个能 bind 成功 —— 其余全部立刻退出变成僵尸。
	// 实测六台模组的宿主上一次就堆出 7 个。
	//
	// 持锁后**再 dial 一次**：等锁期间先到的那个多半已经把 proxy 拉起来了，
	// 此时直接复用，连 fork 都不必。
	proxyStartMu.Lock()
	if conn, err := dialProxyHook(ctx, proxyPath); err == nil {
		proxyStartMu.Unlock()
		return conn, nil
	}
	startErr := startProxyProcessHook(proxyExecutable)
	proxyStartMu.Unlock()
	if startErr != nil {
		return nil, fmt.Errorf("connect qmi-proxy %q failed and start %s failed: %w", proxyPath, proxyExecutable, startErr)
	}

	var lastErr error = firstErr
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("connect qmi-proxy %q after starting %s: last error: %v: %w", proxyPath, proxyExecutable, lastErr, err)
		}
		timer := time.NewTimer(proxyRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, fmt.Errorf("connect qmi-proxy %q after starting %s: last error: %v: %w", proxyPath, proxyExecutable, lastErr, ctx.Err())
		case <-timer.C:
		}
		conn, err := dialProxyHook(ctx, proxyPath)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
}

func dialProxy(ctx context.Context, proxyPath string) (qmiTransport, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", proxySocketAddress(proxyPath))
}

func proxySocketAddress(proxyPath string) string {
	if proxyPath == "" {
		proxyPath = defaultProxyPath
	}
	if strings.HasPrefix(proxyPath, "\x00") {
		return proxyPath
	}
	if strings.HasPrefix(proxyPath, "@") {
		return "\x00" + strings.TrimPrefix(proxyPath, "@")
	}
	return "\x00" + proxyPath
}

func startProxyProcess(proxyExecutable string) error {
	cmd := exec.Command(proxyExecutable)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// **必须收尸，不能用 Process.Release()。**
	//
	// Release 只是释放 Go 侧的 os.Process 句柄，不做 wait4 —— qmi-proxy 退出后
	// 会一直以僵尸挂在父进程名下，直到父进程自己退出。
	//
	// 这不只是进程表难看：实测（2026-08-09，六台模组的宿主）僵尸堆到 7 个之后，
	// 新连接开始 "qmi-proxy open ...: context deadline exceeded"，六台里三台
	// 一起掉线，而硬件层面完好、重启宿主进程即恢复。
	//
	// 用 goroutine 而不是同步 Wait：proxy 是常驻进程，同步等会一直阻塞在这里。
	// 它退出时 Wait 返回、goroutine 随之结束，不会累积。
	go func() { _ = cmd.Wait() }()
	return nil
}
