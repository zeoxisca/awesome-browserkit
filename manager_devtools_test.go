package browserkit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDevtoolsPage struct {
	*fakePage
	mu               sync.Mutex
	emit             func(DevtoolsEvent)
	starts           int
	stops            int
	startEvent       *DevtoolsEvent
	startErr         error
	targetID         string
	navigatedAtStart string
}

func (page *fakeDevtoolsPage) StartDevtools(_ context.Context, emit func(DevtoolsEvent)) (func(), error) {
	page.mu.Lock()
	page.emit = emit
	page.starts++
	page.navigatedAtStart = page.navigated
	page.mu.Unlock()
	if page.startEvent != nil {
		emit(*page.startEvent)
	}
	if page.startErr != nil {
		return nil, page.startErr
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			page.mu.Lock()
			page.stops++
			page.mu.Unlock()
		})
	}, nil
}

func (page *fakeDevtoolsPage) TargetID() string {
	if page.targetID != "" {
		return page.targetID
	}
	return "target-1"
}

func TestOpenDevtoolsAcceptsSynchronousStartEvent(t *testing.T) {
	manager, page, opened := newDevtoolsManager(t)
	page.startEvent = &DevtoolsEvent{Kind: "console", Text: "during start"}
	done := make(chan error, 1)
	go func() {
		_, err := manager.OpenDevtools(context.Background(), DevtoolsOpenRequest{SessionID: opened.SessionID, PageID: opened.PageID})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("StartDevtools 同步回调导致 OpenDevtools 死锁")
	}
	defer manager.Close(context.Background())
	state, err := manager.ReadDevtools(context.Background(), DevtoolsReadRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil || len(state.Entries) != 1 || state.Entries[0].Text != "during start" {
		t.Fatalf("启动期间事件没有进入当前 generation: state=%+v err=%v", state, err)
	}
}

func (page *fakeDevtoolsPage) send(event DevtoolsEvent) {
	page.mu.Lock()
	emit := page.emit
	page.mu.Unlock()
	if emit != nil {
		emit(event)
	}
}

type fakeDevtoolsSession struct {
	page   *fakeDevtoolsPage
	closed bool
}

func (session *fakeDevtoolsSession) NewPage(context.Context, PageOptions) (Page, error) {
	return session.page, nil
}
func (session *fakeDevtoolsSession) Cookies() ([]Cookie, error) { return nil, nil }
func (session *fakeDevtoolsSession) Close() error               { session.closed = true; return nil }
func (session *fakeDevtoolsSession) Closed() bool               { return session.closed }

func newDevtoolsManager(t *testing.T) (*Manager, *fakeDevtoolsPage, OpenResponse) {
	return newDevtoolsManagerWithConfig(t, Config{AllowDevtools: true, AllowPrivateNetwork: true})
}

func newDevtoolsManagerWithConfig(t *testing.T, config Config) (*Manager, *fakeDevtoolsPage, OpenResponse) {
	t.Helper()
	page := &fakeDevtoolsPage{fakePage: &fakePage{info: PageInfo{URL: "https://example.com", Title: "Example"}}}
	session := &fakeDevtoolsSession{page: page}
	config.Factory = func(context.Context, SessionConfig) (Session, error) { return session, nil }
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		_ = manager.Close(context.Background())
		t.Fatal(err)
	}
	return manager, page, opened
}

func TestDevtoolsAutoStartDefaultsToOff(t *testing.T) {
	manager, page, opened := newDevtoolsManager(t)
	defer manager.Close(context.Background())

	page.mu.Lock()
	starts := page.starts
	page.mu.Unlock()
	if starts != 0 {
		t.Fatalf("默认配置自动开始了 %d 次开发诊断", starts)
	}
	state, err := manager.DevtoolsState(context.Background(), DevtoolsReadRequest{PageID: opened.PageID})
	if err != nil || state.Status != "idle" || state.Generation != 0 {
		t.Fatalf("默认开发诊断状态=%+v err=%v", state, err)
	}
}

