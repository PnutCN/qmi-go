//go:build linux

package qmi

import (
	"context"
	"errors"
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

// proxyStartAttempt 是「某个 socket 上正在进行的一次拉起」。
//
// 归属制而不是一把大锁:锁只保护 attempts 表,拉起与轮询 dial 都在锁外做。
// 持锁跑 I/O 会让后到的调用者连"proxy 其实已经好了"都发现不了。
type proxyStartAttempt struct {
	done chan struct{}
	err  error
}

var (
	dialProxyHook         = dialProxy
	startProxyProcessHook = startProxyProcess
	proxyRetryDelay       = 100 * time.Millisecond
	proxyStartMu          sync.Mutex
	proxyStartAttempts    = make(map[string]*proxyStartAttempt)
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

	// **同一个 socket 上只允许一个调用者去拉起 proxy。**
	//
	// 没有这层归属时,N 台设备并发初始化会各自 dial 失败、各自 fork 一个
	// qmi-proxy,而 abstract socket 只有一个能 bind 成功 —— 其余全部立刻退出。
	// 配合 startProxyProcess 里那个僵尸问题,实测六台模组的宿主上一次堆出 7 个。
	//
	// 其余调用者等这一次拉起的结果,而不是各自再 fork 一个。
	key := proxySocketAddress(proxyPath)
	proxyStartMu.Lock()
	if attempt, ok := proxyStartAttempts[key]; ok {
		proxyStartMu.Unlock()
		return waitForProxyStart(ctx, proxyPath, attempt)
	}
	attempt := &proxyStartAttempt{done: make(chan struct{})}
	proxyStartAttempts[key] = attempt
	proxyStartMu.Unlock()

	conn, err := startProxyAndWait(ctx, proxyPath, proxyExecutable, firstErr)
	proxyStartMu.Lock()
	attempt.err = err
	delete(proxyStartAttempts, key)
	close(attempt.done)
	proxyStartMu.Unlock()
	return conn, err
}

// waitForProxyStart 等别人那次拉起的结果。
//
// # owner 失败不等于我也该失败
//
// startProxyAndWait 的失败原因里**包含 owner 自己的 ctx 超时** —— 而各设备的
// QMI 客户端创建各有各的超时,先到的那个 ctx 可能只剩几十毫秒。直接把 owner 的
// error 抄给所有等待者,会让一台设备的短超时把并发初始化的其余几台一起判死,
// 即便 proxy 在那之后立刻就绪。那正是当初"六台里三台一起掉线"的形态。
//
// 所以 owner 失败后,只要自己的 ctx 还有余量,就自己再 dial 一次:proxy 多半
// 已经被 owner fork 起来了,只是没赶上它的截止时间。仍然失败才认输,并且把
// 两个原因都带上 —— 只报一个的话,现场分不清是"没拉起来"还是"拉起来了没赶上"。
func waitForProxyStart(ctx context.Context, proxyPath string, attempt *proxyStartAttempt) (qmiTransport, error) {
	select {
	case <-attempt.done:
		if attempt.err != nil {
			if ctx.Err() != nil {
				return nil, attempt.err
			}
			conn, err := dialProxyHook(ctx, proxyPath)
			if err != nil {
				return nil, fmt.Errorf("connect qmi-proxy %q after a failed startup by another caller: %w", proxyPath, errors.Join(attempt.err, err))
			}
			return conn, nil
		}
		conn, err := dialProxyHook(ctx, proxyPath)
		if err != nil {
			return nil, fmt.Errorf("connect qmi-proxy %q after startup: %w", proxyPath, err)
		}
		return conn, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for qmi-proxy %q startup: %w", proxyPath, ctx.Err())
	}
}

func startProxyAndWait(ctx context.Context, proxyPath, proxyExecutable string, firstErr error) (qmiTransport, error) {
	// 拿到归属与第一次 dial 之间有窗口,别人(甚至系统里本来就有的 proxy)可能
	// 已经把 socket 支起来了。fork 之前再看一眼,省掉一个多余的进程。
	if conn, err := dialProxyHook(ctx, proxyPath); err == nil {
		return conn, nil
	}
	if err := startProxyProcessHook(proxyExecutable); err != nil {
		return nil, fmt.Errorf("connect qmi-proxy %q failed and start %s failed: %w", proxyPath, proxyExecutable, err)
	}

	// fork 返回不等于 socket 已经 bind 好,轮询到自己的 ctx 到期为止。
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
	// **必须收尸,不能用 Process.Release()。**
	//
	// Release 只是释放 Go 侧的 os.Process 句柄,不做 wait4 —— qmi-proxy 退出后
	// 会一直以僵尸挂在父进程名下,直到父进程自己退出。
	//
	// 这不只是进程表难看:实测(2026-08-09,六台模组的宿主)僵尸堆到 7 个之后,
	// 新连接开始 "qmi-proxy open ...: context deadline exceeded",六台里三台
	// 一起掉线,而硬件层面完好、重启宿主进程即恢复。
	//
	// 用 goroutine 而不是同步 Wait:proxy 是常驻进程,同步等会一直阻塞在这里。
	// 它退出时 Wait 返回、goroutine 随之结束,不会累积。
	go func() { _ = cmd.Wait() }()
	return nil
}
