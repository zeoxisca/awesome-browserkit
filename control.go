package browserkit

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// ViewerHeader 携带当前观看者身份；它不是认证凭据。
	ViewerHeader               = "X-Browserkit-Viewer"
	browserTakeoverCooldown    = 30 * time.Second
	browserControllerReconnect = 5 * time.Second
	maxBrowserViewerIDLength   = 128
)

// ControlState 是服务端确定的观看者操作权视图。
type ControlState struct {
	Type         string `json:"type"`
	Controller   bool   `json:"controller"`
	CanTakeover  bool   `json:"can_takeover"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
	Revision     uint64 `json:"revision"`
}

type controlManager struct {
	mu               sync.Mutex
	sessions         map[string]*browserControlSession
	takeoverCooldown time.Duration
	reconnectGrace   time.Duration
	now              func() time.Time
}

type browserControlSession struct {
	owner         string
	revision      uint64
	nextOrder     uint64
	lastTakeover  time.Time
	viewers       map[string]*browserControlViewer
	subscribers   map[chan struct{}]struct{}
	cooldownTimer *time.Timer
}

type browserControlViewer struct {
	writableConnections int
	connections         int
	order               uint64
	dropTimer           *time.Timer
}

// TakeoverCooldownError 表示操作权仍处于接管冷却期。
type TakeoverCooldownError struct {
	retryAfter time.Duration
}

func (err *TakeoverCooldownError) Error() string {
	seconds := int64((err.retryAfter + time.Second - 1) / time.Second)
	return fmt.Sprintf("浏览器操作权正在冷却，请在 %d 秒后重试", seconds)
}

func newControl() *controlManager {
	return &controlManager{
		sessions:         make(map[string]*browserControlSession),
		takeoverCooldown: browserTakeoverCooldown,
		reconnectGrace:   browserControllerReconnect,
		now:              time.Now,
	}
}

func validateBrowserViewerID(value string) error {
	if strings.TrimSpace(value) == "" || len(value) > maxBrowserViewerIDLength {
		return errors.New("浏览器 viewer 标识无效")
	}
	return nil
}

// join 把一条直播连接加入 Session。首个 viewer 自动获得操作权；同一
// viewer 的重连会取消延迟释放，并通过返回的通知通道接收操作权变化。
func (manager *controlManager) Join(sessionID string, viewerID string) (<-chan struct{}, func(), error) {
	return manager.join(sessionID, viewerID, false)
}

// Observe 注册只能观看的连接，不参与控制权分配。
func (manager *controlManager) Observe(sessionID, viewerID string) (<-chan struct{}, func(), error) {
	return manager.join(sessionID, viewerID, true)
}
func (manager *controlManager) join(sessionID, viewerID string, readOnly bool) (<-chan struct{}, func(), error) {
	if err := validateBrowserViewerID(viewerID); err != nil {
		return nil, nil, err
	}
	manager.mu.Lock()
	session := manager.sessionLocked(sessionID)
	viewer := session.viewers[viewerID]
	if viewer == nil {
		session.nextOrder++
		viewer = &browserControlViewer{order: session.nextOrder}
		session.viewers[viewerID] = viewer
	}
	if viewer.dropTimer != nil {
		viewer.dropTimer.Stop()
		viewer.dropTimer = nil
	}
	viewer.connections++
	if !readOnly {
		viewer.writableConnections++
	}
	updates := make(chan struct{}, 1)
	session.subscribers[updates] = struct{}{}
	if session.owner == viewerID && viewer.writableConnections == 0 {
		session.owner = manager.firstConnectedLocked(session)
		session.revision++
		manager.notifyLocked(session)
	}
	if session.owner == "" && viewer.writableConnections > 0 {
		session.owner = viewerID
		session.revision++
		manager.notifyLocked(session)
	} else {
		select {
		case updates <- struct{}{}:
		default:
		}
	}
	manager.mu.Unlock()

	var once sync.Once
	leave := func() {
		once.Do(func() { manager.leave(sessionID, viewerID, updates, readOnly) })
	}
	return updates, leave, nil
}

// leave 移除一条直播连接。控制方暂时断线时保留一个短重连窗口，避免页面
// 刷新或网络抖动立即把正在进行的操作交给其他窗口。
func (manager *controlManager) leave(sessionID string, viewerID string, updates chan struct{}, readOnly bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session := manager.sessions[sessionID]
	if session == nil {
		return
	}
	delete(session.subscribers, updates)
	viewer := session.viewers[viewerID]
	if viewer == nil {
		return
	}
	if !readOnly && viewer.writableConnections > 0 {
		viewer.writableConnections--
	}
	if viewer.connections > 0 {
		viewer.connections--
	}
	if viewer.connections > 0 {
		if session.owner == viewerID && viewer.writableConnections == 0 {
			session.owner = manager.firstConnectedLocked(session)
			session.revision++
			manager.notifyLocked(session)
		}
		return
	}
	if session.owner != viewerID {
		delete(session.viewers, viewerID)
		manager.cleanupLocked(sessionID, session)
		return
	}
	if manager.reconnectGrace <= 0 {
		manager.dropControllerLocked(sessionID, session, viewerID)
		return
	}
	viewer.dropTimer = time.AfterFunc(manager.reconnectGrace, func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		current := manager.sessions[sessionID]
		if current == nil {
			return
		}
		candidate := current.viewers[viewerID]
		if candidate == nil || candidate.connections > 0 {
			return
		}
		if current.owner == viewerID {
			manager.dropControllerLocked(sessionID, current, viewerID)
			return
		}
		delete(current.viewers, viewerID)
		manager.cleanupLocked(sessionID, current)
	})
}

func (manager *controlManager) dropControllerLocked(sessionID string, session *browserControlSession, viewerID string) {
	if viewer := session.viewers[viewerID]; viewer != nil && viewer.dropTimer != nil {
		viewer.dropTimer.Stop()
	}
	delete(session.viewers, viewerID)
	session.owner = manager.firstConnectedLocked(session)
	session.revision++
	manager.notifyLocked(session)
	manager.cleanupLocked(sessionID, session)
}

func (manager *controlManager) firstConnectedLocked(session *browserControlSession) string {
	var selected string
	var order uint64
	for viewerID, viewer := range session.viewers {
		if viewer.writableConnections == 0 || viewer.connections == 0 || selected != "" && viewer.order >= order {
			continue
		}
		selected, order = viewerID, viewer.order
	}
	return selected
}

// takeover 显式转移操作权。冷却时间限制窗口之间反复抢占；返回状态始终
// 以 Manager 当前事实计算，不接受客户端提交的 revision 或 owner。
func (manager *controlManager) Takeover(sessionID string, viewerID string) (ControlState, error) {
	if err := validateBrowserViewerID(viewerID); err != nil {
		return ControlState{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session := manager.sessions[sessionID]
	if session == nil || session.viewers[viewerID] == nil || session.viewers[viewerID].connections == 0 || session.viewers[viewerID].writableConnections == 0 {
		return ControlState{}, errors.New("当前 viewer 没有连接浏览器直播")
	}
	if session.owner == viewerID {
		return manager.messageLocked(session, viewerID), nil
	}
	now := manager.now()
	if retry := manager.retryAfterLocked(session, now); retry > 0 {
		return manager.messageLocked(session, viewerID), &TakeoverCooldownError{retryAfter: retry}
	}
	session.owner = viewerID
	session.lastTakeover = now
	session.revision++
	manager.scheduleCooldownLocked(sessionID, session, now)
	manager.notifyLocked(session)
	return manager.messageLocked(session, viewerID), nil
}

func (manager *controlManager) scheduleCooldownLocked(sessionID string, session *browserControlSession, takeoverAt time.Time) {
	if session.cooldownTimer != nil {
		session.cooldownTimer.Stop()
	}
	if manager.takeoverCooldown <= 0 {
		return
	}
	session.cooldownTimer = time.AfterFunc(manager.takeoverCooldown, func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		current := manager.sessions[sessionID]
		if current == nil || !current.lastTakeover.Equal(takeoverAt) {
			return
		}
		current.revision++
		manager.notifyLocked(current)
	})
}

func (manager *controlManager) Allowed(sessionID string, viewerID string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session := manager.sessions[sessionID]
	return session != nil && viewerID != "" && session.owner == viewerID && session.viewers[viewerID] != nil && session.viewers[viewerID].writableConnections > 0
}

func (manager *controlManager) State(sessionID string, viewerID string) ControlState {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	session := manager.sessions[sessionID]
	if session == nil {
		return ControlState{Type: "browser_control"}
	}
	return manager.messageLocked(session, viewerID)
}

func (manager *controlManager) messageLocked(session *browserControlSession, viewerID string) ControlState {
	viewer := session.viewers[viewerID]
	writable := viewer != nil && viewer.writableConnections > 0
	controller := session.owner == viewerID && writable
	retry := manager.retryAfterLocked(session, manager.now())
	return ControlState{
		Type: "browser_control", Controller: controller,
		CanTakeover:  writable && !controller && retry <= 0,
		RetryAfterMS: max(int64((retry+time.Millisecond-1)/time.Millisecond), 0),
		Revision:     session.revision,
	}
}

func (manager *controlManager) retryAfterLocked(session *browserControlSession, now time.Time) time.Duration {
	if session.lastTakeover.IsZero() || manager.takeoverCooldown <= 0 {
		return 0
	}
	return max(session.lastTakeover.Add(manager.takeoverCooldown).Sub(now), 0)
}

func (manager *controlManager) sessionLocked(sessionID string) *browserControlSession {
	session := manager.sessions[sessionID]
	if session == nil {
		session = &browserControlSession{
			viewers:     make(map[string]*browserControlViewer),
			subscribers: make(map[chan struct{}]struct{}),
		}
		manager.sessions[sessionID] = session
	}
	return session
}

func (manager *controlManager) notifyLocked(session *browserControlSession) {
	for updates := range session.subscribers {
		select {
		case updates <- struct{}{}:
		default:
		}
	}
}

func (manager *controlManager) cleanupLocked(sessionID string, session *browserControlSession) {
	if len(session.viewers) != 0 || len(session.subscribers) != 0 || session.owner != "" {
		return
	}
	if session.cooldownTimer != nil {
		session.cooldownTimer.Stop()
	}
	delete(manager.sessions, sessionID)
}

// Controls 返回本 Manager 唯一的观看者操作权状态。
func (manager *Manager) controls() *controlManager {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.control == nil {
		manager.control = newControl()
	}
	return manager.control
}

// close 停止连接宽限计时器；Manager 结束后所有控制状态立即失效。
func (manager *controlManager) close() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, s := range manager.sessions {
		if s.cooldownTimer != nil {
			s.cooldownTimer.Stop()
		}
		for _, v := range s.viewers {
			if v.dropTimer != nil {
				v.dropTimer.Stop()
			}
		}
	}
	manager.sessions = make(map[string]*browserControlSession)
}

// JoinViewer 注册一个观看者连接，返回状态变化通知及幂等释放函数。
// readOnly 为 true 的连接不会获得操作权。
func (manager *Manager) JoinViewer(viewerID string, readOnly bool) (<-chan struct{}, func(), error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.stopped {
		return nil, nil, errors.New("浏览器运行时已经关闭")
	}
	if manager.control == nil {
		manager.control = newControl()
	}
	if readOnly {
		return manager.control.Observe("", viewerID)
	}
	return manager.control.Join("", viewerID)
}

// ViewerControl 返回指定观看者当前的操作权视图。
func (manager *Manager) ViewerControl(viewerID string) ControlState {
	return manager.controls().State("", viewerID)
}

// CanControl 判断观看者是否拥有当前浏览器的操作权。
func (manager *Manager) CanControl(viewerID string) bool {
	return manager.controls().Allowed("", viewerID)
}

// TakeControl 显式接管浏览器，并使先前排队的输入失效。
func (manager *Manager) TakeControl(viewerID string) (ControlState, error) {
	state, err := manager.controls().Takeover("", viewerID)
	if err != nil {
		return state, err
	}
	manager.mu.Lock()
	sessions := make([]*session, 0, len(manager.sessions))
	for _, current := range manager.sessions {
		sessions = append(sessions, current)
	}
	manager.mu.Unlock()
	for _, current := range sessions {
		current.mu.Lock()
		invalidateInputsLocked(current)
		current.mu.Unlock()
	}
	return state, nil
}
