package browserkit

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	devtoolsMaxEntries   = 500
	devtoolsMaxBytes     = 2 << 20
	devtoolsMaxTextBytes = 16 << 10
	devtoolsMaxURLBytes  = 4 << 10
	devtoolsDefaultLimit = 50
	devtoolsMaxReadLimit = 100
)

// DevtoolsOpenRequest 是 browserDevtoolsOpen 的输入。
type DevtoolsOpenRequest struct {
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id"`
}

// DevtoolsOpenResponse 是 browserDevtoolsOpen 的输出。
type DevtoolsOpenResponse struct {
	SessionID  string   `json:"session_id"`
	PageID     string   `json:"page_id"`
	Status     string   `json:"status"`
	Generation uint64   `json:"generation"`
	Scopes     []string `json:"scopes"`
}

// DevtoolsReadRequest 是 browserDevtoolsRead 的输入。
type DevtoolsReadRequest struct {
	SessionID     string   `json:"session_id"`
	PageID        string   `json:"page_id"`
	Generation    uint64   `json:"generation,omitempty"`
	AfterSequence uint64   `json:"after_sequence,omitempty"`
	Kinds         []string `json:"kinds,omitempty"`
	Limit         int      `json:"limit,omitempty"`
}

// DevtoolsEntry 是一条有序的页面开发诊断记录。
type DevtoolsEntry struct {
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

// DevtoolsReadResponse 是开发诊断的有界只读快照。
type DevtoolsReadResponse struct {
	SessionID       string          `json:"session_id"`
	PageID          string          `json:"page_id"`
	Status          string          `json:"status"`
	Generation      uint64          `json:"generation"`
	Revision        uint64          `json:"revision"`
	Entries         []DevtoolsEntry `json:"entries"`
	NextSequence    uint64          `json:"next_sequence"`
	PendingRequests int             `json:"pending_requests"`
	DroppedBefore   uint64          `json:"dropped_before"`
	Truncated       bool            `json:"truncated"`
}

// DevtoolsCloseResponse 是 browserDevtoolsClose 的输出。
type DevtoolsCloseResponse struct {
	SessionID  string `json:"session_id"`
	PageID     string `json:"page_id"`
	Status     string `json:"status"`
	Generation uint64 `json:"generation"`
	Entries    int    `json:"entries"`
}

type devtoolsCapture struct {
	starting        bool
	status          string
	generation      uint64
	revision        uint64
	nextSequence    uint64
	entries         []DevtoolsEntry
	entryBytes      []int
	bytes           int
	droppedBefore   uint64
	pendingRequests int
	stop            func()
}

// OpenDevtools 为指定页面开启开发诊断采集；重复调用保持当前 generation。
func (manager *Manager) OpenDevtools(ctx context.Context, request DevtoolsOpenRequest) (DevtoolsOpenResponse, error) {
	if manager == nil {
		return DevtoolsOpenResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if !manager.config.AllowDevtools {
		return DevtoolsOpenResponse{}, browserError("devtools_blocked", "宿主未授权 Browser 开发诊断能力", "在 Browser 配置中显式启用 allow_devtools", true)
	}
	if request.SessionID == "" || request.PageID == "" {
		return DevtoolsOpenResponse{}, errors.New("browserDevtoolsOpen 需要 session_id 和 page_id")
	}
	current, value, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return DevtoolsOpenResponse{}, err
	}
	generation, err := manager.startPageDevtools(current, value)
	if err != nil {
		return DevtoolsOpenResponse{}, err
	}
	return devtoolsOpenResponse(request.SessionID, request.PageID, generation), nil
}

// startPageDevtools 在已解析的页面上启动采集，供显式方法调用和新页面
// 自动采集共用同一套 generation、并发启动与失败回滚语义。
func (manager *Manager) startPageDevtools(current *session, value *page) (uint64, error) {
	capable, ok := value.page.(DevtoolsPage)
	if !ok {
		return 0, browserError("devtools_unsupported", "当前浏览器页面不支持开发诊断", "使用支持 BrowserKit DevtoolsPage 的 Browser Factory", true)
	}
	value.devtoolsMu.Lock()
	if value.devtools != nil && value.devtools.starting {
		value.devtoolsMu.Unlock()
		return 0, browserError("devtools_starting", "开发诊断正在启动", "稍后重试 browserDevtoolsOpen", true)
	}
	if value.devtools != nil && value.devtools.status == "collecting" {
		generation := value.devtools.generation
		value.devtoolsMu.Unlock()
		return generation, nil
	}
	generation := value.devtoolsGeneration + 1
	capture := &devtoolsCapture{starting: true, status: "collecting", generation: generation, entries: []DevtoolsEntry{}, entryBytes: []int{}}
	value.devtoolsGeneration = generation
	value.devtools = capture
	value.devtoolsMu.Unlock()
	stop, err := capable.StartDevtools(current.ctx, func(event DevtoolsEvent) {
		manager.recordDevtools(value, capture, event)
	})
	value.devtoolsMu.Lock()
	if err != nil {
		if value.devtools == capture {
			value.devtools = nil
		}
		capture.starting = false
		value.devtoolsMu.Unlock()
		return 0, err
	}
	if value.devtools != capture || capture.status != "collecting" {
		capture.starting = false
		value.devtoolsMu.Unlock()
		if stop != nil {
			stop()
		}
		return 0, browserError("devtools_start_canceled", "开发诊断启动已取消", "重新调用 browserDevtoolsOpen", true)
	}
	capture.starting = false
	capture.stop = stop
	value.devtoolsMu.Unlock()
	manager.notifyDevtools()
	return generation, nil
}

// autoStartPageDevtools 仅在配置开启时为新登记页面启动采集。
func (manager *Manager) autoStartPageDevtools(current *session, value *page) error {
	if !manager.config.DevtoolsAutoStart {
		return nil
	}
	_, err := manager.startPageDevtools(current, value)
	return err
}

func devtoolsOpenResponse(sessionID, pageID string, generation uint64) DevtoolsOpenResponse {
	return DevtoolsOpenResponse{SessionID: sessionID, PageID: pageID, Status: "collecting", Generation: generation, Scopes: []string{"console", "exception", "network"}}
}

// ReadDevtools 返回指定页面当前开发诊断快照，并把这次显式读取视为 session 使用。
func (manager *Manager) ReadDevtools(ctx context.Context, request DevtoolsReadRequest) (DevtoolsReadResponse, error) {
	return manager.readDevtools(ctx, request, true)
}

// DevtoolsState 返回供宿主展示的开发诊断快照，不延长 Browser session 生命周期。
// SessionID 为空时解析 Manager 当前会话，供只持有应用 Session 的宿主使用。
func (manager *Manager) DevtoolsState(ctx context.Context, request DevtoolsReadRequest) (DevtoolsReadResponse, error) {
	return manager.readDevtools(ctx, request, false)
}

func (manager *Manager) readDevtools(ctx context.Context, request DevtoolsReadRequest, touch bool) (DevtoolsReadResponse, error) {
	if manager == nil {
		return DevtoolsReadResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return DevtoolsReadResponse{}, err
	}
	if !manager.config.AllowDevtools {
		return DevtoolsReadResponse{}, browserError("devtools_blocked", "宿主未授权 Browser 开发诊断能力", "在 Browser 配置中显式启用 allow_devtools", true)
	}
	if request.PageID == "" || touch && request.SessionID == "" {
		return DevtoolsReadResponse{}, errors.New("browserDevtoolsRead 需要 session_id 和 page_id")
	}
	if request.Limit < 0 || request.Limit > devtoolsMaxReadLimit {
		return DevtoolsReadResponse{}, errors.New("browserDevtoolsRead limit 必须位于 0 到 100")
	}
	kinds, err := devtoolsKinds(request.Kinds)
	if err != nil {
		return DevtoolsReadResponse{}, err
	}
	var current *session
	var value *page
	if touch {
		current, value, err = manager.get(ctx, request.SessionID, request.PageID)
	} else {
		current, value, err = manager.devtoolsPage(request.SessionID, request.PageID)
	}
	if err != nil {
		return DevtoolsReadResponse{}, err
	}
	limit := request.Limit
	if limit == 0 {
		limit = devtoolsDefaultLimit
	}
	value.devtoolsMu.Lock()
	defer value.devtoolsMu.Unlock()
	response := DevtoolsReadResponse{SessionID: current.id, PageID: request.PageID, Status: "idle", Generation: value.devtoolsGeneration, Entries: []DevtoolsEntry{}, NextSequence: request.AfterSequence}
	capture := value.devtools
	if capture == nil {
		return response, nil
	}
	if request.Generation != 0 && request.Generation != capture.generation {
		return DevtoolsReadResponse{}, browserError("devtools_generation_changed", "开发诊断 generation 已变化", "从新的 generation 和 after_sequence=0 重新读取", true)
	}
	response.Status, response.Generation, response.Revision = capture.status, capture.generation, capture.revision
	response.PendingRequests, response.DroppedBefore = capture.pendingRequests, capture.droppedBefore
	for _, entry := range capture.entries {
		if entry.Sequence <= request.AfterSequence || len(kinds) != 0 && !kinds[entry.Kind] {
			continue
		}
		if len(response.Entries) == limit {
			response.Truncated = true
			break
		}
		response.Entries = append(response.Entries, entry)
		response.NextSequence = entry.Sequence
	}
	return response, nil
}

func devtoolsKinds(values []string) (map[string]bool, error) {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "console" && value != "exception" && value != "network" {
			return nil, errors.New("browserDevtoolsRead kinds 只支持 console、exception 和 network")
		}
		result[value] = true
	}
	return result, nil
}

