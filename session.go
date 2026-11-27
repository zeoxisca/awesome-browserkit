// Package browserkit 提供与 Agent 无关的本地或远程 CDP 浏览器能力。
package browserkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// SessionConfig 描述浏览器会话的启动配置。
type SessionConfig struct {
	ExecPath string
	// RemoteAddress 是 Chrome 的 HTTP 调试入口或 browser WebSocket；关闭会话只断开连接。
	RemoteAddress string
	Leakless      bool
	ForceSandbox  bool
	Proxy         string
	// ProxyBypassList 使用 Chromium 的代理绕过规则；["<-loopback>"] 让环回请求也经过代理。
	ProxyBypassList []string
	// IgnoreCertificateErrors 仅控制本地 Chromium 的证书校验，默认关闭。
	IgnoreCertificateErrors bool
	UserAgent               string
	UserDataDir             string
	CleanupUserDataDir      bool
	InsecureDomains         []string
	// DownloadDir 是本地浏览器允许下载的目录；留空时禁用下载。
	DownloadDir string
	// DownloadMaxBytes 限制每个下载的字节数；零值不限大小。
	DownloadMaxBytes int64
	downloadLimit    func() int64
	// OperationTimeout 限制每次 BrowserKit 操作的最长执行时间；省略时使用 30 秒。
	OperationTimeout time.Duration
	requestPolicy    *browserNetworkPolicy
}

// CurrentDownloadMaxBytes returns the current host download limit. Custom
// SessionFactory implementations should snapshot it when each download begins.
func (config SessionConfig) CurrentDownloadMaxBytes() int64 {
	if config.downloadLimit != nil {
		return config.downloadLimit()
	}
	return config.DownloadMaxBytes
}

// Cookie 是不含敏感值的浏览器 Cookie 元数据。
type Cookie struct {
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
	Secure bool   `json:"secure"`
}

// Session 表示一个可复用的浏览器会话。
type Session interface {
	NewPage(context.Context, PageOptions) (Page, error)
	Cookies() ([]Cookie, error)
	Close() error
	Closed() bool
}

// TabInfo 是浏览器 session 中一个网页 target 的轻量元数据。
// TargetID 供 Manager 绑定 CDP 页面使用。
type TabInfo struct {
	TargetID string
	URL      string
	Title    string
	Type     string
	OpenerID string
}

// TabSession 提供浏览器 session 的多 Tab 能力。
type TabSession interface {
	Session
	ListTabs(context.Context) ([]TabInfo, error)
	PageForTab(context.Context, string) (Page, error)
	ActivateTab(context.Context, string) error
	CloseTab(context.Context, string) error
}

// ActiveTabSession 可在不为每个 target 创建完整 Page 绑定的情况下探测激活 Tab。
// Attach 会优先使用该可选能力，且只为最终选中的 target 调用 PageForTab。
type ActiveTabSession interface {
	TabSession
	ActiveTab(context.Context, []TabInfo) (string, error)
}

// TabChangeSession 是可选能力：浏览器 target 创建、销毁或元数据变化时发出通知。
// 通知不携带 target 详情，消费方应重新枚举 Tab；通道容量为一，连续变化会合并。
type TabChangeSession interface {
	TabChanges() <-chan struct{}
}

// TitleBindingSession 是可选能力：target 导航后重新注册标题变化上报。
// CDP binding 不跨文档保留，导航会产生新的执行上下文。
type TitleBindingSession interface {
	EnsureTitleBinding(context.Context, string) error
}

// ConnectionSession 是可选能力：底层 CDP 连接失效时关闭 Done。
// Manager 据此摘除会话，避免继续返回已经失效的缓存 Tab。
type ConnectionSession interface {
	Done() <-chan struct{}
}

// ViewportTabSession 是可为已有 Tab 设置页面视口的可选能力。
type ViewportTabSession interface {
	TabSession
	SetTabViewport(context.Context, string, Viewport) error
}

// TabReleaseSession 释放已销毁 Tab 在 Browser adapter 中留下的临时状态。
// 这是可选能力，不影响只实现基础 TabSession 的宿主。
type TabReleaseSession interface {
	ReleaseTab(string)
}

