package browserkit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// Viewport 描述页面视口尺寸。
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

const maxViewportDimension = 16_384
const maxViewportPixels = 64 * 1024 * 1024
const maxSnapshotChars = 1 << 20
const maxSnapshotElements = 1_000
const screencastAckTimeout = 250 * time.Millisecond
const screencastStopTimeout = 500 * time.Millisecond
const targetCloseTimeout = 2 * time.Second

// Validate 校验页面视口，避免异常尺寸造成浏览器截图和渲染资源失控。
func (viewport Viewport) Validate() error {
	if viewport.Width <= 0 || viewport.Height <= 0 {
		return errors.New("浏览器 viewport 无效")
	}
	if viewport.Width > maxViewportDimension || viewport.Height > maxViewportDimension ||
		int64(viewport.Width)*int64(viewport.Height) > maxViewportPixels {
		return errors.New("浏览器 viewport 超出允许范围")
	}
	return nil
}

// PageOptions 描述新页面选项。
type PageOptions struct {
	Viewport Viewport
	// InjectScript 会在后续每个 document 创建早期于主 world 执行。
	InjectScript string
}

// PageInfo 描述页面当前的公开状态。
type PageInfo struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Asset 描述当前页面已经观测到的一项网络资源。
type Asset struct {
	Name           string  `json:"name"`
	URL            string  `json:"url"`
	InitiatorType  string  `json:"initiator_type,omitempty"`
	ResponseStatus int     `json:"response_status,omitempty"`
	TransferBytes  int64   `json:"transfer_bytes,omitempty"`
	EncodedBytes   int64   `json:"encoded_bytes,omitempty"`
	DecodedBytes   int64   `json:"decoded_bytes,omitempty"`
	DurationMS     float64 `json:"duration_ms,omitempty"`
}

// ListAssetsOptions 描述资源列表的过滤和数量限制。
type ListAssetsOptions struct {
	// Host 非空时只返回该主机的资源。
	Host string
	// Limit 限制返回资源数量，必须位于 1 到 500。
	Limit int
}

// ListAssetsResult 是页面资源的有界读取结果。
type ListAssetsResult struct {
	// Assets 按页面性能记录顺序返回最近的资源。
	Assets []Asset
	// Truncated 表示更早的资源因 Limit 未返回。
	Truncated bool
}

// AssetsPage 提供当前页面已观测资源的只读列表。
type AssetsPage interface {
	ListAssets(context.Context, ListAssetsOptions) (ListAssetsResult, error)
}

// ElementRef 是快照生成的、绑定 Page（即一个 Tab）及 document revision 的元素引用。
// 同一 document 内的局部 DOM 更新不会改变 revision；节点被替换或移除时，
// 点击执行阶段会根据仍存在的 data-browserkit-ref 判断该节点是否 stale。
type ElementRef struct {
	Ref      string `json:"ref"`
	Revision uint64 `json:"revision"`
}

// Element 描述一个可交互元素，不包含字段值。
type Element struct {
	Ref  string `json:"ref"`
	Role string `json:"role"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// SnapshotOptions 限制快照大小。
type SnapshotOptions struct {
	MaxChars    int
	MaxElements int
	// Offset 跳过前面的可交互候选元素，用于分页读取。
	Offset int
}

// Snapshot 是页面文本和可交互元素的结构化摘要。
type Snapshot struct {
	Info      PageInfo  `json:"info"`
	Revision  uint64    `json:"revision"`
	Text      string    `json:"text"`
	Elements  []Element `json:"elements"`
	Truncated bool      `json:"truncated"`
	// ActiveRefs 是当前 DOM 中仍存在的内部 ref，供宿主回收已删除节点引用。
	ActiveRefs []string `json:"-"`
}

// ScreenshotOptions 描述截图选项。
type ScreenshotOptions struct {
	FullPage bool
}

// ScreencastOptions 描述 CDP 连续画面的编码参数。
type ScreencastOptions struct {
	Quality       int
	MaxWidth      int
	MaxHeight     int
	EveryNthFrame int
}

// Screenshot 是截图二进制结果。
type Screenshot struct {
	Data   []byte
	Width  int
	Height int
	Format string
}

// ScreencastPage 提供可选的 CDP 连续画面能力。
// 普通 Page 实现可以不支持；宿主应回退到定时截图。
type ScreencastPage interface {
	StartScreencast(context.Context, ScreencastOptions) (<-chan Screenshot, func(), error)
}

// InputEvent 是宿主通过 CDP 发送给真实浏览器页面的输入。
// 坐标和滚动距离均使用远程页面 viewport 的 CSS 像素。
// shortcut 类型只接受 select_all、copy、paste，Modifiers 可为 Ctrl(2)、Meta(4)
// 或由运行平台决定的零值。
type InputEvent struct {
	Kind           string            `json:"kind"`
	Action         string            `json:"action,omitempty"`
	X              float64           `json:"x,omitempty"`
	Y              float64           `json:"y,omitempty"`
	DeltaX         float64           `json:"delta_x,omitempty"`
	DeltaY         float64           `json:"delta_y,omitempty"`
	Button         string            `json:"button,omitempty"`
	Buttons        int               `json:"buttons,omitempty"`
	ClickCount     int               `json:"click_count,omitempty"`
	PointerType    string            `json:"pointer_type,omitempty"`
	Touches        []InputTouchPoint `json:"touches,omitempty"`
	Modifiers      int               `json:"modifiers,omitempty"`
	Key            string            `json:"key,omitempty"`
	Code           string            `json:"code,omitempty"`
	KeyCode        int               `json:"key_code,omitempty"`
	Location       int               `json:"location,omitempty"`
	AutoRepeat     bool              `json:"auto_repeat,omitempty"`
	Text           string            `json:"text,omitempty"`
	SelectionStart int               `json:"selection_start,omitempty"`
	SelectionEnd   int               `json:"selection_end,omitempty"`
}

// InputTouchPoint 描述一个活动触点，坐标使用远程 viewport 的 CSS 像素。
type InputTouchPoint struct {
	ID      int     `json:"id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	RadiusX float64 `json:"radius_x,omitempty"`
	RadiusY float64 `json:"radius_y,omitempty"`
	Force   float64 `json:"force,omitempty"`
}

// InputPage 提供基于 CDP Input 域的真实浏览器输入能力。
type InputPage interface {
	DispatchInput(context.Context, InputEvent) error
}

// HoverPage 提供基于真实指针坐标的元素悬停能力。
type HoverPage interface {
	Hover(context.Context, ElementRef) error
}

// EvaluateOptions 描述一次 console 域 JavaScript 执行。
type EvaluateOptions struct {
	AwaitPromise bool
	Timeout      time.Duration
}

