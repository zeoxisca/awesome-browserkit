// Package browserhttp 将独立浏览器运行时挂载为标准 HTTP 服务。
package browserhttp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zeoxisca/awesome-browserkit"
	xwebsocket "golang.org/x/net/websocket"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// Options 配置浏览器入口；零值允许直播并沿用 Manager 的诊断权限。
type Options struct {
	// AuthMiddleware 在所有 HTTP 请求和 WebSocket 握手进入 BrowserKit 前执行。
	// 宿主可在这里接入 Bearer Token、Cookie Session、mTLS 身份或反向代理认证；
	// 中间件拒绝请求时不应调用 next。零值不启用认证。
	AuthMiddleware func(next http.Handler) http.Handler
	// DisableLiveView 禁用图流及页面交互，只提供已授权诊断。
	DisableLiveView bool
	// DisableDevtools 禁用诊断入口。
	DisableDevtools bool
	// ReadOnly 拒绝页面修改、操作权接管和诊断启停。
	ReadOnly bool
	// AllowedOrigins 是允许跨源访问的完整 origin，默认仅同源。
	AllowedOrigins []string
}

// Handler 将一份 Manager 暴露为相对路径的 HTTP 服务。
// 连接生命周期由请求管理，浏览器生命周期始终由调用者管理。
type Handler struct {
	manager *browserkit.Manager
	options Options
	mux     *http.ServeMux
	auth    http.Handler
}

// NewHandler 注册全部浏览器子路由，不监听端口，也不修改全局 ServeMux。
func NewHandler(manager *browserkit.Manager, options Options) *Handler {
	h := &Handler{manager: manager, options: options, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /state", h.state)
	h.mux.HandleFunc("GET /state/ws", h.stateStream)
	h.mux.HandleFunc("GET /frame", h.frame)
	h.mux.HandleFunc("GET /cached-frame", h.frame)
	h.mux.HandleFunc("GET /ws", h.stream)
	h.mux.HandleFunc("POST /control", h.takeControl)
	h.mux.HandleFunc("POST /close", h.closeBrowser)
	h.mux.HandleFunc("POST /tabs", h.newTab)
	h.mux.HandleFunc("POST /tabs/{pageID}/activate", h.tab)
	h.mux.HandleFunc("POST /tabs/{pageID}/navigate", h.tab)
	h.mux.HandleFunc("POST /tabs/{pageID}/reload", h.tab)
	h.mux.HandleFunc("PUT /tabs/{pageID}/viewport", h.tab)
	h.mux.HandleFunc("DELETE /tabs/{pageID}", h.tab)
	h.mux.HandleFunc("GET /tabs/{pageID}/devtools", h.diagnostics)
	h.mux.HandleFunc("POST /tabs/{pageID}/devtools", h.diagnostics)
	h.mux.HandleFunc("DELETE /tabs/{pageID}/devtools", h.diagnostics)
	h.mux.HandleFunc("GET /tabs/{pageID}/devtools/ws", h.diagnosticStream)
	h.mux.HandleFunc("GET /tabs/{pageID}/assets", h.assets)
	authorized := http.Handler(http.HandlerFunc(h.serveAuthorized))
	if options.AuthMiddleware != nil {
		authorized = options.AuthMiddleware(authorized)
	}
	h.auth = authorized
	return h
}

// ServeHTTP 服务一次浏览器请求；可通过 StripPrefix 挂载到任意路径。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.auth == nil {
		fail(w, http.StatusServiceUnavailable, errors.New("浏览器 HTTP Handler 尚未初始化"))
		return
	}
	h.auth.ServeHTTP(w, r)
}