func TestDevtoolsAutoStartCollectsNewSessionPage(t *testing.T) {
	manager, page, opened := newDevtoolsManagerWithConfig(t, Config{
		AllowDevtools: true, DevtoolsAutoStart: true, AllowPrivateNetwork: true,
	})
	defer manager.Close(context.Background())

	page.mu.Lock()
	starts := page.starts
	page.mu.Unlock()
	if starts != 1 {
		t.Fatalf("初始页面启动开发诊断 %d 次", starts)
	}
	if page.navigatedAtStart != "" {
		t.Fatalf("开发诊断在首个导航之后才启动: %q", page.navigatedAtStart)
	}
	state, err := manager.ReadDevtools(context.Background(), DevtoolsReadRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil || state.Status != "collecting" || state.Generation != 1 {
		t.Fatalf("自动采集状态=%+v err=%v", state, err)
	}
	if _, err := manager.OpenDevtools(context.Background(), DevtoolsOpenRequest{SessionID: opened.SessionID, PageID: opened.PageID}); err != nil {
		t.Fatal(err)
	}
	page.mu.Lock()
	starts = page.starts
	page.mu.Unlock()
	if starts != 1 {
		t.Fatalf("手动重复开启后启动了 %d 次", starts)
	}
}

func TestDevtoolsAutoStartRequiresPermission(t *testing.T) {
	_, err := NewManager(Config{DevtoolsAutoStart: true})
	if err == nil || !strings.Contains(err.Error(), "AllowDevtools") {
		t.Fatalf("未授权自动采集 error=%v", err)
	}
}

func TestDevtoolsAutoStartFailureClosesNewSession(t *testing.T) {
	page := &fakeDevtoolsPage{
		fakePage: &fakePage{info: PageInfo{URL: "about:blank", Title: "blank"}},
		startErr: errors.New("devtools unavailable"),
	}
	session := &fakeDevtoolsSession{page: page}
	manager, err := NewManager(Config{
		AllowDevtools: true, DevtoolsAutoStart: true, AllowPrivateNetwork: true,
		Factory: func(context.Context, SessionConfig) (Session, error) { return session, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if _, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"}); err == nil {
		t.Fatal("自动采集失败后 browserOpen 仍成功")
	}
	if !session.closed {
		t.Fatal("自动采集失败后浏览器 session 未关闭")
	}
}

type fakeDevtoolsTabSession struct {
	*fakeDevtoolsSession
	tabs  []TabInfo
	pages map[string]Page
}

func (session *fakeDevtoolsTabSession) ListTabs(context.Context) ([]TabInfo, error) {
	return append([]TabInfo(nil), session.tabs...), nil
}

func (session *fakeDevtoolsTabSession) PageForTab(_ context.Context, targetID string) (Page, error) {
	return session.pages[targetID], nil
}

func (*fakeDevtoolsTabSession) ActivateTab(context.Context, string) error { return nil }
func (*fakeDevtoolsTabSession) CloseTab(context.Context, string) error    { return nil }

func TestDevtoolsAutoStartCollectsDiscoveredTab(t *testing.T) {
	initial := &fakeDevtoolsPage{fakePage: &fakePage{info: PageInfo{URL: "https://example.com"}}, targetID: "target-1"}
	popup := &fakeDevtoolsPage{fakePage: &fakePage{info: PageInfo{URL: "https://popup.example"}}, targetID: "target-2"}
	session := &fakeDevtoolsTabSession{
		fakeDevtoolsSession: &fakeDevtoolsSession{page: initial},
		tabs: []TabInfo{
			{TargetID: "target-1", Type: "page", URL: "https://example.com"},
			{TargetID: "target-2", Type: "page", URL: "https://popup.example", OpenerID: "target-1"},
		},
		pages: map[string]Page{"target-1": initial, "target-2": popup},
	}
	manager, err := NewManager(Config{
		AllowDevtools: true, DevtoolsAutoStart: true, AllowPrivateNetwork: true,
		Factory: func(context.Context, SessionConfig) (Session, error) { return session, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	current := manager.sessions[opened.SessionID]
	manager.mu.Unlock()
	if err := manager.reconcileTabs(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	popup.mu.Lock()
	starts := popup.starts
	popup.mu.Unlock()
	if starts != 1 {
		t.Fatalf("新发现页面启动开发诊断 %d 次", starts)
	}
}

func TestDevtoolsCaptureIsIncrementalAndGenerationBound(t *testing.T) {
	manager, page, opened := newDevtoolsManager(t)
	defer manager.Close(context.Background())
	request := DevtoolsOpenRequest{SessionID: opened.SessionID, PageID: opened.PageID}
	started, err := manager.OpenDevtools(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if started.Generation != 1 || started.Status != "collecting" {
		t.Fatalf("start=%+v", started)
	}
	if _, err := manager.OpenDevtools(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	page.mu.Lock()
	starts := page.starts
	page.mu.Unlock()
	if starts != 1 {
		t.Fatalf("重复开启启动了 %d 次", starts)
	}
	page.send(DevtoolsEvent{PendingDelta: 1})
	page.send(DevtoolsEvent{Kind: "console", Level: "error", Text: "boom", SourceURL: "app.js", Line: 7, At: time.Unix(1, 0)})
	page.send(DevtoolsEvent{Kind: "network", Method: "GET", ResourceType: "XHR", URL: "https://example.com/api", Status: 500, MIMEType: "application/json", DurationMS: 12, EncodedBytes: 42, RequestBody: `{"query":"status"}`, ResponseBody: `{"ok":false}`, BodyAvailable: true, PendingDelta: -1})
	read, err := manager.ReadDevtools(context.Background(), DevtoolsReadRequest{SessionID: opened.SessionID, PageID: opened.PageID, Generation: 1, Kinds: []string{"network"}})
	if err != nil {
		t.Fatal(err)
	}
	if read.PendingRequests != 0 || len(read.Entries) != 1 || read.Entries[0].Sequence != 2 || read.NextSequence != 2 {
		t.Fatalf("read=%+v", read)
	}
	if read.Entries[0].ResourceType != "XHR" || read.Entries[0].RequestBody == "" || read.Entries[0].ResponseBody == "" || !read.Entries[0].BodyAvailable {
		t.Fatalf("正文没有透传: %+v", read.Entries[0])
	}
	closed, err := manager.CloseDevtools(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != "stopped" || closed.Entries != 2 {
		t.Fatalf("close=%+v", closed)
	}
	page.send(DevtoolsEvent{Kind: "console", Text: "late"})
	stopped, err := manager.ReadDevtools(context.Background(), DevtoolsReadRequest{SessionID: opened.SessionID, PageID: opened.PageID, Generation: 1})
	if err != nil || stopped.Status != "stopped" || len(stopped.Entries) != 2 {
		t.Fatalf("stopped=%+v err=%v", stopped, err)
	}
	restarted, err := manager.OpenDevtools(context.Background(), request)
	if err != nil || restarted.Generation != 2 {
		t.Fatalf("restart=%+v err=%v", restarted, err)
	}
	if _, err := manager.ReadDevtools(context.Background(), DevtoolsReadRequest{SessionID: opened.SessionID, PageID: opened.PageID, Generation: 1}); err == nil {
		t.Fatal("旧 generation 仍可读取")
	}
}

func TestDevtoolsHostStateResolvesActiveSessionWithoutTouchingIt(t *testing.T) {
	manager, page, opened := newDevtoolsManager(t)
	defer manager.Close(context.Background())
	if _, err := manager.OpenDevtools(context.Background(), DevtoolsOpenRequest{SessionID: opened.SessionID, PageID: opened.PageID}); err != nil {
		t.Fatal(err)
	}
	page.send(DevtoolsEvent{Kind: "console", Level: "info", Text: "visible"})
	manager.mu.Lock()
	current := manager.sessions[opened.SessionID]
	manager.mu.Unlock()
	current.mu.Lock()
	before := time.Unix(123, 0)
	current.lastUsed = before
	current.mu.Unlock()

	state, err := manager.DevtoolsState(context.Background(), DevtoolsReadRequest{PageID: opened.PageID})
	if err != nil {
		t.Fatal(err)
	}
	current.mu.Lock()
	after := current.lastUsed
	current.mu.Unlock()
	if state.SessionID != opened.SessionID || state.PageID != opened.PageID || len(state.Entries) != 1 || state.Entries[0].Text != "visible" {
		t.Fatalf("宿主诊断 state=%+v", state)
	}
	if !after.Equal(before) {
		t.Fatalf("宿主诊断读取更新了 lastUsed: before=%s after=%s", before, after)
	}
	if _, err := manager.ReadDevtools(context.Background(), DevtoolsReadRequest{PageID: opened.PageID}); err == nil {
		t.Fatal("Agent 读取路径在没有显式 session_id 时成功")
	}
}

func TestDevtoolsCaptureReportsBoundedHistory(t *testing.T) {
	manager, page, opened := newDevtoolsManager(t)
	defer manager.Close(context.Background())
	request := DevtoolsOpenRequest{SessionID: opened.SessionID, PageID: opened.PageID}
	if _, err := manager.OpenDevtools(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < devtoolsMaxEntries+1; index++ {
		page.send(DevtoolsEvent{Kind: "console", Level: "info", Text: "line"})
	}
	read, err := manager.ReadDevtools(context.Background(), DevtoolsReadRequest{SessionID: opened.SessionID, PageID: opened.PageID, Limit: devtoolsMaxReadLimit})
	if err != nil {
		t.Fatal(err)
	}
	if read.DroppedBefore != 1 || !read.Truncated || len(read.Entries) != devtoolsMaxReadLimit || read.Entries[0].Sequence != 2 {
		t.Fatalf("bounded read=%+v", read)
	}
}

func TestClosingTabStopsDevtoolsCapture(t *testing.T) {
	manager, page, opened := newDevtoolsManager(t)
	defer manager.Close(context.Background())
	if _, err := manager.OpenDevtools(context.Background(), DevtoolsOpenRequest{SessionID: opened.SessionID, PageID: opened.PageID}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CloseTab(context.Background(), TabRequest{SessionID: opened.SessionID, PageID: opened.PageID}); err != nil {
		t.Fatal(err)
	}
	page.mu.Lock()
	stops := page.stops
	page.mu.Unlock()
	if stops != 1 {
		t.Fatalf("关闭 Tab 后 stop=%d", stops)
	}
}
