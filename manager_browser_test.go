package browserkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSession struct {
	page    *fakePage
	closed  bool
	options PageOptions
}

type fakeViewportSession struct {
	*fakeSession
	viewportCalls []Viewport
	viewportErr   error
	closedTargets []string
	activated     []string
	tabs          []TabInfo
}

type blockingCloseSession struct {
	*fakeSession
	closeEntered chan struct{}
	releaseClose chan struct{}
}

func (session *blockingCloseSession) Close() error {
	close(session.closeEntered)
	<-session.releaseClose
	return session.fakeSession.Close()
}

// blockingListViewportSession 让测试精确控制 ListTabs 持有 session.mu 的时间，
// 用于复现图流订阅与 viewport 设置之间的锁顺序问题。
type blockingListViewportSession struct {
	*fakeViewportSession
	listEntered chan struct{}
	releaseList chan struct{}
	listOnce    sync.Once
	blockList   bool
}

func (session *blockingListViewportSession) ListTabs(ctx context.Context) ([]TabInfo, error) {
	if !session.blockList {
		return []TabInfo{{TargetID: "target-1", URL: session.page.info.URL, Title: session.page.info.Title, Type: "page"}}, nil
	}
	session.listOnce.Do(func() { close(session.listEntered) })
	select {
	case <-session.releaseList:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []TabInfo{{TargetID: "target-1", URL: session.page.info.URL, Title: session.page.info.Title, Type: "page"}}, nil
}

func (session *fakeViewportSession) ListTabs(context.Context) ([]TabInfo, error) {
	if session.tabs != nil {
		return append([]TabInfo(nil), session.tabs...), nil
	}
	return []TabInfo{{TargetID: "target-1", URL: session.page.info.URL, Title: session.page.info.Title, Type: "page"}}, nil
}

func (session *fakeViewportSession) PageForTab(context.Context, string) (Page, error) {
	return session.page, nil
}

func (session *fakeViewportSession) ActivateTab(_ context.Context, targetID string) error {
	session.activated = append(session.activated, targetID)
	return nil
}
func (session *fakeViewportSession) CloseTab(_ context.Context, targetID string) error {
	session.closedTargets = append(session.closedTargets, targetID)
	return nil
}
func (session *fakeViewportSession) SetTabViewport(_ context.Context, _ string, viewport Viewport) error {
	session.viewportCalls = append(session.viewportCalls, viewport)
	return session.viewportErr
}

func (session *fakeSession) NewPage(_ context.Context, options PageOptions) (Page, error) {
	session.options = options
	if session.page == nil {
		session.page = &fakePage{info: PageInfo{URL: "about:blank", Title: "blank"}}
	}
	return session.page, nil
}
func (session *fakeSession) Cookies() ([]Cookie, error) { return nil, nil }
func (session *fakeSession) Close() error               { session.closed = true; return nil }
func (session *fakeSession) Closed() bool               { return session.closed }

type fakePage struct {
	info             PageInfo
	closed           bool
	navigated        string
	lastText         string
	lastClear        bool
	lastRef          ElementRef
	waitCalled       bool
	waitOptions      WaitOptions
	hoverCalled      bool
	injectedScripts  []string
	evaluatedScripts []string
	idleCalled       bool
	idleErr          error
	navigateResult   *PageInfo
	inputMu          sync.Mutex
	inputBlock       <-chan struct{}
	inputs           []InputEvent
	screenshotData   []byte
	viewport         Viewport
}

type fakeScreencastPage struct {
	*fakePage
	starts  chan chan Screenshot
	options chan ScreencastOptions
}

type fakeTargetPage struct {
	*fakePage
	targetID string
}

func (page *fakeTargetPage) TargetID() string { return page.targetID }

func (page *fakeScreencastPage) StartScreencast(_ context.Context, options ScreencastOptions) (<-chan Screenshot, func(), error) {
	frames := make(chan Screenshot, 1)
	page.starts <- frames
	if page.options != nil {
		page.options <- options
	}
	var once sync.Once
	return frames, func() { once.Do(func() { close(frames) }) }, nil
}

func (page *fakePage) TargetID() string { return "target-1" }

func (page *fakePage) Info(context.Context) (PageInfo, error) { return page.info, nil }
func (page *fakePage) Navigate(_ context.Context, address string) (PageInfo, error) {
	page.navigated = address
	page.info.URL = address
	if page.navigateResult != nil {
		return *page.navigateResult, nil
	}
	return page.info, nil
}
func (page *fakePage) Snapshot(context.Context, SnapshotOptions) (Snapshot, error) {
	return Snapshot{Info: page.info, Revision: 1, Text: "hello", Elements: []Element{{Ref: "e1", Role: "button", Name: "Save"}}}, nil
}
func (page *fakePage) Click(_ context.Context, ref ElementRef) error {
	page.lastRef = ref
	return nil
}
func (page *fakePage) Hover(_ context.Context, ref ElementRef) error {
	page.lastRef = ref
	page.hoverCalled = true
	return nil
}
func (page *fakePage) Type(_ context.Context, ref ElementRef, text string, clear bool) error {
	page.lastRef, page.lastText, page.lastClear = ref, text, clear
	return nil
}
func (page *fakePage) Press(context.Context, string) error { return nil }
func (page *fakePage) Wait(_ context.Context, options WaitOptions) error {
	page.waitCalled = true
	page.waitOptions = options
	return nil
}
func (page *fakePage) WaitIdle(context.Context, time.Duration) error {
	page.idleCalled = true
	return page.idleErr
}
func (page *fakePage) Screenshot(context.Context, ScreenshotOptions) (Screenshot, error) {
	data := page.screenshotData
	if len(data) == 0 {
		data = []byte("png")
	}
	return Screenshot{Data: data, Format: "png"}, nil
}
func (page *fakePage) Evaluate(_ context.Context, code string, _ EvaluateOptions) (EvaluateResult, error) {
	page.evaluatedScripts = append(page.evaluatedScripts, code)
	if page.viewport.Width > 0 && page.viewport.Height > 0 {
		data, _ := json.Marshal(page.viewport)
		return EvaluateResult{Type: "object", Value: data}, nil
	}
	return EvaluateResult{Type: "string", Value: []byte(`"ok"`)}, nil
}
func (page *fakePage) AddScriptToEvaluateOnNewDocument(_ context.Context, code string) error {
	page.injectedScripts = append(page.injectedScripts, code)
	return nil
}
func (page *fakePage) Close(context.Context) error { page.closed = true; return nil }
func (page *fakePage) DispatchInput(ctx context.Context, input InputEvent) error {
	if page.inputBlock != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-page.inputBlock:
		}
	}
	page.inputMu.Lock()
	page.inputs = append(page.inputs, input)
	page.inputMu.Unlock()
	return nil
}