type rodSession struct {
	browser           *rod.Browser
	launcher          *launcher.Launcher
	ownsLauncher      bool
	cleanup           bool
	operationTimeout  time.Duration
	cancel            context.CancelFunc
	transport         *boundedWebSocket
	attachMu          sync.Mutex
	pagesMu           sync.Mutex
	pages             map[proto.TargetTargetID]*rodPage
	closed            atomic.Bool
	closeOnce         sync.Once
	stopNetworkPolicy func()
	downloadMu        sync.Mutex
	downloadDir       string
	downloadTempDir   string
	downloadMaxBytes  int64
	downloadLimit     func() int64
	downloadGuids     map[string]*downloadRecord
	tabEventsOnce     sync.Once
	tabChanges        chan struct{}
	connectionDone    chan struct{}
	connectionOnce    sync.Once
}

const defaultOperationTimeout = 30 * time.Second

// ctx 为单次 Browser 操作创建独立的、有界生命周期 context。
// rod 的 Browser 对象只能绑定 sessionContext，不能把带 deadline 的 context
// 留在对象内部；所有实际调用都必须通过这个方法取得临时 context。
func (session *rodSession) ctx(parent context.Context) (context.Context, context.CancelFunc, error) {
	if session == nil {
		return nil, func() {}, errors.New("浏览器 session 不能为空")
	}
	return contextWithTimeout(parent, session.operationTimeout)
}

func (session *rodSession) controlCtx(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if session != nil && session.operationTimeout > 0 && session.operationTimeout < timeout {
		timeout = session.operationTimeout
	}
	return contextWithTimeout(parent, timeout)
}

