package browserhttp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zeoxisca/awesome-browserkit"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type testPage struct {
	browserkit.Page
	mu       sync.Mutex
	id       string
	info     browserkit.PageInfo
	inputs   chan browserkit.InputEvent
	viewport browserkit.Viewport
}

func (p *testPage) Info(context.Context) (browserkit.PageInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info, nil
}
func (p *testPage) Navigate(_ context.Context, address string) (browserkit.PageInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.info = browserkit.PageInfo{URL: address, Title: "Fixture"}
	return p.info, nil
}
func (p *testPage) Close(context.Context) error                   { return nil }
func (p *testPage) WaitIdle(context.Context, time.Duration) error { return nil }
func (p *testPage) Screenshot(context.Context, browserkit.ScreenshotOptions) (browserkit.Screenshot, error) {
	return browserkit.Screenshot{Data: []byte("jpeg"), Format: "jpeg"}, nil
}
func (p *testPage) DispatchInput(_ context.Context, event browserkit.InputEvent) error {
	p.inputs <- event
	return nil
}
func (p *testPage) TargetID() string { return p.id }
func (p *testPage) StartDevtools(_ context.Context, emit func(browserkit.DevtoolsEvent)) (func(), error) {
	emit(browserkit.DevtoolsEvent{Kind: "console", Level: "info", Text: "fixture console"})
	return func() {}, nil
}

type testSession struct {
	browserkit.Session
	mu     sync.Mutex
	pages  []*testPage
	closed bool
}