func TestManagerLiveInputIsBestEffort(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com", Viewport: Viewport{Width: 800, Height: 600}})
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	session.page.inputBlock = blocked
	defer close(blocked)
	started := time.Now()
	for range liveInputQueueSize + 32 {
		if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "mouse", Action: "move", X: 1, Y: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("best-effort 输入被后台 CDP 阻塞: %s", elapsed)
	}
}

func TestManagerStateSubscriptionReceivesInternalBrowserSessionEvents(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := manager.SubscribeState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager.emit(context.Background(), Event{Kind: "tab_opened", SessionID: "bsess-internal"})
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("顶层 Session 的状态订阅没有收到内部 Browser session 事件")
	}
}

func TestManagerDropsAndCancelsInputsFromPreviousActivePage(t *testing.T) {
	manager, browser := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com/a", Viewport: Viewport{Width: 800, Height: 600}})
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	defer close(blocked)
	browser.page.inputBlock = blocked
	current := manager.sessions[opened.SessionID]
	secondPage := &fakePage{info: PageInfo{URL: "https://example.com/b"}}
	current.mu.Lock()
	second := manager.registerPage(current, secondPage, Viewport{Width: 800, Height: 600}, "")
	current.mu.Unlock()
	beforeSwitch, err := manager.ListTabs(context.Background(), TabsRequest{SessionID: opened.SessionID})
	if err != nil {
		t.Fatal(err)
	}

	first := InputEvent{Kind: "mouse", Action: "down", X: 10, Y: 10, Button: "left"}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, first); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current.mu.Lock()
		started := current.inputCancel != nil
		current.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("首个输入没有进入后台 dispatcher")
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "text", Text: "stale"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ActivateTab(context.Background(), TabRequest{SessionID: opened.SessionID, PageID: second.id}); err != nil {
		t.Fatal(err)
	}
	tabs, err := manager.ListTabs(context.Background(), TabsRequest{SessionID: opened.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if tabs.ActivePageID != second.id || tabs.TabsRevision <= beforeSwitch.TabsRevision {
		t.Fatalf("切页提交状态没有单调 revision: %+v, before=%d", tabs, beforeSwitch.TabsRevision)
	}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "text", Text: "also stale"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DispatchLiveInput(context.Background(), second.id, InputEvent{Kind: "text", Text: "current"}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		secondPage.inputMu.Lock()
		gotSecond := append([]InputEvent(nil), secondPage.inputs...)
		secondPage.inputMu.Unlock()
		if len(gotSecond) == 1 {
			if gotSecond[0].Text != "current" {
				t.Fatalf("新页面输入=%+v", gotSecond)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("新页面没有收到输入: %+v", gotSecond)
		}
		time.Sleep(time.Millisecond)
	}
	browser.page.inputMu.Lock()
	gotFirst := append([]InputEvent(nil), browser.page.inputs...)
	browser.page.inputMu.Unlock()
	if len(gotFirst) != 0 {
		t.Fatalf("切页后仍回放旧页面输入: %+v", gotFirst)
	}
}

func TestManagerRestartsLiveFramesWhenActiveTabChanges(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{
		URL: "https://example.com/a", Viewport: Viewport{Width: 800, Height: 600},
	})
	if err != nil {
		t.Fatal(err)
	}
	current := manager.sessions[opened.SessionID]
	secondPage := &fakePage{info: PageInfo{URL: "https://example.com/b"}}
	current.mu.Lock()
	first := current.tabs[opened.PageID]
	second := manager.registerPage(current, secondPage, Viewport{Width: 800, Height: 600}, "")
	current.mu.Unlock()
	cachePageLiveFrame(first, LiveFrame{Data: []byte("first"), MIME: "image/jpeg"})
	cachePageLiveFrame(second, LiveFrame{Data: []byte("second"), MIME: "image/jpeg"})

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstFrames, err := manager.SubscribeLiveFrames(firstCtx)
	if err != nil {
		t.Fatal(err)
	}
	if frame := <-firstFrames; string(frame.Data) != "first" {
		t.Fatalf("切换前画面=%q", frame.Data)
	}
	if _, err := manager.ActivateTab(context.Background(), TabRequest{SessionID: opened.SessionID, PageID: second.id}); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-firstFrames:
		if ok {
			t.Fatal("切换 Tab 后旧图流仍然打开")
		}
	case <-time.After(time.Second):
		t.Fatal("切换 Tab 后旧图流没有关闭")
	}

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	secondFrames, err := manager.SubscribeLiveFrames(secondCtx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-secondFrames:
		if string(frame.Data) != "second" {
			t.Fatalf("切换后画面=%q", frame.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("切换后没有收到新 Tab 缓存画面")
	}
}

func TestCloseActiveTabDoesNotWaitForLiveHubWhileHoldingTabLock(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	hubCtx, stop := context.WithCancel(context.Background())
	manager.liveMu.Lock()
	manager.live = &liveHub{manager: manager, ctx: hubCtx, stop: stop, done: make(chan struct{})}
	manager.liveMu.Unlock()

	started := time.Now()
	if _, err := manager.CloseTab(context.Background(), TabRequest{SessionID: opened.SessionID, PageID: opened.PageID}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("关闭 active Tab 等待了未退出的 live hub: %s", elapsed)
	}
}

func fakeManager(t *testing.T, config Config) (*Manager, *fakeSession) {
	t.Helper()
	session := &fakeSession{}
	config.Factory = func(_ context.Context, _ SessionConfig) (Session, error) {
		return session, nil
	}
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	return manager, session
}

func TestManagerDefaultsViewportToFullHD(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	if _, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := session.options.Viewport; got != (Viewport{Width: 1920, Height: 1080}) {
		t.Fatalf("默认 viewport=%#v", got)
	}
}

func TestManagerDispatchesValidatedLiveInputToActivePage(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{
		URL: "https://example.com", Viewport: Viewport{Width: 800, Height: 600},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []InputEvent{
		{Kind: "mouse", Action: "down", X: 400, Y: 300, Button: "left", Buttons: 1, ClickCount: 1},
		{Kind: "touch", Action: "start", Touches: []InputTouchPoint{{ID: 7, X: 200, Y: 150, Force: 0.75}}},
		{Kind: "key", Action: "down", Key: "A", Code: "KeyA", KeyCode: 65, Text: "A"},
		{Kind: "composition", Text: "中文", SelectionStart: 2, SelectionEnd: 2},
		{Kind: "text", Text: "中文"},
		{Kind: "shortcut", Action: "select_all"},
		{Kind: "shortcut", Action: "copy"},
		{Kind: "shortcut", Action: "paste"},
	}
	for _, event := range events {
		if err := manager.DispatchLiveInput(context.Background(), opened.PageID, event); err != nil {
			t.Fatalf("DispatchLiveInput(%+v): %v", event, err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		session.page.inputMu.Lock()
		inputs := append([]InputEvent(nil), session.page.inputs...)
		session.page.inputMu.Unlock()
		if len(inputs) == len(events) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("页面输入=%+v", inputs)
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.DispatchLiveInput(context.Background(), "", InputEvent{Kind: "text", Text: "active"}); err != nil {
		t.Fatalf("未指定 page_id 的 active page 输入失败: %v", err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		session.page.inputMu.Lock()
		inputs := append([]InputEvent(nil), session.page.inputs...)
		session.page.inputMu.Unlock()
		if len(inputs) == len(events)+1 {
			if inputs[len(events)].Text != "active" {
				t.Fatalf("active page 输入=%+v", inputs[len(events)])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("未指定 page_id 的输入没有到达 active page: %+v", inputs)
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "mouse", Action: "move", X: 800, Y: 300}); err == nil {
		t.Fatal("没有拒绝 viewport 右边界之外的坐标")
	}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "key", Action: "down", Key: "a", Code: "KeyA", Modifiers: 2}); err == nil {
		t.Fatal("没有拒绝直接透传 Ctrl 组合键")
	}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "shortcut", Action: "cut"}); err == nil {
		t.Fatal("没有拒绝未允许的语义化快捷键")
	}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "shortcut", Action: "copy", Modifiers: 1}); err == nil {
		t.Fatal("没有拒绝快捷键不支持的修饰键")
	}
}

func TestManagerSetsTabViewportAfterBrowserAcceptsIt(t *testing.T) {
	base := &fakeSession{page: &fakePage{info: PageInfo{URL: "about:blank", Title: "blank"}}}
	session := &fakeViewportSession{fakeSession: base}
	manager, err := NewManager(Config{
		AllowPrivateNetwork: true,
		Factory:             func(context.Context, SessionConfig) (Session, error) { return session, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com", Viewport: Viewport{Width: 800, Height: 600}})
	if err != nil {
		t.Fatal(err)
	}
	next := Viewport{Width: 927, Height: 641}
	if err := manager.SetViewport(context.Background(), ViewportRequest{SessionID: opened.SessionID, PageID: opened.PageID, Viewport: next}); err != nil {
		t.Fatal(err)
	}
	if len(session.viewportCalls) != 1 || session.viewportCalls[0] != next {
		t.Fatalf("viewport calls=%+v", session.viewportCalls)
	}
	state, err := manager.LiveState(context.Background())
	if err != nil || state.Viewport != next {
		t.Fatalf("LiveState()=%+v, %v", state, err)
	}
	if err := manager.SetViewport(context.Background(), ViewportRequest{SessionID: opened.SessionID, PageID: opened.PageID, Viewport: next}); err != nil {
		t.Fatal(err)
	}
	if len(session.viewportCalls) != 1 {
		t.Fatalf("相同 viewport 被重复发送: %+v", session.viewportCalls)
	}
}

func TestManagerClosesTabsBeyondConfiguredLimit(t *testing.T) {
	base := &fakeSession{page: &fakePage{info: PageInfo{URL: "about:blank", Title: "blank"}}}
	session := &fakeViewportSession{fakeSession: base}
	manager, err := NewManager(Config{
		AllowPrivateNetwork: true, MaxTabsPerSession: 1,
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
	session.tabs = []TabInfo{
		{TargetID: "target-1", URL: "https://example.com", Type: "page"},
		{TargetID: "target-2", URL: "https://example.com/popup", Type: "page"},
	}
	tabs, err := manager.ListTabs(context.Background(), TabsRequest{SessionID: opened.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs.Tabs) != 1 || len(session.closedTargets) != 1 || session.closedTargets[0] != "target-2" || len(session.activated) == 0 || session.activated[len(session.activated)-1] != "target-1" {
		t.Fatalf("tabs=%+v closed targets=%v activation calls=%v", tabs.Tabs, session.closedTargets, session.activated)
	}
}

func TestStateReconcileDoesNotBlockViewportOrLiveSubscription(t *testing.T) {
	base := &fakeSession{page: &fakePage{info: PageInfo{URL: "about:blank", Title: "blank"}}}
	session := &blockingListViewportSession{
		fakeViewportSession: &fakeViewportSession{fakeSession: base},
		listEntered:         make(chan struct{}),
		releaseList:         make(chan struct{}),
	}
	manager, err := NewManager(Config{
		AllowPrivateNetwork: true,
		Factory:             func(context.Context, SessionConfig) (Session, error) { return session, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com", Viewport: Viewport{Width: 800, Height: 600}})
	if err != nil {
		t.Fatal(err)
	}
	session.blockList = true
	cachePageLiveFrame(manager.sessions[opened.SessionID].tabs[opened.PageID], LiveFrame{Data: []byte("cached"), MIME: "image/png"})
	reconcileDone := make(chan error, 1)
	go func() {
		_, listErr := manager.ListTabs(context.Background(), TabsRequest{SessionID: opened.SessionID})
		reconcileDone <- listErr
	}()
	select {
	case <-session.listEntered:
	case <-time.After(time.Second):
		t.Fatal("状态读取没有进入受控的 ListTabs")
	}

	viewportDone := make(chan error, 1)
	go func() {
		viewportDone <- manager.SetViewport(context.Background(), ViewportRequest{
			SessionID: opened.SessionID, PageID: opened.PageID,
			Viewport: Viewport{Width: 801, Height: 601},
		})
	}()
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	streamDone := make(chan error, 1)
	go func() {
		_, subscribeErr := manager.SubscribeLiveFrames(streamCtx)
		streamDone <- subscribeErr
	}()

	select {
	case err := <-viewportDone:
		if err != nil {
			t.Fatalf("SetViewport() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("viewport 被状态 reconcile 阻塞")
	}
	select {
	case err := <-streamDone:
		if err != nil {
			t.Fatalf("SubscribeLiveFrames() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("直播订阅被状态 reconcile 阻塞")
	}
	close(session.releaseList)
	select {
	case err := <-reconcileDone:
		if err != nil {
			t.Fatalf("ListTabs() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("状态 reconcile 没有退出")
	}
}

func TestManagerKeepsOldViewportWhenBrowserRejectsResize(t *testing.T) {
	base := &fakeSession{page: &fakePage{info: PageInfo{URL: "about:blank"}}}
	session := &fakeViewportSession{fakeSession: base, viewportErr: errors.New("CDP failed")}
	manager, err := NewManager(Config{
		AllowPrivateNetwork: true,
		Factory:             func(context.Context, SessionConfig) (Session, error) { return session, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	initial := Viewport{Width: 800, Height: 600}
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com", Viewport: initial})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.SetViewport(context.Background(), ViewportRequest{SessionID: opened.SessionID, PageID: opened.PageID, Viewport: Viewport{Width: 900, Height: 700}})
	if err == nil || !strings.Contains(err.Error(), "CDP failed") {
		t.Fatalf("SetViewport() error=%v", err)
	}
	state, stateErr := manager.LiveState(context.Background())
	if stateErr != nil || state.Viewport != initial {
		t.Fatalf("失败后 LiveState()=%+v, %v", state, stateErr)
	}
}

func TestManagerLiveFramesFallBackToScreenshots(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	if _, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	frames, err := manager.SubscribeLiveFrames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frames:
		if string(frame.Data) != "png" || frame.MIME != "image/png" {
			t.Fatalf("回退画面=%+v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("没有收到截图回退画面")
	}
	cancel()
	select {
	case _, ok := <-frames:
		if ok {
			t.Fatal("取消订阅后图流仍然打开")
		}
	case <-time.After(time.Second):
		t.Fatal("取消订阅后图流没有关闭")
	}
}

func TestLiveScreencastRestartsWhenViewportChanges(t *testing.T) {
	previousFirstFrameTimeout := liveFirstFrameTimeout
	liveFirstFrameTimeout = 20 * time.Millisecond
	t.Cleanup(func() { liveFirstFrameTimeout = previousFirstFrameTimeout })
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{
		URL: "https://example.com", Viewport: Viewport{Width: 800, Height: 600},
	})
	if err != nil {
		t.Fatal(err)
	}
	current := manager.sessions[opened.SessionID]
	streamer := &fakeScreencastPage{fakePage: &fakePage{info: PageInfo{URL: opened.URL}}, starts: make(chan chan Screenshot, 2)}
	current.mu.Lock()
	current.tabs[opened.PageID].page = streamer
	current.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, err := manager.SubscribeLiveFrames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamer.starts:
	case <-time.After(time.Second):
		t.Fatal("screencast 没有启动")
	}
	select {
	case frame := <-frames:
		if string(frame.Data) != "png" {
			t.Fatalf("无首帧时的兜底画面=%q", frame.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("screencast 无首帧时没有发布有界截图")
	}
	current.mu.Lock()
	current.tabs[opened.PageID].viewport = Viewport{Width: 801, Height: 601}
	current.mu.Unlock()
	select {
	case <-streamer.starts:
	case <-time.After(time.Second):
		t.Fatal("viewport 变化后 screencast 没有原地重启")
	}
	select {
	case frame := <-frames:
		if string(frame.Data) != "png" {
			t.Fatalf("重启后的直播帧=%q", frame.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("重启后没有通过原 WebSocket 订阅发布新尺寸兜底帧")
	}
	// 先等待 live goroutine 退出，再由 Cleanup 恢复测试用的全局 timeout。
	// 仅 cancel 订阅会让 hub 从 Manager 脱钩，但不能保证 run 已经结束。
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLiveScreencastAppliesDefaultSourceCompression(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	current := manager.sessions[opened.SessionID]
	streamer := &fakeScreencastPage{
		fakePage: &fakePage{info: PageInfo{URL: opened.URL}},
		starts:   make(chan chan Screenshot, 1),
		options:  make(chan ScreencastOptions, 1),
	}
	current.mu.Lock()
	current.tabs[opened.PageID].page = streamer
	current.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := manager.SubscribeLiveFrames(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case options := <-streamer.options:
		if options.Quality != 75 || options.EveryNthFrame != 1 {
			t.Fatalf("默认 screencast options=%+v", options)
		}
	case <-time.After(time.Second):
		t.Fatal("screencast 没有启动")
	}
}

func TestManagerLiveFramesFollowActiveSession(t *testing.T) {
	created := []*fakeSession{
		{page: &fakePage{info: PageInfo{URL: "about:blank"}, screenshotData: []byte("first")}},
		{page: &fakePage{info: PageInfo{URL: "about:blank"}, screenshotData: []byte("second")}},
	}
	next := 0
	manager, err := NewManager(Config{AllowPrivateNetwork: true, Factory: func(context.Context, SessionConfig) (Session, error) {
		session := created[next]
		next++
		return session, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if _, err := manager.Open(context.Background(), OpenRequest{URL: "https://one.example"}); err != nil {
		t.Fatal(err)
	}
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	firstFrames, err := manager.SubscribeLiveFrames(firstCtx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-firstFrames:
		if string(frame.Data) != "first" {
			t.Fatalf("第一条图流=%q", frame.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("没有收到第一条图流")
	}
	if _, err := manager.Open(context.Background(), OpenRequest{URL: "https://two.example"}); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-firstFrames:
		if ok {
			t.Fatal("切换 active session 后旧图流没有关闭")
		}
	case <-time.After(time.Second):
		t.Fatal("切换 active session 后旧图流关闭超时")
	}
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	secondFrames, err := manager.SubscribeLiveFrames(secondCtx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-secondFrames:
		if string(frame.Data) != "second" {
			t.Fatalf("第二条图流=%q", frame.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("没有收到第二条图流")
	}
}

func TestManagerBlocksPrivateAndUnapprovedOrigins(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowedOrigins: []string{"https://example.com"}})
	for _, address := range []string{"http://127.0.0.1:8080", "http://localhost.:8080", "https://other.example"} {
		if _, err := manager.Open(context.Background(), OpenRequest{URL: address}); err == nil {
			t.Fatalf("Open(%q) 没有拒绝", address)
		} else {
			var structured *Error
			if !errors.As(err, &structured) || structured.Kind != "navigation_blocked" {
				t.Fatalf("Open(%q) 错误=%v", address, err)
			}
		}
	}
}

func TestManagerCloseIsIdempotent(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	session := manager.sessions[opened.SessionID].browser.(*fakeSession)
	ctx := context.Background()
	first, err := manager.CloseSession(ctx, CloseRequest{SessionID: opened.SessionID})
	if err != nil || !first.Closed {
		t.Fatalf("第一次关闭=%+v, %v", first, err)
	}
	second, err := manager.CloseSession(ctx, CloseRequest{SessionID: opened.SessionID})
	if err != nil || !second.Closed {
		t.Fatalf("重复关闭=%+v, %v", second, err)
	}
	if !session.closed {
		t.Fatal("关闭 session 没有释放 fake 浏览器")
	}
}

func TestCloseActivePublishesLogicalCloseBeforeBrowserCleanup(t *testing.T) {
	session := &blockingCloseSession{
		fakeSession:  &fakeSession{page: &fakePage{info: PageInfo{URL: "about:blank"}}},
		closeEntered: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	manager, err := NewManager(Config{
		AllowPrivateNetwork: true,
		Factory: func(context.Context, SessionConfig) (Session, error) {
			return session, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if _, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	subscribeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := manager.SubscribeState(subscribeCtx)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.CloseActive(context.Background()) }()
	select {
	case <-session.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("Browser 清理没有开始")
	}
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("底层 Browser 清理阻塞时没有先发布逻辑关闭")
	}
	close(session.releaseClose)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestBrowserToolsUseRevisionAndDoNotEchoTypeText(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil || snapshot.Revision != 1 {
		t.Fatalf("快照=%+v, %v", snapshot, err)
	}
	response, err := manager.Type(ctx, TypeRequest{SessionID: opened.SessionID, PageID: opened.PageID, Ref: "e1", Revision: snapshot.Revision, Text: "secret-password"})
	if err != nil {
		t.Fatal(err)
	}
	if response.TextLength != len([]rune("secret-password")) || response.Submitted {
		t.Fatalf("输入响应=%+v", response)
	}
}

func TestBrowserHoverUsesSnapshotReferenceWithoutExposingMoves(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.Hover(ctx, HoverRequest{SessionID: opened.SessionID, PageID: opened.PageID, Ref: snapshot.Elements[0].Ref, Revision: snapshot.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if !session.page.hoverCalled || response.Ref != snapshot.Elements[0].Ref || response.TabsRevision == 0 {
		t.Fatalf("hover 没有使用快照 ref 或返回稳定 Tab 元数据: response=%+v hover=%v", response, session.page.hoverCalled)
	}
}

func TestBrowserNavigateHasOneWaitBehavior(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	session.page.idleCalled = false
	if _, err := manager.Navigate(context.Background(), NavigateRequest{SessionID: opened.SessionID, PageID: opened.PageID, URL: "https://example.com/next"}); err != nil {
		t.Fatal(err)
	}
	if !session.page.idleCalled {
		t.Fatal("Manager.Navigate 没有执行统一的页面空闲等待")
	}
}

func TestTabStatusChangesTabsRevision(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := manager.ListTabs(context.Background(), TabsRequest{SessionID: opened.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	current := manager.sessions[opened.SessionID]
	current.mu.Lock()
	current.tabs[opened.PageID].status = "loading"
	current.mu.Unlock()
	after, err := manager.ListTabs(context.Background(), TabsRequest{SessionID: opened.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if after.TabsRevision <= before.TabsRevision {
		t.Fatalf("Tab status 改变没有推进 tabs_revision: before=%d after=%d", before.TabsRevision, after.TabsRevision)
	}
}

func TestBrowserHoverWaitsForSessionInteraction(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil {
		t.Fatal(err)
	}
	current := manager.sessions[opened.SessionID]
	current.interactionMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := manager.Hover(ctx, HoverRequest{SessionID: opened.SessionID, PageID: opened.PageID, Ref: snapshot.Elements[0].Ref, Revision: snapshot.Revision})
		done <- err
	}()
	select {
	case err := <-done:
		current.interactionMu.Unlock()
		t.Fatalf("hover 没有等待 session 交互锁: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	current.interactionMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("释放交互锁后 hover 没有继续")
	}
}

func TestBrowserSnapshotDefaultsToReconciledActiveTab(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	activeSnapshot, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID})
	if err != nil || activeSnapshot.PageID != opened.PageID || activeSnapshot.ActivePageID != opened.PageID {
		t.Fatalf("browserSnapshot 缺少 page_id 应使用 active Tab: %+v, %v", activeSnapshot, err)
	}
	if _, err := manager.Type(ctx, TypeRequest{SessionID: opened.SessionID, Ref: "e1", Revision: 1, Text: "value"}); err == nil {
		t.Fatal("browserType 缺少 page_id 时不应回退到 active Tab")
	}
	if _, err := manager.Screenshot(ctx, ScreenshotRequest{SessionID: opened.SessionID}); err == nil {
		t.Fatal("browserScreenshot 缺少 page_id 时不应回退到 active Tab")
	}
	current := manager.sessions[opened.SessionID]
	current.mu.Lock()
	second := manager.registerPage(current, &fakePage{info: PageInfo{URL: "https://other.example"}}, defaultBrowserViewport, "")
	current.mu.Unlock()
	snapshot, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil || len(snapshot.Elements) == 0 {
		t.Fatalf("源 Tab 快照=%+v, %v", snapshot, err)
	}
	if _, err := manager.Type(ctx, TypeRequest{SessionID: opened.SessionID, PageID: second.id, Ref: snapshot.Elements[0].Ref, Revision: snapshot.Revision, Text: "value"}); err == nil {
		t.Fatal("来自另一个 Tab 的 ref 不应被接受")
	}
}

func TestBrowserSnapshotReportsStableTabRevisionAndNewActiveTab(t *testing.T) {
	session := &fakeViewportSession{fakeSession: &fakeSession{}}
	manager, err := NewManager(Config{AllowPrivateNetwork: true, Factory: func(context.Context, SessionConfig) (Session, error) {
		return session, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if first.TabsRevision == 0 || second.TabsRevision != first.TabsRevision || second.TabsChanged {
		t.Fatalf("无 Tab 变化时 revision 应稳定且 tabs_changed=false: first=%+v second=%+v", first, second)
	}
	current := manager.sessions[opened.SessionID]
	current.mu.Lock()
	newTarget := "target-popup"
	current.mu.Unlock()
	session.tabs = []TabInfo{
		{TargetID: "target-1", URL: "https://example.com", Title: "example", Type: "page"},
		{TargetID: newTarget, URL: "https://popup.example", Title: "popup", Type: "page"},
	}
	third, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if third.TabsRevision <= second.TabsRevision || !third.TabsChanged || third.ActivePageID == first.ActivePageID || third.PageID != third.ActivePageID || len(third.Tabs) != 2 {
		t.Fatalf("新 Tab 应在 Snapshot 中被发现并成为 active: %+v", third)
	}
	fourth, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if fourth.TabsRevision != third.TabsRevision || fourth.TabsChanged {
		t.Fatalf("重复 Snapshot 不应重复报告 Tab 变化: third=%+v fourth=%+v", third, fourth)
	}
}

func TestBrowserInteractionReleasesLockBeforeTabReconcile(t *testing.T) {
	previous := tabReconcileTimeout
	tabReconcileTimeout = 300 * time.Millisecond
	t.Cleanup(func() { tabReconcileTimeout = previous })
	base := &fakeSession{page: &fakePage{info: PageInfo{URL: "about:blank"}}}
	session := &blockingListViewportSession{
		fakeViewportSession: &fakeViewportSession{fakeSession: base},
		listEntered:         make(chan struct{}),
		releaseList:         make(chan struct{}),
	}
	manager, err := NewManager(Config{AllowPrivateNetwork: true, Factory: func(context.Context, SessionConfig) (Session, error) {
		return session, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil {
		t.Fatal(err)
	}
	session.blockList = true
	session.listOnce = sync.Once{}
	done := make(chan struct{})
	go func() {
		_, _ = manager.Click(ctx, ClickRequest{SessionID: opened.SessionID, PageID: opened.PageID, Ref: snapshot.Elements[0].Ref, Revision: snapshot.Revision})
		close(done)
	}()
	select {
	case <-session.listEntered:
	case <-time.After(time.Second):
		t.Fatal("点击没有进入 Tab reconcile")
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	if err := lockInteraction(probeCtx, &manager.sessions[opened.SessionID].interactionMu); err != nil {
		cancel()
		close(session.releaseList)
		t.Fatalf("Tab reconcile 期间 interactionMu 仍被点击持有，可能卡死: %v", err)
	}
	manager.sessions[opened.SessionID].interactionMu.Unlock()
	cancel()
	close(session.releaseList)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("点击在 Tab reconcile 后没有返回")
	}
}

func TestBrowserScreenshotUsesDistinctPaths(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true, ScreenshotDir: t.TempDir()})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := ScreenshotRequest{SessionID: opened.SessionID, PageID: opened.PageID}
	first, err := manager.Screenshot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Screenshot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path {
		t.Fatalf("连续截图覆盖了已返回的文件: %q", first.Path)
	}
}

func TestBrowserScreenshotRestoresActiveTab(t *testing.T) {
	base := &fakeSession{page: &fakePage{info: PageInfo{URL: "about:blank", Title: "blank"}}}
	session := &fakeViewportSession{fakeSession: base}
	manager, err := NewManager(Config{
		AllowPrivateNetwork: true, ScreenshotDir: t.TempDir(),
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
	current := manager.sessions[opened.SessionID]
	current.mu.Lock()
	background := manager.registerPage(current, &fakeTargetPage{
		fakePage: &fakePage{info: PageInfo{URL: "https://example.com/background"}},
		targetID: "target-2",
	}, defaultBrowserViewport, opened.PageID)
	current.mu.Unlock()
	ctx := context.Background()
	if _, err := manager.Screenshot(ctx, ScreenshotRequest{SessionID: opened.SessionID, PageID: background.id}); err != nil {
		t.Fatal(err)
	}
	current.mu.Lock()
	active := current.activeTab
	current.mu.Unlock()
	if active != opened.PageID || len(session.activated) == 0 || session.activated[len(session.activated)-1] != "target-1" {
		t.Fatalf("active=%q activation calls=%v", active, session.activated)
	}
}

func TestBrowserWaitAcceptsSnapshotRefAndCSSSelector(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil || snapshot.Revision == 0 {
		t.Fatalf("snapshot=%+v error=%v", snapshot, err)
	}
	request := WaitRequest{
		SessionID: opened.SessionID, PageID: opened.PageID,
		Condition: "selector_visible", RefOrSelector: snapshot.Elements[0].Ref, Revision: snapshot.Revision,
	}
	if _, err := manager.Wait(ctx, request); err != nil {
		t.Fatalf("snapshot ref 应可直接用于 browserWait: %v", err)
	}
	if session.page.waitOptions.Selector != `[data-browserkit-ref="e1"]` {
		t.Fatalf("snapshot ref 没有转换为安全 selector: %+v", session.page.waitOptions)
	}
	request.RefOrSelector = "example-widget"
	request.Revision = 0
	if _, err := manager.Wait(ctx, request); err != nil {
		t.Fatalf("e 开头的 CSS selector 不应被误判为 ref: %v", err)
	}
	if session.page.waitOptions.Selector != "example-widget" {
		t.Fatalf("CSS selector 被意外改写: %+v", session.page.waitOptions)
	}
	request.RefOrSelector = "eabc123_4"
	if _, err := manager.Wait(ctx, request); err == nil {
		t.Fatal("未知的 snapshot ref 应返回 invalid_reference")
	}
}

func TestBrowserTypeDefaultsToClearAndAllowsExplicitAppend(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot, err := manager.Snapshot(ctx, SnapshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil {
		t.Fatal(err)
	}

	var request TypeRequest
	err = json.Unmarshal([]byte(`{"session_id":"`+opened.SessionID+`","page_id":"`+opened.PageID+`","ref":"e1","revision":`+fmt.Sprint(snapshot.Revision)+`,"text":"new value"}`), &request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Type(ctx, request); err != nil {
		t.Fatal(err)
	}
	if !session.page.lastClear {
		t.Fatal("省略 clear 的 browserType 没有清空原值")
	}

	var appendRequest TypeRequest
	err = json.Unmarshal([]byte(`{"session_id":"`+opened.SessionID+`","page_id":"`+opened.PageID+`","ref":"e1","revision":`+fmt.Sprint(snapshot.Revision)+`,"text":"suffix","append":true}`), &appendRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Type(ctx, appendRequest); err != nil {
		t.Fatal(err)
	}
	if session.page.lastClear {
		t.Fatal("append=true 时不应清空原值")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestBrowserOpenInjectsBeforeNavigationAndEvaluateIsGated(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true, AllowEvaluate: true})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com", InjectJS: "window.__browserkit = true"})
	if err != nil {
		t.Fatal(err)
	}
	if session.options.InjectScript != "window.__browserkit = true" {
		t.Fatalf("注入脚本没有在创建页面时传递: %#v", session.options)
	}
	if !session.page.idleCalled {
		t.Fatal("browserOpen 没有等待页面空闲")
	}
	if opened.LoadStatus != OpenLoadLoaded {
		t.Fatalf("browserOpen load_status=%q", opened.LoadStatus)
	}
	ctx := context.Background()
	response, err := manager.Evaluate(ctx, EvaluateRequest{SessionID: opened.SessionID, PageID: opened.PageID, Code: "document.title"})
	if err != nil || string(response.Value) != `"ok"` {
		t.Fatalf("evaluate=%#v error=%v", response, err)
	}
	blocked, _ := fakeManager(t, Config{AllowPrivateNetwork: true})
	if _, err := blocked.Evaluate(ctx, EvaluateRequest{SessionID: opened.SessionID, PageID: opened.PageID, Code: "1"}); err == nil {
		t.Fatal("未启用 JavaScript 时 evaluate 没有拒绝")
	}
}

func TestBrowserOpenExistingPageAppliesInjectScript(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true, AllowEvaluate: true})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	session.page.injectedScripts = nil
	session.page.evaluatedScripts = nil
	const script = "window.__diagnostic = true"
	if _, err := manager.Open(context.Background(), OpenRequest{SessionID: opened.SessionID, InjectJS: script}); err != nil {
		t.Fatalf("复用 page 时 inject_js 不应被拒绝: %v", err)
	}
	if len(session.page.injectedScripts) != 1 || session.page.injectedScripts[0] != script {
		t.Fatalf("已有 page 没有注册后续 document 脚本: %+v", session.page.injectedScripts)
	}
	if len(session.page.evaluatedScripts) != 1 || session.page.evaluatedScripts[0] != script {
		t.Fatalf("已有 page 没有执行当前 document 脚本: %+v", session.page.evaluatedScripts)
	}
	if _, err := manager.Open(context.Background(), OpenRequest{SessionID: opened.SessionID, InjectJS: script}); err != nil {
		t.Fatal(err)
	}
	if len(session.page.injectedScripts) != 1 || len(session.page.evaluatedScripts) != 2 {
		t.Fatalf("重复 browserOpen 不应重复注册脚本: injected=%d evaluated=%d", len(session.page.injectedScripts), len(session.page.evaluatedScripts))
	}
}

func TestBrowserOpenValidatesURLBeforeInjectingIntoExistingPage(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true, AllowEvaluate: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	session.page.injectedScripts = nil
	session.page.evaluatedScripts = nil
	if _, err := manager.Open(context.Background(), OpenRequest{SessionID: opened.SessionID, URL: "file:///tmp/blocked", InjectJS: "window.__blocked = true"}); err == nil {
		t.Fatal("非法 URL 没有被拒绝")
	}
	if len(session.page.injectedScripts) != 0 || len(session.page.evaluatedScripts) != 0 {
		t.Fatalf("URL 校验失败前已经产生注入副作用: injected=%v evaluated=%v", session.page.injectedScripts, session.page.evaluatedScripts)
	}
}

func TestBrowserOpenReportsIdleTimeoutWithoutDiscardingPage(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	session.page = &fakePage{info: PageInfo{URL: "about:blank", Title: "blank"}, idleErr: context.DeadlineExceeded}
	previous := openIdleTimeout
	openIdleTimeout = time.Millisecond
	t.Cleanup(func() { openIdleTimeout = previous })

	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if opened.LoadStatus != OpenLoadTimeout {
		t.Fatalf("超时状态=%q", opened.LoadStatus)
	}
	if _, _, err := manager.get(context.Background(), opened.SessionID, opened.PageID); err != nil {
		t.Fatalf("页面超时后不应丢失: %v", err)
	}
}

func TestBrowserOpenRefreshesPageInfoAfterIdle(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	session.page = &fakePage{
		info:           PageInfo{URL: "about:blank", Title: "Example Domain"},
		navigateResult: &PageInfo{URL: "", Title: "about:blank"},
	}
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if opened.URL != "https://example.com" || opened.Title != "Example Domain" {
		t.Fatalf("browserOpen 没有返回稳定后的页面信息: %+v", opened)
	}
}

func TestBrowserOpenReportsOtherErrorsAsOpenFailed(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	session.page = &fakePage{info: PageInfo{URL: "about:blank", Title: "blank"}, idleErr: errors.New("attach failed")}
	if _, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"}); err == nil {
		t.Fatal("非超时等待错误没有失败")
	} else {
		var structured *Error
		if !errors.As(err, &structured) || structured.Kind != "open_failed" {
			t.Fatalf("非超时错误=%v", err)
		}
	}
}

func TestManagerReclaimsIdleSessionWithoutExternalClose(t *testing.T) {
	previousInterval := sessionIdleCheckInterval
	sessionIdleCheckInterval = time.Millisecond
	t.Cleanup(func() { sessionIdleCheckInterval = previousInterval })
	manager, _ := fakeManager(t, Config{AllowPrivateNetwork: true, IdleTimeout: 5 * time.Millisecond})
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		_, present := manager.sessions[opened.SessionID]
		manager.mu.Unlock()
		if !present {
			break
		}
		time.Sleep(time.Millisecond)
	}
	manager.mu.Lock()
	_, present := manager.sessions[opened.SessionID]
	manager.mu.Unlock()
	if present {
		t.Fatal("空闲浏览器 session 未被自动回收")
	}
	if _, _, err := manager.get(context.Background(), opened.SessionID, opened.PageID); err == nil {
		t.Fatal("自动回收后 session 仍可访问")
	}
	_ = manager.Close(context.Background())
}

func TestReconcileTabsUsesOneShortBudget(t *testing.T) {
	previous := tabReconcileTimeout
	tabReconcileTimeout = 20 * time.Millisecond
	t.Cleanup(func() { tabReconcileTimeout = previous })
	base := &fakeSession{page: &fakePage{info: PageInfo{URL: "about:blank"}}}
	session := &blockingListViewportSession{
		fakeViewportSession: &fakeViewportSession{fakeSession: base},
		listEntered:         make(chan struct{}),
		releaseList:         make(chan struct{}),
	}
	manager, err := NewManager(Config{
		AllowPrivateNetwork: true,
		Factory:             func(context.Context, SessionConfig) (Session, error) { return session, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	session.blockList = true
	session.listOnce = sync.Once{}
	manager.mu.Lock()
	manager.active = ""
	manager.mu.Unlock()
	started := time.Now()
	err = manager.reconcileTabs(context.Background(), manager.sessions[opened.SessionID])
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("Tab reconcile 没有共享短预算: elapsed=%s err=%v", time.Since(started), err)
	}
}

func TestExplicitPageOperationDoesNotRunStateReconcile(t *testing.T) {
	previous := tabReconcileTimeout
	tabReconcileTimeout = 500 * time.Millisecond
	t.Cleanup(func() { tabReconcileTimeout = previous })
	base := &fakeSession{page: &fakePage{info: PageInfo{URL: "about:blank"}}}
	session := &blockingListViewportSession{
		fakeViewportSession: &fakeViewportSession{fakeSession: base},
		listEntered:         make(chan struct{}),
		releaseList:         make(chan struct{}),
	}
	manager, err := NewManager(Config{
		AllowPrivateNetwork: true,
		Factory:             func(context.Context, SessionConfig) (Session, error) { return session, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	session.blockList = true
	session.listOnce = sync.Once{}
	started := time.Now()
	if _, err := manager.ActivateTab(context.Background(), TabRequest{SessionID: opened.SessionID, PageID: opened.PageID}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("显式 page 操作被状态 reconcile 阻塞: %s", elapsed)
	}
	manager.mu.Lock()
	activeSession := manager.active
	manager.mu.Unlock()
	if activeSession != opened.SessionID {
		t.Fatalf("显式 page 操作没有恢复 active browser session: %q", activeSession)
	}
}
