package browserkit

import (
	"context"
	"sync"
	"time"
)

// LiveFrame 是 实时视图使用的带页面身份的图帧。
type LiveFrame struct {
	// PageID 是图片所属页面。
	PageID string `json:"page_id"`
	// Generation 是活动页面和视口变化时更新的输入边界。
	Generation uint64 `json:"generation"`
	Data       []byte `json:"-"`
	MIME       string `json:"mime"`
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
}

type liveHub struct {
	manager   *Manager
	ctx       context.Context
	stop      context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	subs      map[chan LiveFrame]struct{}
	latest    LiveFrame
	hasLatest bool
	closed    bool
}

func cloneLiveFrame(frame LiveFrame) LiveFrame {
	copyFrame := frame
	if len(frame.Data) != 0 {
		copyFrame.Data = append([]byte(nil), frame.Data...)
	}
	return copyFrame
}

func cachePageFrame(value *page, screenshot Screenshot) {
	if value == nil || len(screenshot.Data) == 0 {
		return
	}
	cachePageLiveFrame(value, LiveFrame{Data: screenshot.Data, MIME: "image/" + screenshot.Format})
}

func cachePageLiveFrame(value *page, frame LiveFrame) {
	if value == nil || len(frame.Data) == 0 {
		return
	}
	value.frameMu.Lock()
	frame.PageID = value.id
	cached := cloneLiveFrame(frame)
	value.lastFrame = &cached
	value.frameMu.Unlock()
}

func (manager *Manager) cachedActiveFrame() (LiveFrame, bool) {
	manager.mu.Lock()
	current := manager.sessionLocked("")
	manager.mu.Unlock()
	if current == nil {
		return LiveFrame{}, false
	}
	current.mu.Lock()
	active := manager.activePageLocked(current)
	if active == nil || current.closed || current.browser.Closed() {
		current.mu.Unlock()
		return LiveFrame{}, false
	}
	frame, ok := cachedPageFrame(active)
	if ok {
		frame.URL = active.info.URL
		frame.Title = active.info.Title
	}
	current.mu.Unlock()
	return frame, ok
}

func cachedPageFrame(active *page) (LiveFrame, bool) {
	if active == nil {
		return LiveFrame{}, false
	}
	active.frameMu.Lock()
	frame, ok := LiveFrame{}, active.lastFrame != nil
	if ok {
		frame = cloneLiveFrame(*active.lastFrame)
	}
	active.frameMu.Unlock()
	return frame, ok
}

