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
	timing            timing
	sequence          atomic.Uint64
	downloadMaxBytes  atomic.Int64
	// interactiveUntil 在用户输入后短暂放开直播帧间隔，优先保证跟手；
	// 停顿后自然回到档位帧率。interactiveWake 用于立即唤醒图流发布。
	interactiveUntil  atomic.Int64
	interactiveWake   chan struct{}
	stateMu           sync.Mutex
	stateSubs         map[chan struct{}]struct{}
	devtoolsStateMu   sync.Mutex
	devtoolsStateSubs map[chan struct{}]struct{}
	replayMu          sync.Mutex
	replays           map[string]*sequenceReplayState
}

type timing struct {
	openIdle             time.Duration
	tabReconcileTimeout  time.Duration
	tabReconcileInterval time.Duration
	liveFirstFrame       time.Duration
	sessionIdle          time.Duration
	sessionIdleCheck     time.Duration
	viewport             Viewport
}

func defaultTiming() timing {
	return timing{
		openIdle: 3 * time.Second, tabReconcileTimeout: 2 * time.Second,
		tabReconcileInterval: 2 * time.Second, liveFirstFrame: 2 * time.Second,
		sessionIdle: 30 * time.Minute, sessionIdleCheck: time.Minute,
		viewport: Viewport{Width: 1920, Height: 1080},
	}
}

const liveInputQueueSize = 64

const liveInputDispatchTimeout = time.Second

type liveInput struct {
	viewerID string
	page     *page
	event    InputEvent
	epoch    uint64
	// received 是单条直播输入通过远程链路到达 Manager 的时刻，
	// 本地指针插值据此恢复相邻真实目标的节奏。
	received time.Time
	// lossless 表示该输入来自批量轨迹，必须逐点投递，不能与相邻采样合并。
	lossless bool
	// pace 是同一批采样的共享回放锚点，offset 是相对批次首个点的偏移。
	pace   *inputBatchPace
	offset time.Duration
	// done 非空时表示顺序屏障；消费者到达该位置后关闭它。
	done chan struct{}
}

// inputBatchPace 在批次首个点真正投递时建立时间锚点，保证回放节奏稳定。
type inputBatchPace struct {
	once  sync.Once
	start time.Time
}

type session struct {
	id        string
	browser   Session
	external  bool
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
	sequenceDraft *sequenceDraft
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
	refs        map[string]struct{}
	refRevision uint64
	// lastSnapshot 是 renderer 被原生弹窗暂停时可返回的最近页面投影。
	lastSnapshot *Snapshot
	// generation 在导航或外部 Tab 状态变化时递增，用于丢弃并发旧快照。
	generation         uint64
	elements           map[string]Element
	injectedScripts    map[string]struct{}
	injectMu           sync.Mutex
	frameMu            sync.Mutex
	lastFrame          *LiveFrame
	devtoolsMu         sync.Mutex
	devtools           *devtoolsCapture
	devtoolsGeneration uint64
}

// EvaluateEnabled 报告宿主是否允许 browserEvaluate 和 browserAct 的 evaluate 步骤。
func (manager *Manager) EvaluateEnabled() bool {
	return manager != nil && manager.config.AllowEvaluate
}

// NewManager 创建一个独立浏览器管理器。
func NewManager(config Config) (*Manager, error) {
	return newManager(config, defaultTiming())
}

