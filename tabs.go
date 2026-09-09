package browserkit

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (manager *Manager) registerPage(current *session, value Page, viewport Viewport, opener string) *page {
	entry := &page{id: manager.newID("page"), page: value, viewport: viewport, openerTabID: opener, status: "ready", refs: make(map[string]struct{}), injectedScripts: make(map[string]struct{})}
	if target, ok := value.(TargetPage); ok {
		entry.targetID = target.TargetID()
	}
	if current.tabs == nil {
		current.tabs = make(map[string]*page)
	}
	current.tabs[entry.id] = entry
	current.tabOrder = append(current.tabOrder, entry.id)
	if current.activeTab == "" {
		current.activeTab = entry.id
	}
	return entry
}

func (manager *Manager) activePageLocked(current *session) *page {
	if current == nil {
		return nil
	}
	if current.activeTab != "" {
		if value := current.tabs[current.activeTab]; value != nil {
			return value
		}
	}
	return nil
}

// restoreActiveTarget 在一次可能激活其他 target 的 CDP 操作后，
// 用 session 生命周期的短 context 恢复 Manager 记录的活动 Tab。
// 调用方必须持有 session.tabMu。
func restoreActiveTarget(current *session, browser Session, active *page) error {
	if current == nil || active == nil || active.targetID == "" {
		return nil
	}
	tabSession, ok := browser.(TabSession)
	if !ok {
		return nil
	}
	restoreCtx, cancel := context.WithTimeout(current.ctx, time.Second)
	defer cancel()
	return tabSession.ActivateTab(restoreCtx, active.targetID)
}

