package browserkit

import (
	"context"
	"errors"
	"math"
	"time"
)

func (manager *Manager) activeSession() *session {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.sessionLocked("")
}

// activePageForOperation 返回当前页面；返回后不持有 session.mu。
func (manager *Manager) activePageForOperation(ctx context.Context) (*session, *page, error) {
	current := manager.activeSession()
	if current == nil {
		return nil, nil, browserError("page_not_found", "当前没有打开的浏览器页面", "调用 browserOpen 打开页面", true)
	}
	current.mu.Lock()
	closed := current.closed || current.browser.Closed()
	current.mu.Unlock()
	if closed {
		return nil, nil, browserError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen", true)
	}
	_ = manager.reconcileTabs(ctx, current)
	current.mu.Lock()
	active := manager.activePageLocked(current)
	if active == nil {
		current.mu.Unlock()
		return nil, nil, browserError("page_not_found", "当前没有打开的浏览器页面", "调用 browserOpen 打开页面", true)
	}
	current.lastUsed = time.Now()
	current.mu.Unlock()
	return current, active, nil
}

func (manager *Manager) stopLive() {
	manager.liveMu.Lock()
	hub := manager.live
	manager.live = nil
	manager.liveMu.Unlock()
	if hub != nil {
		hub.closeSubscribers()
		hub.stop()
	}
}

// stopLiveAndWait 停止直播并等待其 goroutine 退出。
// 只能在未持有 session.mu 时调用，避免直播循环正在等待同一把锁。
func (manager *Manager) stopLiveAndWait() {
	manager.liveMu.Lock()
	hub := manager.live
	manager.live = nil
	manager.liveMu.Unlock()
	if hub == nil {
		return
	}
	hub.stop()
	select {
	case <-hub.done:
	case <-time.After(2 * time.Second):
	}
}

// LiveState 是 Web 浮窗查询的当前页面状态，不包含截图数据。
type LiveState struct {
	URL      string
	Title    string
	Viewport Viewport
}

