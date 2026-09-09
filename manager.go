// Manager 在普通 Go 程序中维护可复用的浏览器运行时。
package browserkit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Manager 保存一份独立浏览器运行时的 session。
type Manager struct {
	control           *controlManager
	config            Config
	allowedOrigins    map[string]struct{}
	mu                sync.Mutex
	sessions          map[string]*session
	closed            map[string]struct{}
	active            string
	stopped           bool
	opening           int
	openDone          chan struct{}
	lifecycleCtx      context.Context
	lifecycleCancel   context.CancelFunc
	reclaimDone       chan struct{}
	liveMu            sync.Mutex
	live              *liveHub
	idleCheckInterval time.Duration
	sequence          atomic.Uint64
	stateMu           sync.Mutex
	stateSubs         map[chan struct{}]struct{}
	devtoolsStateMu   sync.Mutex
	devtoolsStateSubs map[chan struct{}]struct{}
}

var openIdleTimeout = 5 * time.Second
var tabReconcileTimeout = 2 * time.Second
var tabReconcileInterval = 2 * time.Second
var liveFirstFrameTimeout = 2 * time.Second
var defaultSessionIdleTimeout = 30 * time.Minute
var sessionIdleCheckInterval = time.Minute
var defaultBrowserViewport = Viewport{Width: 1920, Height: 1080}

const liveInputQueueSize = 64

const liveInputDispatchTimeout = time.Second

type liveInput struct {
	viewerID string
	page     *page
	event    InputEvent
	epoch    uint64
}

type session struct {
	id        string
	browser   Session
	tabs      map[string]*page
	tabOrder  []string
	activeTab string
	mu        sync.Mutex
	closed    bool
	createdAt time.Time
	lastUsed  time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	// interactionMu 只串行用户可见交互；状态查询、导航、截图和 Tab
	// 枚举不应被它或 session.mu 阻塞。
	interactionMu sync.Mutex
	reconcileMu   sync.Mutex
	tabMu         sync.Mutex
	inputQueue    chan liveInput
	inputEpoch    uint64
	inputCancel   context.CancelFunc
	// tabsSignature/tabsRevision 是 session 级 Tab 状态投影。revision 只在
	// active、Tab 集合或 Tab 的地址/标题/状态/视口实际变化后递增，重复读取保持稳定。
	tabsSignature       string
	tabsRevision        uint64
	snapshotRevision    uint64
	snapshotInitialized bool
}

type page struct {
	id          string
	targetID    string
	openerTabID string
	page        Page
	viewport    Viewport
	info        PageInfo
	status      string
	// refs 只记录本页面快照返回的 ref，作为 manager 层的归属校验，
	// 防止不同 Tab 的 Page 实现意外接受同名 ref。
	refs               map[string]struct{}
	refRevision        uint64
	injectedScripts    map[string]struct{}
	injectMu           sync.Mutex
	frameMu            sync.Mutex
	lastFrame          *LiveFrame
	devtoolsMu         sync.Mutex
	devtools           *devtoolsCapture
	devtoolsGeneration uint64
}

// NewManager 创建一个独立浏览器管理器。
func NewManager(config Config) (*Manager, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	manager := &Manager{
		config:            config,
		allowedOrigins:    make(map[string]struct{}, len(config.AllowedOrigins)),
		sessions:          make(map[string]*session),
		closed:            make(map[string]struct{}),
		lifecycleCtx:      lifecycleCtx,
		lifecycleCancel:   lifecycleCancel,
		reclaimDone:       make(chan struct{}),
		idleCheckInterval: sessionIdleCheckInterval,
		stateSubs:         make(map[chan struct{}]struct{}),
		devtoolsStateSubs: make(map[chan struct{}]struct{}),
	}
	for _, origin := range config.AllowedOrigins {
		key, err := allowedOriginKey(origin)
		if err != nil {
			lifecycleCancel()
			return nil, err
		}
		manager.allowedOrigins[key] = struct{}{}
	}
	manager.openDone = make(chan struct{})
	close(manager.openDone)
	go manager.reclaimIdleSessions()
	return manager, nil
}

// Open 创建或复用当前 Manager 的浏览器会话。
func (manager *Manager) Open(ctx context.Context, request OpenRequest) (OpenResponse, error) {
	if manager == nil {
		return OpenResponse{}, browserError("browser_unavailable", "Browser Manager 不存在", "调用 NewManager 创建浏览器运行时", true)
	}
	if err := contextErr(ctx); err != nil {
		return OpenResponse{}, err
	}
	if err := manager.validateOpen(request); err != nil {
		return OpenResponse{}, err
	}
	if request.SessionID != "" {
		return manager.openExisting(ctx, request)
	}
	return manager.openNew(ctx, request)
}

