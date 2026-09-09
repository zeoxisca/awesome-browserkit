package browserkit

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// BrowserViewState 是 Web 查询当前 Browser Page 的临时状态。
type BrowserViewState struct {
	Available    bool             `json:"available"`
	Closed       bool             `json:"closed"`
	SessionID    string           `json:"session_id,omitempty"`
	ActivePageID string           `json:"active_page_id,omitempty"`
	TabsRevision uint64           `json:"tabs_revision"`
	Tabs         []BrowserTabView `json:"tabs"`
	URL          string           `json:"url,omitempty"`
	Title        string           `json:"title,omitempty"`
	Viewport     Viewport         `json:"viewport,omitempty"`
}

// BrowserTabView 是 Web 浏览器侧栏使用的 Tab 摘要。
type BrowserTabView struct {
	PageID       string   `json:"page_id"`
	URL          string   `json:"url,omitempty"`
	Title        string   `json:"title,omitempty"`
	Viewport     Viewport `json:"viewport,omitempty"`
	OpenerPageID string   `json:"opener_page_id,omitempty"`
	Status       string   `json:"status"`
	Active       bool     `json:"active"`
}

// BrowserStateFromTabs 将页面列表投影为可嵌入浏览器状态。
func BrowserStateFromTabs(list TabsResponse) BrowserViewState {
	result := BrowserViewState{
		Available: len(list.Tabs) > 0, SessionID: list.SessionID,
		ActivePageID: list.ActivePageID, TabsRevision: list.TabsRevision,
		Tabs: make([]BrowserTabView, 0, len(list.Tabs)),
	}
	for _, tab := range list.Tabs {
		view := BrowserTabView{PageID: tab.PageID, URL: BrowserViewURL(tab.URL), Title: tab.Title, Viewport: tab.Viewport, OpenerPageID: tab.OpenerPageID, Status: tab.Status, Active: tab.Active}
		result.Tabs = append(result.Tabs, view)
		if tab.Active {
			result.URL, result.Title, result.Viewport = view.URL, view.Title, view.Viewport
		}
	}
	return result
}

// BrowserViewURL 去掉展示地址中的凭据、查询参数和 fragment。
func BrowserViewURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

// BrowserDevtoolsViewState 是 Web 使用的页面开发诊断临时投影。
type BrowserDevtoolsViewState struct {
	Available       bool                   `json:"available"`
	SessionID       string                 `json:"session_id,omitempty"`
	PageID          string                 `json:"page_id,omitempty"`
	Status          string                 `json:"status"`
	Generation      uint64                 `json:"generation"`
	Revision        uint64                 `json:"revision"`
	Entries         []BrowserDevtoolsEntry `json:"entries"`
	NextSequence    uint64                 `json:"next_sequence"`
	PendingRequests int                    `json:"pending_requests"`
	DroppedBefore   uint64                 `json:"dropped_before"`
	Truncated       bool                   `json:"truncated"`
}

// BrowserDevtoolsEntry 是去除 URL 查询参数、并限制正文大小的开发诊断展示记录。
type BrowserDevtoolsEntry struct {
	Sequence      uint64    `json:"sequence"`
	Kind          string    `json:"kind"`
	Level         string    `json:"level,omitempty"`
	Text          string    `json:"text,omitempty"`
	Stack         string    `json:"stack,omitempty"`
	SourceURL     string    `json:"source_url,omitempty"`
	Line          int       `json:"line,omitempty"`
	Column        int       `json:"column,omitempty"`
	Method        string    `json:"method,omitempty"`
	ResourceType  string    `json:"resource_type,omitempty"`
	URL           string    `json:"url,omitempty"`
	Status        int       `json:"status,omitempty"`
	MIMEType      string    `json:"mime_type,omitempty"`
	DurationMS    int64     `json:"duration_ms,omitempty"`
	EncodedBytes  int64     `json:"encoded_bytes,omitempty"`
	Failed        bool      `json:"failed,omitempty"`
	Error         string    `json:"error,omitempty"`
	RequestBody   string    `json:"request_body,omitempty"`
	ResponseBody  string    `json:"response_body,omitempty"`
	BodyAvailable bool      `json:"body_available,omitempty"`
	BodyTruncated bool      `json:"body_truncated,omitempty"`
	At            time.Time `json:"at"`
}