// CachedLiveFrame 返回 active Tab 的最后一帧，不触发截图或 CDP 页面操作。
// 用于 Tab 切换期间在 screencast 初始化前消除黑屏。
func (manager *Manager) CachedLiveFrame(ctx context.Context) (LiveFrame, error) {
	if manager == nil {
		return LiveFrame{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return LiveFrame{}, err
	}
	frame, ok := manager.cachedActiveFrame()
	if !ok {
		return LiveFrame{}, browserError("frame_not_found", "当前没有缓存的浏览器画面", "等待浏览器产生首帧", true)
	}
	return frame, nil
}

// SubscribeLiveFrames 订阅当前页面的最新 JPEG 画面。
// Manager 只维护一条 CDP screencast；慢客户端会跳过旧帧。
func (manager *Manager) SubscribeLiveFrames(ctx context.Context) (<-chan LiveFrame, error) {
	if manager == nil {
		return nil, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	// 先读取缓存，再获取 liveMu。cachedActiveFrame 会依次获取 manager.mu、
	// current.mu 和 page.frameMu；不能在持有 liveMu 时进入这些锁，否则会与
	// SetViewport/ActivateTab 持有 current.mu 后停止直播形成锁顺序反转。
	cached, hasCached := manager.cachedActiveFrame()
	manager.liveMu.Lock()
	hub := manager.live
	if hub != nil {
		hub.mu.Lock()
		closed := hub.closed
		hub.mu.Unlock()
		if closed {
			manager.live = nil
			hub = nil
		}
	}
	start := false
	if hub == nil {
		hubCtx, cancel := context.WithCancel(context.Background())
		hub = &liveHub{manager: manager, ctx: hubCtx, stop: cancel, done: make(chan struct{}), subs: make(map[chan LiveFrame]struct{})}
		if hasCached {
			hub.latest, hub.hasLatest = cached, true
		}
		manager.live = hub
		start = true
	}
	frames := make(chan LiveFrame, 1)
	hub.mu.Lock()
	hub.subs[frames] = struct{}{}
	if hub.hasLatest {
		frames <- hub.latest
	}
	hub.mu.Unlock()
	manager.liveMu.Unlock()
	if start {
		go hub.run()
	}
	go func() {
		select {
		case <-ctx.Done():
			hub.remove(frames)
		case <-hub.done:
		}
	}()
	return frames, nil
}

func (hub *liveHub) remove(frames chan LiveFrame) {
	hub.mu.Lock()
	if _, ok := hub.subs[frames]; ok {
		delete(hub.subs, frames)
		close(frames)
	}
	empty := len(hub.subs) == 0
	hub.mu.Unlock()
	if empty {
		hub.manager.liveMu.Lock()
		if hub.manager.live == hub {
			hub.manager.live = nil
		}
		hub.manager.liveMu.Unlock()
		hub.stop()
	}
}

func (hub *liveHub) publish(page *page, frame LiveFrame) {
	if page != nil {
		frame.PageID = page.id
	}

	if page != nil {
		cachePageLiveFrame(page, frame)
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return
	}
	hub.latest, hub.hasLatest = frame, true
	for frames := range hub.subs {
		select {
		case <-frames:
		default:
		}
		select {
		case frames <- frame:
		default:
		}
	}
	hub.mu.Unlock()
}

// closeSubscribers 同步丢弃排队的旧帧；无需等待持有页面锁的采集协程。
func (hub *liveHub) closeSubscribers() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.closed = true
	for frames := range hub.subs {
		select {
		case <-frames:
		default:
		}
		close(frames)
		delete(hub.subs, frames)
	}
}

func (hub *liveHub) run() {
	defer func() {
		hub.closeSubscribers()
		close(hub.done)
		hub.manager.liveMu.Lock()
		if hub.manager.live == hub {
			hub.manager.live = nil
		}
		hub.manager.liveMu.Unlock()
	}()
	manager := hub.manager
	for {
		select {
		case <-hub.ctx.Done():
			return
		default:
		}
		current := manager.activeSession()
		if current == nil {
			return
		}
		_ = manager.reconcileTabs(hub.ctx, current)
		current.mu.Lock()
		page := manager.activePageLocked(current)
		closed := current.closed || current.browser.Closed()
		generation := current.inputEpoch
		viewport := Viewport{}
		if page != nil {
			viewport = page.viewport
		}
		current.mu.Unlock()
		if page == nil || closed {
			return
		}
		// 同一 WebSocket 内切换 Tab 时，先把目标 Tab 的最后一帧推送出去，
		// 再等待新的 screencast 首帧；订阅端无需重新建立连接。
		if cached, ok := cachedPageFrame(page); ok {
			hub.publish(page, cached)
		}
		changed := false
		fps, quality, everyNthFrame := manager.config.liveOptions()
		frameInterval := time.Second / time.Duration(fps)
		if streamer, ok := page.page.(ScreencastPage); ok {
			// 省流档由 EveryNthFrame 在 Chrome 编码前先粗略降频；其他档位
			// 保留每次页面变化，交给 frameInterval 控制 Web 直播链路带宽。
			frames, stop, err := streamer.StartScreencast(hub.ctx, ScreencastOptions{
				Quality: quality, EveryNthFrame: everyNthFrame,
			})
			if err == nil {
				interval := time.NewTicker(frameInterval) // 帧锁。
				reconcileTicker := time.NewTicker(tabReconcileInterval)
				firstFrameTimer := time.NewTimer(liveFirstFrameTimeout)
				firstFrameC := firstFrameTimer.C
				var pending *Screenshot
				for {
					select {
					case <-hub.ctx.Done():
						stop()
						interval.Stop()
						reconcileTicker.Stop()
						firstFrameTimer.Stop()
						return
					case screenshot, ok := <-frames:
						if !ok {
							changed = true
							break
						}
						if firstFrameC != nil {
							firstFrameTimer.Stop()
							firstFrameC = nil
						}
						pending = &screenshot
					case <-firstFrameC:
						firstFrameC = nil
						// Chrome 偶尔接受 startScreencast 却不产生首帧，特别是
						// device metrics 刚变化时。补一张有界截图，既恢复画面，
						// 也让前端确认新尺寸后重新开放坐标输入；screencast 本身
						// 保持订阅，后续事件仍可继续接管。
						screenshotCtx, screenshotCancel := context.WithTimeout(hub.ctx, liveFirstFrameTimeout)
						screenshot, screenshotErr := page.page.Screenshot(screenshotCtx, ScreenshotOptions{})
						screenshotCancel()
						if screenshotErr == nil {
							current.mu.Lock()
							info := page.info
							current.mu.Unlock()
							hub.publish(page, LiveFrame{Generation: generation, Data: screenshot.Data, MIME: "image/" + screenshot.Format, URL: info.URL, Title: info.Title})
						}
					case <-interval.C:
						if pending != nil {
							current.mu.Lock()
							info := page.info
							current.mu.Unlock()
							hub.publish(page, LiveFrame{Generation: generation, Data: pending.Data, MIME: "image/jpeg", URL: info.URL, Title: info.Title})
							pending = nil
						}
						if manager.liveBindingChanged(current, page, viewport) {
							changed = true
						}
					case <-reconcileTicker.C:
						_ = manager.reconcileTabs(hub.ctx, current)
						if manager.liveBindingChanged(current, page, viewport) {
							changed = true
						}
					}
					if changed {
						break
					}
				}
				stop()
				interval.Stop()
				reconcileTicker.Stop()
				firstFrameTimer.Stop()
				if !changed {
					return
				}
				continue
			}
		}
		// 自定义 Browser Factory 可以只实现稳定 Page 接口。此时按同一直播档位定时截图。
		interval := time.NewTicker(frameInterval)
		reconcileTicker := time.NewTicker(tabReconcileInterval)
		for {
			select {
			case <-hub.ctx.Done():
				interval.Stop()
				reconcileTicker.Stop()
				return
			case <-reconcileTicker.C:
				_ = manager.reconcileTabs(hub.ctx, current)
				if manager.liveBindingChanged(current, page, viewport) {
					changed = true
				}
				if changed {
					interval.Stop()
					reconcileTicker.Stop()
					break
				}
			case <-interval.C:
				if manager.liveBindingChanged(current, page, viewport) {
					changed = true
				}
				if changed {
					interval.Stop()
					reconcileTicker.Stop()
					break
				}
				current.mu.Lock()
				closed := current.closed || current.browser.Closed()
				current.mu.Unlock()
				if closed {
					interval.Stop()
					reconcileTicker.Stop()
					return
				}
				screenshot, err := page.page.Screenshot(hub.ctx, ScreenshotOptions{})
				if err != nil {
					interval.Stop()
					reconcileTicker.Stop()
					return
				}
				current.mu.Lock()
				info := page.info
				current.mu.Unlock()
				hub.publish(page, LiveFrame{Generation: generation, Data: screenshot.Data, MIME: "image/" + screenshot.Format, URL: info.URL, Title: info.Title})
			}
			if changed {
				break
			}
		}
		if !changed {
			return
		}
	}
}

// liveBindingChanged 判断当前图流是否还绑定到同一个 Page 和 viewport。
// viewport 变化时必须重启 screencast：Chrome 不保证已有 screencast 会在
// device metrics 更新后主动发送一张新尺寸帧，继续等待会让前端永久停在
// “等待新尺寸首帧”状态并拒绝坐标输入。
func (manager *Manager) liveBindingChanged(current *session, value *page, viewport Viewport) bool {
	manager.mu.Lock()
	activeSession := manager.sessionLocked("")
	manager.mu.Unlock()
	if activeSession != current {
		return true
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.closed || current.browser.Closed() || manager.activePageLocked(current) != value || value.viewport != viewport
}
