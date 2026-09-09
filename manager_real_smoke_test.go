//go:build browser_smoke

package browserkit

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRealBrowserManagerWorkflow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`<!doctype html><title>Main</title>
<style>@keyframes drift { from { transform: translateX(0) } to { transform: translateX(1px) } } #apply { animation: drift 1s linear infinite alternate }</style>
<label>Name <input aria-label="Name" onkeydown="if(event.key === 'Enter') document.querySelector('#result').textContent = 'enter:' + this.value"></label>
<button id="apply" onclick="document.querySelector('#result').textContent = document.querySelector('input').value">Apply</button>
<a href="/popup" target="_blank">Popup</a><div id="result"></div>`))
	})
	mux.HandleFunc("/popup", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`<!doctype html><title>Popup</title><h1>Popup ready</h1>`))
	})
	mux.HandleFunc("/other", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`<!doctype html><title>Other</title><h1>Other page</h1>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	manager, err := NewManager(Config{
		AllowEvaluate: true, AllowPrivateNetwork: true,
		ScreenshotDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	ctx := context.Background()

	open := functionByName(t, manager, "browserOpen").Handler.(func(context.Context, OpenRequest) (OpenResponse, error))
	snapshotPage := functionByName(t, manager, "browserSnapshot").Handler.(func(context.Context, SnapshotRequest) (SnapshotResponse, error))
	typeText := functionByName(t, manager, "browserType").Handler.(func(context.Context, TypeRequest) (TypeResponse, error))
	opened, err := open(ctx, OpenRequest{
		URL: server.URL, Viewport: Viewport{Width: 960, Height: 640},
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.LoadStatus != OpenLoadLoaded || opened.Title != "Main" {
		t.Fatalf("browserOpen=%+v", opened)
	}

	snapshot, err := snapshotPage(ctx, SnapshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil {
		t.Fatal(err)
	}
	inputRef := smokeElementRef(t, snapshot, "textbox", "Name")
	applyRef := smokeElementRef(t, snapshot, "button", "Apply")
	popupRef := smokeElementRef(t, snapshot, "link", "Popup")
	if _, err := typeText(ctx, TypeRequest{
		SessionID: opened.SessionID, PageID: opened.PageID,
		Ref: inputRef, Revision: snapshot.Revision, Text: "browserkit",
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DispatchLiveInput(ctx, opened.PageID, InputEvent{Kind: "shortcut", Action: "select_all"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DispatchLiveInput(ctx, opened.PageID, InputEvent{Kind: "text", Text: "shortcut"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		value, evaluateErr := manager.Evaluate(ctx, EvaluateRequest{
			SessionID: opened.SessionID, PageID: opened.PageID, Code: `document.querySelector('input').value`,
		})
		if evaluateErr == nil && string(value.Value) == `"shortcut"` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("直播快捷键没有在真实页面全选文本: value=%s err=%v", value.Value, evaluateErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, event := range []InputEvent{
		{Kind: "shortcut", Action: "select_all"},
		{Kind: "shortcut", Action: "copy"},
		{Kind: "text", Text: "changed"},
		{Kind: "shortcut", Action: "select_all"},
		{Kind: "shortcut", Action: "paste"},
	} {
		if err := manager.DispatchLiveInput(ctx, opened.PageID, event); err != nil {
			t.Fatal(err)
		}
	}
	deadline = time.Now().Add(time.Second)
	for {
		value, evaluateErr := manager.Evaluate(ctx, EvaluateRequest{
			SessionID: opened.SessionID, PageID: opened.PageID, Code: `document.querySelector('input').value`,
		})
		if evaluateErr == nil && string(value.Value) == `"shortcut"` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("直播复制粘贴没有在真实页面恢复文本: value=%s err=%v", value.Value, evaluateErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := manager.Press(ctx, PressRequest{SessionID: opened.SessionID, PageID: opened.PageID, Key: "Enter"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(ctx, WaitRequest{
		SessionID: opened.SessionID, PageID: opened.PageID,
		Condition: "selector_visible", RefOrSelector: "#result", TimeoutMS: 1_000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(ctx, WaitRequest{
		SessionID: opened.SessionID, PageID: opened.PageID,
		Condition: "selector_visible", RefOrSelector: inputRef, Revision: snapshot.Revision, TimeoutMS: 1_000,
	}); err != nil {
		t.Fatalf("browserWait 不支持 snapshot ref: %v", err)
	}
	pressed, err := manager.Evaluate(ctx, EvaluateRequest{
		SessionID: opened.SessionID, PageID: opened.PageID,
		Code: `document.querySelector('#result').textContent`,
	})
	if err != nil || string(pressed.Value) != `"enter:shortcut"` {
		t.Fatalf("browserPress=%s, %v", pressed.Value, err)
	}
	if _, err := manager.Click(ctx, ClickRequest{
		SessionID: opened.SessionID, PageID: opened.PageID,
		Ref: applyRef, Revision: snapshot.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	evaluated, err := manager.Evaluate(ctx, EvaluateRequest{
		SessionID: opened.SessionID, PageID: opened.PageID,
		Code: `document.querySelector('#result').textContent`,
	})
	if err != nil || string(evaluated.Value) != `"shortcut"` {
		t.Fatalf("browserEvaluate=%s, %v", evaluated.Value, err)
	}
	shot, err := manager.Screenshot(ctx, ScreenshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
	if err != nil || shot.Bytes == 0 || shot.Format != "png" {
		t.Fatalf("browserScreenshot=%+v, %v", shot, err)
	}

	if _, err := manager.Click(ctx, ClickRequest{
		SessionID: opened.SessionID, PageID: opened.PageID,
		Ref: popupRef, Revision: snapshot.Revision, WaitAfterMS: 200,
	}); err != nil {
		t.Fatal(err)
	}
	tabs := waitForSmokeTabs(t, manager, opened.SessionID, 2)
	popupID := tabs.ActivePageID
	if popupID == opened.PageID {
		t.Fatalf("Popup 没有成为 active Tab: %+v", tabs)
	}
	if _, err := manager.Screenshot(ctx, ScreenshotRequest{SessionID: opened.SessionID, PageID: opened.PageID}); err != nil {
		t.Fatalf("后台 Tab 截图失败: %v", err)
	}
	tabs, err = manager.ListTabs(ctx, TabsRequest{SessionID: opened.SessionID})
	if err != nil || tabs.ActivePageID != popupID {
		t.Fatalf("后台 Tab 截图改变了 active Tab: %+v, %v", tabs, err)
	}
	state, err := manager.LiveState(ctx)
	if err != nil || state.Title != "Popup" {
		t.Fatalf("Popup LiveState=%+v, %v", state, err)
	}
	frame, err := manager.LiveFrame(ctx)
	if err != nil || len(frame.Data) == 0 || !strings.HasPrefix(frame.MIME, "image/") {
		t.Fatalf("Popup LiveFrame=%+v, %v", frame, err)
	}
	liveCtx, stopLive := context.WithCancel(ctx)
	liveFrames, err := manager.SubscribeLiveFrames(liveCtx)
	if err != nil {
		stopLive()
		t.Fatal(err)
	}
	waitForSmokeFrameSize(t, liveFrames, 960, 640)
	resizedViewport := Viewport{Width: 900, Height: 620}
	if err := manager.SetViewport(ctx, ViewportRequest{SessionID: opened.SessionID, PageID: popupID, Viewport: resizedViewport}); err != nil {
		stopLive()
		t.Fatal(err)
	}
	waitForSmokeFrameSize(t, liveFrames, resizedViewport.Width, resizedViewport.Height)
	stopLive()
	if _, err := manager.ActivateTab(ctx, TabRequest{SessionID: opened.SessionID, PageID: opened.PageID}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CloseTab(ctx, TabRequest{SessionID: opened.SessionID, PageID: popupID}); err != nil {
		t.Fatal(err)
	}
	tabs, err = manager.ListTabs(ctx, TabsRequest{SessionID: opened.SessionID})
	if err != nil || len(tabs.Tabs) != 1 || tabs.ActivePageID != opened.PageID {
		t.Fatalf("关闭 Popup 后 tabs=%+v, %v", tabs, err)
	}

	reused, err := manager.Open(ctx, OpenRequest{SessionID: opened.SessionID, URL: "/other"})
	if err != nil || reused.PageID != opened.PageID || reused.Title != "Other" {
		t.Fatalf("复用 session 导航=%+v, %v", reused, err)
	}
	closed, err := manager.CloseSession(ctx, CloseRequest{SessionID: opened.SessionID})
	if err != nil || !closed.Closed {
		t.Fatalf("browserClose=%+v, %v", closed, err)
	}
	closed, err = manager.CloseSession(ctx, CloseRequest{SessionID: opened.SessionID})
	if err != nil || !closed.Closed {
		t.Fatalf("重复 browserClose=%+v, %v", closed, err)
	}
}

func waitForSmokeFrameSize(t *testing.T, frames <-chan LiveFrame, width, height int) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatalf("等待 %dx%d 直播帧时图流关闭", width, height)
			}
			config, _, err := image.DecodeConfig(bytes.NewReader(frame.Data))
			if err == nil && config.Width == width && config.Height == height {
				return
			}
		case <-timer.C:
			t.Fatalf("等待 %dx%d 直播帧超时", width, height)
		}
	}
}

func smokeElementRef(t *testing.T, snapshot SnapshotResponse, role, name string) string {
	t.Helper()
	for _, element := range snapshot.Elements {
		if element.Role == role && element.Name == name {
			return element.Ref
		}
	}
	t.Fatalf("快照中没有 %s %q: %+v", role, name, snapshot.Elements)
	return ""
}

func waitForSmokeTabs(t *testing.T, manager *Manager, sessionID string, count int) TabsResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tabs, err := manager.ListTabs(context.Background(), TabsRequest{SessionID: sessionID})
		if err == nil && len(tabs.Tabs) == count {
			return tabs
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待 %d 个 Browser Tab 超时", count)
	return TabsResponse{}
}