// EvaluateResult 是只包含可 JSON 序列化值的 JavaScript 返回值。
type EvaluateResult struct {
	Value json.RawMessage `json:"value,omitempty"`
	Type  string          `json:"type"`
}

// Page 是浏览器页面的最小稳定操作接口。
type Page interface {
	Info(context.Context) (PageInfo, error)
	Navigate(context.Context, string) (PageInfo, error)
	Snapshot(context.Context, SnapshotOptions) (Snapshot, error)
	Click(context.Context, ElementRef) error
	Type(context.Context, ElementRef, string, bool) error
	Press(context.Context, string) error
	Wait(context.Context, WaitOptions) error
	// WaitIdle 等待页面下一次 requestIdleCallback，超时由调用方控制。
	WaitIdle(context.Context, time.Duration) error
	Screenshot(context.Context, ScreenshotOptions) (Screenshot, error)
	Evaluate(context.Context, string, EvaluateOptions) (EvaluateResult, error)
	AddScriptToEvaluateOnNewDocument(context.Context, string) error
	Close(context.Context) error
}

// TargetPage 是能提供底层 target 身份的 Page。身份只供 Browser Manager
// 绑定已有 Tab 使用，不应进入模型或 Web 协议。
type TargetPage interface {
	Page
	TargetID() string
}

// WaitOptions 描述浏览器页面的有限等待条件。
type WaitOptions struct {
	Condition string
	Selector  string
}

type rodPage struct {
	page             *rod.Page
	stateMu          sync.Mutex
	interactionMu    sync.Mutex
	releaseOnce      sync.Once
	cancel           context.CancelFunc
	owner            *rodSession
	revision         uint64
	refs             map[string]struct{}
	documentID       string
	stateKnown       bool
	operationTimeout time.Duration
	pointerMu        sync.Mutex
	pointerX         float64
	pointerY         float64
	pointerKnown     bool
}

func (page *rodPage) release() {
	if page == nil {
		return
	}
	page.releaseOnce.Do(func() {
		if page.cancel != nil {
			page.cancel()
		}
		if page.owner != nil {
			page.owner.forgetPage(page)
		}
	})
}

// ctx 为单次 Page 操作创建独立的、有界生命周期 context。
// Page 内部的 root、Browser 和 CDP 请求都必须从这个 context 派生，避免
// 页面操作因远端 renderer 或 websocket 无响应而永久阻塞。
func (page *rodPage) ctx(parent context.Context) (context.Context, context.CancelFunc, error) {
	if page == nil {
		return nil, func() {}, errors.New("浏览器 page 不能为空")
	}
	return contextWithTimeout(parent, page.operationTimeout)
}

func (page *rodPage) controlCtx(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if page != nil && page.operationTimeout > 0 && page.operationTimeout < timeout {
		timeout = page.operationTimeout
	}
	return contextWithTimeout(parent, timeout)
}