// serveAuthorized 只在宿主认证中间件允许请求后执行，因此同一认证边界同时
// 保护普通 HTTP、状态流、诊断流和包含浏览器输入的 WebSocket 握手。
func (h *Handler) serveAuthorized(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		fail(w, 503, errors.New("浏览器运行时尚未启用"))
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		allowed := err == nil && u.Host == r.Host && u.Scheme == scheme
		for _, v := range h.options.AllowedOrigins {
			if v == origin {
				allowed = true
			}
		}
		if !allowed {
			fail(w, 403, errors.New("浏览器请求来源未授权"))
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Add("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Browserkit-Viewer")
		w.WriteHeader(204)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}
func (h *Handler) live(w http.ResponseWriter) bool {
	if h.options.DisableLiveView {
		fail(w, 403, errors.New("浏览器页面直播未启用"))
		return false
	}
	return true
}
func (h *Handler) diagnosticAllowed(w http.ResponseWriter) bool {
	if h.options.DisableDevtools || !h.manager.DevtoolsEnabled() {
		fail(w, 403, errors.New("浏览器开发诊断未授权"))
		return false
	}
	return true
}
func (h *Handler) writable(w http.ResponseWriter, r *http.Request) bool {
	if h.options.ReadOnly {
		fail(w, 403, errors.New("浏览器入口只读"))
		return false
	}
	if !h.manager.CanControl(r.Header.Get(browserkit.ViewerHeader)) {
		fail(w, 409, errors.New("当前窗口没有浏览器操作权"))
		return false
	}
	return true
}
func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	if h.options.DisableLiveView && !h.diagnosticAllowed(w) {
		return
	}
	v, e := h.manager.View(r.Context())
	result(w, v, e)
}
func (h *Handler) frame(w http.ResponseWriter, r *http.Request) {
	if !h.live(w) {
		return
	}
	var f browserkit.LiveFrame
	var err error
	if r.URL.Path == "/cached-frame" {
		f, err = h.manager.CachedLiveFrame(r.Context())
	} else {
		f, err = h.manager.LiveFrame(r.Context())
	}
	if err != nil {
		fail(w, 404, err)
		return
	}
	w.Header().Set("Content-Type", f.MIME)
	w.Header().Set("X-Browser-Page", f.PageID)
	w.Header().Set("X-Browser-Generation", strconv.FormatUint(f.Generation, 10))
	w.Header().Set("Access-Control-Expose-Headers", "X-Browser-Page, X-Browser-Generation")
	_, _ = w.Write(f.Data)
}
func (h *Handler) takeControl(w http.ResponseWriter, r *http.Request) {
	if !h.live(w) {
		return
	}
	if h.options.ReadOnly {
		fail(w, 403, errors.New("浏览器入口只读"))
		return
	}
	state, err := h.manager.TakeControl(r.Header.Get(browserkit.ViewerHeader))
	if err != nil {
		var cooldown *browserkit.TakeoverCooldownError
		if errors.As(err, &cooldown) {
			fail(w, 429, err)
		} else {
			fail(w, 409, err)
		}
		return
	}
	result(w, state, nil)
}
func (h *Handler) closeBrowser(w http.ResponseWriter, r *http.Request) {
	if !h.live(w) || !h.writable(w, r) {
		return
	}
	result(w, nil, h.manager.CloseActive(r.Context()))
}

// NavigationRequest 是地址栏提交的导航请求。
type NavigationRequest struct {
	URL string `json:"url"`
}

