package browserkit

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

type blockingWebSocket struct {
	closed chan struct{}
	once   sync.Once
}

func (socket *blockingWebSocket) Send([]byte) error {
	<-socket.closed
	return io.ErrClosedPipe
}

func (socket *blockingWebSocket) Read() ([]byte, error) {
	<-socket.closed
	return nil, io.ErrClosedPipe
}

func (socket *blockingWebSocket) Close() error {
	socket.once.Do(func() { close(socket.closed) })
	return nil
}

func TestBoundedWebSocketClosesBlockedWrite(t *testing.T) {
	socket := &blockingWebSocket{closed: make(chan struct{})}
	transport := newBoundedWebSocket(socket, 20*time.Millisecond)
	started := time.Now()
	err := transport.Send([]byte("request"))
	if err == nil || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("阻塞写入没有按时失败: elapsed=%s err=%v", time.Since(started), err)
	}
	select {
	case <-socket.closed:
	default:
		t.Fatal("写入超时后底层连接仍然打开")
	}
}

type pageBindingCDPClient struct {
	events      chan *cdp.Event
	blockAttach bool
	attachStart chan struct{}
	attachOnce  sync.Once
	blockClose  bool
	stopStart   chan struct{}
	stopOnce    sync.Once
	stopRelease <-chan struct{}
}

func (client *pageBindingCDPClient) Event() <-chan *cdp.Event { return client.events }

func (client *pageBindingCDPClient) Call(ctx context.Context, _ string, method string, _ any) ([]byte, error) {
	if method == (proto.TargetAttachToTarget{}).ProtoReq() {
		if client.attachStart != nil {
			client.attachOnce.Do(func() { close(client.attachStart) })
		}
		if client.blockAttach {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte(`{"sessionId":"session-1"}`), nil
	}
	if method == (proto.TargetCloseTarget{}).ProtoReq() && client.blockClose {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if method == (proto.PageStopScreencast{}).ProtoReq() {
		if client.stopStart != nil {
			client.stopOnce.Do(func() { close(client.stopStart) })
		}
		if client.stopRelease != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-client.stopRelease:
			}
		}
	}
	return []byte(`{}`), nil
}

func newPageBindingSession(t *testing.T, blockAttach bool) (*rodSession, context.CancelFunc) {
	t.Helper()
	client := &pageBindingCDPClient{events: make(chan *cdp.Event), blockAttach: blockAttach}
	ctx, cancel := context.WithCancel(context.Background())
	browser := rod.New().Client(client).NoDefaultDevice().Context(ctx)
	if err := browser.Connect(); err != nil {
		cancel()
		close(client.events)
		t.Fatal(err)
	}
	return &rodSession{browser: browser, operationTimeout: time.Second, pages: make(map[proto.TargetTargetID]*rodPage)}, func() {
		cancel()
		close(client.events)
	}
}

func TestPageFromTargetAttachHonorsOperationContext(t *testing.T) {
	session, cleanup := newPageBindingSession(t, true)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := session.pageFromTarget(ctx, "target-1")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("Target.attachToTarget 没有按时退出: elapsed=%s err=%v", time.Since(started), err)
	}
}

func TestPageFromTargetKeepsSessionContextAfterAttach(t *testing.T) {
	session, cleanup := newPageBindingSession(t, false)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	page, release, err := session.pageFromTarget(ctx, "target-1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	<-ctx.Done()
	if err := page.GetContext().Err(); err != nil {
		t.Fatalf("绑定成功后的 Page 仍继承临时 deadline: %v", err)
	}
}

func TestPageFromTargetDoesNotWaitOnRodsUncancelableLock(t *testing.T) {
	client := &pageBindingCDPClient{
		events:      make(chan *cdp.Event),
		blockAttach: true,
		attachStart: make(chan struct{}),
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	browser := rod.New().Client(client).NoDefaultDevice().Context(sessionCtx)
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	session := &rodSession{browser: browser, operationTimeout: time.Second, pages: make(map[proto.TargetTargetID]*rodPage)}
	defer func() {
		sessionCancel()
		close(client.events)
	}()
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer firstCancel()
	firstDone := make(chan struct{})
	go func() {
		_, _, _ = session.pageFromTarget(firstCtx, "target-1")
		close(firstDone)
	}()
	<-client.attachStart
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer secondCancel()
	started := time.Now()
	_, _, err := session.pageFromTarget(secondCtx, "target-2")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("等待 PageFromTarget 串行入口没有按自身 context 退出: elapsed=%s err=%v", time.Since(started), err)
	}
	<-firstDone
}

func TestPageCloseHonorsShortContext(t *testing.T) {
	client := &pageBindingCDPClient{events: make(chan *cdp.Event), blockClose: true}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	browser := rod.New().Client(client).NoDefaultDevice().Context(sessionCtx)
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		sessionCancel()
		close(client.events)
	}()
	_, pageCancel := context.WithCancel(sessionCtx)
	defer pageCancel()
	page := browser.PageFromSession("session-1")
	page.TargetID = "target-1"
	wrapped := &rodPage{page: page, operationTimeout: time.Second, cancel: pageCancel}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := wrapped.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("Page.Close 没有按短 context 退出: elapsed=%s err=%v", time.Since(started), err)
	}
}

func TestScreencastStopIsBoundedAndOrderedBeforeReturn(t *testing.T) {
	stopRelease := make(chan struct{})
	client := &pageBindingCDPClient{
		events:      make(chan *cdp.Event),
		stopStart:   make(chan struct{}),
		stopRelease: stopRelease,
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	browser := rod.New().Client(client).NoDefaultDevice().Context(sessionCtx)
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	session := &rodSession{browser: browser, operationTimeout: time.Second, pages: make(map[proto.TargetTargetID]*rodPage)}
	defer func() {
		sessionCancel()
		close(client.events)
	}()
	raw, release, err := session.pageFromTarget(context.Background(), "target-1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	page := &rodPage{page: raw, operationTimeout: time.Second}
	_, stop, err := page.StartScreencast(context.Background(), ScreencastOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-client.stopStart:
	case <-time.After(time.Second):
		t.Fatal("没有发送 Page.stopScreencast")
	}
	select {
	case <-stopped:
		t.Fatal("远端 stop 尚未完成时 stop() 已经返回，可能与下一次 start 乱序")
	default:
	}
	close(stopRelease)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Page.stopScreencast 完成后 stop() 没有返回")
	}
}