// reconcileTabs 同步 CDP 中的网页 target。CDP 查询始终在 session.mu 外执行；
// session.mu 只保护本地 Tab 元数据提交。
func (manager *Manager) reconcileTabs(ctx context.Context, current *session) error {
	if current == nil || current.browser == nil {
		return nil
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	// reconcile 只是可重建的观察投影；已有一次同步进行时，其他读请求直接
	// 使用当前映射，不等待远程 CDP，避免观察路径把整个 session 阻塞。
	if !current.reconcileMu.TryLock() {
		return nil
	}
	defer current.reconcileMu.Unlock()
	// Tab 列表只是可重建的观察投影，整轮同步共用一个短预算；不能让每个
	// CDP 子请求各自重新获得默认 30 秒并把一次状态刷新拖到数分钟。
	reconcileCtx, cancel := context.WithTimeout(ctx, tabReconcileTimeout)
	defer cancel()
	ctx = reconcileCtx
	current.mu.Lock()
	browser := current.browser
	if current.tabs == nil {
		current.tabs = make(map[string]*page)
	}
	current.mu.Unlock()
	tabSession, ok := browser.(TabSession)
	if !ok {
		return nil
	}
	listed, err := tabSession.ListTabs(ctx)
	if err != nil {
		return err
	}
	current.mu.Lock()
	known := make(map[string]*page, len(current.tabs))
	for _, value := range current.tabs {
		if value.targetID != "" {
			known[value.targetID] = value
		}
	}
	current.mu.Unlock()
	seen := make(map[string]bool, len(listed))
	for _, info := range listed {
		if err := contextErr(ctx); err != nil {
			return err
		}
		if info.TargetID == "" || info.Type != "" && info.Type != "page" {
			continue
		}
		if manager.externalBrowser() && !strings.HasPrefix(info.URL, "http://") && !strings.HasPrefix(info.URL, "https://") {
			continue
		}
		seen[info.TargetID] = true
		value := known[info.TargetID]
		newTab := false
		if value == nil {
			current.mu.Lock()
			atLimit := len(current.tabs) >= manager.config.MaxTabsPerSession
			current.mu.Unlock()
			if atLimit {
				if manager.externalBrowser() {
					continue
				}
				// 限制必须作用于真实 target；只忽略本地登记会让
				// Chrome 激活一个 Manager 无法寻址的 Tab，导致直播与输入分叉。
				current.tabMu.Lock()
				closeErr := tabSession.CloseTab(ctx, info.TargetID)
				current.mu.Lock()
				active := manager.activePageLocked(current)
				current.mu.Unlock()
				restoreErr := restoreActiveTarget(current, browser, active)
				current.tabMu.Unlock()
				if closeErr != nil {
					return browserError("tab_limit_failed", "关闭超出上限的浏览器 Tab 失败: "+closeErr.Error(), "关闭不用的 Tab 后重试", true)
				}
				if restoreErr != nil {
					return browserError("tab_restore_failed", "恢复活动浏览器 Tab 失败: "+restoreErr.Error(), "重新获取 tablist 后重试", true)
				}
				continue
			}
			bound, bindErr := tabSession.PageForTab(ctx, info.TargetID)
			if bindErr != nil {
				continue
			}
			viewport := defaultBrowserViewport
			current.mu.Lock()
			if current.closed {
				current.mu.Unlock()
				return browserError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen", true)
			}
			opener := ""
			if candidate := known[info.OpenerID]; candidate != nil && current.tabs[candidate.id] == candidate {
				opener = candidate.id
				if candidate.viewport.Width > 0 && candidate.viewport.Height > 0 {
					viewport = candidate.viewport
				}
			} else if active := manager.activePageLocked(current); active != nil && active.viewport.Width > 0 && active.viewport.Height > 0 {
				viewport = active.viewport
			}
			// Chrome popup 通常继承 opener 的 viewport；没有 opener 时才沿用
			// 当前 active Tab，避免切换后浮窗按错误比例缩放。
			value = manager.registerPage(current, bound, viewport, opener)
			value.targetID = info.TargetID
			current.mu.Unlock()
			known[info.TargetID] = value
			newTab = true
			// 远端 target 激活和本地 activeTab 提交必须共用一个串行边界。
			// 否则并发的用户切换可能先提交本地 page_id，随后又被这个较早的
			// popup 激活请求覆盖，造成状态与真实 CDP target 分叉。
			current.tabMu.Lock()
			var activationErr error
			if !manager.externalBrowser() {
				activationErr = tabSession.ActivateTab(ctx, info.TargetID)
			}
			current.mu.Lock()
			value.status = "loading"
			if !manager.externalBrowser() && activationErr == nil && current.tabs[value.id] == value && current.activeTab != value.id {
				current.activeTab = value.id
				invalidateInputsLocked(current)
			}
			current.mu.Unlock()
			current.tabMu.Unlock()
			manager.emit(ctx, Event{Kind: "tab_opened", SessionID: current.id, PageID: value.id, URL: info.URL})
			if viewportSession, viewportOK := browser.(ViewportTabSession); viewportOK && !manager.externalBrowser() {
				go func() {
					viewportCtx, cancel := context.WithTimeout(current.ctx, time.Second)
					defer cancel()
					_ = viewportSession.SetTabViewport(viewportCtx, info.TargetID, viewport)
				}()
			}
			go manager.awaitTabReady(current, value)
		}
		current.mu.Lock()
		previousURL := value.info.URL
		value.info = PageInfo{URL: info.URL, Title: info.Title}
		if !newTab && value.status == "" {
			value.status = "ready"
		}
		if previousURL != "" && previousURL != info.URL {
			// 外部导航同样会让该 Tab 的 ref 失效，不能只依赖
			// browserNavigate 清理 manager 侧的归属表。
			clearPageRefs(value)
		}
		current.mu.Unlock()
		if previousURL != "" && previousURL != info.URL {
			manager.emit(ctx, Event{Kind: "tab_updated", SessionID: current.id, PageID: value.id, URL: info.URL})
		}
		if newTab {
			if err := manager.autoStartPageDevtools(current, value); err != nil {
				return err
			}
		}
	}
	current.mu.Lock()
	stale := make([]*page, 0)
	for id, value := range current.tabs {
		if value.targetID == "" || seen[value.targetID] {
			continue
		}
		stale = append(stale, value)
		delete(current.tabs, id)
		current.tabOrder = removeTabOrder(current.tabOrder, id)
		if current.activeTab == id {
			current.activeTab = manager.fallbackTabLocked(current)
			invalidateInputsLocked(current)
		}
	}
	if current.activeTab == "" {
		current.activeTab = manager.fallbackTabLocked(current)
	}
	current.mu.Unlock()
	for _, value := range stale {
		stopPageDevtools(value)
		if releaser, ok := browser.(TabReleaseSession); ok {
			releaser.ReleaseTab(value.targetID)
		}
		value.frameMu.Lock()
		value.lastFrame = nil
		value.frameMu.Unlock()
		manager.emit(ctx, Event{Kind: "tab_closed", SessionID: current.id, PageID: value.id})
	}
	if len(stale) != 0 {
		manager.notifyDevtools()
	}
	return nil
}

func (manager *Manager) fallbackTabLocked(current *session) string {
	for _, id := range current.tabOrder {
		if current.tabs[id] != nil {
			return id
		}
	}
	return ""
}

func removeTabOrder(order []string, id string) []string {
	for index, value := range order {
		if value == id {
			copy(order[index:], order[index+1:])
			return order[:len(order)-1]
		}
	}
	return order
}

func (manager *Manager) awaitTabReady(current *session, value *page) {
	// 使用独立的短生命周期等待，不绑定触发 tablist 的 HTTP/工具请求；
	// 请求结束后页面仍可继续加载，状态会在下一次 BrowserState 刷新中体现。
	base := context.Background()
	if current != nil && current.ctx != nil {
		base = current.ctx
	}
	ctx, cancel := context.WithTimeout(base, openIdleTimeout)
	defer cancel()
	_, _ = waitPageIdle(ctx, value.page)
	current.mu.Lock()
	if current.tabs[value.id] != value || current.closed {
		current.mu.Unlock()
		return
	}
	value.status = "ready"
	current.mu.Unlock()
	manager.emit(context.Background(), Event{Kind: "tab_updated", SessionID: current.id, PageID: value.id})
}

// lockSession 返回持有 current.mu 的会话；调用方必须解锁 current。
func (manager *Manager) lockSession(sessionID string) (*session, error) {
	manager.mu.Lock()
	current := manager.sessionLocked(sessionID)
	manager.mu.Unlock()
	if current == nil {
		return nil, browserError("session_not_found", "浏览器 session 不存在", "重新调用 browserOpen", true)
	}
	current.mu.Lock()
	if current.closed || current.browser.Closed() {
		current.mu.Unlock()
		return nil, browserError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen", true)
	}
	current.lastUsed = time.Now()
	return current, nil
}

// get 只解析已经发现的 session_id/page_id 映射，不访问 CDP。
// target 枚举集中在 ListTabs 和 live reconcile，状态观察失败不能阻塞显式操作。
func (manager *Manager) get(ctx context.Context, sessionID, pageID string) (*session, *page, error) {
	if err := contextErr(ctx); err != nil {
		return nil, nil, err
	}
	current, err := manager.lockSession(sessionID)
	if err != nil {
		return nil, nil, err
	}
	selected := manager.activePageLocked(current)
	if pageID != "" {
		selected = current.tabs[pageID]
	}
	if selected == nil {
		current.mu.Unlock()
		return nil, nil, browserError("page_not_found", "浏览器 page 不存在", "使用 browserOpen 返回的 page_id", true)
	}
	current.mu.Unlock()
	manager.mu.Lock()
	activeChanged := false
	if manager.sessions[current.id] == current && !manager.stopped {
		activeChanged = manager.active != current.id
		manager.active = current.id
	}
	manager.mu.Unlock()
	if activeChanged {
		manager.stopLive()
	}
	return current, selected, nil
}

// ListTabs 返回指定 Browser session 的当前网页 Tab 列表。
func (manager *Manager) ListTabs(ctx context.Context, request TabsRequest) (TabsResponse, error) {
	if manager == nil {
		return TabsResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return TabsResponse{}, err
	}
	current, err := manager.lockSession(request.SessionID)
	if err != nil {
		return TabsResponse{}, err
	}
	current.mu.Unlock()
	if err := manager.reconcileTabs(ctx, current); err != nil {
		current.mu.Lock()
		empty := len(current.tabs) == 0
		current.mu.Unlock()
		if empty {
			return TabsResponse{}, err
		}
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.closed {
		return TabsResponse{}, browserError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen", true)
	}
	return manager.tabsResponseLocked(current), nil
}

func (manager *Manager) tabsResponseLocked(current *session) TabsResponse {
	revision := tabsRevisionLocked(current)
	response := TabsResponse{SessionID: current.id, ActivePageID: current.activeTab, TabsRevision: revision, Tabs: make([]TabView, 0, len(current.tabOrder))}
	for _, id := range current.tabOrder {
		value := current.tabs[id]
		if value == nil {
			continue
		}
		response.Tabs = append(response.Tabs, TabView{PageID: value.id, URL: value.info.URL, Title: value.info.Title, Viewport: value.viewport, OpenerPageID: value.openerTabID, Status: value.status, Active: value.id == current.activeTab})
	}
	return response
}

// tabsRevisionLocked 返回稳定的 session 级 Tab 版本。这里使用当前可见
// Tab 投影生成签名，使 reconcile、外部新 Tab 和显式切换都统一进入同一版本。
// 调用方必须持有 current.mu。
func tabsRevisionLocked(current *session) uint64 {
	if current == nil {
		return 0
	}
	var signature strings.Builder
	signature.WriteString(current.activeTab)
	signature.WriteByte(0)
	for _, id := range current.tabOrder {
		value := current.tabs[id]
		if value == nil {
			continue
		}
		signature.WriteString(id)
		signature.WriteByte(0)
		signature.WriteString(value.targetID)
		signature.WriteByte(0)
		signature.WriteString(value.info.URL)
		signature.WriteByte(0)
		signature.WriteString(value.info.Title)
		signature.WriteByte(0)
		signature.WriteString(value.status)
		signature.WriteByte(0)
		signature.WriteString(strconv.Itoa(value.viewport.Width))
		signature.WriteByte('x')
		signature.WriteString(strconv.Itoa(value.viewport.Height))
		signature.WriteByte(0)
	}
	value := signature.String()
	if value != current.tabsSignature {
		current.tabsSignature = value
		current.tabsRevision++
	}
	return current.tabsRevision
}

// tabViewsLocked 返回给模型的紧凑 Tab 摘要。调用方必须持有 current.mu。
func tabViewsLocked(current *session) []TabView {
	if current == nil {
		return nil
	}
	views := make([]TabView, 0, len(current.tabOrder))
	for _, id := range current.tabOrder {
		value := current.tabs[id]
		if value == nil {
			continue
		}
		views = append(views, TabView{PageID: value.id, URL: value.info.URL, Title: value.info.Title, Viewport: value.viewport, OpenerPageID: value.openerTabID, Status: value.status, Active: value.id == current.activeTab})
	}
	return views
}

// tabChangeLocked 返回交互结果中需要模型立即看到的轻量 Tab 状态。
// 调用方必须持有 current.mu，before 是操作开始时观察到的版本。
func tabChangeLocked(current *session, before uint64) (string, uint64, bool) {
	revision := tabsRevisionLocked(current)
	return current.activeTab, revision, revision != before
}

// SetViewport 设置指定网页 Tab 的真实 CSS 视口。
func (manager *Manager) SetViewport(ctx context.Context, request ViewportRequest) error {
	if manager == nil {
		return browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if request.PageID == "" {
		return errors.New("浏览器 page_id 不能为空")
	}
	if err := request.Viewport.Validate(); err != nil {
		return err
	}
	current, value, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return err
	}
	current.mu.Lock()
	oldViewport := value.viewport
	targetID := value.targetID
	browser := current.browser
	current.mu.Unlock()
	if oldViewport == request.Viewport {
		return nil
	}
	viewportSession, ok := browser.(ViewportTabSession)
	if !ok || targetID == "" {
		return browserError("viewport_unsupported", "当前浏览器不支持动态设置视口", "使用支持 CDP viewport 的 Browser Factory", true)
	}
	if err := viewportSession.SetTabViewport(ctx, targetID, request.Viewport); err != nil {
		return browserError("tab_viewport_failed", "浏览器 Tab 视口设置失败: "+err.Error(), "重新获取 tablist 后重试", true)
	}
	current.mu.Lock()
	value = current.tabs[request.PageID]
	if value == nil || value.targetID != targetID {
		current.mu.Unlock()
		return browserError("page_not_found", "浏览器 page 不存在", "使用 browserTabs 返回的 page_id", true)
	}
	value.viewport = request.Viewport
	current.lastUsed = time.Now()
	current.mu.Unlock()
	value.frameMu.Lock()
	value.lastFrame = nil
	value.frameMu.Unlock()
	return nil
}

// ActivateTab 激活指定网页 Tab，并让直播与输入跟随它。
func (manager *Manager) ActivateTab(ctx context.Context, request TabRequest) (TabResponse, error) {
	if manager == nil {
		return TabResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return TabResponse{}, err
	}
	current, value, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return TabResponse{}, err
	}
	current.tabMu.Lock()
	defer current.tabMu.Unlock()
	current.mu.Lock()
	beforeTabsRevision := tabsRevisionLocked(current)
	wasActive := current.activeTab == request.PageID
	targetID := value.targetID
	viewport := value.viewport
	browser := current.browser
	info := value.info
	current.mu.Unlock()
	changed := !wasActive
	if tabSession, ok := browser.(TabSession); ok && targetID != "" {
		if err := tabSession.ActivateTab(ctx, targetID); err != nil {
			return TabResponse{}, browserError("tab_activate_failed", "浏览器 Tab 激活失败: "+err.Error(), "重新获取 tablist 后重试", true)
		}
	}
	current.mu.Lock()
	if current.tabs[request.PageID] != value || value.targetID != targetID {
		current.mu.Unlock()
		return TabResponse{}, browserError("page_not_found", "浏览器 page 不存在", "使用 browserTabs 返回的 page_id", true)
	}
	changed = current.activeTab != request.PageID
	current.activeTab = request.PageID
	if changed {
		invalidateInputsLocked(current)
	}
	current.lastUsed = time.Now()
	current.mu.Unlock()
	manager.mu.Lock()
	manager.active = current.id
	manager.mu.Unlock()
	if changed {
		// 帧协议本身不携带 PageID。切换提交后关闭旧 hub，让所有 WebSocket
		// 从新 active Tab 的缓存重新订阅，避免旧 Tab 已排队帧覆盖新画面。
		manager.stopLive()
		manager.emit(ctx, Event{Kind: "tab_activated", SessionID: current.id, PageID: request.PageID, URL: info.URL})
	}
	// target 激活成功就是 PageID 切换的提交点。viewport 只是显示属性，
	// 失败时不能把本地 activeTab 留在旧页，否则 Chrome 和 Manager 会永久
	// 指向不同页面。目标在发现时已经设置过 viewport，无需让切换请求再次等待。
	if viewportSession, ok := browser.(ViewportTabSession); ok && targetID != "" && viewport.Width > 0 && viewport.Height > 0 {
		go func() {
			viewportCtx, cancel := context.WithTimeout(current.ctx, time.Second)
			defer cancel()
			_ = viewportSession.SetTabViewport(viewportCtx, targetID, viewport)
		}()
	}
	current.mu.Lock()
	activePageID, tabsRevision, tabsChanged := tabChangeLocked(current, beforeTabsRevision)
	current.mu.Unlock()
	return TabResponse{SessionID: current.id, PageID: request.PageID, URL: info.URL, Title: info.Title, Active: true, ActivePageID: activePageID, TabsRevision: tabsRevision, TabsChanged: tabsChanged}, nil
}

// Navigate 在指定网页 Tab 中导航到地址。
func (manager *Manager) Navigate(ctx context.Context, request NavigateRequest) (NavigateResponse, error) {
	if manager == nil {
		return NavigateResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return NavigateResponse{}, err
	}
	if request.PageID == "" || strings.TrimSpace(request.URL) == "" {
		return NavigateResponse{}, errors.New("browserNavigate 需要 page_id 和 url")
	}
	current, currentPage, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return NavigateResponse{}, err
	}
	current.mu.Lock()
	beforeTabsRevision := tabsRevisionLocked(current)
	current.mu.Unlock()
	info, err := currentPage.page.Info(ctx)
	if err != nil {
		return NavigateResponse{}, err
	}
	if err := manager.validateAddress(request.URL, info.URL); err != nil {
		return NavigateResponse{}, err
	}
	target := request.URL
	if parsed, parseErr := url.Parse(request.URL); parseErr == nil && !parsed.IsAbs() {
		base, _ := url.Parse(info.URL)
		target = base.ResolveReference(parsed).String()
	}
	manager.emit(ctx, Event{Kind: "navigation_started", SessionID: current.id, PageID: currentPage.id, URL: target})
	info, err = currentPage.page.Navigate(ctx, target)
	if err != nil {
		return NavigateResponse{}, err
	}
	if _, err := waitPageIdle(ctx, currentPage.page); err != nil {
		return NavigateResponse{}, err
	}
	info, err = currentPage.page.Info(ctx)
	if err != nil {
		return NavigateResponse{}, err
	}
	current.mu.Lock()
	clearPageRefs(currentPage)
	currentPage.info = info
	currentPage.status = "ready"
	current.mu.Unlock()
	manager.emit(ctx, Event{Kind: "navigation_finished", SessionID: current.id, PageID: currentPage.id, URL: info.URL})
	activePageID, tabsRevision, tabsChanged := manager.tabChange(ctx, current, beforeTabsRevision)
	return NavigateResponse{SessionID: current.id, PageID: currentPage.id, URL: info.URL, Title: info.Title, ActivePageID: activePageID, TabsRevision: tabsRevision, TabsChanged: tabsChanged}, nil
}