// LiveState 返回当前活动浏览器页面 的状态，不执行截图。
func (manager *Manager) LiveState(ctx context.Context) (LiveState, error) {
	if manager == nil {
		return LiveState{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return LiveState{}, err
	}
	_, activePage, err := manager.activePageForOperation(ctx)
	if err != nil {
		return LiveState{}, err
	}
	info, err := activePage.page.Info(ctx)
	if err != nil {
		return LiveState{}, err
	}
	return LiveState{URL: info.URL, Title: info.Title, Viewport: activePage.viewport}, nil
}

// DispatchLiveInput 把 Web 直播中的输入发送给当前真实浏览器页面。
func (manager *Manager) DispatchLiveInput(ctx context.Context, pageID string, event InputEvent) error {
	return manager.dispatchInput(ctx, "", pageID, event)
}

// DispatchViewerInput 只接收当前控制方的显式页面输入。
func (manager *Manager) DispatchViewerInput(ctx context.Context, viewerID, pageID string, event InputEvent) error {
	if viewerID == "" || pageID == "" {
		return errors.New("viewer_id 和 page_id 不能为空")
	}
	return manager.dispatchInput(ctx, viewerID, pageID, event)
}
func (manager *Manager) dispatchInput(ctx context.Context, viewerID, pageID string, event InputEvent) error {
	if manager == nil {
		return browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if viewerID != "" && !manager.CanControl(viewerID) {
		return errors.New("当前窗口没有浏览器操作权")
	}
	current, activePage, err := manager.get(ctx, "", pageID)
	if err != nil {
		return err
	}
	if pageID == "" {
		// 未携带目标的输入在接收瞬间绑定当前 active page。它仍然只进入
		// 该 page/epoch 的队列，后续切 Tab 时会被正常丢弃。
		pageID = activePage.id
	}
	current.mu.Lock()
	if current.activeTab != pageID {
		current.mu.Unlock()
		return nil
	}
	viewport := activePage.viewport
	epoch := current.inputEpoch
	current.mu.Unlock()
	if err := validateLiveInput(event, viewport); err != nil {
		return err
	}
	_, ok := activePage.page.(InputPage)
	if !ok {
		return browserError("input_unsupported", "当前浏览器页面不支持直播交互", "使用支持 CDP Input 域的 Browser Factory", true)
	}
	queued := liveInput{viewerID: viewerID, page: activePage, event: event, epoch: epoch}
	if coalescibleLiveInput(event) {
		// 连续采样由后续位置覆盖；队列满时不能为了保留它而淘汰快捷键、
		// key up 或触摸边界等不可重建的离散输入。
		select {
		case current.inputQueue <- queued:
		default:
		}
		return nil
	}
	// WebSocket 接收循环不能在这里等待队列空位，否则 renderer 变慢时会
	// 停止读取整条连接，并把积压扩散到客户端发送缓冲。离散输入不能静默
	// 伪装成已接收；队列满时立即返回可恢复错误，由外围决定如何提示或重试。
	select {
	case current.inputQueue <- queued:
		return nil
	default:
		return browserError("input_overloaded", "浏览器输入队列已满", "等待输入恢复后重试", true)
	}
}

func (manager *Manager) dispatchLiveInputs(current *session) {
	var pending *liveInput
	for {
		var input liveInput
		if pending != nil {
			input = *pending
			pending = nil
		} else {
			select {
			case <-current.ctx.Done():
				return
			case input = <-current.inputQueue:
			}
		}
		input, pending = coalesceLiveInputs(input, current.inputQueue)
		if input.viewerID != "" && !manager.CanControl(input.viewerID) {
			continue
		}
		current.mu.Lock()
		valid := !current.closed && current.activeTab == input.page.id &&
			current.tabs[input.page.id] == input.page && current.inputEpoch == input.epoch
		current.mu.Unlock()
		if !valid {
			continue
		}
		inputPage, ok := input.page.page.(InputPage)
		if !ok {
			continue
		}
		operationCtx, cancel := context.WithTimeout(current.ctx, liveInputDispatchTimeout)
		current.mu.Lock()
		valid = !current.closed && current.activeTab == input.page.id &&
			current.tabs[input.page.id] == input.page && current.inputEpoch == input.epoch
		if valid {
			current.inputCancel = cancel
		}
		current.mu.Unlock()
		if !valid {
			cancel()
			continue
		}
		if lockInteraction(operationCtx, &current.interactionMu) == nil {
			current.mu.Lock()
			valid = !current.closed && current.activeTab == input.page.id &&
				current.tabs[input.page.id] == input.page && current.inputEpoch == input.epoch
			current.mu.Unlock()
			if input.viewerID != "" {
				valid = valid && manager.CanControl(input.viewerID)
			}
			if valid {
				_ = inputPage.DispatchInput(operationCtx, input.event)
			}
			current.interactionMu.Unlock()
		}
		cancel()
		current.mu.Lock()
		current.inputCancel = nil
		current.mu.Unlock()
	}
}

// coalesceLiveInputs 丢弃尚未投递的过时连续采样。鼠标和触摸移动是绝对状态，
// 因此保留最新一条；滚动是相对位移，因此合并位移并采用最新指针状态。
// 遇到按下、抬起、键盘等离散事件立即停止，保证其前后顺序不变。
func coalesceLiveInputs(first liveInput, queue <-chan liveInput) (liveInput, *liveInput) {
	if !coalescibleLiveInput(first.event) {
		return first, nil
	}
	result := first
	for {
		select {
		case next := <-queue:
			if next.viewerID != result.viewerID || next.page != result.page || next.epoch != result.epoch ||
				next.event.Kind != result.event.Kind || next.event.Action != result.event.Action {
				return result, &next
			}
			if result.event.Action == "wheel" {
				next.event.DeltaX += result.event.DeltaX
				next.event.DeltaY += result.event.DeltaY
			}
			result = next
		default:
			return result, nil
		}
	}
}

func coalescibleLiveInput(event InputEvent) bool {
	return event.Action == "move" && (event.Kind == "mouse" || event.Kind == "touch") ||
		event.Kind == "mouse" && event.Action == "wheel"
}

func validateLiveInput(event InputEvent, viewport Viewport) error {
	if event.Modifiers < 0 || event.Modifiers > 15 {
		return errors.New("浏览器输入 modifiers 无效")
	}
	if event.Buttons < 0 || event.Buttons > 31 || event.ClickCount < 0 || event.ClickCount > 3 {
		return errors.New("浏览器输入鼠标状态无效")
	}
	if len(event.Text) > 64<<10 || len(event.Key) > 128 || len(event.Code) > 128 {
		return errors.New("浏览器输入文本过长")
	}
	if event.Kind == "key" && event.Modifiers&7 != 0 {
		return errors.New("浏览器不允许直接透传带修饰键的按键，请使用语义化快捷键")
	}
	if event.Kind == "shortcut" {
		if event.Modifiers != 0 && event.Modifiers != 2 && event.Modifiers != 4 {
			return errors.New("语义化快捷键只允许 Ctrl 或 Meta 修饰键")
		}
		switch event.Action {
		case "select_all", "copy", "paste":
		default:
			return errors.New("浏览器只支持全选、复制和粘贴快捷键")
		}
	}
	if event.Kind == "mouse" {
		if err := validateLivePoint(event.X, event.Y, viewport); err != nil {
			return err
		}
		if math.IsNaN(event.DeltaX) || math.IsInf(event.DeltaX, 0) || math.IsNaN(event.DeltaY) || math.IsInf(event.DeltaY, 0) {
			return errors.New("浏览器滚动距离无效")
		}
	}
	for _, point := range event.Touches {
		if err := validateLivePoint(point.X, point.Y, viewport); err != nil {
			return err
		}
		if point.ID < 0 || point.RadiusX < 0 || point.RadiusY < 0 || point.Force < 0 || point.Force > 1 {
			return errors.New("浏览器触点状态无效")
		}
	}
	return nil
}

func validateLivePoint(x, y float64, viewport Viewport) error {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) ||
		x < 0 || y < 0 || viewport.Width <= 0 || viewport.Height <= 0 || x >= float64(viewport.Width) || y >= float64(viewport.Height) {
		return errors.New("浏览器输入坐标超出 viewport")
	}
	return nil
}

// LiveFrame 返回当前活动浏览器页面 的最新画面。
func (manager *Manager) LiveFrame(ctx context.Context) (LiveFrame, error) {
	if manager == nil {
		return LiveFrame{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return LiveFrame{}, err
	}
	current, activePage, err := manager.activePageForOperation(ctx)
	if err != nil {
		return LiveFrame{}, err
	}
	current.mu.Lock()
	generation := current.inputEpoch
	current.mu.Unlock()
	info, err := activePage.page.Info(ctx)
	if err != nil {
		return LiveFrame{}, err
	}
	screenshot, err := activePage.page.Screenshot(ctx, ScreenshotOptions{})
	if err != nil {
		return LiveFrame{}, err
	}
	frame := LiveFrame{PageID: activePage.id, Generation: generation, Data: screenshot.Data, MIME: "image/" + screenshot.Format, URL: info.URL, Title: info.Title}
	cachePageLiveFrame(activePage, frame)
	return frame, nil
}