func BrowserDevtoolsView(state DevtoolsReadResponse) BrowserDevtoolsViewState {
	view := BrowserDevtoolsViewState{
		Available: state.Status != "idle", SessionID: state.SessionID, PageID: state.PageID,
		Status: state.Status, Generation: state.Generation, Revision: state.Revision,
		Entries: make([]BrowserDevtoolsEntry, 0, len(state.Entries)), NextSequence: state.NextSequence,
		PendingRequests: state.PendingRequests, DroppedBefore: state.DroppedBefore, Truncated: state.Truncated,
	}
	for _, entry := range state.Entries {
		view.Entries = append(view.Entries, BrowserDevtoolsEntry{
			Sequence: entry.Sequence, Kind: entry.Kind, Level: entry.Level, Text: entry.Text, Stack: entry.Stack,
			SourceURL: BrowserViewURL(entry.SourceURL), Line: entry.Line, Column: entry.Column,
			Method: entry.Method, ResourceType: entry.ResourceType, URL: BrowserViewURL(entry.URL), Status: entry.Status, MIMEType: entry.MIMEType,
			DurationMS: entry.DurationMS, EncodedBytes: entry.EncodedBytes, Failed: entry.Failed, Error: entry.Error, RequestBody: entry.RequestBody, ResponseBody: entry.ResponseBody, BodyAvailable: entry.BodyAvailable, BodyTruncated: entry.BodyTruncated, At: entry.At,
		})
	}
	return view
}

// BrowserAssetView 是 Web 使用的页面资源临时投影。
type BrowserAssetView struct {
	Name           string  `json:"name"`
	URL            string  `json:"url"`
	InitiatorType  string  `json:"initiator_type,omitempty"`
	ResponseStatus int     `json:"response_status,omitempty"`
	TransferBytes  int64   `json:"transfer_bytes,omitempty"`
	EncodedBytes   int64   `json:"encoded_bytes,omitempty"`
	DecodedBytes   int64   `json:"decoded_bytes,omitempty"`
	DurationMS     float64 `json:"duration_ms,omitempty"`
}

// BrowserAssetsViewState 是 Web 使用的页面资源临时投影。
type BrowserAssetsViewState struct {
	// PageID 是资源所属的浏览器页面。
	PageID string `json:"page_id"`
	// Assets 是当前页面已经观测到的有界资源列表。
	Assets []BrowserAssetView `json:"assets"`
	// Truncated 表示还有更早的资源未返回。
	Truncated bool `json:"truncated"`
}

// View 返回当前浏览器视图，不启动浏览器。
func (manager *Manager) View(ctx context.Context) (BrowserViewState, error) {
	if ctx == nil {
		return BrowserViewState{}, errors.New("Browser 状态 context 不能为空")
	}
	if manager == nil {
		return BrowserViewState{Closed: true, Tabs: []BrowserTabView{}}, nil
	}
	// ListTabs 已经通过一次有界 reconcile 得到 active Tab 的 URL、标题和
	// viewport；不要再调用 LiveState 重复枚举 target 和读取 TargetInfo。
	list, err := manager.ListTabs(ctx, TabsRequest{})
	if err != nil {
		var browserErr *Error
		if errors.As(err, &browserErr) && (browserErr.Kind == "session_closed" || browserErr.Kind == "session_not_found" || browserErr.Kind == "browser_closed") {
			// Browser session 一旦从 Manager 的活动表中摘除，ListTabs 返回
			// session_not_found。对 Web 临时视图而言这和 session_closed 是
			// 同一终态，必须显式发送 tabs: []，让前端丢弃旧 Tab 和最后帧。
			return BrowserViewState{Closed: true, Tabs: []BrowserTabView{}}, nil
		}
		if errors.As(err, &browserErr) && browserErr.Kind == "page_not_found" {
			return BrowserViewState{Available: false, Tabs: []BrowserTabView{}}, nil
		}
		// CDP 在导航、target 切换和页面刚建立时可能暂时无法读取状态；
		// 这不是 browserClose，前端应保留已经显示的浮窗等待下一次同步。
		return BrowserViewState{}, nil
	}
	return BrowserStateFromTabs(list), nil
}

// BrowserAssetsFromResponse 将观测资源投影为前端列表。
func BrowserAssetsFromResponse(state AssetsResponse) BrowserAssetsViewState {
	assets := make([]BrowserAssetView, 0, len(state.Assets))
	for _, asset := range state.Assets {
		assets = append(assets, BrowserAssetView{Name: asset.Name, URL: BrowserViewURL(asset.URL), InitiatorType: asset.InitiatorType, ResponseStatus: asset.ResponseStatus, TransferBytes: asset.TransferBytes, EncodedBytes: asset.EncodedBytes, DecodedBytes: asset.DecodedBytes, DurationMS: asset.DurationMS})
	}
	return BrowserAssetsViewState{PageID: state.PageID, Assets: assets, Truncated: state.Truncated}
}