func newManager(config Config, timing timing) (*Manager, error) {
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
		idleCheckInterval: timing.sessionIdleCheck,
		timing:            timing,
		stateSubs:         make(map[chan struct{}]struct{}),
		devtoolsStateSubs: make(map[chan struct{}]struct{}),
		replays:           make(map[string]*sequenceReplayState),
		interactiveWake:   make(chan struct{}, 1),
	}
	manager.downloadMaxBytes.Store(config.DownloadMaxBytes)
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
		// get 直接返回生命周期错误；解释类型不能交给原生 errors.As 校验。
		browserErr, ok := err.(*Error)
		if ok && browserErr.Kind == "page_not_found" {
			// 本地 Tab 映射可能落后于外部浏览器；先做一次有界 reconcile，
			// 再决定是否真的需要新建页面。
			if recovered, recoveredPage, recoveryErr := manager.recoverSessionPage(ctx, request.SessionID); recoveryErr == nil {
				current, currentPage, err = recovered, recoveredPage, nil
			}
		}
		if err != nil {
			// 最后一个 Tab 被关闭后 session 仍然存在；此时允许 browserOpen
			// 在原 session 中新建页面，避免只能关闭整个 session 重来。
			if ok && browserErr.Kind == "page_not_found" {
				return manager.NewTab(ctx, request)
			}
			return OpenResponse{}, err
		}
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
		if info, err = currentPage.page.Navigate(ctx, target); err != nil {
			return OpenResponse{}, openFailed(err)
		}
		if !info.IsDownload {
			current.mu.Lock()
			clearPageRefs(currentPage)
			currentPage.generation++
			current.mu.Unlock()
		}
		loadStatus, err = manager.navigationLoadStatus(ctx, currentPage.page, info)
		if err != nil {
			return OpenResponse{}, openFailed(err)
		}
		if pendingPageDialog(currentPage.page) == nil {
			info, err = currentPage.page.Info(ctx)
			if err != nil {
				return OpenResponse{}, openFailed(err)
			}
		} else {
			info = PageInfo{URL: target}
		}
		current.mu.Lock()
		currentPage.info = info
		current.mu.Unlock()
	}
	snapshot, warning := manager.loadSnapshot(ctx, current, currentPage, loadStatus)
	return OpenResponse{SessionID: current.id, PageID: currentPage.id, URL: info.URL, Title: info.Title, LoadStatus: loadStatus, Snapshot: snapshot, Warning: warning, Dialogs: pendingPageDialogs(currentPage.page)}, nil
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
	return manager.openNewWithConfig(ctx, request, manager.config.Session)
}

func (manager *Manager) openNewWithConfig(ctx context.Context, request OpenRequest, sessionConfig SessionConfig) (OpenResponse, error) {
	if err := manager.beginOpening(); err != nil {
		return OpenResponse{}, err
	}
	defer manager.finishOpening()
	created, createdPage, loadStatus, err := manager.createSessionWithConfig(ctx, request, false, sessionConfig)
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
	return manager.createSessionWithConfig(ctx, request, attach, manager.config.Session)
}

