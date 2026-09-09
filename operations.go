package browserkit

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

// Snapshot 执行浏览器页面操作，使用普通 context 和明确目标。
func (manager *Manager) Snapshot(ctx context.Context, request SnapshotRequest) (SnapshotResponse, error) {
	if err := contextErr(ctx); err != nil {
		return SnapshotResponse{}, err
	}
	if request.SessionID == "" {
		return SnapshotResponse{}, errors.New("browserSnapshot 需要 session_id")
	}
	// Snapshot 是 调用方读取 Tab 的同步点：先从浏览器枚举 target，再选择
	// active page。这样省略 page_id 时不会沿用上一次工具调用的页面。
	current, _, err := manager.get(ctx, request.SessionID, "")
	if err != nil {
		return SnapshotResponse{}, err
	}
	if err := manager.reconcileTabs(ctx, current); err != nil {
		return SnapshotResponse{}, err
	}
	current, currentPage, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return SnapshotResponse{}, err
	}
	if request.MaxChars < 0 || request.MaxElements < 0 || request.Offset < 0 || request.Offset > 1_000_000_000 {
		return SnapshotResponse{}, errors.New("browserSnapshot 的 max_chars、max_elements 和 offset 无效")
	}

	snapshot, err := currentPage.page.Snapshot(ctx, SnapshotOptions{MaxChars: request.MaxChars, MaxElements: request.MaxElements, Offset: request.Offset})
	if err != nil {
		return SnapshotResponse{}, err
	}
	current.mu.Lock()
	rememberSnapshotRefs(currentPage, snapshot)
	tabsRevision := tabsRevisionLocked(current)
	tabsChanged := !current.snapshotInitialized || current.snapshotRevision != tabsRevision
	current.snapshotRevision = tabsRevision
	current.snapshotInitialized = true
	activePageID := current.activeTab
	tabs := tabViewsLocked(current)
	current.mu.Unlock()
	return SnapshotResponse{SessionID: current.id, PageID: currentPage.id, Revision: snapshot.Revision, URL: snapshot.Info.URL, Title: snapshot.Info.Title, Text: snapshot.Text, Elements: snapshot.Elements, Truncated: snapshot.Truncated, NextOffset: request.Offset + len(snapshot.Elements), ActivePageID: activePageID, TabsRevision: tabsRevision, TabsChanged: tabsChanged, Tabs: tabs}, nil
}

// Click 执行浏览器页面操作，使用普通 context 和明确目标。
func (manager *Manager) Click(ctx context.Context, request ClickRequest) (ClickResponse, error) {
	operationCtx, cancel, err := operationContext(ctx, request.TimeoutMS)
	if err != nil {
		return ClickResponse{}, err
	}
	defer cancel()
	if err := requirePage(request.SessionID, request.PageID, "browserClick"); err != nil {
		return ClickResponse{}, err
	}

	current, currentPage, err := manager.get(operationCtx, request.SessionID, request.PageID)
	if err != nil {
		return ClickResponse{}, err
	}
	if request.Ref == "" || request.Revision == 0 {
		return ClickResponse{}, errors.New("browserClick 需要 ref 和 revision")
	}
	if request.WaitAfterMS < 0 {
		return ClickResponse{}, errors.New("browserClick wait_after_ms 不能为负数")
	}
	unlockInteraction, err := lockSessionInteraction(operationCtx, current)
	if err != nil {
		return ClickResponse{}, err
	}
	defer unlockInteraction()
	current.mu.Lock()
	beforeTabsRevision := tabsRevisionLocked(current)
	refErr := validatePageRef(currentPage, request.Ref, request.Revision)
	current.mu.Unlock()
	if refErr != nil {
		return ClickResponse{}, refErr
	}
	if err := currentPage.page.Click(operationCtx, ElementRef{Ref: request.Ref, Revision: request.Revision}); err != nil {
		return ClickResponse{}, err
	}
	if request.WaitAfterMS > 0 {
		if err := waitContext(operationCtx, time.Duration(request.WaitAfterMS)*time.Millisecond); err != nil {
			return ClickResponse{}, err
		}
	}
	manager.emit(operationCtx, Event{Kind: "interaction_finished", SessionID: current.id, PageID: currentPage.id})
	unlockInteraction()
	activePageID, tabsRevision, tabsChanged := manager.tabChange(operationCtx, current, beforeTabsRevision)
	response := ClickResponse{SessionID: current.id, PageID: currentPage.id, Ref: request.Ref, Changed: true, ActivePageID: activePageID, TabsRevision: tabsRevision, TabsChanged: tabsChanged}
	return response, nil
}

