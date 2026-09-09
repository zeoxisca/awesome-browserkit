package browserkit

import (
	"context"
	"errors"
	"time"
)

func (manager *Manager) sessionLocked(sessionID string) *session {
	if sessionID != "" {
		return manager.sessions[sessionID]
	}
	current := manager.sessions[manager.active]
	if current != nil {
		return current
	}
	for _, candidate := range manager.sessions {
		if current == nil || candidate.createdAt.After(current.createdAt) {
			current = candidate
		}
	}
	return current
}

// CloseActive 回收当前 Manager 最近使用的浏览器 session，供宿主 UI 的关闭按钮调用。
func (manager *Manager) CloseActive(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	current := manager.sessions[manager.active]
	if current == nil {
		for _, candidate := range manager.sessions {
			if current == nil || candidate.createdAt.After(current.createdAt) {
				current = candidate
			}
		}
	}
	if current == nil {
		manager.mu.Unlock()
		return nil
	}
	delete(manager.sessions, current.id)
	manager.closed[current.id] = struct{}{}
	if manager.active == current.id {
		manager.active = ""
	}
	manager.mu.Unlock()
	// 从活动表摘除就是 Browser session 的逻辑终态。先通知观察者清空
	// Tab/画面，再做可能较慢或失败的 screencast、CDP 和进程回收。
	manager.emit(ctx, Event{Kind: "session_closed", SessionID: current.id})
	manager.stopLiveAndWait()
	return manager.closeSession(ctx, current)
}

func (manager *Manager) reclaimIdleSessions() {
	defer close(manager.reclaimDone)
	interval := manager.idleCheckInterval
	if interval <= 0 || interval > manager.config.IdleTimeout {
		interval = manager.config.IdleTimeout
	}
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-manager.lifecycleCtx.Done():
			return
		case <-ticker.C:
		}
		manager.mu.Lock()
		if manager.stopped {
			manager.mu.Unlock()
			return
		}
		candidates := make([]*session, 0, len(manager.sessions))
		activeID := manager.active
		for _, current := range manager.sessions {
			candidates = append(candidates, current)
		}
		manager.mu.Unlock()
		now := time.Now()
		expired := make([]*session, 0)
		for _, current := range candidates {
			// Web 浏览器状态 WebSocket 代表该 Session 仍在观察页面；保持
			// 与原先前端轮询相同的活跃语义，但不影响其他 Session 回收。
			if current.id == activeID && manager.hasStateSubscribers() {
				continue
			}
			current.mu.Lock()
			lastUsed := current.lastUsed
			current.mu.Unlock()
			if !lastUsed.IsZero() && now.Sub(lastUsed) >= manager.config.IdleTimeout {
				manager.mu.Lock()
				if _, present := manager.sessions[current.id]; present {
					delete(manager.sessions, current.id)
					manager.closed[current.id] = struct{}{}
					if manager.active == current.id {
						manager.active = ""
					}
					expired = append(expired, current)
				}
				manager.mu.Unlock()
			}
		}
		if len(expired) != 0 {
			for _, current := range expired {
				manager.emit(context.Background(), Event{Kind: "session_reclaimed", SessionID: current.id})
			}
			// 先停止 screencast，避免关闭 Browser 时 live hub 仍在向同一
			// CDP 连接发送截图/ack，导致关闭等待和连接 goroutine 堆积。
			manager.stopLiveAndWait()
		}
		for _, current := range expired {
			_ = manager.closeSession(context.Background(), current)
		}
	}
}

func (manager *Manager) closeSession(ctx context.Context, current *session) error {
	if current == nil {
		return nil
	}
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		return nil
	}
	current.closed = true
	if current.cancel != nil {
		current.cancel()
	}
	browser := current.browser
	var releaser TabReleaseSession
	if candidate, ok := browser.(TabReleaseSession); ok {
		releaser = candidate
	}
	targets := make([]string, 0, len(current.tabs))
	pages := make([]*page, 0, len(current.tabs))
	for _, value := range current.tabs {
		pages = append(pages, value)
		if releaser != nil && value.targetID != "" {
			targets = append(targets, value.targetID)
		}
		value.frameMu.Lock()
		value.lastFrame = nil
		value.frameMu.Unlock()
	}
	active := manager.activePageLocked(current)
	current.mu.Unlock()
	for _, value := range pages {
		stopPageDevtools(value)
	}
	if len(pages) != 0 {
		manager.notifyDevtools()
	}
	for _, targetID := range targets {
		releaser.ReleaseTab(targetID)
	}
	shutdownCtx, cancel := shutdownContext(ctx)
	defer cancel()
	if active != nil && !manager.externalBrowser() {
		_ = active.page.Close(shutdownCtx)
	}
	return browser.Close()
}

func (manager *Manager) removeSession(current *session) {
	if current == nil {
		return
	}
	manager.stopLiveAndWait()
	_ = manager.closeSession(context.Background(), current)
	manager.mu.Lock()
	delete(manager.sessions, current.id)
	if manager.active == current.id {
		manager.active = ""
	}
	manager.mu.Unlock()
}

// Close 回收当前 Manager 的全部 session；外部浏览器只断开连接并保留 Tab。
func (manager *Manager) Close(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	manager.mu.Lock()
	if manager.control != nil {
		manager.control.close()
	}
	manager.stopped = true
	openDone := manager.openDone
	if openDone == nil {
		openDone = make(chan struct{})
		close(openDone)
		manager.openDone = openDone
	}
	manager.mu.Unlock()
	manager.closeStateSubscribers()
	manager.closeDevtoolsSubscribers()
	if manager.lifecycleCancel != nil {
		manager.lifecycleCancel()
	}
	manager.stopLiveAndWait()
	select {
	case <-openDone:
	case <-ctx.Done():
	}
	manager.mu.Lock()
	values := make([]*session, 0, len(manager.sessions))
	for _, current := range manager.sessions {
		values = append(values, current)
	}
	manager.sessions = make(map[string]*session)
	manager.closed = make(map[string]struct{})
	manager.active = ""
	manager.mu.Unlock()
	var result error
	for _, current := range values {
		result = errors.Join(result, manager.closeSession(ctx, current))
	}
	if manager.reclaimDone != nil {
		select {
		case <-manager.reclaimDone:
		case <-time.After(2 * time.Second):
		}
	}
	return result
}

func (manager *Manager) finishOpening() {
	manager.mu.Lock()
	manager.opening--
	if manager.opening == 0 {
		close(manager.openDone)
	}
	manager.mu.Unlock()
}

func shutdownContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, 2*time.Second)
}
