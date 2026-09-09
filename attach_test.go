package browserkit

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAttachKeepsPageAndDoesNotNavigate(t *testing.T) {
	page := &fakePage{info: PageInfo{URL: "https://example.com/existing", Title: "Existing"}}
	browser := &focusSession{fakeViewportSession: &fakeViewportSession{fakeSession: &fakeSession{page: page}}, pages: map[string]*focusPage{"target-1": {fakePage: page, focused: true, visible: true}}}
	manager, err := NewManager(Config{Session: SessionConfig{RemoteAddress: "http://127.0.0.1:9222"}, Factory: func(context.Context, SessionConfig) (Session, error) { return browser, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	opened, err := manager.Attach(context.Background(), AttachRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Title != "Existing" || opened.PageID == "" || page.navigated != "" || page.idleCalled || len(browser.activated) > 0 {
		t.Fatalf("unexpected attach: %+v", opened)
	}
	_ = manager.Close(context.Background())
	if page.closed {
		t.Fatal("external page closed on detach")
	}
}

// 同地址已打开或存在多个匹配，也必须创建新 Tab。
func TestAttachURLCreatesNewTab(t *testing.T) {
	for _, address := range []string{"https://example.com/", "https://example.com/new"} {
		t.Run(address, func(t *testing.T) {
			existing := &fakePage{info: PageInfo{URL: "https://example.com/", Title: "Existing"}}
			fresh := &fakePage{info: PageInfo{URL: "about:blank", Title: "New"}}
			browser := &focusSession{fakeViewportSession: &fakeViewportSession{fakeSession: &fakeSession{page: fresh}, tabs: []TabInfo{{TargetID: "a", Type: "page", URL: existing.info.URL}, {TargetID: "b", Type: "page", URL: existing.info.URL}}}, pages: map[string]*focusPage{"a": {fakePage: existing}, "b": {fakePage: existing}}}
			manager, err := NewManager(Config{Session: SessionConfig{RemoteAddress: "http://localhost:9222"}, Factory: func(context.Context, SessionConfig) (Session, error) { return browser, nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close(context.Background())
			opened, err := manager.Attach(context.Background(), AttachRequest{URL: address})
			if err != nil || opened.URL != address || fresh.navigated != address || opened.Title != "New" {
				t.Fatalf("opened=%+v err=%v", opened, err)
			}
			if existing.navigated != "" || existing.closed {
				t.Fatal("existing page altered")
			}
			_ = manager.Close(context.Background())
			if fresh.closed || existing.closed {
				t.Fatal("detach closed external tabs")
			}
		})
	}
}

func TestAttachURLRejectsBlockedAddressBeforeConnecting(t *testing.T) {
	connected := false
	manager, err := NewManager(Config{Session: SessionConfig{RemoteAddress: "http://localhost:9222"}, Factory: func(context.Context, SessionConfig) (Session, error) { connected = true; return &fakeSession{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	for _, address := range []string{"http://127.0.0.1/private", "file:///tmp/file"} {
		if _, err := manager.Attach(context.Background(), AttachRequest{URL: address}); err == nil {
			t.Fatal("blocked address accepted")
		}
	}
	if connected {
		t.Fatal("connected before policy validation")
	}
}

func TestExternalTabLimitDoesNotCloseOrActivateTabs(t *testing.T) {
	p := &fakePage{info: PageInfo{URL: "https://example.com/"}}
	b := &focusSession{fakeViewportSession: &fakeViewportSession{fakeSession: &fakeSession{page: p}, tabs: []TabInfo{{TargetID: "a", Type: "page", URL: p.info.URL}, {TargetID: "b", Type: "page", URL: "https://example.com/other"}}}, pages: map[string]*focusPage{"a": {fakePage: p, focused: true, visible: true}, "b": {fakePage: &fakePage{info: PageInfo{URL: "https://example.com/other"}}}}}
	m, err := NewManager(Config{MaxTabsPerSession: 1, Session: SessionConfig{RemoteAddress: "http://localhost:9222"}, Factory: func(context.Context, SessionConfig) (Session, error) { return b, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	opened, err := m.Attach(context.Background(), AttachRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ListTabs(context.Background(), TabsRequest{SessionID: opened.SessionID}); err != nil {
		t.Fatal(err)
	}
	if len(b.closedTargets) > 0 || len(b.activated) > 0 || len(b.viewportCalls) > 0 {
		t.Fatal("reconcile mutated external tabs")
	}
}

type focusPage struct {
	*fakePage
	focused, visible bool
}

func (p *focusPage) Evaluate(context.Context, string, EvaluateOptions) (EvaluateResult, error) {
	value, _ := json.Marshal(map[string]bool{"focused": p.focused, "visible": p.visible})
	return EvaluateResult{Value: value}, nil
}

type focusSession struct {
	*fakeViewportSession
	pages map[string]*focusPage
}

func (s *focusSession) PageForTab(_ context.Context, id string) (Page, error) {
	return s.pages[id], nil
}
func TestAttachActiveTab(t *testing.T) {
	for _, tc := range []struct {
		name           string
		focus, visible []bool
		want           string
		private        bool
	}{
		{name: "focused second tab", focus: []bool{false, true}, visible: []bool{true, true}, want: "b"},
		{name: "unfocused window", focus: []bool{false, false}, visible: []bool{false, true}, want: "b"},
		{name: "multiple windows", focus: []bool{false, false}, visible: []bool{true, true}},
		{name: "no visible page", focus: []bool{false, false}, visible: []bool{false, false}},
		{name: "private active page rejected", focus: []bool{false, true}, visible: []bool{false, true}, private: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &focusSession{fakeViewportSession: &fakeViewportSession{fakeSession: &fakeSession{}}, pages: map[string]*focusPage{}}
			for i, id := range []string{"a", "b"} {
				address := "https://example.com/" + id
				if tc.private && id == "b" {
					address = "http://127.0.0.1/private"
				}
				p := &focusPage{fakePage: &fakePage{info: PageInfo{URL: address, Title: id}}, focused: tc.focus[i], visible: tc.visible[i]}
				b.pages[id] = p
				b.tabs = append(b.tabs, TabInfo{TargetID: id, Type: "page", URL: address})
			}
			m, err := NewManager(Config{Session: SessionConfig{RemoteAddress: "http://localhost:9222"}, Factory: func(context.Context, SessionConfig) (Session, error) { return b, nil }})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close(context.Background())
			opened, err := m.Attach(context.Background(), AttachRequest{})
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected selection/policy error")
				}
			} else if err != nil || opened.Title != tc.want {
				t.Fatalf("opened=%+v err=%v", opened, err)
			}
			for _, p := range b.pages {
				if p.closed || p.navigated != "" {
					t.Fatal("attach altered existing page")
				}
			}
			if len(b.activated) > 0 {
				t.Fatal("attach activated another tab")
			}
		})
	}
}