func (h *Handler) newTab(w http.ResponseWriter, r *http.Request) {
	if !h.live(w) || !h.writable(w, r) {
		return
	}
	var body NavigationRequest
	if !decode(w, r, &body) {
		return
	}
	v, e := h.manager.NewTab(r.Context(), browserkit.OpenRequest{URL: body.URL})
	result(w, v, e)
}
func (h *Handler) tab(w http.ResponseWriter, r *http.Request) {
	if !h.live(w) || !h.writable(w, r) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("pageID")
	var err error
	switch {
	case r.Method == http.MethodDelete:
		_, err = h.manager.CloseTab(ctx, browserkit.TabRequest{PageID: id})
	case strings.HasSuffix(r.URL.Path, "/activate"):
		_, err = h.manager.ActivateTab(ctx, browserkit.TabRequest{PageID: id})
	case strings.HasSuffix(r.URL.Path, "/navigate"):
		var body NavigationRequest
		if !decode(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.URL) == "" {
			fail(w, 400, errors.New("导航地址不能为空"))
			return
		}
		_, err = h.manager.Navigate(ctx, browserkit.NavigateRequest{PageID: id, URL: body.URL})
	case strings.HasSuffix(r.URL.Path, "/reload"):
		_, err = h.manager.Reload(ctx, browserkit.TabRequest{PageID: id})
	case strings.HasSuffix(r.URL.Path, "/viewport"):
		var v browserkit.Viewport
		if !decode(w, r, &v) {
			return
		}
		if err = v.Validate(); err != nil {
			fail(w, 400, err)
			return
		}
		err = h.manager.SetViewport(ctx, browserkit.ViewportRequest{PageID: id, Viewport: v})
	}
	result(w, nil, err)
}
func (h *Handler) readDiagnostic(ctx context.Context, page string, after uint64, limit int) (browserkit.BrowserDevtoolsViewState, error) {
	v, e := h.manager.DevtoolsState(ctx, browserkit.DevtoolsReadRequest{PageID: page, AfterSequence: after, Limit: limit})
	return browserkit.BrowserDevtoolsView(v), e
}
func (h *Handler) diagnostics(w http.ResponseWriter, r *http.Request) {
	if !h.diagnosticAllowed(w) {
		return
	}
	page := r.PathValue("pageID")
	after, err := strconv.ParseUint(defaultString(r.URL.Query().Get("after"), "0"), 10, 64)
	limit, e := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "0"))
	if err != nil || e != nil || limit < 0 || limit > 100 {
		fail(w, 400, errors.New("开发诊断分页参数无效"))
		return
	}
	if r.Method != http.MethodGet {
		if !h.writable(w, r) {
			return
		}
		state, e := h.manager.DevtoolsState(r.Context(), browserkit.DevtoolsReadRequest{PageID: page})
		if e != nil {
			fail(w, 404, e)
			return
		}
		req := browserkit.DevtoolsOpenRequest{SessionID: state.SessionID, PageID: page}
		if r.Method == http.MethodPost {
			_, err = h.manager.OpenDevtools(r.Context(), req)
		} else {
			_, err = h.manager.CloseDevtools(r.Context(), req)
		}
		if err != nil {
			fail(w, 400, err)
			return
		}
	}
	v, err := h.readDiagnostic(r.Context(), page, after, limit)
	result(w, v, err)
}
func (h *Handler) assets(w http.ResponseWriter, r *http.Request) {
	if h.options.DisableLiveView && !h.diagnosticAllowed(w) {
		return
	}
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "100"))
	if err != nil || limit < 0 || limit > 500 {
		fail(w, 400, errors.New("资源列表 limit 必须位于 0 到 500"))
		return
	}
	v, err := h.manager.ListAssets(r.Context(), browserkit.AssetsRequest{PageID: r.PathValue("pageID"), Host: r.URL.Query().Get("host"), Limit: limit})
	result(w, browserkit.BrowserAssetsFromResponse(v), err)
}
func (h *Handler) accept(w http.ResponseWriter, r *http.Request, images bool) (*websocket.Conn, error) {
	options := &websocket.AcceptOptions{CompressionMode: websocket.CompressionNoContextTakeover, CompressionThreshold: 128}
	if images {
		options.CompressionMode = websocket.CompressionDisabled
	}
	// ServeHTTP 已完成完整 origin 校验，握手复用同一结果。
	options.InsecureSkipVerify = true
	return websocket.Accept(w, r, options)
}
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	if h.options.DisableLiveView && !h.diagnosticAllowed(w) {
		return
	}
	// 图流使用独立读写的连接；不再因单帧写超时切断同一连接中的按键。
	// ServeHTTP 已完成 Origin 检查，握手复用该结果。
	server := xwebsocket.Server{
		Handshake: func(*xwebsocket.Config, *http.Request) error { return nil },
		Handler:   h.socket,
	}
	server.ServeHTTP(w, r)
}
func (h *Handler) socket(conn *xwebsocket.Conn) {
	r := conn.Request()
	defer conn.Close()
	conn.MaxPayloadBytes = 128 << 10
	conn.PayloadType = xwebsocket.BinaryFrame
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// 读写可能阻塞在 socket 上；连接的 context 结束时同时解除两侧等待。
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	viewer := r.URL.Query().Get("viewer_id")
	updates, leave, err := h.manager.JoinViewer(viewer, h.options.ReadOnly || r.URL.Query().Get("read_only") == "1")
	if err != nil {
		return
	}
	defer leave()
	var frames <-chan browserkit.LiveFrame
	stateUpdates, err := h.manager.SubscribeState(ctx)
	if err != nil {
		return
	}
	subscribeFrames := func() {
		if h.options.DisableLiveView {
			return
		}
		// 浏览器关闭后等待状态通知，不能反复订阅一个立即结束的空图流。
		state, err := h.manager.View(ctx)
		if err == nil && state.Available {
			frames, _ = h.manager.SubscribeLiveFrames(ctx)
		}
	}
	subscribeFrames()
	inputErrors := make(chan error, 1)
	go func() {
		defer cancel()
		for {
			var data []byte
			if err := xwebsocket.Message.Receive(conn, &data); err != nil {
				return
			}
			var input struct {
				PageID string `json:"page_id"`
				browserkit.InputEvent
			}
			d := json.NewDecoder(bytes.NewReader(data))
			d.DisallowUnknownFields()
			if d.Decode(&input) != nil {
				return
			}
			var extra any
			if !errors.Is(d.Decode(&extra), io.EOF) {
				return
			}
			if input.PageID != "" && !h.options.ReadOnly && !h.options.DisableLiveView {
				if err := h.manager.DispatchViewerInput(ctx, viewer, input.PageID, input.InputEvent); err != nil {
					// 慢观看者只需保留一个待提示错误，接收循环继续处理后续输入。
					select {
					case inputErrors <- err:
					default:
					}
				}
			}
		}
	}()
	for {
		select {
		case inputErr := <-inputErrors:
			var detail *browserkit.Error
			if !errors.As(inputErr, &detail) {
				detail = &browserkit.Error{Kind: "input_failed", Message: inputErr.Error()}
			}
			if err := xwebsocket.JSON.Send(conn, struct {
				Type  string            `json:"type"`
				Error *browserkit.Error `json:"error"`
			}{Type: "browser_input_error", Error: detail}); err != nil {
				return
			}
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
			if err := xwebsocket.JSON.Send(conn, h.manager.ViewerControl(viewer)); err != nil {
				return
			}
		case _, ok := <-stateUpdates:
			if !ok {
				return
			}
			if frames == nil {
				subscribeFrames()
			}
		case f, ok := <-frames:
			if !ok {
				frames = nil
				subscribeFrames()
				continue
			}
			if len(f.Data) == 0 {
				continue
			}
			data, e := EncodeFrame(f)
			if e != nil {
				return
			}
			if _, err := conn.Write(data); err != nil {
				return
			}
		}
	}
}

