package qmi

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedTransport 是读写分离的假传输：读端由测试注入错误，写端永远成功。
//
// 用 net.Pipe 测不出这个 bug —— 对端一关，写也立刻失败，SendRequest 在写这一步就返回了。
// 真机上读失败时写不一定失败（cdc-wdm 读出错、qmi-proxy 半关闭），那时请求会写出去、
// 再等一个永远不会来的回包。
type scriptedTransport struct {
	readErrCh chan error
	closeCh   chan struct{}
	closeOnce sync.Once

	mu       sync.Mutex
	deadline time.Time
}

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{
		readErrCh: make(chan error, 1),
		closeCh:   make(chan struct{}),
	}
}

func (s *scriptedTransport) Read([]byte) (int, error) {
	s.mu.Lock()
	dl := s.deadline
	s.mu.Unlock()
	var timerC <-chan time.Time
	if !dl.IsZero() {
		timer := time.NewTimer(time.Until(dl))
		defer timer.Stop()
		timerC = timer.C
	}
	select {
	case err := <-s.readErrCh:
		return 0, err
	case <-s.closeCh:
		return 0, io.EOF
	case <-timerC:
		return 0, os.ErrDeadlineExceeded
	}
}

func (s *scriptedTransport) Write(p []byte) (int, error) { return len(p), nil }

func (s *scriptedTransport) Close() error {
	s.closeOnce.Do(func() { close(s.closeCh) })
	return nil
}

func (s *scriptedTransport) SetReadDeadline(dl time.Time) error {
	s.mu.Lock()
	s.deadline = dl
	s.mu.Unlock()
	return nil
}

// newScriptedClient 起一个走真实 readLoop/writerLoop 的 Client。
// DefaultRequestTimeout 设成 0：调用方不带截止时间时请求会一直等，
// 「该立刻失败却在等回包」在测试里就表现为挂住，而不是 30 秒后碰巧通过。
func newScriptedClient(t *testing.T) (*Client, *scriptedTransport, <-chan struct{}) {
	t.Helper()
	tr := newScriptedTransport()
	readFailed := make(chan struct{})
	var once sync.Once
	opts := DefaultClientOptions()
	opts.DefaultRequestTimeout = 0
	opts.ReadDeadline = 5 * time.Millisecond
	opts.Logf = func(_ ClientLogLevel, format string, _ ...any) {
		if strings.Contains(format, "read failed") {
			once.Do(func() { close(readFailed) })
		}
	}
	c := newClientWithTransport("/dev/fake-qmi", opts, tr)
	t.Cleanup(func() { _ = c.Close() })
	return c, tr, readFailed
}

func sendRequestAsync(c *Client, ctx context.Context) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		_, err := c.SendRequest(ctx, ServiceUIM, 1, UIMReadRecord, nil)
		errCh <- err
	}()
	return errCh
}

func waitRequestErr(t *testing.T, errCh <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("%s：请求在等一个永远不会来的回包，没有立刻失败", what)
		return nil
	}
}

func waitTransactionCount(t *testing.T, c *Client, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		got := len(c.transactions)
		c.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("事务表长度 = %d，等不到 %d", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSendRequestAfterReadLoopExitFailsImmediatelyWithCause(t *testing.T) {
	c, tr, readFailed := newScriptedClient(t)
	cause := errors.New("read /dev/cdc-wdm0: no such device")
	tr.readErrCh <- cause
	<-readFailed

	err := waitRequestErr(t, sendRequestAsync(c, context.Background()), "读循环退出后的新请求")
	if !errors.Is(err, cause) {
		t.Fatalf("新请求的错误应带上读循环退出的原因 %v，得到 %v", cause, err)
	}
	if !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("新请求的错误应能用 errors.Is(err, ErrConnectionClosed) 识别，得到 %v", err)
	}
	c.mu.Lock()
	left := len(c.transactions)
	c.mu.Unlock()
	if left != 0 {
		t.Fatalf("读循环已死，新请求不该再登记事务，残留 %d 个", left)
	}
}

func TestReadLoopExitWakesPendingRequestWithCause(t *testing.T) {
	c, tr, _ := newScriptedClient(t)
	errCh := sendRequestAsync(c, context.Background())
	waitTransactionCount(t, c, 1)

	tr.readErrCh <- io.EOF

	err := waitRequestErr(t, errCh, "已登记在等回包的请求")
	if !errors.Is(err, io.EOF) || !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("在等的请求应带上读循环退出原因（EOF）且可识别为连接已断，得到 %v", err)
	}
	// 下游（nsim、manager/uim_readiness）按子串识别传输已断，错误文本里这些片段不能丢。
	msg := strings.ToLower(err.Error())
	for _, fragment := range []string{"qmi: read failed", "read failed: eof", "connection closed"} {
		if !strings.Contains(msg, fragment) {
			t.Fatalf("错误文本 %q 缺少下游依赖的片段 %q", err.Error(), fragment)
		}
	}
}

func TestSendRequestAfterCloseFailsImmediately(t *testing.T) {
	c, _, _ := newScriptedClient(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	err := waitRequestErr(t, sendRequestAsync(c, context.Background()), "Close 之后的新请求")
	if !errors.Is(err, ErrClientClosed) || !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("Close 之后的新请求应得 ErrClientClosed（也是 ErrConnectionClosed），得到 %v", err)
	}
	if !strings.Contains(err.Error(), "client closed") || !strings.Contains(err.Error(), "connection closed") {
		t.Fatalf("错误文本 %q 缺少下游依赖的片段", err.Error())
	}
}

func TestCloseWakesPendingRequestWithClientClosed(t *testing.T) {
	c, _, _ := newScriptedClient(t)
	errCh := sendRequestAsync(c, context.Background())
	waitTransactionCount(t, c, 1)

	_ = c.Close()

	err := waitRequestErr(t, errCh, "Close 时在等回包的请求")
	if !errors.Is(err, ErrClientClosed) {
		t.Fatalf("Close 时在等的请求应得 ErrClientClosed，得到 %v", err)
	}
}

// 读循环退出与新请求登记同时发生：无论谁先，请求都必须立刻失败。
// 登记与「连接已断」的判断不在同一把锁里时，failPending 可能先清空等待表、请求随后才登记，
// 那个请求就再也没人唤醒（nsim a7b4752f 在 MBIM 上踩过的同一个顺序）。
func TestReadLoopExitRacingNewRequestNeverStrandsIt(t *testing.T) {
	for i := 0; i < 200; i++ {
		c, tr, _ := newScriptedClient(t)
		errCh := sendRequestAsync(c, context.Background())
		tr.readErrCh <- io.ErrUnexpectedEOF
		err := waitRequestErr(t, errCh, "与读循环退出并发登记的请求")
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("第 %d 轮：错误应带上读循环退出原因，得到 %v", i, err)
		}
		_ = c.Close()
	}
}

func TestAllocateClientIDStopsRetryingOnceConnectionClosed(t *testing.T) {
	c, tr, readFailed := newScriptedClient(t)
	tr.readErrCh <- io.EOF
	<-readFailed

	start := time.Now()
	_, err := c.AllocateClientIDWithContext(context.Background(), ServiceUIM)
	if !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("应得 ErrConnectionClosed，得到 %v", err)
	}
	// 连接已断时重试只会得到同一个错误；每轮之间 sleep 500ms，重试三轮就是白等 1 秒。
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("连接已断仍在重试，耗时 %v", elapsed)
	}
}