// NewSession 创建本地或远程浏览器会话。下载默认被禁用。
func NewSession(ctx context.Context, config SessionConfig) (result Session, err error) {
	if ctx == nil {
		return nil, errors.New("browserkit context 不能为空")
	}
	if config.OperationTimeout < 0 {
		return nil, errors.New("Browser OperationTimeout 不能为负数")
	}
	if config.DownloadMaxBytes < 0 {
		return nil, errors.New("Browser DownloadMaxBytes 不能为负数")
	}
	if config.OperationTimeout == 0 {
		config.OperationTimeout = defaultOperationTimeout
	}
	proxy, err := parseBrowserProxy(config.Proxy)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.ExecPath) != "" && strings.TrimSpace(config.RemoteAddress) != "" {
		return nil, errors.New("ExecPath 和 RemoteAddress 不能同时设置")
	}
	address := strings.TrimSpace(config.RemoteAddress)
	if address == "" {
		address = strings.TrimSpace(config.ExecPath)
	}
	if config.DownloadDir != "" && (config.RemoteAddress != "" || isRemoteAddress(address)) {
		return nil, errors.New("Browser DownloadDir 只支持本地浏览器")
	}
	if config.DownloadDir != "" {
		resolved, resolveErr := filepath.EvalSymlinks(config.DownloadDir)
		if resolveErr != nil {
			return nil, fmt.Errorf("Browser DownloadDir 无效: %w", resolveErr)
		}
		absolute, absErr := filepath.Abs(resolved)
		if absErr != nil {
			return nil, fmt.Errorf("Browser DownloadDir 无效: %w", absErr)
		}
		info, statErr := os.Stat(absolute)
		if statErr != nil || !info.IsDir() {
			return nil, errors.New("Browser DownloadDir 必须是目录")
		}
		config.DownloadDir = absolute
	}
	var client *cdp.Client
	var transport *boundedWebSocket
	var launch *launcher.Launcher
	var ownsLauncher bool
	var profile localBrowserProfile
	sessionContext, cancel := context.WithCancel(context.Background())
	startupCtx, startupCancel := context.WithTimeout(ctx, config.OperationTimeout)
	defer startupCancel()
	if config.RemoteAddress != "" || isRemoteAddress(address) {
		var remoteURL string
		remoteURL, err = resolveCDPAddress(startupCtx, address)
		if err == nil {
			client, transport, err = startCDPClient(startupCtx, remoteURL, nil, config.OperationTimeout)
		}
	} else {
		if address == "" {
			var found bool
			address, found = launcher.LookPath()
			if !found {
				cancel()
				return nil, newError("browser_unavailable", "没有找到本地 Chrome", "配置 ExecPath 或启动远程 CDP 浏览器", true)
			}
		}
		profile, err = newLocalBrowserProfile(config)
		if err != nil {
			cancel()
			return nil, err
		}
		profile.useInstalledBrowserIdentity(startupCtx, address)
		defer func() {
			if err != nil {
				profile.cleanupFailedLaunch(launch)
			}
		}()
		// Browser 永久运行在 headless 模式；直播来自 CDP 截图，不打开宿主 GUI。
		// Launcher 只负责启动阶段；使用 startupCtx 限制等待 Chrome
		// remote-debugging URL，启动完成后由 sessionContext 管理会话生命周期。
		launch = launcher.New().Bin(address).Context(startupCtx).Leakless(config.Leakless).
			Headless(true).Proxy(proxy.server)
		profile.configureLauncher(launch)
		if config.IgnoreCertificateErrors {
			launch.Set("ignore-certificate-errors")
		}
		if len(config.ProxyBypassList) > 0 {
			launch.Set("proxy-bypass-list", strings.Join(config.ProxyBypassList, ";"))
		}
		if config.ForceSandbox {
			launch.Delete("no-sandbox")
		} else {
			launch.Set("no-sandbox")
		}
		if config.DownloadDir == "" {
			launch.Set("disable-downloads")
		}
		launch.Set("disable-background-networking").Set("disable-extensions")
		launch.Set("no-first-run").Set("no-default-browser-check").Set("disable-popup-blocking")
		if len(config.InsecureDomains) > 0 {
			launch.Set("unsafely-treat-insecure-origin-as-secure", strings.Join(config.InsecureDomains, ","))
		}
		controlURL, launchErr := launch.Launch()
		err = launchErr
		if err == nil {
			client, transport, err = startCDPClient(startupCtx, controlURL, nil, config.OperationTimeout)
		}
		ownsLauncher = true
	}
	if err != nil {
		cancel()
		if launch != nil && ownsLauncher {
			launch.Kill()
		}
		return nil, fmt.Errorf("连接浏览器: %w", err)
	}
	// Connect 需要在成功后继续持有 session 生命周期，但握手请求本身必须受
	// startupCtx 限制。AfterFunc 只在启动超时时取消；成功后停止回调，避免
	// 把已经完成的临时 deadline 留在 Browser 根 context。
	browserCtx, browserCancel := context.WithCancel(sessionContext)
	cancelConnect := context.AfterFunc(startupCtx, browserCancel)
	browser := rod.New().Client(client).NoDefaultDevice().Context(browserCtx)
	// Browser 和 Page 是跨操作复用的生命周期对象，必须绑定 sessionContext；
	// 有 deadline 的 context 只用于后续链式调用，不能留在对象内部。
	connectErr := browser.Connect()
	if !cancelConnect() {
		browserCancel()
		if connectErr == nil {
			connectErr = startupCtx.Err()
		}
	}
	if connectErr != nil {
		browserCancel()
		cancel()
		if transport != nil {
			_ = transport.Close()
		}
		if launch != nil && ownsLauncher {
			launch.Kill()
		}
		return nil, fmt.Errorf("连接浏览器: %w", connectErr)
	}
	if !ownsLauncher {
		proxy = browserProxy{}
	}
	stopNetworkPolicy, policyErr := startBrowserNetworkPolicy(sessionContext, startupCtx, browser, config.requestPolicy, proxy)
	if policyErr != nil {
		browserCancel()
		cancel()
		if transport != nil {
			_ = transport.Close()
		}
		if launch != nil && ownsLauncher {
			launch.Kill()
		}
		return nil, fmt.Errorf("启用浏览器网络策略: %w", policyErr)
	}
	// Rod 当前没有下载策略的高层包装，只能使用 CDP Browser 域；调用使用
	// startupCtx，避免初始化阶段无限等待（此处是初始化期唯一的 Browser
	// 域 proto 调用）。本地浏览器默认禁用下载。
	if ownsLauncher {
		if err := (proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorDeny}).Call(browser.Context(startupCtx)); err != nil {
			cancel()
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), config.OperationTimeout)
			_ = browser.Context(cleanupCtx).Close()
			cleanupCancel()
			if transport != nil {
				_ = transport.Close()
			}
			if launch != nil && ownsLauncher {
				launch.Kill()
			}
			return nil, fmt.Errorf("设置浏览器下载策略: %w", err)
		}
	}
	session := &rodSession{browser: browser, launcher: launch, ownsLauncher: ownsLauncher,
		cleanup: profile.cleanup, operationTimeout: config.OperationTimeout,
		cancel: cancel, transport: transport, pages: make(map[proto.TargetTargetID]*rodPage), stopNetworkPolicy: stopNetworkPolicy,
		connectionDone: make(chan struct{})}
	if config.DownloadDir != "" {
		session.downloadDir = config.DownloadDir
		session.downloadMaxBytes = config.DownloadMaxBytes
		session.downloadLimit = config.CurrentDownloadMaxBytes
		session.downloadGuids = make(map[string]*downloadRecord)
		session.downloadTempDir, err = os.MkdirTemp(config.DownloadDir, ".browserkit-")
		if err != nil {
			_ = session.Close()
			return nil, fmt.Errorf("创建下载临时目录: %w", err)
		}
		session.startDownloadWatcher()
		if err = setDownloadBehavior(startupCtx, browser, proto.BrowserSetDownloadBehaviorBehaviorAllowAndName, session.downloadTempDir); err != nil {
			_ = session.Close()
			return nil, fmt.Errorf("启用浏览器下载: %w", err)
		}
	}
	if transport != nil {
		// 下载状态初始化完成后才观察断线，避免关闭与初始化并发访问。
		go func() {
			<-transport.Done()
			session.markClosed()
		}()
	}
	return session, nil
}

