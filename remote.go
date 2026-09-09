package browserkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// resolveCDPAddress 将 Chrome 的 HTTP 调试入口转换为 browser WebSocket。
func resolveCDPAddress(ctx context.Context, address string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("CDP 地址无效，需要 HTTP 调试地址或 browser WebSocket")
	}
	switch u.Scheme {
	case "ws", "wss":
		if !strings.HasPrefix(u.Path, "/devtools/browser/") {
			return "", errors.New("需要 /devtools/browser/ WebSocket，不能使用页面 WebSocket")
		}
		return u.String(), nil
	case "http", "https":
		if u.Path == "" || u.Path == "/" {
			u.Path = "/json/version"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("读取 Chrome CDP 入口: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("Chrome CDP 入口返回 HTTP %d", resp.StatusCode)
		}
		var version struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&version); err != nil {
			return "", fmt.Errorf("读取 Chrome CDP 信息: %w", err)
		}
		if version.WebSocketDebuggerURL == "" {
			return "", errors.New("Chrome 未返回 webSocketDebuggerUrl，请检查调试设置和授权")
		}
		ws, err := url.Parse(version.WebSocketDebuggerURL)
		if err != nil || (ws.Scheme != "ws" && ws.Scheme != "wss") {
			return "", errors.New("Chrome 返回的 WebSocket 地址无效")
		}
		return resolveCDPAddress(ctx, ws.String())
	default:
		return "", errors.New("CDP 地址必须使用 http、https、ws 或 wss")
	}
}