// Hover 执行浏览器页面操作，使用普通 context 和明确目标。
func (manager *Manager) Hover(ctx context.Context, request HoverRequest) (HoverResponse, error) {
	operationCtx, cancel, err := operationContext(ctx, 0)
	if err != nil {
		return HoverResponse{}, err
	}
	defer cancel()
	if err := requirePage(request.SessionID, request.PageID, "browserHover"); err != nil {
		return HoverResponse{}, err
	}
	if request.Ref == "" || request.Revision == 0 {
		return HoverResponse{}, errors.New("browserHover 需要 ref 和 revision")
	}

	current, currentPage, err := manager.get(operationCtx, request.SessionID, request.PageID)
	if err != nil {
		return HoverResponse{}, err
	}
	hoverPage, ok := currentPage.page.(HoverPage)
	if !ok {
		return HoverResponse{}, browserError("hover_unsupported", "当前浏览器页面不支持真实指针悬停", "使用支持 BrowserKit HoverPage 的 Browser Factory", true)
	}
	unlockInteraction, err := lockSessionInteraction(operationCtx, current)
	if err != nil {
		return HoverResponse{}, err
	}
	defer unlockInteraction()
	current.mu.Lock()
	beforeTabsRevision := tabsRevisionLocked(current)
	if err := validatePageRef(currentPage, request.Ref, request.Revision); err != nil {
		current.mu.Unlock()
		return HoverResponse{}, err
	}
	current.mu.Unlock()
	if err := hoverPage.Hover(operationCtx, ElementRef{Ref: request.Ref, Revision: request.Revision}); err != nil {
		return HoverResponse{}, err
	}
	unlockInteraction()
	activePageID, tabsRevision, tabsChanged := manager.tabChange(operationCtx, current, beforeTabsRevision)
	return HoverResponse{SessionID: current.id, PageID: currentPage.id, Ref: request.Ref, ActivePageID: activePageID, TabsRevision: tabsRevision, TabsChanged: tabsChanged}, nil
}

// Type 执行浏览器页面操作，使用普通 context 和明确目标。
func (manager *Manager) Type(ctx context.Context, request TypeRequest) (TypeResponse, error) {
	if err := contextErr(ctx); err != nil {
		return TypeResponse{}, err
	}
	if err := requirePage(request.SessionID, request.PageID, "browserType"); err != nil {
		return TypeResponse{}, err
	}

	current, currentPage, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return TypeResponse{}, err
	}
	if request.Ref == "" || request.Revision == 0 {
		return TypeResponse{}, errors.New("browserType 需要 ref 和 revision")
	}
	unlockInteraction, err := lockSessionInteraction(ctx, current)
	if err != nil {
		return TypeResponse{}, err
	}
	defer unlockInteraction()
	current.mu.Lock()
	beforeTabsRevision := tabsRevisionLocked(current)
	refErr := validatePageRef(currentPage, request.Ref, request.Revision)
	current.mu.Unlock()
	if refErr != nil {
		return TypeResponse{}, refErr
	}
	if err := currentPage.page.Type(ctx, ElementRef{Ref: request.Ref, Revision: request.Revision}, request.Text, !request.Append); err != nil {
		return TypeResponse{}, err
	}
	if request.Submit {
		if err := currentPage.page.Press(ctx, "Enter"); err != nil {
			return TypeResponse{}, err
		}
	}
	unlockInteraction()
	activePageID, tabsRevision, tabsChanged := manager.tabChange(ctx, current, beforeTabsRevision)
	response := TypeResponse{SessionID: current.id, PageID: currentPage.id, Ref: request.Ref, TextLength: len([]rune(request.Text)), Submitted: request.Submit, ActivePageID: activePageID, TabsRevision: tabsRevision, TabsChanged: tabsChanged}
	return response, nil
}