// lockInteraction 可取消地获取交互锁，避免前一个失去响应的 CDP 输入
// 调用拖住后续输入请求。
func lockInteraction(ctx context.Context, mutex *sync.Mutex) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	for {
		if mutex.TryLock() {
			return nil
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// TargetID 返回 rod 页面对应的 CDP target 身份。
func (page *rodPage) TargetID() string {
	if page == nil || page.page == nil {
		return ""
	}
	return string(page.page.TargetID)
}

type domSnapshot struct {
	URL      string    `json:"url"`
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	Elements []Element `json:"elements"`
	Refs     []string  `json:"refs"`
}

type domState struct {
	DocumentID string `json:"document_id"`
}

// pageStateObserverBody 在每个 document 中维护唯一的文档身份，并由
// MutationObserver 清理已从 DOM 移除节点上的 ref，避免 SPA 长时间运行时
// ref 集合无限增长。
const pageStateObserverBody = `
  const key = '__browserkit_page_state_v1__';
  const current = window[key];
  if (current && current.observerInstalled) return current;
  const token = Math.random().toString(36).slice(2);
  const state = {
    documentId: String(performance.timeOrigin || Date.now()) + ':' + token,
    refPrefix: 'e' + token,
    nextRef: 0,
    refs: new Set(),
    observerInstalled: true,
  };
  state.observer = new MutationObserver(mutations => {
    for (const mutation of mutations) for (const node of mutation.removedNodes) {
      if (node.nodeType !== 1) continue;
      const removed = node.matches?.('[data-browserkit-ref]') ? [node] : [];
      const descendants = node.querySelectorAll ? Array.from(node.querySelectorAll('[data-browserkit-ref]')) : [];
      for (const element of [...removed, ...descendants]) {
        const ref = element.getAttribute('data-browserkit-ref');
        if (ref) state.refs.delete(ref);
      }
    }
  });
  if (document.documentElement) state.observer.observe(document.documentElement, {subtree: true, childList: true});
  else document.addEventListener('DOMContentLoaded', () => {
    if (document.documentElement) state.observer.observe(document.documentElement, {subtree: true, childList: true});
  }, {once: true});
  Object.defineProperty(window, key, { configurable: true, value: state });
  return state;`

// pageStateObserverScript 用于 CDP 的新 document 注入，必须是普通脚本。
const pageStateObserverScript = `(function() {` + pageStateObserverBody + `})()`

// pageStateObserverEval 用于 go-rod Eval。使用普通 function，避免 rod 再包一层 apply 时
// 被页面中的异常脚本或非标准 Function 原型影响。
const pageStateObserverEval = `function() {` + pageStateObserverBody + `}`

func (page *rodPage) Info(ctx context.Context) (PageInfo, error) {
	if err := contextErr(ctx); err != nil {
		return PageInfo{}, err
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return PageInfo{}, err
	}
	defer operationCancel()
	return page.info(operationCtx)
}

func (page *rodPage) info(ctx context.Context) (PageInfo, error) {
	// Rod Page.Info 内部通过未带 context 的 Browser.pageInfo 调用，无法
	// 继承本次操作 deadline；直接使用同一 Target.getTargetInfo 请求并绑定
	// 有界 Browser context。
	info, err := (proto.TargetGetTargetInfo{TargetID: page.page.TargetID}).Call(page.page.Browser().Context(ctx))
	if err != nil {
		return PageInfo{}, err
	}
	if info.TargetInfo == nil {
		return PageInfo{}, errors.New("浏览器 target 信息为空")
	}
	return PageInfo{URL: info.TargetInfo.URL, Title: info.TargetInfo.Title}, nil
}

func (page *rodPage) activate(ctx context.Context) error {
	// Rod Page.Activate 的实现使用 p.browser（持久 context），不能保证
	// 调用方的 deadline；这里用同一 Target.activateTarget 请求绑定 ctx。
	return (proto.TargetActivateTarget{TargetID: page.page.TargetID}).Call(page.page.Browser().Context(ctx))
}

func (page *rodPage) Navigate(ctx context.Context, address string) (PageInfo, error) {
	if err := contextErr(ctx); err != nil {
		return PageInfo{}, err
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return PageInfo{}, err
	}
	defer operationCancel()
	if err := page.page.Context(operationCtx).Navigate(address); err != nil {
		return PageInfo{}, err
	}
	page.stateMu.Lock()
	page.revision++
	page.refs = make(map[string]struct{})
	page.documentID = ""
	page.stateKnown = false
	page.stateMu.Unlock()
	return page.info(operationCtx)
}

func (page *rodPage) Snapshot(ctx context.Context, options SnapshotOptions) (Snapshot, error) {
	if err := contextErr(ctx); err != nil {
		return Snapshot{}, err
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer operationCancel()
	if err := page.syncState(operationCtx); err != nil {
		return Snapshot{}, err
	}
	page.stateMu.Lock()
	if page.revision == 0 {
		page.revision = 1
	}
	revision := page.revision
	page.stateMu.Unlock()
	if options.MaxChars <= 0 {
		options.MaxChars = 12000
	} else if options.MaxChars > maxSnapshotChars {
		options.MaxChars = maxSnapshotChars
	}
	if options.MaxElements <= 0 {
		options.MaxElements = 100
	} else if options.MaxElements > maxSnapshotElements {
		options.MaxElements = maxSnapshotElements
	}
	if options.Offset < 0 {
		options.Offset = 0
	}
	args := map[string]int{"max_chars": options.MaxChars, "max_elements": options.MaxElements, "offset": options.Offset}
	result, err := page.page.Context(operationCtx).Eval(`(options) => {
		const clean = value => (value || '').replace(/[\u0000-\u001f]/g, ' ').replace(/\s+/g, ' ').trim();
		const selectors = 'button,a,input,textarea,select,summary,[contenteditable="true"],[role="button"],[role="link"],[role="textbox"],[role="menuitem"],[role="option"],[role="tab"],[role="menuitemcheckbox"],[role="menuitemradio"],[role="combobox"],[aria-haspopup],[aria-expanded],[onclick],[tabindex]:not([tabindex="-1"])';
		const visible = el => {
			const style = getComputedStyle(el);
			const rect = el.getBoundingClientRect();
			return !el.hidden && !el.closest('[hidden],[aria-hidden="true"]') && style.display !== 'none' && style.visibility !== 'hidden' && style.opacity !== '0' && style.pointerEvents !== 'none' && rect.width > 0 && rect.height > 0;
		};
		const disabled = el => el.disabled || el.getAttribute('aria-disabled') === 'true';
		const text = el => clean(el.innerText || el.textContent || '');
		const offset = Math.max(0, options.offset || 0);
		const maxElements = Math.max(1, options.max_elements || 100);
		const scanLimit = offset + maxElements;
		const candidates = [];
		const candidateSet = new Set();
		const addCandidate = el => {
			if (candidates.length >= scanLimit || candidateSet.has(el)) return;
			candidateSet.add(el);
			candidates.push(el);
		};
		for (const el of document.querySelectorAll(selectors)) {
			if (disabled(el) || !visible(el)) continue;
			addCandidate(el);
			if (candidates.length >= scanLimit) break;
		}
		if (candidates.length < scanLimit) for (const el of document.querySelectorAll('*')) {
			if (disabled(el) || !visible(el) || getComputedStyle(el).cursor !== 'pointer') continue;
			const name = text(el);
			if (name.length === 0 || name.length > 160) continue;
			const parent = el.parentElement;
			if (parent && getComputedStyle(parent).cursor === 'pointer') continue;
			if (el.closest('button,a,[role="button"],[role="link"],[role="menuitem"],[role="option"],[role="tab"]')) continue;
			addCandidate(el);
			if (candidates.length >= scanLimit) break;
		}
		const windowed = candidates.slice(offset, offset + maxElements);
		const state = window.__browserkit_page_state_v1__;
		const elements = windowed.map(el => {
			const previous = el.getAttribute('data-browserkit-ref') || '';
			const ref = previous.startsWith(state.refPrefix + '_') ? previous : state.refPrefix + '_' + (++state.nextRef);
			el.setAttribute('data-browserkit-ref', ref);
			state.refs.add(ref);
			const type = (el.getAttribute('type') || '').toLowerCase();
			const role = el.getAttribute('role') || (el.tagName === 'A' ? 'link' : el.tagName.toLowerCase() === 'button' ? 'button' : el.tagName.toLowerCase() === 'input' ? 'textbox' : el.tagName.toLowerCase() === 'select' ? 'combobox' : 'button');
			const labelledBy = (el.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => document.getElementById(id)?.innerText || document.getElementById(id)?.textContent || '').join(' ');
			const label = el.getAttribute('aria-label') || labelledBy || el.getAttribute('placeholder') || el.getAttribute('title') || text(el) || el.getAttribute('name') || '';
			return {ref, role, name: clean(label).slice(0, 160), type: type === 'password' ? 'password' : type};
		});
		const bodyText = clean(document.body ? document.body.innerText : '');
		return {url: location.href, title: document.title, text: bodyText.slice(0, Math.max(0, options.max_chars || 12000)), elements, refs: [...state.refs]};
	}`, args)
	if err != nil {
		return Snapshot{}, err
	}
	var raw domSnapshot
	if err := result.Value.Unmarshal(&raw); err != nil {
		return Snapshot{}, err
	}
	if raw.URL == "" {
		raw.URL = "about:blank"
	}
	truncated := len(raw.Text) >= options.MaxChars
	activeRefs := make(map[string]struct{}, len(raw.Refs))
	for _, ref := range raw.Refs {
		activeRefs[ref] = struct{}{}
	}
	page.stateMu.Lock()
	if page.revision != revision {
		page.stateMu.Unlock()
		return Snapshot{Info: PageInfo{URL: raw.URL, Title: raw.Title}, Revision: revision, Text: raw.Text, Elements: raw.Elements, Truncated: truncated || len(raw.Elements) == options.MaxElements, ActiveRefs: raw.Refs}, nil
	}
	for ref := range page.refs {
		if _, ok := activeRefs[ref]; !ok {
			delete(page.refs, ref)
		}
	}
	for _, element := range raw.Elements {
		page.refs[element.Ref] = struct{}{}
	}
	revision = page.revision
	page.stateMu.Unlock()
	return Snapshot{Info: PageInfo{URL: raw.URL, Title: raw.Title}, Revision: revision, Text: raw.Text, Elements: raw.Elements, Truncated: truncated || len(raw.Elements) == options.MaxElements, ActiveRefs: raw.Refs}, nil
}

// elementLocked 只在持有 page.interactionMu 时调用。返回的 Element 仍会使用传入的
// operationCtx，因此不能在这里创建并取消一个临时 context。
func (page *rodPage) elementLocked(ctx context.Context, ref ElementRef) (*rod.Element, error) {
	if err := page.syncState(ctx); err != nil {
		return nil, err
	}
	page.stateMu.Lock()
	if ref.Ref == "" || ref.Revision == 0 || ref.Revision != page.revision {
		page.stateMu.Unlock()
		return nil, newError("stale_reference", "元素引用已经过期", "重新调用 browserSnapshot 获取当前 ref", true)
	}
	if _, ok := page.refs[ref.Ref]; !ok {
		page.stateMu.Unlock()
		return nil, newError("invalid_reference", "当前快照不存在该元素引用", "重新调用 browserSnapshot", true)
	}
	page.stateMu.Unlock()
	// 这是对快照 ref 的一次性校验，不能使用 rod 默认会持续重试的 Element。
	// 节点被替换或删除时必须立即返回 stale_reference，否则 browserClick 会表现为卡死。
	el, err := page.page.Context(ctx).Sleeper(rod.NotFoundSleeper).Element(`[data-browserkit-ref="` + ref.Ref + `"]`)
	if err != nil {
		return nil, newError("stale_reference", "页面结构已变化，元素引用已经过期", "重新调用 browserSnapshot 获取当前 ref", true)
	}
	return el, nil
}

func (page *rodPage) Click(ctx context.Context, ref ElementRef) error {
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	if err := lockInteraction(operationCtx, &page.interactionMu); err != nil {
		return err
	}
	defer page.interactionMu.Unlock()
	el, err := page.elementLocked(operationCtx, ref)
	if err != nil {
		return err
	}
	if err := scrollElementNaturally(operationCtx, el); err != nil {
		return err
	}
	target, err := page.elementPoint(operationCtx, el)
	if err != nil {
		return err
	}
	if err := page.movePointer(page.page.Context(operationCtx), target.X, target.Y); err != nil {
		return err
	}
	return dispatchAtomicClick(page.page.Context(operationCtx), target.X, target.Y)
}

// Hover 将真实指针移动到快照元素的可交互坐标。移动轨迹由 movePointer
// 内部生成，属于浏览器执行细节，不向调用方单独报告。
func (page *rodPage) Hover(ctx context.Context, ref ElementRef) error {
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	if err := lockInteraction(operationCtx, &page.interactionMu); err != nil {
		return err
	}
	defer page.interactionMu.Unlock()
	el, err := page.elementLocked(operationCtx, ref)
	if err != nil {
		return err
	}
	if err := scrollElementNaturally(operationCtx, el); err != nil {
		return err
	}
	target, err := page.elementPoint(operationCtx, el)
	if err != nil {
		return err
	}
	return page.movePointer(page.page.Context(operationCtx), target.X, target.Y)
}

type elementPointResult struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Visible bool    `json:"visible"`
	Enabled bool    `json:"enabled"`
	Covered bool    `json:"covered"`
}

func (page *rodPage) elementPoint(ctx context.Context, element *rod.Element) (elementPointResult, error) {
	result, err := element.Evaluate(rod.Eval(`function() {
		const rect = this.getBoundingClientRect();
		const style = getComputedStyle(this);
		const x = rect.left + rect.width / 2;
		const y = rect.top + rect.height / 2;
		const hit = document.elementFromPoint(x, y);
		return {
			x, y,
			visible: !this.hidden && !this.closest('[hidden],[aria-hidden="true"]') &&
				style.display !== 'none' && style.visibility !== 'hidden' &&
				style.opacity !== '0' && style.pointerEvents !== 'none' &&
				rect.width > 0 && rect.height > 0,
			enabled: !this.disabled && this.getAttribute('aria-disabled') !== 'true',
			covered: !hit || !(hit === this || this.contains(hit)),
		};
	}`).ByUser())
	if err != nil {
		return elementPointResult{}, err
	}
	var target elementPointResult
	if err := result.Value.Unmarshal(&target); err != nil {
		return elementPointResult{}, err
	}
	if !target.Visible || !target.Enabled || target.Covered || validateInputPoint(target.X, target.Y) != nil {
		return elementPointResult{}, newError("element_not_interactable", "元素没有可交互的页面区域", "重新调用 browserSnapshot 获取当前 ref", true)
	}
	return target, nil
}

// scrollElementNaturally 将目标滚动拆成最多两段，每段之间留出短暂的
// renderer 时间。它只操作页面滚动容器，不向调用方暴露中间滚动事件。
func scrollElementNaturally(ctx context.Context, element *rod.Element) error {
	for step := 0; step < 2; step++ {
		fraction := 0.6
		if step == 1 {
			fraction = 1
		}
		result, err := element.Evaluate(rod.Eval(`function() {
			const rect = this.getBoundingClientRect();
			const viewportHeight = window.innerHeight || document.documentElement.clientHeight;
			const viewportWidth = window.innerWidth || document.documentElement.clientWidth;
			const margin = Math.min(48, Math.max(12, Math.min(viewportHeight, viewportWidth) * 0.05));
			const visible = rect.top >= margin && rect.bottom <= viewportHeight - margin &&
				rect.left >= margin && rect.right <= viewportWidth - margin;
			if (visible) return false;
			let container = document.scrollingElement;
			let node = this.parentElement;
			while (node && node !== document.body) {
				const style = getComputedStyle(node);
				if ((style.overflowY === 'auto' || style.overflowY === 'scroll' || style.overflowY === 'overlay') && node.scrollHeight > node.clientHeight ||
					(style.overflowX === 'auto' || style.overflowX === 'scroll' || style.overflowX === 'overlay') && node.scrollWidth > node.clientWidth) {
					container = node;
					break;
				}
				node = node.parentElement;
			}
			const containerRect = container === document.scrollingElement ?
				{top: 0, left: 0, height: viewportHeight, width: viewportWidth} : container.getBoundingClientRect();
			const deltaY = (rect.top + rect.height / 2) - (containerRect.top + containerRect.height / 2);
			const deltaX = (rect.left + rect.width / 2) - (containerRect.left + containerRect.width / 2);
			container.scrollBy({top: deltaY * ` + fmt.Sprintf("%g", fraction) + `, left: deltaX * ` + fmt.Sprintf("%g", fraction) + `, behavior: 'auto'});
			return true;
		}`).ByUser())
		if err != nil {
			return err
		}
		if !result.Value.Bool() {
			return nil
		}
		if err := pauseContext(ctx, 35*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

func pauseContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (page *rodPage) movePointer(client proto.Client, x, y float64) error {
	if err := validateInputPoint(x, y); err != nil {
		return err
	}
	page.pointerMu.Lock()
	startX, startY := page.pointerX, page.pointerY
	if !page.pointerKnown {
		startX, startY = 0, 0
	}
	page.pointerMu.Unlock()
	distance := math.Hypot(x-startX, y-startY)
	steps := int(distance/80) + 3
	if steps < 3 {
		steps = 3
	} else if steps > 24 {
		steps = 24
	}
	// 固定的二次贝塞尔控制点提供平滑、可复现的轻微弧线，不引入随机数。
	controlX := (startX+x)/2 - (y-startY)*0.12
	controlY := (startY+y)/2 + (x-startX)*0.12
	released := 0
	for index := 1; index <= steps; index++ {
		t := float64(index) / float64(steps)
		eased := t * t * (3 - 2*t)
		oneMinus := 1 - eased
		pointX := oneMinus*oneMinus*startX + 2*oneMinus*eased*controlX + eased*eased*x
		pointY := oneMinus*oneMinus*startY + 2*oneMinus*eased*controlY + eased*eased*y
		event := proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved, X: pointX, Y: pointY, Button: proto.InputMouseButtonNone, Buttons: &released, PointerType: proto.InputDispatchMouseEventPointerTypeMouse}
		if err := event.Call(client); err != nil {
			return err
		}
	}
	page.pointerMu.Lock()
	page.pointerX, page.pointerY, page.pointerKnown = x, y, true
	page.pointerMu.Unlock()
	return nil
}

func dispatchAtomicClick(client proto.Client, x, y float64) error {
	pressed, released := 1, 0
	move := proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved, X: x, Y: y, Button: proto.InputMouseButtonNone, Buttons: &released, PointerType: proto.InputDispatchMouseEventPointerTypeMouse}
	if err := move.Call(client); err != nil {
		return err
	}
	down := proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, X: x, Y: y, Button: proto.InputMouseButtonLeft, Buttons: &pressed, ClickCount: 1, PointerType: proto.InputDispatchMouseEventPointerTypeMouse}
	if err := down.Call(client); err != nil {
		return err
	}
	up := proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseReleased, X: x, Y: y, Button: proto.InputMouseButtonLeft, Buttons: &released, ClickCount: 1, PointerType: proto.InputDispatchMouseEventPointerTypeMouse}
	return up.Call(client)
}

func (page *rodPage) Type(ctx context.Context, ref ElementRef, text string, clear bool) error {
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	if err := lockInteraction(operationCtx, &page.interactionMu); err != nil {
		return err
	}
	defer page.interactionMu.Unlock()
	el, err := page.elementLocked(operationCtx, ref)
	if err != nil {
		return err
	}
	if err := scrollElementNaturally(operationCtx, el); err != nil {
		return err
	}
	target, err := page.elementPoint(operationCtx, el)
	if err != nil {
		return err
	}
	if err := page.movePointer(page.page.Context(operationCtx), target.X, target.Y); err != nil {
		return err
	}
	// 滚动、聚焦和选区在一次 JS 任务中完成，文本再通过一个独立 CDP 命令
	// 插入；整个路径不依赖动画帧、重绘或 Rod Element 交互包装。
	prepare := `function() {
		this.focus({preventScroll: true});
		if (` + fmt.Sprintf("%t", clear) + `) {
			if (typeof this.select === 'function') this.select();
			else {
				const range = document.createRange();
				range.selectNodeContents(this);
				const selection = window.getSelection();
				selection.removeAllRanges();
				selection.addRange(range);
			}
		}
	}`
	if _, err := el.Evaluate(rod.Eval(prepare).ByUser()); err != nil {
		return err
	}
	if err := (proto.InputInsertText{Text: text}).Call(page.page.Context(operationCtx)); err != nil {
		return err
	}
	_, _ = el.Evaluate(rod.Eval(`function() {
		this.dispatchEvent(new Event('input', {bubbles: true, composed: true}));
	}`).ByUser())
	return nil
}

func (page *rodPage) Press(ctx context.Context, key string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	keys := map[string]input.Key{"Enter": input.Enter, "Tab": input.Tab, "Escape": input.Escape, "Backspace": input.Backspace, "ArrowUp": input.ArrowUp, "ArrowDown": input.ArrowDown, "ArrowLeft": input.ArrowLeft, "ArrowRight": input.ArrowRight, "Space": input.Space}
	keyValue, ok := keys[key]
	if !ok {
		return newError("invalid_key", "不支持的按键", "使用 Enter、Tab、Escape、Backspace、方向键或 Space", true)
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	if err := lockInteraction(operationCtx, &page.interactionMu); err != nil {
		return err
	}
	defer page.interactionMu.Unlock()
	err = page.syncState(operationCtx)
	if err != nil {
		return err
	}
	client := page.page.Context(operationCtx)
	if err := keyValue.Encode(proto.InputDispatchKeyEventTypeKeyDown, keyValue.Modifier()).Call(client); err != nil {
		return err
	}
	return keyValue.Encode(proto.InputDispatchKeyEventTypeKeyUp, 0).Call(client)
}

func (page *rodPage) Wait(ctx context.Context, options WaitOptions) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	supported := map[string]bool{
		"selector_visible": true, "selector_hidden": true,
		"url_contains": true, "title_contains": true,
		"text_contains": true, "load_complete": true,
	}
	if !supported[options.Condition] {
		return newError("unsupported_wait", "不支持的等待条件", "使用 selector_visible、selector_hidden、url_contains、title_contains、text_contains 或 load_complete", true)
	}
	if options.Condition != "load_complete" && options.Selector == "" {
		return errors.New("等待条件需要 ref_or_selector")
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	deadline := time.NewTicker(25 * time.Millisecond)
	defer deadline.Stop()
	for {
		if err := contextErr(operationCtx); err != nil {
			return err
		}
		result, err := page.page.Context(operationCtx).Eval(`(condition, value) => {
			const visible = element => {
				if (!element) return false;
				const style = getComputedStyle(element);
				const rect = element.getBoundingClientRect();
				return !element.hidden && !element.closest('[hidden],[aria-hidden="true"]') &&
					style.display !== 'none' && style.visibility !== 'hidden' &&
					style.opacity !== '0' && rect.width > 0 && rect.height > 0;
			};
			switch (condition) {
			case 'selector_visible': return visible(document.querySelector(value));
			case 'selector_hidden': return !visible(document.querySelector(value));
			case 'url_contains': return location.href.includes(value);
			case 'title_contains': return document.title.includes(value);
			case 'text_contains': return (document.body?.innerText || '').includes(value);
			case 'load_complete': return document.readyState === 'complete';
			default: return false;
			}
		}`, options.Condition, options.Selector)
		if err == nil && result.Value.Bool() {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-operationCtx.Done():
			return operationCtx.Err()
		case <-deadline.C:
		}
	}
}

func (page *rodPage) WaitIdle(ctx context.Context, timeout time.Duration) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	maxTimeout := page.operationTimeout
	if maxTimeout <= 0 {
		maxTimeout = defaultOperationTimeout
	}
	if timeout <= 0 || timeout > maxTimeout {
		timeout = maxTimeout
	}
	return page.page.Context(operationCtx).WaitIdle(timeout)
}

func (page *rodPage) syncState(ctx context.Context) error {
	result, err := page.page.Context(ctx).Eval(`() => {
		const state = window.__browserkit_page_state_v1__;
		return state ? {document_id: state.documentId} : null;
	}`)
	if err != nil {
		return err
	}
	var state *domState
	if err := result.Value.Unmarshal(&state); err != nil {
		return err
	}
	if state == nil || state.DocumentID == "" {
		result, err = page.page.Context(ctx).Eval(pageStateObserverEval)
		if err != nil {
			return err
		}
		state = &domState{}
		if err := result.Value.Unmarshal(state); err != nil {
			return err
		}
	}
	page.stateMu.Lock()
	defer page.stateMu.Unlock()
	if page.stateKnown && state.DocumentID != page.documentID {
		page.revision++
		page.refs = make(map[string]struct{})
	}
	page.documentID = state.DocumentID
	page.stateKnown = true
	return nil
}

func (page *rodPage) Screenshot(ctx context.Context, options ScreenshotOptions) (Screenshot, error) {
	if err := contextErr(ctx); err != nil {
		return Screenshot{}, err
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return Screenshot{}, err
	}
	defer operationCancel()
	// Chrome 新版的 headless target 在刚创建或导航后可能还不是 active page；
	// 先显式激活 target，避免 Page.captureScreenshot 返回 attach 错误。
	if err := page.activate(operationCtx); err != nil {
		return Screenshot{}, err
	}
	data, err := page.page.Context(operationCtx).Screenshot(options.FullPage, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return Screenshot{}, err
	}
	return Screenshot{Data: data, Format: "png"}, nil
}

func (page *rodPage) StartScreencast(ctx context.Context, options ScreencastOptions) (<-chan Screenshot, func(), error) {
	if err := contextErr(ctx); err != nil {
		return nil, nil, err
	}
	// 连续画面的订阅本身是由调用方 stop/ctx 结束的长生命周期操作，不能
	// 套用单次 30 秒默认时限；启动、激活、停止和每一帧 ack 仍分别通过
	// page.ctx 获得有界 context。
	streamCtx, cancel := context.WithCancel(ctx)
	frames := make(chan Screenshot, 1)
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			// 先解除本地事件订阅，再有界提交远端 stop。同一 Page 可能因
			// viewport 变化或 WebSocket 重连立即启动下一条 screencast；stop
			// 不能放到后台，否则旧 stop 可能晚于新 start 到达 Chrome，并把
			// 新流静默停掉。这里最多占用 live goroutine 500ms，不持有页面交互锁。
			cancel()
			stopCtx, stopCancel, err := page.controlCtx(context.Background(), screencastStopTimeout)
			if err != nil {
				return
			}
			defer stopCancel()
			_ = (proto.PageStopScreencast{}).Call(page.page.Context(stopCtx))
		})
	}
	quality := options.Quality
	if quality <= 0 || quality > 100 {
		quality = 100
	}
	// Rod 当前未提供 screencast 启停和帧确认的高层 API。
	start := proto.PageStartScreencast{Format: proto.PageStartScreencastFormatJpeg, Quality: &quality}
	if options.MaxWidth > 0 {
		start.MaxWidth = &options.MaxWidth
	}
	if options.MaxHeight > 0 {
		start.MaxHeight = &options.MaxHeight
	}
	if options.EveryNthFrame > 0 {
		start.EveryNthFrame = &options.EveryNthFrame
	}
	streamPage := page.page.Context(streamCtx)
	var callbackMu sync.Mutex
	wait := streamPage.EachEvent(func(event *proto.PageScreencastFrame) {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		frame := Screenshot{Data: event.Data, Format: "jpeg"}
		// 只保留最新一帧，避免 CDP 生产速度超过消费速度时积压旧画面。
		select {
		case <-frames:
		default:
		}
		select {
		case frames <- frame:
		default:
		}
		ackCtx, ackCancel, err := page.controlCtx(streamCtx, screencastAckTimeout)
		if err == nil {
			_ = (proto.PageScreencastFrameAck{SessionID: event.SessionID}).Call(page.page.Context(ackCtx))
			ackCancel()
		}
	})
	setupCtx, setupCancel, err := page.ctx(streamCtx)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if err := page.activate(setupCtx); err != nil {
		setupCancel()
		cancel()
		return nil, nil, err
	}
	if err := start.Call(page.page.Context(setupCtx)); err != nil {
		setupCancel()
		cancel()
		return nil, nil, err
	}
	setupCancel()
	go func() {
		wait()
		callbackMu.Lock()
		close(frames)
		callbackMu.Unlock()
	}()
	return frames, stop, nil
}

