package browserkit

import (
	"context"
)

// SubscribeState 订阅浏览器状态变化通知。通知不携带状态正文，消费方应重新读取
// 当前状态；通道只有一个缓冲槽，连续变化会合并，避免慢客户端造成内存增长。
func (manager *Manager) SubscribeState(ctx context.Context) (<-chan struct{}, error) {
	if manager == nil {
		return nil, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	updates := make(chan struct{}, 1)
	manager.stateMu.Lock()
	if manager.stopped {
		manager.stateMu.Unlock()
		close(updates)
		return updates, nil
	}
	manager.stateSubs[updates] = struct{}{}
	manager.stateMu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-manager.lifecycleCtx.Done():
		}
		manager.stateMu.Lock()
		if _, ok := manager.stateSubs[updates]; ok {
			delete(manager.stateSubs, updates)
			close(updates)
		}
		manager.stateMu.Unlock()
	}()
	return updates, nil
}

func (manager *Manager) closeStateSubscribers() {
	manager.stateMu.Lock()
	for updates := range manager.stateSubs {
		close(updates)
		delete(manager.stateSubs, updates)
	}
	manager.stateMu.Unlock()
}

func (manager *Manager) hasStateSubscribers() bool {
	manager.stateMu.Lock()
	defer manager.stateMu.Unlock()
	return len(manager.stateSubs) != 0
}