func isRemoteAddress(address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	return strings.HasPrefix(address, "ws://") || strings.HasPrefix(address, "wss://") || strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://")
}

// NewPage 创建一个新页面。
func (session *rodSession) NewPage(ctx context.Context, options PageOptions) (Page, error) {
	if session == nil || session.closed.Load() {
		return nil, newError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen 创建 session", true)
	}
	if ctx == nil {
		return nil, errors.New("browserkit page context 不能为空")
	}
	operationCtx, operationCancel, err := session.ctx(ctx)
	if err != nil {
		return nil, err
	}
	defer operationCancel()
	// 先用有界 context 创建 target，再用持久 Browser context 绑定 Page。
	// 不能直接用 browser.Context(operationCtx).Page：Rod 的 Page.root 会保留
	// 这个临时 context，operationCancel 返回后页面后续操作会全部收到
	// context canceled。
	// Rod 的 Browser.Page 会把同一个临时 context 继续放进 Page.root，无法
	// 同时满足“创建调用有超时”和“页面生命周期持久”两个条件。因此只用
	// Target.createTarget（Rod 没有拆开的高层包装）创建 target，再交给
	// Browser.PageFromTarget 绑定持久 Page。
	target, err := (proto.TargetCreateTarget{URL: "about:blank"}).Call(session.browser.Context(operationCtx))
	if err != nil {
		return nil, fmt.Errorf("创建浏览器页面: %w", err)
	}
	page, pageCancel, err := session.pageFromTarget(operationCtx, target.TargetID)
	if err != nil {
		// 绑定失败时回收已创建 target；这是与 Rod Browser.Page 相同的
		// 防泄漏语义，但关闭请求本身也必须受 operationCtx 限制。
		_, _ = (proto.TargetCloseTarget{TargetID: target.TargetID}).Call(session.browser.Context(operationCtx))
		return nil, fmt.Errorf("绑定浏览器页面: %w", err)
	}
	result := &rodPage{page: page, refs: make(map[string]struct{}), operationTimeout: session.operationTimeout, cancel: pageCancel, owner: session}
	result.startDialogWatcher()
	result.registerTitleBindingAsync()
	if options.Viewport.Width != 0 || options.Viewport.Height != 0 {
		if err := options.Viewport.Validate(); err != nil {
			_ = result.Close(operationCtx)
			return nil, err
		}
		if err := page.Context(operationCtx).SetViewport(viewportParams(options.Viewport)); err != nil {
			_ = result.Close(operationCtx)
			return nil, fmt.Errorf("设置浏览器视口: %w", err)
		}
	}
	// 状态观察器必须注册到每个新 document；只在 about:blank 上执行一次会在
	// 首次导航后丢失文档身份，导致旧 ref 无法可靠区分为 stale。
	if err := result.AddScriptToEvaluateOnNewDocument(ctx, pageStateObserverScript); err != nil {
		_ = result.Close(operationCtx)
		return nil, fmt.Errorf("注册页面状态观察器: %w", err)
	}
	if strings.TrimSpace(options.InjectScript) != "" {
		if err := result.AddScriptToEvaluateOnNewDocument(ctx, options.InjectScript); err != nil {
			_ = result.Close(operationCtx)
			return nil, fmt.Errorf("注册 document 注入脚本: %w", err)
		}
	}
	result.stateMu.Lock()
	if _, err := page.Context(operationCtx).Eval(pageStateObserverEval); err != nil {
		result.stateMu.Unlock()
		_ = result.Close(operationCtx)
		return nil, fmt.Errorf("注册页面状态观察器: %w", err)
	}
	result.stateMu.Unlock()
	session.rememberPage(result)
	return result, nil
}