// Press 执行浏览器页面操作，使用普通 context 和明确目标。
func (manager *Manager) Press(ctx context.Context, request PressRequest) (PressResponse, error) {
	if err := contextErr(ctx); err != nil {
		return PressResponse{}, err
	}
	if err := requirePage(request.SessionID, request.PageID, "browserPress"); err != nil {
		return PressResponse{}, err
	}

	current, currentPage, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return PressResponse{}, err
	}
	if request.Key == "" {
		return PressResponse{}, errors.New("browserPress 需要 key")
	}
	unlockInteraction, err := lockSessionInteraction(ctx, current)
	if err != nil {
		return PressResponse{}, err
	}
	defer unlockInteraction()
	current.mu.Lock()
	beforeTabsRevision := tabsRevisionLocked(current)
	current.mu.Unlock()
	if err := currentPage.page.Press(ctx, request.Key); err != nil {
		return PressResponse{}, err
	}
	unlockInteraction()
	activePageID, tabsRevision, tabsChanged := manager.tabChange(ctx, current, beforeTabsRevision)
	response := PressResponse{SessionID: current.id, PageID: currentPage.id, Key: request.Key, ActivePageID: activePageID, TabsRevision: tabsRevision, TabsChanged: tabsChanged}
	return response, nil
}

// tabChange 在交互返回前刷新一次真实 Tab 列表，并返回模型需要立即
// 看到的轻量状态。reconcile 失败时保留本地投影，避免把已经完成的点击
// 误报成工具失败；下一次 browserSnapshot 会再次强制同步。
func (manager *Manager) tabChange(ctx context.Context, current *session, before uint64) (string, uint64, bool) {
	if current == nil {
		return "", 0, false
	}
	_ = manager.reconcileTabs(ctx, current)
	current.mu.Lock()
	defer current.mu.Unlock()
	return tabChangeLocked(current, before)
}