func (manager *Manager) validateOpen(request OpenRequest) error {
	hasScript := strings.TrimSpace(request.InjectJS) != ""
	if hasScript && !manager.config.AllowEvaluate {
		return browserError("javascript_blocked", "宿主未启用 Browser JavaScript 能力", "在 Browser 配置中显式设置 allow_evaluate", true)
	}
	if hasScript && len(request.InjectJS) > manager.config.MaxScriptBytes {
		return browserError("inject_script_too_large", "document 注入脚本超过大小限制", "缩短 inject_js 后重试", true)
	}
	if request.SessionID == "" {
		if err := manager.validateAddress(request.URL, ""); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) openExisting(ctx context.Context, request OpenRequest) (OpenResponse, error) {
	current, currentPage, err := manager.get(ctx, request.SessionID, "")
	if err != nil {
		return OpenResponse{}, err
	}
	info, err := currentPage.page.Info(ctx)
	if err != nil {
		return OpenResponse{}, openFailed(err)
	}
	current.mu.Lock()
	currentPage.info = info
	current.mu.Unlock()
	loadStatus := OpenLoadLoaded
	if request.URL != "" {
		if err := manager.validateAddress(request.URL, info.URL); err != nil {
			return OpenResponse{}, err
		}
	}
	if strings.TrimSpace(request.InjectJS) != "" {
		if err := manager.applyInjectScript(ctx, currentPage, request.InjectJS); err != nil {
			return OpenResponse{}, openFailed(err)
		}
	}
	if request.URL != "" {
		target := resolveURL(request.URL, info.URL)
		if _, err = currentPage.page.Navigate(ctx, target); err != nil {
			return OpenResponse{}, openFailed(err)
		}
		current.mu.Lock()
		clearPageRefs(currentPage)
		current.mu.Unlock()
		loadStatus, err = pageLoadStatus(ctx, currentPage.page)
		if err != nil {
			return OpenResponse{}, openFailed(err)
		}
		info, err = currentPage.page.Info(ctx)
		if err != nil {
			return OpenResponse{}, openFailed(err)
		}
		current.mu.Lock()
		currentPage.info = info
		current.mu.Unlock()
	}
	return OpenResponse{SessionID: current.id, PageID: currentPage.id, URL: info.URL, Title: info.Title, LoadStatus: loadStatus}, nil
}

// applyInjectScript 允许复用已有 session 时继续使用 inject_js：脚本只向
// 后续 document 注册一次，并立即在当前 document 执行一次，避免 调用方因
// 重载或诊断而重复 browserOpen 时被旧页面脚本策略阻断。
func (manager *Manager) applyInjectScript(ctx context.Context, currentPage *page, script string) error {
	if currentPage == nil || strings.TrimSpace(script) == "" {
		return nil
	}
	// 同一个 page 可能同时收到多个 browserOpen（例如 调用方重试和宿主
	// 重放）。整个“检查、注册、执行”过程必须串行，否则两个请求都会
	// 观察到未注册并重复调用 CDP 的 document 注入接口。
	currentPage.injectMu.Lock()
	defer currentPage.injectMu.Unlock()
	if currentPage.injectedScripts == nil {
		currentPage.injectedScripts = make(map[string]struct{})
	}
	_, registered := currentPage.injectedScripts[script]
	if !registered {
		if err := currentPage.page.AddScriptToEvaluateOnNewDocument(ctx, script); err != nil {
			return err
		}
		currentPage.injectedScripts[script] = struct{}{}
	}
	_, err := currentPage.page.Evaluate(ctx, script, EvaluateOptions{AwaitPromise: false})
	return err
}

func (manager *Manager) openNew(ctx context.Context, request OpenRequest) (OpenResponse, error) {
	if err := manager.beginOpening(); err != nil {
		return OpenResponse{}, err
	}
	defer manager.finishOpening()
	created, createdPage, loadStatus, err := manager.createSession(ctx, request, false)
	if err != nil {
		return OpenResponse{}, openFailed(err)
	}
	if err := manager.addSession(created); err != nil {
		_ = manager.closeSession(context.Background(), created)
		return OpenResponse{}, err
	}
	// 新 session 此时只有刚创建的 page；不要在 browserOpen 的返回路径上
	// 再同步枚举一次 CDP target。Tab 感知由后续 browserSnapshot/browserTabs
	// 的有界 reconcile 负责，避免 Chrome target 锁让页面已打开的 Open 长时间
	// 不返回。
	manager.stopLive()
	manager.emit(ctx, Event{Kind: "session_opened", SessionID: created.id, PageID: createdPage.id})
	response, err := manager.finishNewPage(ctx, created, createdPage, request.URL, loadStatus)
	if err != nil {
		manager.removeSession(created)
		return OpenResponse{}, openFailed(err)
	}
	return response, nil
}

func (manager *Manager) beginOpening() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.stopped {
		return browserError("browser_closed", "Browser Manager 已关闭", "调用 NewManager 重新创建浏览器运行时", true)
	}
	if len(manager.sessions)+manager.opening >= manager.config.MaxSessions {
		return browserError("session_limit", "浏览器 session 数量已达到上限", "关闭不用的浏览器 session", true)
	}
	if manager.opening == 0 {
		manager.openDone = make(chan struct{})
	}
	manager.opening++
	return nil
}