// DispatchInput 使用 CDP Input 域向页面发送真实浏览器输入，不执行页面 JavaScript。
func (page *rodPage) DispatchInput(ctx context.Context, event InputEvent) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	if err := lockInteraction(operationCtx, &page.interactionMu); err != nil {
		return err
	}
	defer page.interactionMu.Unlock()
	client := page.page.Context(operationCtx)
	switch event.Kind {
	case "mouse":
		typeValue, err := mouseEventType(event.Action)
		if err != nil {
			return err
		}
		button, err := mouseButton(event.Button)
		if err != nil {
			return err
		}
		pointerType := proto.InputDispatchMouseEventPointerTypeMouse
		if event.PointerType == "pen" {
			pointerType = proto.InputDispatchMouseEventPointerTypePen
		} else if event.PointerType != "" && event.PointerType != "mouse" {
			return fmt.Errorf("不支持的鼠标 pointer_type: %q", event.PointerType)
		}
		if err := validateInputPoint(event.X, event.Y); err != nil {
			return err
		}
		buttons := event.Buttons
		if event.Action == "down" && buttons == 0 {
			buttons = mouseButtonMask(button)
		}
		err = (proto.InputDispatchMouseEvent{
			Type: typeValue, X: event.X, Y: event.Y, DeltaX: event.DeltaX, DeltaY: event.DeltaY,
			Button: button, Buttons: &buttons, ClickCount: event.ClickCount, Modifiers: event.Modifiers,
			PointerType: pointerType,
		}).Call(client)
		if err == nil {
			page.pointerMu.Lock()
			page.pointerX, page.pointerY, page.pointerKnown = event.X, event.Y, true
			page.pointerMu.Unlock()
		}
		return err
	case "touch":
		typeValue, err := touchEventType(event.Action)
		if err != nil {
			return err
		}
		points := make([]*proto.InputTouchPoint, 0, len(event.Touches))
		for _, touch := range event.Touches {
			if err := validateInputPoint(touch.X, touch.Y); err != nil {
				return err
			}
			id := float64(touch.ID)
			point := &proto.InputTouchPoint{X: touch.X, Y: touch.Y, ID: &id}
			if touch.RadiusX > 0 {
				point.RadiusX = &touch.RadiusX
			}
			if touch.RadiusY > 0 {
				point.RadiusY = &touch.RadiusY
			}
			if touch.Force > 0 {
				point.Force = &touch.Force
			}
			points = append(points, point)
		}
		if (event.Action == "start" || event.Action == "move") && len(points) == 0 {
			return errors.New("touch start/move 必须包含活动触点")
		}
		if (event.Action == "end" || event.Action == "cancel") && len(points) != 0 {
			return errors.New("touch end/cancel 不能包含活动触点")
		}
		return (proto.InputDispatchTouchEvent{Type: typeValue, TouchPoints: points, Modifiers: event.Modifiers}).Call(client)
	case "key":
		typeValue := proto.InputDispatchKeyEventTypeKeyUp
		if event.Action == "down" {
			typeValue = proto.InputDispatchKeyEventTypeKeyDown
			if event.Text == "" {
				typeValue = proto.InputDispatchKeyEventTypeRawKeyDown
			}
		} else if event.Action != "up" {
			return fmt.Errorf("不支持的键盘 action: %q", event.Action)
		}
		location := event.Location
		// KeyEvent 携带 code、location、native key code 等 Rod Keyboard API
		// 无法完整表达的字段，必须保留 CDP Input.dispatchKeyEvent。
		return (proto.InputDispatchKeyEvent{
			Type: typeValue, Modifiers: event.Modifiers, Text: event.Text, UnmodifiedText: event.Text,
			Code: event.Code, Key: event.Key, WindowsVirtualKeyCode: event.KeyCode,
			NativeVirtualKeyCode: event.KeyCode, AutoRepeat: event.AutoRepeat,
			IsKeypad: event.Location == 3, IsSystemKey: event.Modifiers&1 != 0, Location: &location,
		}).Call(client)
	case "shortcut":
		return dispatchShortcut(client, event.Action, event.Modifiers)
	case "composition":
		if event.SelectionStart < 0 || event.SelectionEnd < event.SelectionStart {
			return errors.New("输入法选区无效")
		}
		// Rod 没有输入法 composition 包装，只能调用 CDP Input.imeSetComposition。
		return (proto.InputImeSetComposition{
			Text: event.Text, SelectionStart: event.SelectionStart, SelectionEnd: event.SelectionEnd,
		}).Call(client)
	case "text":
		if event.Text == "" {
			return nil
		}
		return (proto.InputInsertText{Text: event.Text}).Call(client)
	default:
		return fmt.Errorf("不支持的浏览器输入 kind: %q", event.Kind)
	}
}

