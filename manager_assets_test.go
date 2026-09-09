package browserkit

import (
	"context"
	"testing"
)

type fakeAssetsPage struct {
	*fakePage
	options ListAssetsOptions
	result  ListAssetsResult
}

func (page *fakeAssetsPage) ListAssets(_ context.Context, options ListAssetsOptions) (ListAssetsResult, error) {
	page.options = options
	return page.result, nil
}

type fakeAssetsSession struct {
	page   *fakeAssetsPage
	closed bool
}

func (session *fakeAssetsSession) NewPage(context.Context, PageOptions) (Page, error) {
	return session.page, nil
}

func (session *fakeAssetsSession) Cookies() ([]Cookie, error) { return nil, nil }
func (session *fakeAssetsSession) Close() error               { session.closed = true; return nil }
func (session *fakeAssetsSession) Closed() bool               { return session.closed }

func TestBrowserAssetsUsesExplicitTruncationResult(t *testing.T) {
	page := &fakeAssetsPage{
		fakePage: &fakePage{info: PageInfo{URL: "https://example.com", Title: "Example"}},
		result: ListAssetsResult{
			Assets: []Asset{
				{Name: "a.js", URL: "https://example.com/a.js"},
				{Name: "b.js", URL: "https://example.com/b.js"},
				{Name: "c.js", URL: "https://example.com/c.js"},
			},
			Truncated: false,
		},
	}
	session := &fakeAssetsSession{page: page}
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
	opened, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.ListAssets(context.Background(), AssetsRequest{SessionID: opened.SessionID, PageID: opened.PageID, Host: "example.com", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if state.Truncated || len(state.Assets) != 3 {
		t.Fatalf("恰好达到 limit 不应误报截断: %+v", state)
	}
	if page.options != (ListAssetsOptions{Host: "example.com", Limit: 3}) {
		t.Fatalf("资源查询选项=%+v", page.options)
	}

	page.result.Truncated = true
	state, err = manager.ListAssets(context.Background(), AssetsRequest{SessionID: opened.SessionID, PageID: opened.PageID, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !state.Truncated {
		t.Fatal("BrowserKit 报告的截断信息没有透传")
	}
}
