package browserkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// AttachRequest 默认连接激活 Tab；设置 URL 时在已有浏览器中新建 Tab。
type AttachRequest struct {
	// URL 可选；设置后在新 Tab 打开该 HTTP/HTTPS 地址，保留已有页面。
	URL string `json:"url,omitempty"`
}

// Attach 接入 RemoteAddress 指向的浏览器，绑定激活页或打开新 Tab。
// Close 和 CloseSession 只断开外部浏览器；CloseTab 仍是显式关闭页面操作。
func (manager *Manager) Attach(ctx context.Context, request AttachRequest) (OpenResponse, error) {
	if manager == nil {
		return OpenResponse{}, errors.New("Manager 不能为空")
	}
	if err := contextErr(ctx); err != nil {
		return OpenResponse{}, err
	}
	if !manager.externalBrowser() {
		return OpenResponse{}, errors.New("Attach 需要配置 Session.RemoteAddress")
	}
	if request.URL != "" {
		return manager.Open(ctx, OpenRequest{URL: request.URL})
	}
	if err := manager.beginOpening(); err != nil {
		return OpenResponse{}, err
	}
	defer manager.finishOpening()
	created, page, status, err := manager.createSession(ctx, OpenRequest{}, true)
	if err != nil {
		return OpenResponse{}, err
	}
	info, err := page.page.Info(ctx)
	if err != nil {
		_ = manager.closeSession(context.Background(), created)
		return OpenResponse{}, err
	}
	if err := manager.validateAddress(info.URL, ""); err != nil {
		_ = manager.closeSession(context.Background(), created)
		return OpenResponse{}, err
	}
	page.info = info
	if err := manager.addSession(created); err != nil {
		_ = manager.closeSession(context.Background(), created)
		return OpenResponse{}, err
	}
	manager.stopLive()
	manager.emit(ctx, Event{Kind: "session_opened", SessionID: created.id, PageID: page.id, URL: info.URL})
	return OpenResponse{SessionID: created.id, PageID: page.id, URL: info.URL, Title: info.Title, LoadStatus: status}, nil
}

func (manager *Manager) externalBrowser() bool {
	return manager.config.Session.RemoteAddress != "" || isRemoteAddress(manager.config.Session.ExecPath)
}

func attachPage(ctx context.Context, browser Session) (Page, error) {
	tabs, ok := browser.(TabSession)
	if !ok {
		return nil, errors.New("浏览器不支持接管已有 Tab")
	}
	listed, err := tabs.ListTabs(ctx)
	if err != nil {
		return nil, err
	}
	if detector, ok := browser.(ActiveTabSession); ok {
		targetID, err := detector.ActiveTab(ctx, listed)
		if err != nil {
			return nil, err
		}
		page, err := tabs.PageForTab(ctx, targetID)
		if err != nil {
			return nil, fmt.Errorf("绑定激活 Tab: %w", err)
		}
		return page, nil
	}
	return activePage(ctx, tabs, listed)
}

// activePage 优先选择焦点页；浏览器失焦时选择唯一可见页。
// CDP 的 target 列表无激活顺序，多窗口存在歧义时要求用户激活目标页。
func activePage(ctx context.Context, tabs TabSession, listed []TabInfo) (Page, error) {
	var focused, visible []Page
	for _, tab := range listed {
		if tab.Type != "page" {
			continue
		}
		p, err := tabs.PageForTab(ctx, tab.TargetID)
		if err != nil {
			return nil, fmt.Errorf("读取 Tab 状态: %w", err)
		}
		result, err := p.Evaluate(ctx, `({focused: document.hasFocus(), visible: document.visibilityState === "visible"})`, EvaluateOptions{})
		if err != nil {
			return nil, fmt.Errorf("读取激活页状态: %w", err)
		}
		var state struct {
			Focused bool
			Visible bool
		}
		if err := json.Unmarshal(result.Value, &state); err != nil {
			return nil, fmt.Errorf("解析激活页状态: %w", err)
		}
		if state.Focused {
			focused = append(focused, p)
		}
		if state.Visible {
			visible = append(visible, p)
		}
	}
	if len(focused) == 1 {
		return focused[0], nil
	}
	if len(focused) == 0 && len(visible) == 1 {
		return visible[0], nil
	}
	return nil, errors.New("无法唯一确定激活 Tab；请切回目标网页并保持浏览器焦点后重试")
}
