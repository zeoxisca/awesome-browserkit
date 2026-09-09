package browserkit

import (
	"encoding/json"
)

// OpenRequest 是 browserOpen 的输入。
type OpenRequest struct {
	SessionID string   `json:"session_id,omitempty"`
	URL       string   `json:"url,omitempty"`
	InjectJS  string   `json:"inject_js,omitempty" jsonschema_description:"可选的 JavaScript。新建 session 时会在每个后续 document 创建早期注入；复用已有 session 时只注册一次并立即执行当前 document，不会替换或删除已有注入脚本。重复 browserOpen 可省略此字段。"`
	Viewport  Viewport `json:"viewport,omitempty"`
}

// ViewportRequest 描述宿主为指定浏览器页面设置的 CSS 视口。
type ViewportRequest struct {
	SessionID string   `json:"session_id,omitempty"`
	PageID    string   `json:"page_id"`
	Viewport  Viewport `json:"viewport"`
}

// OpenLoadStatus 是 browserOpen 完成页面空闲等待后的状态。
type OpenLoadStatus string

const (
	// OpenLoadLoaded 表示页面在等待窗口内进入空闲。
	OpenLoadLoaded OpenLoadStatus = "loaded"
	// OpenLoadTimeout 表示页面已经打开，但在等待窗口内没有进入空闲。
	OpenLoadTimeout OpenLoadStatus = "timeout"
)

// OpenResponse 是 browserOpen 的输出。
type OpenResponse struct {
	SessionID  string         `json:"session_id"`
	PageID     string         `json:"page_id"`
	URL        string         `json:"url"`
	Title      string         `json:"title,omitempty"`
	LoadStatus OpenLoadStatus `json:"load_status"`
}

// TabView 是模型和宿主可见的浏览器 Tab 摘要。
type TabView struct {
	PageID       string   `json:"page_id"`
	URL          string   `json:"url,omitempty"`
	Title        string   `json:"title,omitempty"`
	Viewport     Viewport `json:"viewport,omitempty"`
	OpenerPageID string   `json:"opener_page_id,omitempty"`
	Status       string   `json:"status"`
	Active       bool     `json:"active"`
}

// TabsResponse 是 browserTabs 的输出。
type TabsResponse struct {
	SessionID    string    `json:"session_id"`
	ActivePageID string    `json:"active_page_id,omitempty"`
	TabsRevision uint64    `json:"tabs_revision"`
	Tabs         []TabView `json:"tabs"`
}

// TabsRequest 是 browserTabs 的输入。
type TabsRequest struct {
	SessionID string `json:"session_id"`
}

// TabRequest 是 browserActivateTab 的输入。
type TabRequest struct {
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id"`
}

// TabResponse 是 browserActivateTab 的输出。
type TabResponse struct {
	SessionID    string `json:"session_id"`
	PageID       string `json:"page_id"`
	URL          string `json:"url,omitempty"`
	Title        string `json:"title,omitempty"`
	Active       bool   `json:"active"`
	ActivePageID string `json:"active_page_id,omitempty"`
	TabsRevision uint64 `json:"tabs_revision"`
	TabsChanged  bool   `json:"tabs_changed"`
}

// CloseTabResponse 是 browserCloseTab 的输出。
type CloseTabResponse struct {
	SessionID    string `json:"session_id"`
	PageID       string `json:"page_id"`
	Closed       bool   `json:"closed"`
	ActivePageID string `json:"active_page_id,omitempty"`
	TabsRevision uint64 `json:"tabs_revision"`
	TabsChanged  bool   `json:"tabs_changed"`
}