// EncodeFrame 编码带页面身份的图帧：四字节大端 JSON 头长度、JSON 头、原始图片。
func EncodeFrame(f browserkit.LiveFrame) ([]byte, error) {
	header, e := json.Marshal(struct {
		PageID     string `json:"page_id"`
		Generation uint64 `json:"generation"`
		MIME       string `json:"mime"`
	}{f.PageID, f.Generation, f.MIME})
	if e != nil {
		return nil, e
	}
	data := make([]byte, 4+len(header)+len(f.Data))
	binary.BigEndian.PutUint32(data, uint32(len(header)))
	copy(data[4:], header)
	copy(data[4+len(header):], f.Data)
	return data, nil
}
func (h *Handler) stateStream(w http.ResponseWriter, r *http.Request) {
	if h.options.DisableLiveView && !h.diagnosticAllowed(w) {
		return
	}
	h.updates(w, r, false)
}
func (h *Handler) diagnosticStream(w http.ResponseWriter, r *http.Request) {
	if !h.diagnosticAllowed(w) {
		return
	}
	h.updates(w, r, true)
}
func (h *Handler) updates(w http.ResponseWriter, r *http.Request, diagnostics bool) {
	conn, e := h.accept(w, r, false)
	if e != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()
	var updates <-chan struct{}
	if diagnostics {
		updates, e = h.manager.SubscribeDevtools(ctx)
	} else {
		updates, e = h.manager.SubscribeState(ctx)
	}
	if e != nil {
		return
	}
	var generation, after uint64
	send := func() error {
		for {
			operation, stop := context.WithTimeout(ctx, 2500*time.Millisecond)
			var value any
			var err error
			again := false
			if diagnostics {
				v, e := h.readDiagnostic(operation, r.PathValue("pageID"), after, 100)
				err = e
				if generation != 0 && v.Generation != generation {
					generation, after = v.Generation, 0
					stop()
					continue
				}
				generation = v.Generation
				after = v.NextSequence
				again = v.Truncated
				value = v
			} else {
				value, err = h.manager.View(operation)
			}
			stop()
			if err != nil {
				return err
			}
			write, finish := context.WithTimeout(ctx, time.Second)
			err = wsjson.Write(write, conn, value)
			finish()
			if err != nil || !again {
				return err
			}
		}
	}
	if send() != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				_ = send()
				return
			}
			if send() != nil {
				return
			}
		}
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, err)
		return false
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		fail(w, 400, errors.New("请求必须是单个 JSON 对象"))
		return false
	}
	return true
}
func result(w http.ResponseWriter, v any, e error) {
	if e != nil {
		fail(w, 400, e)
		return
	}
	if v == nil {
		w.WriteHeader(204)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, e error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{e.Error()})
}
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