func dispatchShortcut(client proto.Client, action string, modifiers int) error {
	key, err := shortcutInputKey(action)
	if err != nil {
		return err
	}
	modifierKey := input.ControlLeft
	switch modifiers {
	case 0:
		if input.IsMac {
			modifierKey = input.MetaLeft
		}
	case input.ModifierControl:
	case input.ModifierMeta:
		modifierKey = input.MetaLeft
	default:
		return errors.New("快捷键只允许 Ctrl 或 Meta 修饰键")
	}
	modifier := modifierKey.Modifier()
	modifierUp := modifierKey.Encode(proto.InputDispatchKeyEventTypeKeyUp, 0)
	if err := modifierKey.Encode(proto.InputDispatchKeyEventTypeKeyDown, modifier).Call(client); err != nil {
		return err
	}
	keyDown := key.Encode(proto.InputDispatchKeyEventTypeKeyDown, modifier)
	keyDown.Text = ""
	keyDown.Commands = []string{shortcutInputCommand(action)}
	if err := keyDown.Call(client); err != nil {
		_ = modifierUp.Call(client)
		return err
	}
	if err := key.Encode(proto.InputDispatchKeyEventTypeKeyUp, modifier).Call(client); err != nil {
		_ = modifierUp.Call(client)
		return err
	}
	return modifierUp.Call(client)
}

