package browserkit

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

const (
	assetsDefaultLimit = 100
	assetsMaxLimit     = 500
)

// AssetsRequest 是 browserAssets 的输入。
type AssetsRequest struct {
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id,omitempty"`
	Host      string `json:"host,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// AssetsResponse 是 browserAssets 的有界只读响应。
type AssetsResponse struct {
	SessionID string  `json:"session_id"`
	PageID    string  `json:"page_id"`
	Host      string  `json:"host,omitempty"`
	Assets    []Asset `json:"assets"`
	Truncated bool    `json:"truncated,omitempty"`
}

func (manager *Manager) listAssets(ctx context.Context, request AssetsRequest) (AssetsResponse, error) {
	if manager == nil {
		return AssetsResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if request.Limit == 0 {
		request.Limit = assetsDefaultLimit
	}
	if request.Limit < 1 || request.Limit > assetsMaxLimit {
		return AssetsResponse{}, errors.New("browserAssets limit 必须位于 1 到 500")
	}
	host := strings.TrimSpace(request.Host)
	if host != "" {
		if strings.Contains(host, "://") {
			parsed, err := url.Parse(host)
			if err != nil || parsed.Hostname() == "" {
				return AssetsResponse{}, errors.New("browserAssets host 无效")
			}
			host = parsed.Hostname()
		}
		if len(host) > 253 || strings.ContainsAny(host, "/?#") {
			return AssetsResponse{}, errors.New("browserAssets host 无效")
		}
	}
	var current *session
	var value *page
	var err error
	if request.SessionID == "" {
		current, value, err = manager.devtoolsPage("", request.PageID)
	} else {
		current, value, err = manager.get(ctx, request.SessionID, request.PageID)
	}
	if err != nil {
		return AssetsResponse{}, err
	}
	page, ok := value.page.(AssetsPage)
	if !ok {
		return AssetsResponse{}, browserError("assets_unsupported", "当前浏览器页面不支持资源列表", "使用支持 BrowserKit AssetsPage 的 Browser Factory", true)
	}
	result, err := page.ListAssets(ctx, ListAssetsOptions{Host: host, Limit: request.Limit})
	if err != nil {
		return AssetsResponse{}, err
	}
	return AssetsResponse{SessionID: current.id, PageID: value.id, Host: host, Assets: result.Assets, Truncated: result.Truncated}, nil
}

// ListAssets 返回当前页面已经观测到的资源列表。
func (manager *Manager) ListAssets(ctx context.Context, request AssetsRequest) (AssetsResponse, error) {
	return manager.listAssets(ctx, request)
}