// pageFromTarget 只在 attach 阶段继承单次操作的取消信号；绑定成功后 Page
// 继续继承 session 生命周期。这样既不会把临时 deadline 留在 Rod Page.root，
// 也不会让 Target.attachToTarget 使用永久 context。
func (session *rodSession) pageFromTarget(operationCtx context.Context, targetID proto.TargetTargetID) (*rod.Page, context.CancelFunc, error) {
	// Rod 会在发出 Target.attachToTarget 前获取不可取消的 targetsLock。
	// BrowserKit 所有 PageFromTarget 入口先经过这把可取消锁，保证同一时刻
	// 最多只有一个调用进入 Rod；等待者在自己的 operationCtx 到期时直接
	// 返回，不会继续堆积在 Rod 的全局锁里。
	if err := lockInteraction(operationCtx, &session.attachMu); err != nil {
		return nil, func() {}, err
	}
	defer session.attachMu.Unlock()
	pageCtx, pageCancel := context.WithCancel(session.browser.GetContext())
	cancelOnTimeout := context.AfterFunc(operationCtx, pageCancel)
	page, err := session.browser.Context(pageCtx).PageFromTarget(targetID)
	if !cancelOnTimeout() {
		pageCancel()
		if contextErr := operationCtx.Err(); contextErr != nil {
			err = contextErr
		}
	}
	if err != nil {
		pageCancel()
		session.browser.RemoveState(targetID)
		return nil, func() {}, err
	}
	return page, pageCancel, nil
}

func (session *rodSession) rememberPage(page *rodPage) {
	if page == nil || page.page == nil {
		return
	}
	session.pagesMu.Lock()
	session.pages[page.page.TargetID] = page
	session.pagesMu.Unlock()
}

func (session *rodSession) knownPage(targetID proto.TargetTargetID) *rodPage {
	session.pagesMu.Lock()
	defer session.pagesMu.Unlock()
	return session.pages[targetID]
}

func (session *rodSession) forgetPage(page *rodPage) {
	if session == nil || page == nil || page.page == nil {
		return
	}
	targetID := page.page.TargetID
	session.pagesMu.Lock()
	if session.pages[targetID] == page {
		delete(session.pages, targetID)
		session.browser.RemoveState(targetID)
	}
	session.pagesMu.Unlock()
}

// connectionUsable 检查 CDP 传输是否仍然可用；断开时立即标记会话关闭。
func (session *rodSession) connectionUsable() bool {
	if session == nil || session.transport == nil {
		return true
	}
	select {
	case <-session.transport.Done():
		session.markClosed()
		return false
	default:
		return true
	}
}