func (s *testSession) NewPage(_ context.Context, options browserkit.PageOptions) (browserkit.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &testPage{id: string(rune('a' + len(s.pages))), info: browserkit.PageInfo{URL: "about:blank", Title: "Fixture"}, inputs: make(chan browserkit.InputEvent, 20), viewport: options.Viewport}
	s.pages = append(s.pages, p)
	return p, nil
}
func (s *testSession) Closed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }
func (s *testSession) Close() error { s.mu.Lock(); defer s.mu.Unlock(); s.closed = true; return nil }
func (s *testSession) ListTabs(ctx context.Context) ([]browserkit.TabInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []browserkit.TabInfo
	for _, p := range s.pages {
		i, _ := p.Info(ctx)
		result = append(result, browserkit.TabInfo{TargetID: p.id, URL: i.URL, Title: i.Title, Type: "page"})
	}
	return result, nil
}
func (s *testSession) PageForTab(_ context.Context, id string) (browserkit.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pages {
		if p.id == id {
			return p, nil
		}
	}
	return nil, io.EOF
}
func (s *testSession) ActivateTab(context.Context, string) error { return nil }
func (s *testSession) CloseTab(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.pages {
		if p.id == id {
			s.pages = append(s.pages[:i], s.pages[i+1:]...)
			break
		}
	}
	return nil
}
func (s *testSession) SetTabViewport(_ context.Context, id string, v browserkit.Viewport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pages {
		if p.id == id {
			p.mu.Lock()
			p.viewport = v
			p.mu.Unlock()
		}
	}
	return nil
}
func fixture(t *testing.T) (*browserkit.Manager, *testSession, browserkit.OpenResponse) {
	t.Helper()
	s := &testSession{}
	m, err := browserkit.NewManager(browserkit.Config{AllowDevtools: true, Factory: func(context.Context, browserkit.SessionConfig) (browserkit.Session, error) {
		if s.Closed() {
			return &testSession{}, nil
		}
		return s, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	opened, err := m.Open(context.Background(), browserkit.OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return m, s, opened
}
func call(t *testing.T, h http.Handler, method, path, body, viewer string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(browserkit.ViewerHeader, viewer)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func connect(t *testing.T, address string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}
func control(t *testing.T, c *websocket.Conn) browserkit.ControlState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		kind, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.MessageText {
			var v browserkit.ControlState
			if err = json.Unmarshal(data, &v); err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
}
func TestHandlerMountsAllRoutesWithoutBackend(t *testing.T) {
	m, _, opened := fixture(t)
	h := NewHandler(m, Options{})
	mux := http.NewServeMux()
	mux.Handle("/nested/browser/", http.StripPrefix("/nested/browser", h))
	w := call(t, mux, "GET", "/nested/browser/state", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), opened.PageID) {
		t.Fatalf("state %d: %s", w.Code, w.Body.String())
	}
	w = call(t, mux, "POST", "/nested/browser/state", "", "")
	if w.Code != 405 {
		t.Fatalf("wrong method: %d", w.Code)
	}
}
func TestOriginAndReadOnlyPolicies(t *testing.T) {
	m, _, opened := fixture(t)
	h := NewHandler(m, Options{})
	r := httptest.NewRequest("POST", "http://localhost/tabs", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://untrusted.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("origin: %d", w.Code)
	}
	for _, path := range []string{"/tabs/" + opened.PageID + "/activate", "/control", "/close"} {
		w = call(t, NewHandler(m, Options{ReadOnly: true}), "POST", path, "{}", "viewer")
		if w.Code != 403 {
			t.Fatalf("readonly %s: %d", path, w.Code)
		}
	}
}

func TestAuthMiddlewareProtectsHTTPAndWebSocketHandshake(t *testing.T) {
	m, _, _ := fixture(t)
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-secret" {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	h := NewHandler(m, Options{AuthMiddleware: auth})
	w := call(t, h, http.MethodGet, "/state", "", "")
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("HTTP auth response=%d headers=%v", w.Code, w.Header())
	}
	r := httptest.NewRequest(http.MethodGet, "/state", nil)
	r.Header.Set("Authorization", "Bearer test-secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("authorized HTTP response=%d %s", w.Code, w.Body.String())
	}

	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if connection, response, err := websocket.Dial(ctx, server.URL+"/state/ws", nil); err == nil {
		_ = connection.CloseNow()
		t.Fatal("unauthorized WebSocket handshake succeeded")
	} else if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized WebSocket response=%v err=%v", response, err)
	}
	connection, _, err := websocket.Dial(ctx, server.URL+"/state/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer test-secret"}}})
	if err != nil {
		t.Fatalf("authorized WebSocket handshake failed: %v", err)
	}
	_ = connection.CloseNow()
}
func TestSharedControlAcrossHandlersAndObserverCannotTakeOver(t *testing.T) {
	m, _, _ := fixture(t)
	one := httptest.NewServer(NewHandler(m, Options{}))
	defer one.Close()
	two := httptest.NewServer(NewHandler(m, Options{}))
	defer two.Close()
	readonly := httptest.NewServer(NewHandler(m, Options{ReadOnly: true}))
	defer readonly.Close()
	observer := connect(t, readonly.URL+"/ws?viewer_id=observer")
	if control(t, observer).Controller {
		t.Fatal("readonly viewer became controller")
	}
	a := connect(t, one.URL+"/ws?viewer_id=a")
	if !control(t, a).Controller {
		t.Fatal("first writable viewer missing control")
	}
	b := connect(t, two.URL+"/ws?viewer_id=b")
	if control(t, b).Controller {
		t.Fatal("second handler created separate control state")
	}
	w := call(t, NewHandler(m, Options{}), "POST", "/control", "", "b")
	if w.Code != 200 {
		t.Fatalf("takeover %d %s", w.Code, w.Body.String())
	}
	if m.CanControl("a") || !m.CanControl("b") {
		t.Fatal("takeover not shared")
	}
	w = call(t, NewHandler(m, Options{}), "POST", "/control", "", "a")
	if w.Code != 429 {
		t.Fatalf("cooldown: %d", w.Code)
	}
}
func TestStreamCarriesExplicitFrameIdentityAndRealInput(t *testing.T) {
	m, s, opened := fixture(t)
	server := httptest.NewServer(NewHandler(m, Options{}))
	defer server.Close()
	c := connect(t, server.URL+"/ws?viewer_id=a")
	_ = control(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		kind, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageBinary {
			continue
		}
		n := int(binary.BigEndian.Uint32(data))
		var header struct {
			PageID string `json:"page_id"`
		}
		if err = json.Unmarshal(data[4:4+n], &header); err != nil {
			t.Fatal(err)
		}
		if header.PageID != opened.PageID || !bytes.Equal(data[4+n:], []byte("jpeg")) {
			t.Fatalf("frame identity %s %q", header.PageID, data)
		}
		break
	}
	if err := wsjson.Write(ctx, c, map[string]any{"page_id": opened.PageID, "kind": "text", "text": "中文输入"}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-s.pages[0].inputs:
		if event.Text != "中文输入" {
			t.Fatal(event)
		}
	case <-ctx.Done():
		t.Fatal("input missing")
	}
}
func TestStrictJSONAndExplicitTab(t *testing.T) {
	m, _, opened := fixture(t)
	h := NewHandler(m, Options{})
	_, leave, err := m.JoinViewer("a", false)
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	for _, body := range []string{`{"url":"https://example.com","unknown":true}`, `{"url":"https://example.com"} {}`, `{"width":0,"height":300}`} {
		path := "/tabs/" + opened.PageID + "/navigate"
		if strings.Contains(body, "width") {
			path = "/tabs/" + opened.PageID + "/viewport"
		}
		method := "POST"
		if strings.Contains(path, "viewport") {
			method = "PUT"
		}
		w := call(t, h, method, path, body, "a")
		if w.Code != 400 {
			t.Fatalf("strict input %d %s", w.Code, w.Body.String())
		}
	}
	w := call(t, h, "POST", "/tabs/"+opened.PageID+"/activate", "", "other")
	if w.Code != 409 {
		t.Fatalf("control validation: %d", w.Code)
	}
}
func TestNewTabAndCloseStayInOneRuntime(t *testing.T) {
	m, _, opened := fixture(t)
	h := NewHandler(m, Options{})
	_, leave, _ := m.JoinViewer("a", false)
	defer leave()
	w := call(t, h, "POST", "/tabs", `{"url":"https://example.com/second"}`, "a")
	if w.Code != 200 {
		t.Fatalf("new tab %d %s", w.Code, w.Body.String())
	}
	var second browserkit.OpenResponse
	_ = json.Unmarshal(w.Body.Bytes(), &second)
	if second.SessionID != opened.SessionID || second.PageID == opened.PageID {
		t.Fatalf("not same runtime %+v", second)
	}
	state, err := m.View(context.Background())
	if err != nil || len(state.Tabs) != 2 || state.ActivePageID != second.PageID {
		t.Fatalf("state %+v %v", state, err)
	}
	w = call(t, h, "DELETE", "/tabs/"+second.PageID, "", "a")
	if w.Code != 204 {
		t.Fatalf("close: %d", w.Code)
	}
	state, _ = m.View(context.Background())
	if state.ActivePageID != opened.PageID {
		t.Fatal(state)
	}
}
func TestDiagnosticsPermissionPaginationAndGeneration(t *testing.T) {
	m, _, opened := fixture(t)
	h := NewHandler(m, Options{})
	path := "/tabs/" + opened.PageID + "/devtools"
	if w := call(t, NewHandler(m, Options{DisableDevtools: true}), "GET", path, "", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call(t, h, "GET", path+"?after=-1", "", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	_, leave, _ := m.JoinViewer("a", false)
	defer leave()
	w := call(t, h, "POST", path, "", "a")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "fixture console") {
		t.Fatalf("start %d %s", w.Code, w.Body.String())
	}
	server := httptest.NewServer(h)
	defer server.Close()
	c := connect(t, server.URL+path+"/ws")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var first browserkit.BrowserDevtoolsViewState
	if err := wsjson.Read(ctx, c, &first); err != nil {
		t.Fatal(err)
	}
	_ = call(t, h, "DELETE", path, "", "a")
	_ = call(t, h, "POST", path, "", "a")
	for {
		var next browserkit.BrowserDevtoolsViewState
		if err := wsjson.Read(ctx, c, &next); err != nil {
			t.Fatal(err)
		}
		if next.Generation > first.Generation {
			if len(next.Entries) == 0 {
				t.Fatal("new generation omitted initial entries")
			}
			break
		}
	}
}
func TestDisconnectDoesNotCloseBrowserAndStateReportsFinalClose(t *testing.T) {
	m, s, _ := fixture(t)
	server := httptest.NewServer(NewHandler(m, Options{}))
	defer server.Close()
	c := connect(t, server.URL+"/state/ws")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var state browserkit.BrowserViewState
	if err := wsjson.Read(ctx, c, &state); err != nil {
		t.Fatal(err)
	}
	_ = c.CloseNow()
	if s.Closed() {
		t.Fatal("viewer owns browser lifecycle")
	}
	c = connect(t, server.URL+"/state/ws")
	if err := wsjson.Read(ctx, c, &state); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for {
		if err := wsjson.Read(ctx, c, &state); err != nil {
			t.Fatal(err)
		}
		if state.Closed {
			if len(state.Tabs) != 0 {
				t.Fatal(state)
			}
			break
		}
	}
}

func TestControllerClosesBrowserWhileLiveSocketCarriesDiscreteInput(t *testing.T) {
	m, session, opened := fixture(t)
	h := NewHandler(m, Options{})
	server := httptest.NewServer(h)
	defer server.Close()
	c := connect(t, server.URL+"/ws?viewer_id=writer")
	if !control(t, c).Controller {
		t.Fatal("首个观看者没有操作权")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, event := range []browserkit.InputEvent{
		{Kind: "shortcut", Action: "copy", Modifiers: 4},
		{Kind: "text", Text: "中文输入"},
	} {
		input := struct {
			PageID string `json:"page_id"`
			browserkit.InputEvent
		}{opened.PageID, event}
		if err := wsjson.Write(ctx, c, input); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-session.pages[0].inputs:
			if got.Kind != event.Kind || got.Action != event.Action || got.Modifiers != event.Modifiers || got.Text != event.Text {
				t.Fatalf("input=%+v want=%+v", got, event)
			}
		case <-ctx.Done():
			t.Fatal("图流连接没有继续接收离散输入")
		}
	}
	w := call(t, h, "POST", "/close", "", "writer")
	if w.Code != http.StatusNoContent || !session.Closed() {
		t.Fatalf("关闭浏览器失败: %d %s", w.Code, w.Body.String())
	}
	// 同一连接在关闭后保持操作权，重新打开时继续收到带新页面身份的图片。
	w = call(t, h, "POST", "/tabs", `{"url":"https://example.com/new"}`, "writer")
	if w.Code != http.StatusOK {
		t.Fatalf("reopen=%d %s", w.Code, w.Body.String())
	}
	var reopened browserkit.OpenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &reopened); err != nil {
		t.Fatal(err)
	}
	for {
		kind, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageBinary || len(data) < 4 {
			continue
		}
		size := int(binary.BigEndian.Uint32(data[:4]))
		var header struct {
			PageID string `json:"page_id"`
		}
		if err := json.Unmarshal(data[4:4+size], &header); err != nil {
			t.Fatal(err)
		}
		if header.PageID == reopened.PageID {
			break
		}
	}

}