// NavigateRequest 是 browserNavigate 的输入。
type NavigateRequest struct {
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id"`
	URL       string `json:"url"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

// NavigateResponse 是 browserNavigate 的输出。
type NavigateResponse struct {
	SessionID    string `json:"session_id"`
	PageID       string `json:"page_id"`
	URL          string `json:"url"`
	Title        string `json:"title,omitempty"`
	ActivePageID string `json:"active_page_id,omitempty"`
	TabsRevision uint64 `json:"tabs_revision"`
	TabsChanged  bool   `json:"tabs_changed"`
}

// SnapshotRequest 是 browserSnapshot 的输入。
type SnapshotRequest struct {
	SessionID   string `json:"session_id" jsonschema:"required,minLength=1"`
	PageID      string `json:"page_id,omitempty" jsonschema_description:"可选；省略时先同步 Tab 状态并使用当前 active page。"`
	MaxChars    int    `json:"max_chars,omitempty" jsonschema:"minimum=1,maximum=1048576" jsonschema_description:"页面正文最多返回的字符数，浏览器内先截断。"`
	MaxElements int    `json:"max_elements,omitempty" jsonschema:"minimum=1,maximum=1000" jsonschema_description:"本页最多返回的可交互元素数量。"`
	Offset      int    `json:"offset,omitempty" jsonschema:"minimum=0,maximum=1000000000" jsonschema_description:"跳过前面的可交互元素；使用上一页返回的 next_offset 继续读取。"`
}

// SnapshotResponse 是 browserSnapshot 的输出；elements 中的 ref 只属于 page_id。
// tabs_changed 表示相对上一次 Snapshot 的 Tab 投影是否变化；首次 Snapshot 为 true。
type SnapshotResponse struct {
	SessionID    string    `json:"session_id"`
	PageID       string    `json:"page_id"`
	Revision     uint64    `json:"revision"`
	URL          string    `json:"url"`
	Title        string    `json:"title,omitempty"`
	Text         string    `json:"text"`
	Elements     []Element `json:"elements"`
	Truncated    bool      `json:"truncated"`
	NextOffset   int       `json:"next_offset"`
	ActivePageID string    `json:"active_page_id,omitempty"`
	TabsRevision uint64    `json:"tabs_revision"`
	TabsChanged  bool      `json:"tabs_changed"`
	Tabs         []TabView `json:"tabs"`
}

// ClickRequest 是 browserClick 的输入。
type ClickRequest struct {
	SessionID   string `json:"session_id"`
	PageID      string `json:"page_id"`
	Ref         string `json:"ref"`
	Revision    uint64 `json:"revision"`
	WaitAfterMS int    `json:"wait_after_ms,omitempty"`
	TimeoutMS   int    `json:"timeout_ms,omitempty"`
}

// ClickResponse 是 browserClick 的输出。
type ClickResponse struct {
	SessionID    string `json:"session_id"`
	PageID       string `json:"page_id"`
	Ref          string `json:"ref"`
	Changed      bool   `json:"changed"`
	ActivePageID string `json:"active_page_id,omitempty"`
	TabsRevision uint64 `json:"tabs_revision"`
	TabsChanged  bool   `json:"tabs_changed"`
}

// HoverRequest 是 browserHover 的输入。
type HoverRequest struct {
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id"`
	Ref       string `json:"ref"`
	Revision  uint64 `json:"revision"`
}

// HoverResponse 是 browserHover 的输出。内部鼠标轨迹不作为响应内容暴露。
type HoverResponse struct {
	SessionID    string `json:"session_id"`
	PageID       string `json:"page_id"`
	Ref          string `json:"ref"`
	ActivePageID string `json:"active_page_id,omitempty"`
	TabsRevision uint64 `json:"tabs_revision"`
	TabsChanged  bool   `json:"tabs_changed"`
}

// TypeRequest 是 browserType 的输入。text 只用于执行，不出现在响应中。
type TypeRequest struct {
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id"`
	Ref       string `json:"ref"`
	Revision  uint64 `json:"revision"`
	Text      string `json:"text"`
	Append    bool   `json:"append,omitempty" jsonschema_description:"是否把文本追加到元素当前值末尾；省略或为 false 时先清空当前值。"`
	Submit    bool   `json:"submit,omitempty"`
}

// TypeResponse 是 browserType 的输出，不回显输入内容。
type TypeResponse struct {
	SessionID    string `json:"session_id"`
	PageID       string `json:"page_id"`
	Ref          string `json:"ref"`
	TextLength   int    `json:"text_length"`
	Submitted    bool   `json:"submitted"`
	ActivePageID string `json:"active_page_id,omitempty"`
	TabsRevision uint64 `json:"tabs_revision"`
	TabsChanged  bool   `json:"tabs_changed"`
}