func (manager *Manager) createSession(ctx context.Context, request OpenRequest, attach bool) (*session, *page, OpenLoadStatus, error) {
	browser, err := manager.config.Factory(ctx, manager.config.Session)
	if err != nil {
		return nil, nil, "", err
	}
	if browser == nil {
		return nil, nil, "", errors.New("浏览器 Factory 返回空 session")
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	created := &session{
		id: manager.newID("bsess"), browser: browser,
		tabs: make(map[string]*page), createdAt: time.Now(), lastUsed: time.Now(),
		ctx: sessionCtx, cancel: sessionCancel, inputQueue: make(chan liveInput, liveInputQueueSize), inputEpoch: 1,
	}
	viewport := request.Viewport
	if viewport.Width <= 0 || viewport.Height <= 0 {
		viewport = defaultBrowserViewport
	}
	var pageValue Page
	if attach {
		viewport = Viewport{}
		pageValue, err = attachPage(ctx, browser)
		if err == nil {
			var info PageInfo
			info, err = pageValue.Info(ctx)
			if err == nil {
				err = manager.validateAddress(info.URL, "")
			}
		}
	} else {
		pageValue, err = browser.NewPage(ctx, PageOptions{Viewport: viewport, InjectScript: request.InjectJS})
	}
	if err != nil {
		sessionCancel()
		_ = browser.Close()
		return nil, nil, "", err
	}
	createdPage := manager.registerPage(created, pageValue, viewport, "")
	if strings.TrimSpace(request.InjectJS) != "" {
		createdPage.injectedScripts[request.InjectJS] = struct{}{}
	}
	if err := manager.autoStartPageDevtools(created, createdPage); err != nil {
		sessionCancel()
		_ = browser.Close()
		return nil, nil, "", err
	}
	go manager.dispatchLiveInputs(created)
	loadStatus := OpenLoadLoaded
	if request.URL == "" && !attach {
		loadStatus, err = pageLoadStatus(ctx, createdPage.page)
		if err != nil {
			stopPageDevtools(createdPage)
			sessionCancel()
			_ = browser.Close()
			return nil, nil, "", err
		}
	}
	return created, createdPage, loadStatus, nil
}

func (manager *Manager) addSession(created *session) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.stopped {
		return browserError("browser_closed", "Browser Manager 已关闭", "调用 NewManager 重新创建浏览器运行时", true)
	}
	manager.sessions[created.id] = created
	manager.active = created.id
	return nil
}

func (manager *Manager) finishNewPage(ctx context.Context, current *session, currentPage *page, address string, loadStatus OpenLoadStatus) (OpenResponse, error) {
	if address == "" {
		return OpenResponse{SessionID: current.id, PageID: currentPage.id, URL: "about:blank", LoadStatus: loadStatus}, nil
	}
	if _, err := currentPage.page.Navigate(ctx, address); err != nil {
		return OpenResponse{}, err
	}
	loadStatus, err := pageLoadStatus(ctx, currentPage.page)
	if err != nil {
		return OpenResponse{}, err
	}
	info, err := currentPage.page.Info(ctx)
	if err != nil {
		return OpenResponse{}, err
	}
	current.mu.Lock()
	currentPage.info = info
	currentPage.status = "ready"
	current.mu.Unlock()
	manager.emit(ctx, Event{Kind: "navigation_finished", SessionID: current.id, PageID: currentPage.id, URL: info.URL})
	return OpenResponse{SessionID: current.id, PageID: currentPage.id, URL: info.URL, Title: info.Title, LoadStatus: loadStatus}, nil
}