// ListTabs 返回当前浏览器中的网页 target。非 page target（扩展、iframe、worker 等）会被过滤。
func (session *rodSession) ListTabs(ctx context.Context) ([]TabInfo, error) {
	if session == nil || session.closed.Load() || session.browser == nil || !session.connectionUsable() {
		return nil, newError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen 创建 session", true)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	operationCtx, operationCancel, err := session.ctx(ctx)
	if err != nil {
		return nil, err
	}
	defer operationCancel()
	// Rod 没有列出全部 target 的高层 API；这里保留 Target.getTargets，并显式
	// 通过 session.ctx 注入单次操作超时。
	result, err := (proto.TargetGetTargets{}).Call(session.browser.Context(operationCtx))
	if err != nil {
		return nil, err
	}
	tabs := make([]TabInfo, 0, len(result.TargetInfos))
	for _, target := range result.TargetInfos {
		if target.Type != proto.TargetTargetInfoTypePage {
			continue
		}
		tabs = append(tabs, TabInfo{TargetID: string(target.TargetID), URL: target.URL, Title: target.Title, Type: string(target.Type), OpenerID: string(target.OpenerID)})
	}
	return tabs, nil
}

// TabChanges 启动一次浏览器级 target 事件监听，并返回合并后的变化通知。
// 监听覆盖新开、关闭和标题/URL 变化；调用方只需在收到通知后重新枚举 Tab。
func (session *rodSession) TabChanges() <-chan struct{} {
	if session == nil {
		return nil
	}
	session.tabEventsOnce.Do(func() {
		session.tabChanges = make(chan struct{}, 1)
		if session.browser == nil {
			return
		}
		notify := func() {
			select {
			case session.tabChanges <- struct{}{}:
			default:
			}
		}
		wait := session.browser.EachEvent(
			func(*proto.TargetTargetCreated) { notify() },
			func(*proto.TargetTargetDestroyed) { notify() },
			func(*proto.TargetTargetInfoChanged) { notify() },
			func(event *proto.RuntimeBindingCalled) {
				if event.Name == titleBindingName {
					notify()
				}
			},
		)
		go wait()
	})
	return session.tabChanges
}

// registerTitleBinding 在页面 session 中注册标题变化上报入口。
// 失败只影响标题推送的及时性，不能阻塞页面创建。
func registerTitleBinding(ctx context.Context, page *rod.Page) {
	if page == nil {
		return
	}
	_ = (proto.RuntimeAddBinding{Name: titleBindingName}).Call(page.Context(ctx))
}

// EnsureTitleBinding 在 target 导航后重新注册标题变化上报。
func (session *rodSession) EnsureTitleBinding(ctx context.Context, targetID string) error {
	if session == nil || session.closed.Load() || session.browser == nil {
		return nil
	}
	operationCtx, operationCancel, err := session.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	page := session.knownPage(proto.TargetTargetID(targetID))
	if page == nil || page.page == nil {
		return nil
	}
	return (proto.RuntimeAddBinding{Name: titleBindingName}).Call(page.page.Context(operationCtx))
}

// ActiveTab 使用短生命周期 CDP session 探测页面焦点；探测完成后立即 detach，
// 避免 PageForTab 为所有候选页安装观察器、初始化输入设备并长期缓存 Page。
func (session *rodSession) ActiveTab(ctx context.Context, listed []TabInfo) (string, error) {
	if session == nil || session.closed.Load() || session.browser == nil {
		return "", newError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen 创建 session", true)
	}
	if err := contextErr(ctx); err != nil {
		return "", err
	}
	operationCtx, operationCancel, err := session.ctx(ctx)
	if err != nil {
		return "", err
	}
	defer operationCancel()
	if err := lockInteraction(operationCtx, &session.attachMu); err != nil {
		return "", err
	}
	defer session.attachMu.Unlock()

	visibleTarget := ""
	visibleCount := 0
	for _, tab := range listed {
		if tab.Type != "page" {
			continue
		}
		state, err := session.probeTabState(operationCtx, proto.TargetTargetID(tab.TargetID))
		if err != nil {
			return "", fmt.Errorf("读取 Tab 状态: %w", err)
		}
		if state.Focused {
			return tab.TargetID, nil
		}
		if state.Visible {
			visibleTarget = tab.TargetID
			visibleCount++
		}
	}
	if visibleCount == 1 {
		return visibleTarget, nil
	}
	return "", errors.New("无法唯一确定激活 Tab；请切回目标网页并保持浏览器焦点后重试")
}

type tabFocusState struct {
	Focused bool `json:"focused"`
	Visible bool `json:"visible"`
}

func (session *rodSession) probeTabState(ctx context.Context, targetID proto.TargetTargetID) (state tabFocusState, err error) {
	attached, err := (proto.TargetAttachToTarget{TargetID: targetID, Flatten: true}).Call(session.browser.Context(ctx))
	if err != nil {
		return tabFocusState{}, err
	}
	defer func() {
		cleanupCtx, cleanupCancel, cleanupErr := session.controlCtx(session.browser.GetContext(), 5*time.Second)
		if cleanupErr == nil {
			cleanupErr = (proto.TargetDetachFromTarget{SessionID: attached.SessionID}).Call(session.browser.Context(cleanupCtx))
			cleanupCancel()
		}
		if err == nil && cleanupErr != nil {
			err = fmt.Errorf("释放 Tab 探测 session: %w", cleanupErr)
		}
	}()
	probe := cdpSessionClient{ctx: ctx, client: session.browser, sessionID: attached.SessionID}
	result, err := (proto.RuntimeEvaluate{
		Expression:    `({focused: document.hasFocus(), visible: document.visibilityState === "visible"})`,
		ReturnByValue: true,
	}).Call(probe)
	if err != nil {
		return tabFocusState{}, err
	}
	if result.ExceptionDetails != nil || result.Result == nil {
		return tabFocusState{}, errors.New("页面未返回激活状态")
	}
	if err := result.Result.Value.Unmarshal(&state); err != nil {
		return tabFocusState{}, fmt.Errorf("解析激活页状态: %w", err)
	}
	return state, nil
}

// PageForTab 绑定一个已经存在的网页 target，并安装与 NewPage 相同的页面状态观察器。
func (session *rodSession) PageForTab(ctx context.Context, targetID string) (Page, error) {
	if session == nil || session.closed.Load() || session.browser == nil {
		return nil, newError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen 创建 session", true)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(targetID) == "" {
		return nil, errors.New("浏览器 target id 不能为空")
	}
	operationCtx, operationCancel, err := session.ctx(ctx)
	if err != nil {
		return nil, err
	}
	defer operationCancel()
	if existing := session.knownPage(proto.TargetTargetID(targetID)); existing != nil {
		return existing, nil
	}
	// PageFromTarget 返回的 Page 会把 Browser 的 context 作为 root 生命周期；
	// 这里必须用持久 Browser 绑定，随后所有实际操作再通过 page.ctx 限时。
	page, pageCancel, err := session.pageFromTarget(operationCtx, proto.TargetTargetID(targetID))
	if err != nil {
		return nil, err
	}
	result := &rodPage{page: page, refs: make(map[string]struct{}), operationTimeout: session.operationTimeout, cancel: pageCancel, owner: session}
	result.startDialogWatcher()
	result.registerTitleBindingAsync()
	if err := result.AddScriptToEvaluateOnNewDocument(ctx, pageStateObserverScript); err != nil {
		result.release()
		return nil, fmt.Errorf("注册页面状态观察器: %w", err)
	}
	result.stateMu.Lock()
	if _, err := page.Context(operationCtx).Eval(pageStateObserverEval); err != nil {
		result.stateMu.Unlock()
		result.release()
		return nil, fmt.Errorf("注册页面状态观察器: %w", err)
	}
	result.stateMu.Unlock()
	session.rememberPage(result)
	return result, nil
}

// ActivateTab 将网页 target 置为浏览器的激活目标。
func (session *rodSession) ActivateTab(ctx context.Context, targetID string) error {
	if session == nil || session.closed.Load() || session.browser == nil {
		return newError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen 创建 session", true)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	operationCtx, operationCancel, err := session.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	return (proto.TargetActivateTarget{TargetID: proto.TargetTargetID(targetID)}).Call(session.browser.Context(operationCtx))
}

// SetTabViewport 为已有网页 target 应用与新建页面一致的视口尺寸。
func (session *rodSession) SetTabViewport(ctx context.Context, targetID string, viewport Viewport) error {
	if session == nil || session.closed.Load() || session.browser == nil {
		return newError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen 创建 session", true)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := viewport.Validate(); err != nil {
		return err
	}
	operationCtx, operationCancel, err := session.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	page := session.knownPage(proto.TargetTargetID(targetID))
	if page == nil {
		bound, bindErr := session.PageForTab(operationCtx, targetID)
		if bindErr != nil {
			return bindErr
		}
		page, _ = bound.(*rodPage)
	}
	if page == nil || page.page == nil {
		return errors.New("浏览器 target 尚未绑定")
	}
	return page.page.Context(operationCtx).SetViewport(viewportParams(viewport))
}

func viewportParams(viewport Viewport) *proto.EmulationSetDeviceMetricsOverride {
	deviceScaleFactor := float64(1)
	scale := float64(1)
	screenWidth, screenHeight := viewport.Width, viewport.Height
	return &proto.EmulationSetDeviceMetricsOverride{
		Width: viewport.Width, Height: viewport.Height,
		DeviceScaleFactor: deviceScaleFactor, Mobile: false, Scale: &scale,
		ScreenWidth: &screenWidth, ScreenHeight: &screenHeight,
	}
}

// CloseTab 关闭指定网页 target，不影响同一浏览器 session 的其他 Tab。
func (session *rodSession) CloseTab(ctx context.Context, targetID string) error {
	if session == nil || session.closed.Load() || session.browser == nil || !session.connectionUsable() {
		return newError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen 创建 session", true)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	operationCtx, operationCancel, err := session.controlCtx(ctx, targetCloseTimeout)
	if err != nil {
		return err
	}
	defer operationCancel()
	// 直接发送单次 Target.closeTarget。Rod Page.Close 会持有不可取消的
	// targetsLock 等待 targetDestroyed，可能让所有后续 target 操作一起卡住。
	if _, err := (proto.TargetCloseTarget{TargetID: proto.TargetTargetID(targetID)}).Call(session.browser.Context(operationCtx)); err != nil {
		return err
	}
	session.ReleaseTab(targetID)
	return nil
}

// ReleaseTab 删除 rod 为 target 保存的 Page/cache 状态。targetDestroyed 事件
// 会让页面 context 退出，但不会自动删除 Browser 的 target cache。
func (session *rodSession) ReleaseTab(targetID string) {
	if session == nil || session.browser == nil || strings.TrimSpace(targetID) == "" {
		return
	}
	id := proto.TargetTargetID(targetID)
	session.pagesMu.Lock()
	page := session.pages[id]
	session.pagesMu.Unlock()
	if page != nil {
		page.release()
		return
	}
	session.browser.RemoveState(id)
}

func (session *rodSession) Cookies() ([]Cookie, error) {
	if session == nil || session.closed.Load() || session.browser == nil {
		return nil, newError("session_closed", "浏览器 session 已关闭", "重新调用 browserOpen 创建 session", true)
	}
	operationCtx, operationCancel, err := session.ctx(session.browser.GetContext())
	if err != nil {
		return nil, err
	}
	defer operationCancel()
	cookies, err := session.browser.Context(operationCtx).GetCookies()
	if err != nil {
		return nil, err
	}
	result := make([]Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie != nil {
			result = append(result, Cookie{Name: cookie.Name, Domain: cookie.Domain, Path: cookie.Path, Secure: cookie.Secure})
		}
	}
	return result, nil
}

// Done 在 CDP 连接失效或会话关闭时关闭。
func (session *rodSession) Done() <-chan struct{} {
	if session == nil {
		return nil
	}
	return session.connectionDone
}

// markClosed 标记连接已经不可用，并唤醒 Manager 的失效回收。
func (session *rodSession) markClosed() {
	if session == nil {
		return
	}
	session.beginClose()
	session.connectionOnce.Do(func() { close(session.connectionDone) })
	if session.cancel != nil {
		session.cancel()
	}
}

func (session *rodSession) Close() error {
	if session == nil {
		return nil
	}
	session.closeOnce.Do(func() {
		session.markClosed()
		if session.stopNetworkPolicy != nil {
			session.stopNetworkPolicy()
		}
		// 先取消所有页面/事件循环使用的 session context，让并发中的
		// screencast、WaitIdle 和 CDP 请求尽快退出；仅自建浏览器发送 Browser.close。
		// 否则关闭阶段可能与仍在运行的 target 操作互相等待。
		if session.cancel != nil {
			session.cancel()
		}
		if session.browser != nil && session.ownsLauncher {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = session.browser.Context(closeCtx).Close()
			cancel()
		}
		if session.transport != nil {
			_ = session.transport.Close()
		}
		if session.ownsLauncher && session.launcher != nil {
			session.launcher.Kill()
			if session.cleanup {
				session.launcher.Cleanup()
			}
		}
		if session.downloadTempDir != "" {
			_ = os.RemoveAll(session.downloadTempDir)
		}
	})
	return nil
}

func (session *rodSession) Closed() bool { return session == nil || session.closed.Load() }