func shortcutInputCommand(action string) string {
	switch action {
	case "select_all":
		return "selectAll"
	case "copy":
		return "copy"
	case "paste":
		return "paste"
	default:
		return ""
	}
}

func mouseButtonMask(button proto.InputMouseButton) int {
	switch button {
	case proto.InputMouseButtonLeft:
		return 1
	case proto.InputMouseButtonRight:
		return 2
	case proto.InputMouseButtonMiddle:
		return 4
	case proto.InputMouseButtonBack:
		return 8
	case proto.InputMouseButtonForward:
		return 16
	default:
		return 0
	}
}

func shortcutInputKey(action string) (input.Key, error) {
	switch action {
	case "select_all":
		return input.Key('a'), nil
	case "copy":
		return input.Key('c'), nil
	case "paste":
		return input.Key('v'), nil
	default:
		return 0, fmt.Errorf("不支持的浏览器快捷键: %q", action)
	}
}

func mouseEventType(action string) (proto.InputDispatchMouseEventType, error) {
	switch action {
	case "down":
		return proto.InputDispatchMouseEventTypeMousePressed, nil
	case "up":
		return proto.InputDispatchMouseEventTypeMouseReleased, nil
	case "move":
		return proto.InputDispatchMouseEventTypeMouseMoved, nil
	case "wheel":
		return proto.InputDispatchMouseEventTypeMouseWheel, nil
	default:
		return "", fmt.Errorf("不支持的鼠标 action: %q", action)
	}
}

