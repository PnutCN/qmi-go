//go:build linux

package qmi

import (
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func readAsync(tr *qrtrTransport) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		_, err := tr.Read(make([]byte, qrtrMaxFrameSize))
		errCh <- err
	}()
	return errCh
}

// 数据套接字的读协程因不可重试的错误退出后，这个服务的回包再也不会来。
// 从前它静默退出，Client 那边的请求照样写出去、各自等满超时；现在 Read 立刻把错误报给
// Client 的读循环，由它让所有等待者立刻失败。
func TestQRTRTransportDataSocketFatalErrorFailsRead(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
	}}
	tr, factory := newTestQRTRTransport(t, ns)
	allocateTestClientID(t, tr, ServiceDMS)
	if err := tr.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}

	dataSocks := factory.dataSockets()
	if len(dataSocks) != 1 {
		t.Fatalf("data sockets = %d, want 1", len(dataSocks))
	}
	dataSocks[0].failCh <- unix.ENETRESET

	select {
	case err := <-readAsync(tr):
		if !errors.Is(err, unix.ENETRESET) {
			t.Fatalf("Read() 应报数据套接字的致命错误，得到 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("数据套接字读协程已死，Read() 仍在等永远不会来的帧")
	}
}

// 反面：Release / 重新分配关掉的数据套接字，读协程退出是正常收尾，不能被当成传输故障。
func TestQRTRTransportReleasedDataSocketIsNotFatal(t *testing.T) {
	ns := &fakeNameserver{directory: []qrtrCtrlPkt{
		{cmd: qrtrTypeNewServer, service: uint32(ServiceDMS), instance: 0x0101, node: 5, port: 20},
	}}
	tr, _ := newTestQRTRTransport(t, ns)
	cid := allocateTestClientID(t, tr, ServiceDMS)

	req := ctlRequestFrame(11, CTLReleaseClientID, []TLV{encodeCTLServiceClientIDTLV(ServiceDMS, cid)})
	if _, err := tr.Write(req); err != nil {
		t.Fatalf("Write(CTLReleaseClientID) error = %v", err)
	}
	if err := readPacketFrom(t, tr).CheckResult(); err != nil {
		t.Fatalf("CTLReleaseClientID CheckResult() error = %v", err)
	}
	// 再分配一次：旧套接字被替换关闭，同样不是故障。
	allocateTestClientID(t, tr, ServiceDMS)
	allocateTestClientID(t, tr, ServiceDMS)

	if err := tr.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Read(make([]byte, qrtrMaxFrameSize)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("正常释放后 Read() 应只是超时，得到 %v", err)
	}
}

// 控制套接字的读协程死了，NEW_LOOKUP 的回答再也收不到：解析未缓存的服务要立刻失败，
// 而不是每次都等满 qrtrLookupTimeout。
func TestQRTRTransportCtrlReaderFatalErrorFailsLookupImmediately(t *testing.T) {
	tr, factory := newTestQRTRTransport(t, &fakeNameserver{hang: true})
	qrtrLookupTimeout = 5 * time.Second // 在 setQRTRTestTimeouts 之后改，Cleanup 会还原

	factory.ctrlSocket().failCh <- unix.ENETRESET
	waitCtrlReaderFailed(t, tr)

	start := time.Now()
	_, err := tr.resolveService(ServiceVOICE)
	if !errors.Is(err, unix.ENETRESET) {
		t.Fatalf("resolveService 应带上控制读协程的错误，得到 %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("控制读协程已死仍在等 NEW_LOOKUP 回答，耗时 %v", elapsed)
	}
}

// 已在等 NEW_LOOKUP 回答的解析，控制读协程一死就要被唤醒。
func TestQRTRTransportCtrlReaderFatalErrorWakesPendingLookup(t *testing.T) {
	tr, factory := newTestQRTRTransport(t, &fakeNameserver{hang: true})
	qrtrLookupTimeout = 5 * time.Second

	errCh := make(chan error, 1)
	go func() {
		_, err := tr.resolveService(ServiceVOICE)
		errCh <- err
	}()
	// 等它发出 NEW_LOOKUP、进入等待。
	deadline := time.Now().Add(2 * time.Second)
	for factory.ctrlSocket().sentCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("resolveService 没有发出 NEW_LOOKUP")
		}
		time.Sleep(time.Millisecond)
	}
	factory.ctrlSocket().failCh <- unix.ENETRESET

	select {
	case err := <-errCh:
		if !errors.Is(err, unix.ENETRESET) {
			t.Fatalf("resolveService 应带上控制读协程的错误，得到 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("控制读协程已死，在等的解析没有被唤醒")
	}
}

func waitCtrlReaderFailed(t *testing.T, tr *qrtrTransport) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		tr.mu.Lock()
		failed := tr.ctrlErr != nil
		tr.mu.Unlock()
		if failed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("控制读协程的致命错误没有被记下")
		}
		time.Sleep(time.Millisecond)
	}
}