func resolveURL(address, base string) string {
	parsed, err := url.Parse(address)
	if err != nil || parsed.IsAbs() {
		return address
	}
	origin, err := url.Parse(base)
	if err != nil {
		return address
	}
	return origin.ResolveReference(parsed).String()
}

func pageLoadStatus(ctx context.Context, page Page) (OpenLoadStatus, error) {
	timedOut, err := waitPageIdle(ctx, page)
	if err != nil {
		return "", err
	}
	if timedOut {
		return OpenLoadTimeout, nil
	}
	return OpenLoadLoaded, nil
}
func waitPageIdle(ctx context.Context, page Page) (bool, error) {
	idleCtx, cancel := context.WithTimeout(ctx, openIdleTimeout)
	defer cancel()
	err := page.WaitIdle(idleCtx, openIdleTimeout)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return true, nil
	}
	return false, err
}

func openFailed(err error) error {
	if err == nil {
		return nil
	}
	return browserError("open_failed", "浏览器页面打开失败: "+err.Error(), "检查 Chrome/CDP 连接后重试；如果页面已打开，不要重复调用 browserOpen", true)
}

func (manager *Manager) newID(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), manager.sequence.Add(1))
}

// lockSessionInteraction 串行一次用户可见交互。返回的 unlock 可重复调用，
// 便于工具在错误路径 defer，并在 Tab reconcile 前提前释放。
func lockSessionInteraction(ctx context.Context, current *session) (func(), error) {
	if current == nil {
		return nil, errors.New("浏览器 session 不存在")
	}
	if err := lockInteraction(ctx, &current.interactionMu); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(current.interactionMu.Unlock) }, nil
}

// invalidateInputsLocked 使切页前已接收但尚未完成的直播输入立即失效。
// 调用方必须持有 session.mu。
func invalidateInputsLocked(current *session) {
	current.inputEpoch++
	if current.inputCancel != nil {
		current.inputCancel()
		current.inputCancel = nil
	}
}

func (manager *Manager) emit(ctx context.Context, event Event) {
	manager.stateMu.Lock()
	for subscriber := range manager.stateSubs {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
	manager.stateMu.Unlock()
	if manager.config.EventSink != nil {
		event.At = time.Now()
		manager.config.EventSink.OnBrowserEvent(ctx, event)
	}
}

func (manager *Manager) validateAddress(address, base string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return browserError("navigation_blocked", "目标地址不是合法 URL", "使用 http 或 https 地址", true)
	}
	if !parsed.IsAbs() {
		if base == "" {
			return browserError("navigation_blocked", "相对 URL 缺少当前页面地址", "使用绝对 http/https URL", true)
		}
		origin, parseErr := url.Parse(base)
		if parseErr != nil {
			return parseErr
		}
		parsed = origin.ResolveReference(parsed)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return browserError("navigation_blocked", "只允许 http/https 地址", "改用 http 或 https URL", true)
	}
	if !manager.config.AllowPrivateNetwork && isPrivateHost(parsed.Hostname()) {
		return browserError("navigation_blocked", "目标地址属于本机或私有网络，当前策略拒绝访问", "由宿主显式允许私网访问后重试", true)
	}
	if len(manager.allowedOrigins) > 0 {
		origin, originErr := webOriginKey(parsed)
		if originErr != nil {
			return browserError("navigation_blocked", "目标地址不是合法 HTTP/HTTPS URL", "使用完整的 http 或 https 地址", true)
		}
		if _, allowed := manager.allowedOrigins[origin]; !allowed {
			return browserError("navigation_blocked", "目标地址不在允许的 origin 范围内", "请求宿主批准该 origin", true)
		}
	}
	return nil
}

func isPrivateHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

func browserError(kind, message, next string, recoverable bool) error {
	return &Error{Kind: kind, Message: message, RecommendedNextStep: next, Recoverable: recoverable}
}

func (manager *Manager) screenshotPath(sessionID, pageID string, sequence uint64) (string, error) {
	return screenshotPath(manager.config.ScreenshotDir, sessionID, pageID, sequence)
}