func touchEventType(action string) (proto.InputDispatchTouchEventType, error) {
	switch action {
	case "start":
		return proto.InputDispatchTouchEventTypeTouchStart, nil
	case "end":
		return proto.InputDispatchTouchEventTypeTouchEnd, nil
	case "move":
		return proto.InputDispatchTouchEventTypeTouchMove, nil
	case "cancel":
		return proto.InputDispatchTouchEventTypeTouchCancel, nil
	default:
		return "", fmt.Errorf("不支持的 touch action: %q", action)
	}
}

func mouseButton(value string) (proto.InputMouseButton, error) {
	if value == "" {
		return proto.InputMouseButtonNone, nil
	}
	switch button := proto.InputMouseButton(value); button {
	case proto.InputMouseButtonNone, proto.InputMouseButtonLeft, proto.InputMouseButtonMiddle,
		proto.InputMouseButtonRight, proto.InputMouseButtonBack, proto.InputMouseButtonForward:
		return button, nil
	default:
		return "", fmt.Errorf("不支持的鼠标 button: %q", value)
	}
}

func validateInputPoint(x, y float64) error {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) || x < 0 || y < 0 {
		return errors.New("浏览器输入坐标无效")
	}
	return nil
}

func (page *rodPage) Evaluate(ctx context.Context, code string, options EvaluateOptions) (EvaluateResult, error) {
	if err := contextErr(ctx); err != nil {
		return EvaluateResult{}, err
	}
	if strings.TrimSpace(code) == "" {
		return EvaluateResult{}, errors.New("JavaScript 代码不能为空")
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return EvaluateResult{}, err
	}
	defer operationCancel()
	evalCtx := operationCtx
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		evalCtx, cancel = context.WithTimeout(operationCtx, options.Timeout)
		defer cancel()
	}
	// Rod Evaluate 只接受函数形式并固定若干 Runtime 参数，无法保持本接口
	// 的任意表达式、AwaitPromise、CSP 和 command-line API 语义，因此这里
	// 使用 Runtime.evaluate；调用仍绑定 evalCtx 的独立超时。
	result, err := (proto.RuntimeEvaluate{
		Expression:                  code,
		IncludeCommandLineAPI:       true,
		ReturnByValue:               true,
		AwaitPromise:                options.AwaitPromise,
		UserGesture:                 true,
		AllowUnsafeEvalBlockedByCSP: true,
	}).Call(page.page.Context(evalCtx))
	if err != nil {
		return EvaluateResult{}, err
	}
	if result.ExceptionDetails != nil {
		return EvaluateResult{}, fmt.Errorf("JavaScript 执行失败: %s", result.ExceptionDetails.Text)
	}
	if result.Result == nil {
		return EvaluateResult{}, errors.New("JavaScript 没有返回值")
	}
	if result.Result.UnserializableValue != "" {
		return EvaluateResult{}, newError("unserializable_result", "JavaScript 返回值不可序列化", "只返回 JSON、字符串、数字、布尔值或 null", true)
	}
	value, err := json.Marshal(result.Result.Value)
	if err != nil || !json.Valid(value) {
		return EvaluateResult{}, newError("unserializable_result", "JavaScript 返回值不是有效 JSON", "不要返回 DOM 节点、函数或循环引用对象", true)
	}
	return EvaluateResult{Value: value, Type: string(result.Result.Type)}, nil
}