func TestOnlyTextStateStreamNegotiatesCompression(t *testing.T) {
	m, _, _ := fixture(t)
	server := httptest.NewServer(NewHandler(m, Options{}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, path := range []string{"/state/ws", "/ws?viewer_id=writer"} {
		c, response, err := websocket.Dial(ctx, server.URL+path, &websocket.DialOptions{CompressionMode: websocket.CompressionNoContextTakeover})
		if err != nil {
			t.Fatal(err)
		}
		compressed := response.Header.Get("Sec-WebSocket-Extensions") != ""
		_ = c.CloseNow()
		if compressed != (path == "/state/ws") {
			t.Fatalf("%s compression=%t", path, compressed)
		}
	}
}

func TestStreamReportsInputErrorAndKeepsReceiving(t *testing.T) {
	m, s, opened := fixture(t)
	server := httptest.NewServer(NewHandler(m, Options{}))
	defer server.Close()
	c := connect(t, server.URL+"/ws?viewer_id=a")
	if !control(t, c).Controller {
		t.Fatal("观看者未获得操作权")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, c, map[string]any{"page_id": opened.PageID, "kind": "text", "modifiers": -1}); err != nil {
		t.Fatal(err)
	}
	for {
		kind, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var message struct {
			Type  string            `json:"type"`
			Error *browserkit.Error `json:"error"`
		}
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatal(err)
		}
		if message.Type != "browser_input_error" {
			continue
		}
		if message.Error == nil || message.Error.Kind == "" || message.Error.Message == "" {
			t.Fatalf("输入错误丢失结构化信息：%s", data)
		}
		break
	}
	if err := wsjson.Write(ctx, c, map[string]any{"page_id": opened.PageID, "kind": "text", "text": "恢复输入"}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-s.pages[0].inputs:
		if event.Text != "恢复输入" {
			t.Fatal(event)
		}
	case <-ctx.Done():
		t.Fatal("输入错误使接收循环停止")
	}
}
