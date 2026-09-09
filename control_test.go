package browserkit

import (
	"errors"
	"testing"
	"time"
)

func TestBrowserControlFirstViewerOwnsAndTakeoverHasCooldown(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	manager := newControl()
	manager.now = func() time.Time { return now }
	manager.reconnectGrace = 0

	_, leaveA, err := manager.Join("session-a", "viewer-a")
	if err != nil {
		t.Fatal(err)
	}
	defer leaveA()
	_, leaveB, err := manager.Join("session-a", "viewer-b")
	if err != nil {
		t.Fatal(err)
	}
	defer leaveB()
	if !manager.State("session-a", "viewer-a").Controller || manager.State("session-a", "viewer-b").Controller {
		t.Fatal("首个 viewer 应独占浏览器操作权")
	}

	state, err := manager.Takeover("session-a", "viewer-b")
	if err != nil || !state.Controller {
		t.Fatalf("viewer-b takeover=%+v err=%v", state, err)
	}
	if manager.Allowed("session-a", "viewer-a") || !manager.Allowed("session-a", "viewer-b") {
		t.Fatal("接管后旧控制者仍可操作或新控制者未生效")
	}
	_, err = manager.Takeover("session-a", "viewer-a")
	var cooldown *TakeoverCooldownError
	if !errors.As(err, &cooldown) || cooldown.retryAfter != 30*time.Second {
		t.Fatalf("连续接管没有被 30 秒冷却拒绝: %T %v", err, err)
	}

	now = now.Add(30 * time.Second)
	state, err = manager.Takeover("session-a", "viewer-a")
	if err != nil || !state.Controller {
		t.Fatalf("冷却结束后 takeover=%+v err=%v", state, err)
	}
}

func TestBrowserControlPromotesEarliestObserverAfterControllerLeaves(t *testing.T) {
	manager := newControl()
	manager.reconnectGrace = 0
	_, leaveA, _ := manager.Join("session-a", "viewer-a")
	_, leaveB, _ := manager.Join("session-a", "viewer-b")
	_, leaveC, _ := manager.Join("session-a", "viewer-c")
	defer leaveB()
	defer leaveC()

	leaveA()
	if !manager.State("session-a", "viewer-b").Controller || manager.State("session-a", "viewer-c").Controller {
		t.Fatal("控制者离开后没有按加入顺序选择最早观察者")
	}
}

func TestBrowserControlNotifiesObserverAfterReconnectGrace(t *testing.T) {
	manager := newControl()
	manager.reconnectGrace = 10 * time.Millisecond
	_, leaveA, _ := manager.Join("session-a", "viewer-a")
	updatesB, leaveB, _ := manager.Join("session-a", "viewer-b")
	defer leaveB()
	select {
	case <-updatesB:
	default:
		t.Fatal("观察者没有收到初始控制状态")
	}

	leaveA()
	select {
	case <-updatesB:
		if !manager.State("session-a", "viewer-b").Controller {
			t.Fatal("断线宽限结束后通知到达，但最早观察者没有成为控制者")
		}
	case <-time.After(time.Second):
		t.Fatal("控制者断线后没有通知最早观察者")
	}
}

func TestBrowserControlKeepsControllerWhenViewerReconnectsWithinGrace(t *testing.T) {
	manager := newControl()
	manager.reconnectGrace = 100 * time.Millisecond
	_, leaveA, err := manager.Join("session-a", "viewer-a")
	if err != nil {
		t.Fatal(err)
	}
	_, leaveB, err := manager.Join("session-a", "viewer-b")
	if err != nil {
		t.Fatal(err)
	}
	defer leaveB()

	leaveA()
	_, leaveReconnected, err := manager.Join("session-a", "viewer-a")
	if err != nil {
		t.Fatal(err)
	}
	defer leaveReconnected()
	time.Sleep(150 * time.Millisecond)

	if !manager.State("session-a", "viewer-a").Controller || manager.State("session-a", "viewer-b").Controller {
		t.Fatal("控制者在宽限期内重连后丢失了操作权")
	}
}

func TestBrowserControlReadOnlyReconnectDoesNotRetainOwnership(t *testing.T) {
	manager := newControl()
	defer manager.close()
	_, leaveWriter, err := manager.Join("s", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	_, leaveObserver, err := manager.Observe("s", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer leaveObserver()
	leaveWriter()
	if manager.Allowed("s", "viewer") || manager.State("s", "viewer").CanTakeover {
		t.Fatal("仅剩只读连接时仍具有操作权")
	}
	_, leaveNewWriter, err := manager.Join("s", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer leaveNewWriter()
	if !manager.Allowed("s", "viewer") {
		t.Fatal("恢复可写连接后没有获得操作权")
	}
}

func TestBrowserControlReadOnlyReconnectDuringGraceReleasesOwnership(t *testing.T) {
	manager := newControl()
	defer manager.close()
	_, leave, _ := manager.Join("s", "viewer")
	leave()
	_, leaveObserver, err := manager.Observe("s", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer leaveObserver()
	if manager.Allowed("s", "viewer") || manager.State("s", "viewer").Controller {
		t.Fatal("只读重连不能继承宽限期操作权")
	}
}