// Wait 执行浏览器页面操作，使用普通 context 和明确目标。
func (manager *Manager) Wait(ctx context.Context, request WaitRequest) (WaitResponse, error) {
	waitCtx, cancel, err := operationContext(ctx, request.TimeoutMS)
	if err != nil {
		return WaitResponse{}, err
	}
	defer cancel()

	if err := requirePage(request.SessionID, request.PageID, "browserWait"); err != nil {
		return WaitResponse{}, err
	}
	current, currentPage, err := manager.get(waitCtx, request.SessionID, request.PageID)
	if err != nil {
		return WaitResponse{}, err
	}
	if request.Condition == "" {
		return WaitResponse{}, errors.New("browserWait 需要 condition")
	}
	if request.Condition != "load_complete" && request.RefOrSelector == "" {
		return WaitResponse{}, errors.New("browserWait 需要 ref_or_selector；load_complete 可省略")
	}
	selector := request.RefOrSelector
	current.mu.Lock()
	_, known := currentPage.refs[request.RefOrSelector]
	refRevision := currentPage.refRevision
	current.mu.Unlock()
	if request.Condition == "selector_visible" || request.Condition == "selector_hidden" {
		if known {
			if request.Revision != 0 && request.Revision != refRevision {
				return WaitResponse{}, browserError("stale_reference", "元素引用已经过期", "在目标 page_id 上重新调用 browserSnapshot", true)
			}
			selector = `[data-browserkit-ref="` + strings.ReplaceAll(strings.ReplaceAll(request.RefOrSelector, `\`, `\\`), `"`, `\"`) + `"]`
		} else if request.Revision != 0 || isSnapshotRef(request.RefOrSelector) {
			return WaitResponse{}, browserError("invalid_reference", "当前 Tab 没有该元素引用", "在目标 page_id 上重新调用 browserSnapshot", true)
		}
	}
	if err := currentPage.page.Wait(waitCtx, WaitOptions{Condition: request.Condition, Selector: selector}); err != nil {
		return WaitResponse{}, err
	}
	response := WaitResponse{SessionID: current.id, PageID: currentPage.id, Condition: request.Condition, Completed: true}
	return response, nil
}

// isSnapshotRef 只识别 BrowserKit 生成的 e<token>_<number> 形式。
// 只有这种形状且不在当前快照中的值才会被视为失效 ref；普通 CSS 选择器
// （即使以 e 开头）仍按 selector 处理。
func isSnapshotRef(value string) bool {
	separator := strings.LastIndexByte(value, '_')
	if len(value) < 3 || value[0] != 'e' || separator < 1 || separator == len(value)-1 {
		return false
	}
	for _, char := range value[1:separator] {
		if char < '0' || char > '9' && (char < 'a' || char > 'z') {
			return false
		}
	}
	for _, char := range value[separator+1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// Evaluate 执行浏览器页面操作，使用普通 context 和明确目标。
func (manager *Manager) Evaluate(ctx context.Context, request EvaluateRequest) (EvaluateResponse, error) {
	operationCtx, cancel, err := operationContext(ctx, request.TimeoutMS)
	if err != nil {
		return EvaluateResponse{}, err
	}
	defer cancel()

	if !manager.config.AllowEvaluate {
		return EvaluateResponse{}, browserError("javascript_blocked", "宿主未启用 Browser JavaScript 能力", "在 Browser 配置中显式设置 allow_evaluate", true)
	}
	if request.SessionID == "" || request.PageID == "" || strings.TrimSpace(request.Code) == "" {
		return EvaluateResponse{}, errors.New("browserEvaluate 需要 session_id、page_id 和 code")
	}
	if len(request.Code) > manager.config.MaxScriptBytes {
		return EvaluateResponse{}, browserError("script_too_large", "JavaScript 代码超过大小限制", "缩短 code 后重试", true)
	}
	current, currentPage, err := manager.get(operationCtx, request.SessionID, request.PageID)
	if err != nil {
		return EvaluateResponse{}, err
	}
	awaitPromise := true
	if request.AwaitPromise != nil {
		awaitPromise = *request.AwaitPromise
	}
	options := EvaluateOptions{AwaitPromise: awaitPromise}
	if request.TimeoutMS > 0 {
		options.Timeout = time.Duration(request.TimeoutMS) * time.Millisecond
	}
	result, err := currentPage.page.Evaluate(operationCtx, request.Code, options)
	if err != nil {
		return EvaluateResponse{}, err
	}
	if len(result.Value) > manager.config.MaxResultBytes {
		return EvaluateResponse{}, browserError("result_too_large", "JavaScript 返回值超过大小限制", "只返回必要的 JSON 摘要", true)
	}
	manager.emit(ctx, Event{Kind: "evaluation_finished", SessionID: current.id, PageID: currentPage.id})
	response := EvaluateResponse{SessionID: current.id, PageID: currentPage.id, Type: result.Type, Value: result.Value}
	return response, nil
}

// Screenshot 执行浏览器页面操作，使用普通 context 和明确目标。
func (manager *Manager) Screenshot(ctx context.Context, request ScreenshotRequest) (ScreenshotResponse, error) {
	if err := contextErr(ctx); err != nil {
		return ScreenshotResponse{}, err
	}
	if err := requirePage(request.SessionID, request.PageID, "browserScreenshot"); err != nil {
		return ScreenshotResponse{}, err
	}

	current, currentPage, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return ScreenshotResponse{}, err
	}
	screenshot, err := manager.captureScreenshot(ctx, current, currentPage, ScreenshotOptions{FullPage: request.FullPage})
	if err != nil {
		return ScreenshotResponse{}, err
	}
	path, err := manager.screenshotPath(current.id, currentPage.id, manager.sequence.Add(1))
	if err != nil {
		return ScreenshotResponse{}, err
	}
	if err := os.WriteFile(path, screenshot.Data, 0o600); err != nil {
		return ScreenshotResponse{}, err
	}

	manager.emit(ctx, Event{Kind: "screenshot_ready", SessionID: current.id, PageID: currentPage.id})
	return ScreenshotResponse{SessionID: current.id, PageID: currentPage.id, Path: path, Format: screenshot.Format, Bytes: len(screenshot.Data), Data: screenshot.Data}, nil
}

// captureScreenshot 保留 Manager 声明的 active Tab。Chrome 会在截图前
// 激活 target，后台 Tab 截图后必须恢复原 target，
// 否则后续直播与输入会和 Manager.activeTab 分叉。
func (manager *Manager) captureScreenshot(ctx context.Context, current *session, currentPage *page, options ScreenshotOptions) (Screenshot, error) {
	current.tabMu.Lock()
	defer current.tabMu.Unlock()
	current.mu.Lock()
	active := manager.activePageLocked(current)
	browser := current.browser
	current.mu.Unlock()
	screenshot, screenshotErr := currentPage.page.Screenshot(ctx, options)
	if active != currentPage {
		if err := restoreActiveTarget(current, browser, active); err != nil && screenshotErr == nil {
			return Screenshot{}, browserError("tab_restore_failed", "截图后恢复活动 Tab 失败: "+err.Error(), "重新获取 tablist 后重试", true)
		}
	}
	return screenshot, screenshotErr
}

// CloseSession 执行浏览器页面操作，使用普通 context 和明确目标。
func (manager *Manager) CloseSession(ctx context.Context, request CloseRequest) (CloseResponse, error) {
	if err := contextErr(ctx); err != nil {
		return CloseResponse{}, err
	}

	if request.SessionID == "" {
		return CloseResponse{}, errors.New("browserClose 需要 session_id")
	}
	manager.mu.Lock()
	current := manager.sessions[request.SessionID]
	if current == nil {
		_, closed := manager.closed[request.SessionID]
		manager.mu.Unlock()
		if closed {
			return CloseResponse{SessionID: request.SessionID, Closed: true}, nil
		}
		return CloseResponse{}, browserError("session_not_found", "浏览器 session 不存在", "确认 session_id 后重试", true)
	}
	delete(manager.sessions, request.SessionID)
	manager.closed[request.SessionID] = struct{}{}
	if manager.active == request.SessionID {
		manager.active = ""
	}
	manager.mu.Unlock()
	// 逻辑关闭已经完成；底层 Browser.Close 即使超时或失败，也不能让
	// 状态观察者继续显示已经失效的 Tab 和截图。
	manager.emit(ctx, Event{Kind: "session_closed", SessionID: current.id})
	if err := manager.closeSession(ctx, current); err != nil {
		return CloseResponse{}, err
	}
	return CloseResponse{SessionID: current.id, Closed: true}, nil
}

func waitContext(ctx context.Context, duration time.Duration) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// requirePage 用于仍要求显式目标的页面工具。browserSnapshot 是例外：
// 它先 reconcile，再把省略 page_id 绑定到最新 active Tab。
func requirePage(sessionID, pageID, operation string) error {
	if sessionID == "" {
		return errors.New(operation + " 需要 session_id 和 page_id")
	}
	if pageID == "" {
		return errors.New(operation + " 需要 session_id 和 page_id；不能省略 page_id")
	}
	return nil
}

func rememberSnapshotRefs(value *page, snapshot Snapshot) {
	if value == nil {
		return
	}
	if value.refs == nil || value.refRevision != snapshot.Revision {
		value.refs = make(map[string]struct{})
		value.refRevision = snapshot.Revision
	}
	if snapshot.ActiveRefs != nil {
		active := make(map[string]struct{}, len(snapshot.ActiveRefs))
		for _, ref := range snapshot.ActiveRefs {
			active[ref] = struct{}{}
		}
		for ref := range value.refs {
			if _, ok := active[ref]; !ok {
				delete(value.refs, ref)
			}
		}
	}
	for _, element := range snapshot.Elements {
		if element.Ref != "" {
			value.refs[element.Ref] = struct{}{}
		}
	}
}

func clearPageRefs(value *page) {
	if value == nil {
		return
	}
	value.refs = make(map[string]struct{})
	value.refRevision = 0
}

func validatePageRef(value *page, ref string, revision uint64) error {
	if value == nil || value.refs == nil {
		return browserError("invalid_reference", "当前 Tab 没有该元素引用", "在目标 page_id 上重新调用 browserSnapshot", true)
	}
	_, ok := value.refs[ref]
	if !ok {
		return browserError("invalid_reference", "当前 Tab 没有该元素引用", "确认 ref 与 page_id 来自同一次 browserSnapshot", true)
	}
	if value.refRevision != revision {
		return browserError("stale_reference", "元素引用已经过期", "在目标 page_id 上重新调用 browserSnapshot", true)
	}
	return nil
}

// operationContext 是请求级 timeout 的唯一入口。BrowserKit 仍会在
// page/session 内叠加配置的 OperationTimeout，最终期限取调用方和两者中更短的一个。
func operationContext(ctx context.Context, timeoutMS int) (context.Context, context.CancelFunc, error) {
	if err := contextErr(ctx); err != nil {
		return nil, func() {}, err
	}
	if timeoutMS < 0 {
		return nil, func() {}, errors.New("timeout_ms 不能为负数")
	}
	if timeoutMS == 0 {
		return ctx, func() {}, nil
	}
	operationCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	return operationCtx, cancel, nil
}
