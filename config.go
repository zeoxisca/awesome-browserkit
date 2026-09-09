package browserkit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionFactory 允许测试或宿主替换浏览器启动实现。
type SessionFactory func(context.Context, SessionConfig) (Session, error)

// Event 是浏览器生命周期事件。
type Event struct {
	Kind      string    `json:"kind"`
	SessionID string    `json:"session_id"`
	PageID    string    `json:"page_id,omitempty"`
	URL       string    `json:"url,omitempty"`
	At        time.Time `json:"at"`
}

// EventSink 接收浏览器生命周期事件。实现必须尽快返回。
type EventSink interface{ OnBrowserEvent(context.Context, Event) }

// LiveQuality 是浏览器直播的综合质量档位。
type LiveQuality string

const (
	// LiveQualityLow 使用 12 FPS 和 50% JPEG 质量，适合弱网。
	LiveQualityLow LiveQuality = "low"
	// LiveQualityMedium 使用 12 FPS 和 75% JPEG 质量，兼顾流量和清晰度，是默认档位。
	LiveQualityMedium LiveQuality = "medium"
	// LiveQualityHigh 使用 20 FPS 和 100% JPEG 质量。
	LiveQualityHigh LiveQuality = "high"
	// LiveQualityUltra 使用 30 FPS 和 100% JPEG 质量，适合本机或局域网。
	LiveQualityUltra LiveQuality = "ultra"
)

// Config 控制 浏览器运行时 的安全边界和资源上限。
type Config struct {
	Session       SessionConfig
	AllowEvaluate bool
	// AllowDevtools 允许采集页面 Console、异常和 Network 元数据。
	AllowDevtools bool
	// DevtoolsAutoStart 在每个新页面登记后立即开始开发诊断采集。
	DevtoolsAutoStart   bool
	AllowPrivateNetwork bool
	AllowedOrigins      []string
	// LiveQuality 同时控制直播帧率和 JPEG 编码质量；零值为 LiveQualityMedium。
	LiveQuality LiveQuality
	MaxSessions int
	// MaxTabsPerSession 限制单个浏览器 session 的网页 Tab 数量；零值为 16。
	MaxTabsPerSession int
	ScreenshotDir     string
	MaxScriptBytes    int
	MaxResultBytes    int
	// IdleTimeout 是浏览器 session 无操作后的自动回收时间；零值为 30 分钟。
	IdleTimeout time.Duration
	Factory     SessionFactory
	EventSink   EventSink
}

// Validate 检查 浏览器运行时 配置。
func (config Config) Validate() error {
	if strings.TrimSpace(config.Session.ExecPath) != "" && strings.TrimSpace(config.Session.RemoteAddress) != "" {
		return errors.New("Browser ExecPath 和 RemoteAddress 不能同时设置")
	}
	if config.DevtoolsAutoStart && !config.AllowDevtools {
		return errors.New("Browser DevtoolsAutoStart 需要先启用 AllowDevtools")
	}
	if config.MaxSessions < 0 {
		return errors.New("Browser MaxSessions 不能为负数")
	}
	if config.MaxTabsPerSession < 0 {
		return errors.New("Browser MaxTabsPerSession 不能为负数")
	}
	if config.MaxScriptBytes < 0 {
		return errors.New("Browser MaxScriptBytes 不能为负数")
	}
	if config.MaxResultBytes < 0 {
		return errors.New("Browser MaxResultBytes 不能为负数")
	}
	switch config.LiveQuality {
	case "", LiveQualityLow, LiveQualityMedium, LiveQualityHigh, LiveQualityUltra:
	default:
		return fmt.Errorf("Browser LiveQuality 无效: %q", config.LiveQuality)
	}
	if config.IdleTimeout < 0 {
		return errors.New("Browser IdleTimeout 不能为负数")
	}
	if config.Session.OperationTimeout < 0 {
		return errors.New("Browser OperationTimeout 不能为负数")
	}
	for _, origin := range config.AllowedOrigins {
		if _, err := allowedOriginKey(origin); err != nil {
			return fmt.Errorf("Browser AllowedOrigin 无效: %q", origin)
		}
	}
	return nil
}

func allowedOriginKey(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("不是合法 origin")
	}
	return webOriginKey(parsed)
}

// webOriginKey 按 Web Origin 语义归一化 scheme、host 和默认端口，
// 避免配置校验与导航校验各自实现一套匹配规则。
func webOriginKey(parsed *url.URL) (string, error) {
	if parsed == nil {
		return "", errors.New("URL 为空")
	}
	scheme := strings.ToLower(parsed.Scheme)
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if (scheme != "http" && scheme != "https") || hostname == "" {
		return "", errors.New("只支持 http/https origin")
	}
	port := parsed.Port()
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}

func (config Config) normalized() (Config, error) {
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	if config.MaxSessions == 0 {
		config.MaxSessions = 4
	}
	if config.MaxTabsPerSession == 0 {
		config.MaxTabsPerSession = 16
	}
	if config.MaxScriptBytes == 0 {
		config.MaxScriptBytes = 64 << 10
	}
	if config.MaxResultBytes == 0 {
		config.MaxResultBytes = 256 << 10
	}
	if config.LiveQuality == "" {
		config.LiveQuality = LiveQualityMedium
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = defaultSessionIdleTimeout
	}
	if config.Factory == nil {
		config.Factory = NewSession
	}
	if config.ScreenshotDir == "" {
		config.ScreenshotDir = filepath.Join(os.TempDir(), "browserkit-browser")
	}
	return config, nil
}

func (config Config) liveOptions() (fps, quality, everyNthFrame int) {
	switch config.LiveQuality {
	case LiveQualityLow:
		return 12, 50, 2
	case LiveQualityMedium:
		return 12, 75, 1
	case LiveQualityUltra:
		return 30, 100, 1
	case LiveQualityHigh:
		return 20, 100, 1
	default:
		return 12, 75, 1
	}
}

func screenshotPath(directory, sessionID, pageID string, sequence uint64) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(directory, fmt.Sprintf("%s-%s-%03d.png", sessionID, pageID, sequence)), nil
}

// DevtoolsEnabled 返回宿主是否授权开发诊断。
func (manager *Manager) DevtoolsEnabled() bool { return manager != nil && manager.config.AllowDevtools }