// Reload 刷新指定网页 Tab 的当前地址。
func (manager *Manager) Reload(ctx context.Context, request TabRequest) (NavigateResponse, error) {
	if manager == nil {
		return NavigateResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return NavigateResponse{}, err
	}
	current, currentPage, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return NavigateResponse{}, err
	}
	current.mu.Lock()
	address := currentPage.info.URL
	current.mu.Unlock()
	if strings.TrimSpace(address) == "" {
		info, infoErr := currentPage.page.Info(ctx)
		if infoErr != nil {
			return NavigateResponse{}, infoErr
		}
		address = info.URL
	}
	info, err := manager.Navigate(ctx, NavigateRequest{SessionID: request.SessionID, PageID: request.PageID, URL: address})
	if err != nil {
		return NavigateResponse{}, err
	}
	return info, nil
}

// CloseTab 关闭指定网页 Tab；关闭 Browser session 仍由 CloseActive 完成。
func (manager *Manager) CloseTab(ctx context.Context, request TabRequest) (CloseTabResponse, error) {
	if manager == nil {
		return CloseTabResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return CloseTabResponse{}, err
	}
	current, value, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return CloseTabResponse{}, err
	}
	current.mu.Lock()
	beforeTabsRevision := tabsRevisionLocked(current)
	current.mu.Unlock()
	current.tabMu.Lock()
	defer current.tabMu.Unlock()
	current.mu.Lock()
	value = current.tabs[request.PageID]
	if value == nil {
		active := current.activeTab
		revision := tabsRevisionLocked(current)
		current.mu.Unlock()
		return CloseTabResponse{SessionID: current.id, PageID: request.PageID, Closed: true, ActivePageID: active, TabsRevision: revision, TabsChanged: revision != beforeTabsRevision}, nil
	}
	wasActive := current.activeTab == request.PageID
	targetID := value.targetID
	browser := current.browser
	if wasActive {
		// 关闭 active target 前先取消直播，但不能在持有 tabMu 时等待 live
		// goroutine 退出：它可能正在 reconcile 并等待同一把 tabMu。
		// screencast 的远端 stop 已经是异步有界清理。
		current.mu.Unlock()
		manager.stopLive()
		current.mu.Lock()
		// 直播停止期间可能有并发的 target reconcile；重新确认目标仍存在。
		value = current.tabs[request.PageID]
		if value == nil {
			active := current.activeTab
			revision := tabsRevisionLocked(current)
			current.mu.Unlock()
			return CloseTabResponse{SessionID: current.id, PageID: request.PageID, Closed: true, ActivePageID: active, TabsRevision: revision, TabsChanged: revision != beforeTabsRevision}, nil
		}
		targetID = value.targetID
	}
	current.mu.Unlock()
	_, _, devtoolsChanged := stopPageDevtools(value)
	if devtoolsChanged {
		manager.notifyDevtools()
	}
	if tabSession, ok := browser.(TabSession); ok && targetID != "" {
		if err := tabSession.CloseTab(ctx, targetID); err != nil {
			return CloseTabResponse{}, browserError("tab_close_failed", "浏览器 Tab 关闭失败: "+err.Error(), "重新获取 tablist 后重试", true)
		}
	} else {
		if err := value.page.Close(ctx); err != nil {
			return CloseTabResponse{}, err
		}
	}
	current.mu.Lock()
	if current.tabs[request.PageID] == nil {
		active := current.activeTab
		revision := tabsRevisionLocked(current)
		current.mu.Unlock()
		return CloseTabResponse{SessionID: current.id, PageID: request.PageID, Closed: true, ActivePageID: active, TabsRevision: revision, TabsChanged: revision != beforeTabsRevision}, nil
	}
	value = current.tabs[request.PageID]
	delete(current.tabs, request.PageID)
	current.tabOrder = removeTabOrder(current.tabOrder, request.PageID)
	value.frameMu.Lock()
	value.lastFrame = nil
	value.frameMu.Unlock()
	var fallback *page
	if wasActive {
		current.activeTab = manager.fallbackTabLocked(current)
		invalidateInputsLocked(current)
		fallback = manager.activePageLocked(current)
	}
	active := current.activeTab
	revision := tabsRevisionLocked(current)
	current.mu.Unlock()
	if fallback != nil && fallback.targetID != "" {
		if tabSession, ok := browser.(TabSession); ok {
			_ = tabSession.ActivateTab(ctx, fallback.targetID)
		}
		if viewportSession, ok := browser.(ViewportTabSession); ok && fallback.viewport.Width > 0 && fallback.viewport.Height > 0 {
			_ = viewportSession.SetTabViewport(ctx, fallback.targetID, fallback.viewport)
		}
	}
	manager.emit(ctx, Event{Kind: "tab_closed", SessionID: current.id, PageID: request.PageID})
	if wasActive {
		if active != "" {
			manager.emit(ctx, Event{Kind: "tab_activated", SessionID: current.id, PageID: active})
		}
	}
	return CloseTabResponse{SessionID: current.id, PageID: request.PageID, Closed: true, ActivePageID: active, TabsRevision: revision, TabsChanged: revision != beforeTabsRevision}, nil
}