func (manager *Manager) devtoolsPage(sessionID, pageID string) (*session, *page, error) {
	manager.mu.Lock()
	current := manager.sessionLocked(sessionID)
	manager.mu.Unlock()
	if current == nil {
		return nil, nil, browserError("session_not_found", "浏览器 session 不存在", "重新调用 browserOpen", true)
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.closed || current.browser.Closed() {
		return nil, nil, browserError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen", true)
	}
	value := current.tabs[pageID]
	if value == nil {
		return nil, nil, browserError("page_not_found", "浏览器 page 不存在", "使用 browserTabs 返回的 page_id", true)
	}
	return current, value, nil
}

// CloseDevtools 停止指定页面采集并保留当前有界记录供最后一次读取。
func (manager *Manager) CloseDevtools(ctx context.Context, request DevtoolsOpenRequest) (DevtoolsCloseResponse, error) {
	if manager == nil {
		return DevtoolsCloseResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if !manager.config.AllowDevtools {
		return DevtoolsCloseResponse{}, browserError("devtools_blocked", "宿主未授权 Browser 开发诊断能力", "在 Browser 配置中显式启用 allow_devtools", true)
	}
	if request.SessionID == "" || request.PageID == "" {
		return DevtoolsCloseResponse{}, errors.New("browserDevtoolsClose 需要 session_id 和 page_id")
	}
	_, value, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return DevtoolsCloseResponse{}, err
	}
	generation, entries, changed := stopPageDevtools(value)
	if changed {
		manager.notifyDevtools()
	}
	return DevtoolsCloseResponse{SessionID: request.SessionID, PageID: request.PageID, Status: "stopped", Generation: generation, Entries: entries}, nil
}

func stopPageDevtools(value *page) (uint64, int, bool) {
	if value == nil {
		return 0, 0, false
	}
	value.devtoolsMu.Lock()
	capture := value.devtools
	if capture == nil {
		generation := value.devtoolsGeneration
		value.devtoolsMu.Unlock()
		return generation, 0, false
	}
	changed := capture.status == "collecting"
	if changed {
		capture.status = "stopped"
		capture.pendingRequests = 0
		capture.revision++
	}
	stop := capture.stop
	generation, entries := capture.generation, len(capture.entries)
	value.devtoolsMu.Unlock()
	if changed && stop != nil {
		stop()
	}
	return generation, entries, changed
}

func (manager *Manager) recordDevtools(value *page, capture *devtoolsCapture, event DevtoolsEvent) {
	value.devtoolsMu.Lock()
	if value.devtools != capture || capture.status != "collecting" {
		value.devtoolsMu.Unlock()
		return
	}
	if event.PendingDelta != 0 {
		capture.pendingRequests += event.PendingDelta
		if capture.pendingRequests < 0 {
			capture.pendingRequests = 0
		}
	}
	if event.Kind != "" {
		capture.nextSequence++
		entry := DevtoolsEntry{
			Sequence: capture.nextSequence, Kind: event.Kind, Level: limitDevtoolsText(event.Level, 64),
			Text: limitDevtoolsText(event.Text, devtoolsMaxTextBytes), Stack: limitDevtoolsText(event.Stack, devtoolsMaxTextBytes),
			SourceURL: limitDevtoolsText(event.SourceURL, devtoolsMaxURLBytes), Line: event.Line, Column: event.Column,
			Method: limitDevtoolsText(event.Method, 32), ResourceType: limitDevtoolsText(event.ResourceType, 32), URL: limitDevtoolsText(event.URL, devtoolsMaxURLBytes),
			Status: event.Status, MIMEType: limitDevtoolsText(event.MIMEType, 256), DurationMS: event.DurationMS,
			EncodedBytes: event.EncodedBytes, Failed: event.Failed, Error: limitDevtoolsText(event.Error, devtoolsMaxTextBytes), RequestBody: limitDevtoolsText(event.RequestBody, devtoolsMaxTextBytes), ResponseBody: limitDevtoolsText(event.ResponseBody, devtoolsMaxTextBytes), BodyAvailable: event.BodyAvailable, BodyTruncated: event.BodyTruncated, At: event.At,
		}
		if entry.At.IsZero() {
			entry.At = time.Now()
		}
		size := devtoolsEntryBytes(entry)
		capture.entries = append(capture.entries, entry)
		capture.entryBytes = append(capture.entryBytes, size)
		capture.bytes += size
		for len(capture.entries) > devtoolsMaxEntries || capture.bytes > devtoolsMaxBytes && len(capture.entries) > 1 {
			capture.bytes -= capture.entryBytes[0]
			capture.droppedBefore = capture.entries[0].Sequence
			capture.entries = capture.entries[1:]
			capture.entryBytes = capture.entryBytes[1:]
		}
	}
	capture.revision++
	value.devtoolsMu.Unlock()
	manager.notifyDevtools()
}

func devtoolsEntryBytes(entry DevtoolsEntry) int {
	return 128 + len(entry.Level) + len(entry.Text) + len(entry.Stack) + len(entry.SourceURL) + len(entry.Method) + len(entry.ResourceType) + len(entry.URL) + len(entry.MIMEType) + len(entry.Error) + len(entry.RequestBody) + len(entry.ResponseBody)
}

func limitDevtoolsText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// SubscribeDevtools 订阅开发诊断变化。连续通知会合并，消费方重新读取快照。
func (manager *Manager) SubscribeDevtools(ctx context.Context) (<-chan struct{}, error) {
	if manager == nil {
		return nil, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	updates := make(chan struct{}, 1)
	manager.devtoolsStateMu.Lock()
	if manager.stopped {
		manager.devtoolsStateMu.Unlock()
		close(updates)
		return updates, nil
	}
	manager.devtoolsStateSubs[updates] = struct{}{}
	manager.devtoolsStateMu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-manager.lifecycleCtx.Done():
		}
		manager.devtoolsStateMu.Lock()
		if _, ok := manager.devtoolsStateSubs[updates]; ok {
			delete(manager.devtoolsStateSubs, updates)
			close(updates)
		}
		manager.devtoolsStateMu.Unlock()
	}()
	return updates, nil
}

func (manager *Manager) notifyDevtools() {
	manager.devtoolsStateMu.Lock()
	for subscriber := range manager.devtoolsStateSubs {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
	manager.devtoolsStateMu.Unlock()
}

func (manager *Manager) closeDevtoolsSubscribers() {
	manager.devtoolsStateMu.Lock()
	for updates := range manager.devtoolsStateSubs {
		close(updates)
		delete(manager.devtoolsStateSubs, updates)
	}
	manager.devtoolsStateMu.Unlock()
}