func (page *rodPage) ListAssets(ctx context.Context, options ListAssetsOptions) (ListAssetsResult, error) {
	if err := contextErr(ctx); err != nil {
		return ListAssetsResult{}, err
	}
	if options.Limit <= 0 || options.Limit > 500 {
		return ListAssetsResult{}, errors.New("资源列表 limit 必须位于 1 到 500")
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return ListAssetsResult{}, err
	}
	defer operationCancel()
	encodedHost, _ := json.Marshal(strings.TrimSpace(options.Host))
	expression := `(function(host, limit) {
		const wanted = String(host || '').toLowerCase();
		const entries = performance.getEntriesByType('resource');
		const result = [];
		for (const item of entries) {
			if (!item || !item.name) continue;
			let url;
			try { url = new URL(item.name, location.href); } catch { continue; }
			if (wanted && url.hostname.toLowerCase() !== wanted) continue;
			const path = url.pathname.split('/').filter(Boolean);
			result.push({
				name: path[path.length - 1] || url.hostname,
				url: url.href,
				initiator_type: item.initiatorType || '',
				response_status: Number(item.responseStatus) || 0,
				transfer_bytes: Number(item.transferSize) || 0,
				encoded_bytes: Number(item.encodedBodySize) || 0,
				decoded_bytes: Number(item.decodedBodySize) || 0,
				duration_ms: Number(item.duration) || 0
			});
		}
		return result.slice(-Math.max(1, Math.min(501, Number(limit) || 100)));
	})(` + string(encodedHost) + `,` + fmt.Sprint(options.Limit+1) + `)`
	result, err := (proto.RuntimeEvaluate{Expression: expression, ReturnByValue: true}).Call(page.page.Context(operationCtx))
	if err != nil {
		return ListAssetsResult{}, err
	}
	if result.ExceptionDetails != nil || result.Result == nil {
		return ListAssetsResult{}, errors.New("读取页面资源列表失败")
	}
	value, err := json.Marshal(result.Result.Value)
	if err != nil {
		return ListAssetsResult{}, err
	}
	var assets []Asset
	if err := json.Unmarshal(value, &assets); err != nil {
		return ListAssetsResult{}, fmt.Errorf("解码页面资源列表: %w", err)
	}
	resultValue := ListAssetsResult{Assets: assets, Truncated: len(assets) > options.Limit}
	if resultValue.Truncated {
		resultValue.Assets = assets[len(assets)-options.Limit:]
	}
	return resultValue, nil
}

func (page *rodPage) AddScriptToEvaluateOnNewDocument(ctx context.Context, script string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(script) == "" {
		return errors.New("注入 JavaScript 不能为空")
	}
	operationCtx, operationCancel, err := page.ctx(ctx)
	if err != nil {
		return err
	}
	defer operationCancel()
	// EvalOnNewDocument 是 Rod 对 Page.addScriptToEvaluateOnNewDocument 的
	// 包装；这里只需安装脚本，不需要向调用方暴露 remove 回调。
	_, err = page.page.Context(operationCtx).EvalOnNewDocument(script)
	return err
}

func (page *rodPage) Close(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	operationCtx, operationCancel, err := page.controlCtx(ctx, targetCloseTimeout)
	if err != nil {
		return err
	}
	defer operationCancel()
	// Rod Page.Close 会在持有全局 targetsLock 时等待 targetDestroyed，锁等待
	// 本身不能被 context 取消。Target.closeTarget 是单个有界 CDP 请求。
	_, err = (proto.TargetCloseTarget{TargetID: page.page.TargetID}).Call(page.page.Browser().Context(operationCtx))
	if err != nil {
		return err
	}
	page.page.Browser().RemoveState(page.page.TargetID)
	page.release()
	return nil
}

func contextWithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if err := contextErr(ctx); err != nil {
		return nil, nil, err
	}
	if timeout <= 0 {
		timeout = defaultOperationTimeout
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	return operationCtx, cancel, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return errors.New("browserkit context 不能为空")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// EncodeScreenshot 是给不支持二进制 Tool Result 的宿主使用的有限辅助函数。
func EncodeScreenshot(data []byte) string { return base64.StdEncoding.EncodeToString(data) }