// PressRequest 是 browserPress 的输入。
type PressRequest struct {
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id"`
	Key       string `json:"key"`
}

// PressResponse 是 browserPress 的输出。
type PressResponse struct {
	SessionID    string `json:"session_id"`
	PageID       string `json:"page_id"`
	Key          string `json:"key"`
	ActivePageID string `json:"active_page_id,omitempty"`
	TabsRevision uint64 `json:"tabs_revision"`
	TabsChanged  bool   `json:"tabs_changed"`
}

// WaitRequest 是 browserWait 的输入。
type WaitRequest struct {
	SessionID     string `json:"session_id" jsonschema:"required,minLength=1" jsonschema_description:"浏览器 session 标识；原样使用 browserOpen 或 browserTabs 返回的 session_id。"`
	PageID        string `json:"page_id" jsonschema:"required,minLength=1" jsonschema_description:"目标网页 page 标识；原样使用 browserOpen、browserTabs 或 browserSnapshot 返回的 page_id。"`
	Condition     string `json:"condition" jsonschema:"required,enum=selector_visible,enum=selector_hidden,enum=url_contains,enum=title_contains,enum=text_contains,enum=load_complete" jsonschema_description:"等待条件：selector_visible/hidden 等待 CSS selector 或 browserSnapshot 返回的 ref 出现/隐藏；url_contains、title_contains、text_contains 分别匹配当前 URL、标题或正文；load_complete 等待 document.readyState=complete。"`
	RefOrSelector string `json:"ref_or_selector" jsonschema:"maxLength=4096" jsonschema_description:"selector_visible/hidden 时填写 CSS selector，或直接填写同一 page_id 的 browserSnapshot 元素 ref；url/title/text_contains 时填写要匹配的文本；load_complete 时可省略。"`
	Revision      uint64 `json:"revision,omitempty" jsonschema:"minimum=1" jsonschema_description:"可选的 browserSnapshot revision。使用元素 ref 时传入快照响应中的 revision，可在页面变化后尽早得到 stale_reference。"`
	TimeoutMS     int    `json:"timeout_ms,omitempty" jsonschema:"minimum=1,maximum=600000" jsonschema_description:"等待超时毫秒数；省略时使用浏览器默认操作超时。"`
}

// WaitResponse 是 browserWait 的输出。
type WaitResponse struct {
	SessionID string `json:"session_id" jsonschema:"required"`
	PageID    string `json:"page_id" jsonschema:"required"`
	Condition string `json:"condition" jsonschema:"required"`
	Completed bool   `json:"completed" jsonschema:"required"`
}

// EvaluateRequest 是 browserEvaluate 的输入。
type EvaluateRequest struct {
	SessionID    string `json:"session_id"`
	PageID       string `json:"page_id"`
	Code         string `json:"code"`
	TimeoutMS    int    `json:"timeout_ms,omitempty"`
	AwaitPromise *bool  `json:"await_promise,omitempty"`
}

// EvaluateResponse 是 browserEvaluate 的输出。
type EvaluateResponse struct {
	SessionID string          `json:"session_id"`
	PageID    string          `json:"page_id"`
	Type      string          `json:"type"`
	Value     json.RawMessage `json:"value"`
}

// ScreenshotRequest 是 browserScreenshot 的输入。
type ScreenshotRequest struct {
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id"`
	FullPage  bool   `json:"full_page,omitempty"`
}

// ScreenshotResponse 是 browserScreenshot 的输出。
type ScreenshotResponse struct {
	// Data 保存完整截图，供宿主写入自己的存储。
	Data      []byte `json:"-"`
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id"`
	Path      string `json:"path"`
	Format    string `json:"format"`
	Bytes     int    `json:"bytes"`
	Namespace string `json:"namespace,omitempty"`
}

// CloseRequest 是 browserClose 的输入。
type CloseRequest struct {
	SessionID string `json:"session_id"`
}

// CloseResponse 是 browserClose 的输出。
type CloseResponse struct {
	SessionID string `json:"session_id"`
	Closed    bool   `json:"closed"`
}