// NewTab 在现有浏览器会话中新建并激活一个页面；未打开会话时创建初始会话。
func (manager *Manager) NewTab(ctx context.Context, request OpenRequest) (OpenResponse, error) {
	if err := contextErr(ctx); err != nil {
		return OpenResponse{}, err
	}
	if err := manager.validateOpen(request); err != nil {
		return OpenResponse{}, err
	}
	if err := manager.validateAddress(request.URL, ""); err != nil {
		return OpenResponse{}, err
	}
	current := manager.activeSession()
	if request.SessionID != "" {
		var err error
		current, err = manager.lockSession(request.SessionID)
		if err != nil {
			return OpenResponse{}, err
		}
		current.mu.Unlock()
	}
	if current == nil {
		return manager.Open(ctx, request)
	}
	current.reconcileMu.Lock()
	current.mu.Lock()
	if current.closed || len(current.tabs) >= manager.config.MaxTabsPerSession {
		current.mu.Unlock()
		current.reconcileMu.Unlock()
		return OpenResponse{}, errors.New("浏览器已关闭或达到 Tab 数量限制")
	}
	viewport := request.Viewport
	if viewport.Width <= 0 || viewport.Height <= 0 {
		viewport = defaultBrowserViewport
		if active := manager.activePageLocked(current); active != nil {
			viewport = active.viewport
		}
	}
	current.mu.Unlock()
	value, err := current.browser.NewPage(ctx, PageOptions{Viewport: viewport, InjectScript: request.InjectJS})
	if err != nil {
		current.reconcileMu.Unlock()
		return OpenResponse{}, err
	}
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		current.reconcileMu.Unlock()
		_ = value.Close(ctx)
		return OpenResponse{}, errors.New("浏览器会话已经关闭")
	}
	entry := manager.registerPage(current, value, viewport, "")
	current.mu.Unlock()
	current.reconcileMu.Unlock()
	if err = manager.autoStartPageDevtools(current, entry); err != nil {
		return OpenResponse{}, err
	}
	if _, err = manager.ActivateTab(ctx, TabRequest{SessionID: current.id, PageID: entry.id}); err != nil {
		return OpenResponse{}, err
	}
	manager.emit(ctx, Event{Kind: "tab_opened", SessionID: current.id, PageID: entry.id})
	if request.URL != "" {
		nav, err := manager.Navigate(ctx, NavigateRequest{SessionID: current.id, PageID: entry.id, URL: request.URL})
		return OpenResponse{SessionID: current.id, PageID: entry.id, URL: nav.URL, Title: nav.Title, LoadStatus: OpenLoadLoaded}, err
	}
	return OpenResponse{SessionID: current.id, PageID: entry.id, LoadStatus: OpenLoadLoaded}, nil
}