func (manager *Manager) createSessionWithConfig(ctx context.Context, request OpenRequest, attach bool, sessionConfig SessionConfig) (*session, *page, OpenLoadStatus, error) {
	sessionConfig.DownloadMaxBytes = manager.downloadMaxBytes.Load()
	sessionConfig.downloadLimit = manager.downloadMaxBytes.Load
	browser, err := manager.config.Factory(ctx, sessionConfig)
	if err != nil {
		return nil, nil, "", err
	}
	if browser == nil {
		return nil, nil, "", errors.New("浏览器 Factory 返回空 session")
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	created := &session{
		id: manager.newID("bsess"), browser: browser,
		external: externalBrowserConfig(sessionConfig),
		tabs:     make(map[string]*page), createdAt: time.Now(), lastUsed: time.Now(),
		ctx: sessionCtx, cancel: sessionCancel, inputQueue: make(chan liveInput, liveInputQueueSize), inputEpoch: 1,
	}
	viewport := request.Viewport
	if viewport.Width <= 0 || viewport.Height <= 0 {
		viewport = manager.timing.viewport
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
		loadStatus, err = manager.pageLoadStatus(ctx, createdPage.page)
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
	if manager.stopped {
		manager.mu.Unlock()
		return browserError("browser_closed", "Browser Manager 已关闭", "调用 NewManager 重新创建浏览器运行时", true)
	}
	manager.sessions[created.id] = created
	manager.active = created.id
	manager.mu.Unlock()
	manager.watchTabEvents(created)
	manager.watchConnection(created)
	return nil
}

func (manager *Manager) finishNewPage(ctx context.Context, current *session, currentPage *page, address string, loadStatus OpenLoadStatus) (OpenResponse, error) {
	if address == "" {
		snapshot, warning := manager.loadSnapshot(ctx, current, currentPage, loadStatus)
		return OpenResponse{SessionID: current.id, PageID: currentPage.id, URL: "about:blank", LoadStatus: loadStatus, Snapshot: snapshot, Warning: warning, Dialogs: pendingPageDialogs(currentPage.page)}, nil
	}
	info, err := currentPage.page.Navigate(ctx, address)
	if err != nil {
		return OpenResponse{}, err
	}
	loadStatus, err = manager.navigationLoadStatus(ctx, currentPage.page, info)
	if err != nil {
		return OpenResponse{}, err
	}
	if info.URL == "" {
		info.URL = address
	}
	if pendingPageDialog(currentPage.page) == nil {
		info, err = currentPage.page.Info(ctx)
		if err != nil {
			return OpenResponse{}, err
		}
	}
	current.mu.Lock()
	currentPage.info = info
	currentPage.status = "ready"
	current.mu.Unlock()
	manager.emit(ctx, Event{Kind: "navigation_finished", SessionID: current.id, PageID: currentPage.id, URL: info.URL})
	snapshot, warning := manager.loadSnapshot(ctx, current, currentPage, loadStatus)
	return OpenResponse{SessionID: current.id, PageID: currentPage.id, URL: info.URL, Title: info.Title, LoadStatus: loadStatus, Snapshot: snapshot, Warning: warning, Dialogs: pendingPageDialogs(currentPage.page)}, nil
}

func (manager *Manager) navigationLoadStatus(ctx context.Context, page Page, info PageInfo) (OpenLoadStatus, error) {
	if info.IsDownload {
		return OpenLoadDownload, nil
	}
	return manager.pageLoadStatus(ctx, page)
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

func (manager *Manager) pageLoadStatus(ctx context.Context, page Page) (OpenLoadStatus, error) {
	if pendingPageDialog(page) != nil {
		return OpenLoadTimeout, nil
	}
	changes := dialogChanges(page)
	if pendingPageDialog(page) != nil {
		return OpenLoadTimeout, nil
	}
	type idleResult struct {
		timedOut bool
		err      error
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan idleResult, 1)
	go func() {
		timedOut, waitErr := waitPageIdle(waitCtx, page, manager.timing.openIdle)
		done <- idleResult{timedOut: timedOut, err: waitErr}
	}()
	var timedOut bool
	var err error
	select {
	case result := <-done:
		timedOut, err = result.timedOut, result.err
	case <-changes:
		cancel()
		if pendingPageDialog(page) != nil {
			return OpenLoadTimeout, nil
		}
		return "", errors.New("页面弹窗状态发生变化")
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err != nil {
		if pendingPageDialog(page) != nil {
			return OpenLoadTimeout, nil
		}
		return "", err
	}
	if timedOut {
		return OpenLoadTimeout, nil
	}
	return OpenLoadLoaded, nil
}
func waitPageIdle(ctx context.Context, page Page, timeout time.Duration) (bool, error) {
	idleCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := page.Wait(idleCtx, WaitOptions{Condition: "load_complete"})
	if err == nil {
		err = page.WaitIdle(idleCtx, timeout)
	}
	if err == nil {
		return false, nil
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return true, nil
	}
	return false, err
}

// loadSnapshot 只读取超时页面的现有内容，不再次等待加载或枚举 Tab。
// 快照沿用 browserSnapshot 的状态投影，保证返回的 ref 可直接操作。
func (manager *Manager) recoverSessionPage(ctx context.Context, sessionID string) (*session, *page, error) {
	current, err := manager.lockSession(sessionID)
	if err != nil {
		return nil, nil, err
	}
	current.mu.Unlock()
	if err := manager.reconcileTabsWait(ctx, current); err != nil {
		return nil, nil, err
	}
	return manager.get(ctx, sessionID, "")
}

func (manager *Manager) loadSnapshot(ctx context.Context, current *session, currentPage *page, status OpenLoadStatus) (*SnapshotResponse, string) {
	warning := ""
	if status == OpenLoadTimeout {
		warning = fmt.Sprintf("页面未在总计 %g 秒的等待预算内完成加载并进入空闲，当前内容可能不完整；请根据已有内容决定继续操作，或调用 browserWait/browserSnapshot 检查后续状态。", manager.timing.openIdle.Seconds())
	}
	// 成功导航也返回快照：绝大多数调用方紧接着就会读取页面，
	// 提前附带可省掉一次额外的 browserSnapshot 往返。
	snapshot, err := manager.snapshotPage(ctx, current, currentPage, SnapshotOptions{})
	if err != nil {
		if status == OpenLoadTimeout {
			return nil, warning + " 当前内容读取失败：" + err.Error() + "；页面仍保留，请勿重复打开或导航。"
		}
		return nil, ""
	}
	return &snapshot, warning
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
	manager.notifyState()
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
