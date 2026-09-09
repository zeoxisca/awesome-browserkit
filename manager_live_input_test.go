package browserkit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestManagerCoalescesQueuedTrackpadSamplesBeforeCDP(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{
		URL: "https://example.com", Viewport: Viewport{Width: 800, Height: 600},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	session.page.inputBlock = blocked
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "mouse", Action: "wheel", X: 10, Y: 10, DeltaY: 1}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current := manager.sessions[opened.SessionID]
		current.mu.Lock()
		started := current.inputCancel != nil
		current.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("首条滚动没有开始投递")
		}
		time.Sleep(time.Millisecond)
	}
	for index := 2; index <= 31; index++ {
		if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "mouse", Action: "wheel", X: float64(index), Y: 10, DeltaY: 1}); err != nil {
			t.Fatal(err)
		}
	}
	close(blocked)

	deadline = time.Now().Add(time.Second)
	for {
		session.page.inputMu.Lock()
		inputs := append([]InputEvent(nil), session.page.inputs...)
		session.page.inputMu.Unlock()
		if len(inputs) >= 2 {
			if len(inputs) != 2 || inputs[0].DeltaY != 1 || inputs[1].DeltaY != 30 || inputs[1].X != 31 {
				t.Fatalf("高频滚动没有收敛为首条和最新聚合: %+v", inputs)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待聚合滚动投递超时: %+v", inputs)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerCoalescesQueuedTouchMovesBeforeCDP(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{
		URL: "https://example.com", Viewport: Viewport{Width: 800, Height: 600},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	session.page.inputBlock = blocked
	start := InputEvent{Kind: "touch", Action: "start", Touches: []InputTouchPoint{{ID: 7, X: 10, Y: 10}}}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, start); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current := manager.sessions[opened.SessionID]
		current.mu.Lock()
		started := current.inputCancel != nil
		current.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("触摸开始没有进入 CDP 投递")
		}
		time.Sleep(time.Millisecond)
	}
	for index := 2; index <= 31; index++ {
		move := InputEvent{Kind: "touch", Action: "move", Touches: []InputTouchPoint{{ID: 7, X: float64(index), Y: 10}}}
		if err := manager.DispatchLiveInput(context.Background(), opened.PageID, move); err != nil {
			t.Fatal(err)
		}
	}
	end := InputEvent{Kind: "touch", Action: "end"}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, end); err != nil {
		t.Fatal(err)
	}
	close(blocked)

	deadline = time.Now().Add(time.Second)
	for {
		session.page.inputMu.Lock()
		inputs := append([]InputEvent(nil), session.page.inputs...)
		session.page.inputMu.Unlock()
		if len(inputs) >= 3 {
			if len(inputs) != 3 || inputs[0].Action != "start" || inputs[1].Action != "move" || inputs[1].Touches[0].X != 31 || inputs[2].Action != "end" {
				t.Fatalf("高频触摸没有收敛为开始、最新位置和结束: %+v", inputs)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待聚合触摸投递超时: %+v", inputs)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerDoesNotEvictShortcutFromFullMoveQueue(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{
		URL: "https://example.com", Viewport: Viewport{Width: 800, Height: 600},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	session.page.inputBlock = blocked
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "mouse", Action: "move", X: 1, Y: 1}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current := manager.sessions[opened.SessionID]
		current.mu.Lock()
		started := current.inputCancel != nil
		current.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("首条移动没有开始投递")
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "shortcut", Action: "copy", Modifiers: 4}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < liveInputQueueSize+32; index++ {
		if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "mouse", Action: "move", X: float64(index + 2), Y: 1}); err != nil {
			t.Fatal(err)
		}
	}
	close(blocked)

	deadline = time.Now().Add(time.Second)
	for {
		session.page.inputMu.Lock()
		inputs := append([]InputEvent(nil), session.page.inputs...)
		session.page.inputMu.Unlock()
		for _, event := range inputs {
			if event.Kind == "shortcut" && event.Action == "copy" && event.Modifiers == 4 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("满载移动队列丢失了快捷键: %+v", inputs)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCoalesceLiveInputsKeepsLatestPointerMove(t *testing.T) {
	page := &page{id: "page-a"}
	queue := make(chan liveInput, 3)
	queue <- liveInput{page: page, epoch: 1, event: InputEvent{Kind: "mouse", Action: "move", X: 20, Y: 30}}
	queue <- liveInput{page: page, epoch: 1, event: InputEvent{Kind: "mouse", Action: "move", X: 40, Y: 50}}
	queue <- liveInput{page: page, epoch: 1, event: InputEvent{Kind: "mouse", Action: "up", X: 40, Y: 50}}

	result, pending := coalesceLiveInputs(liveInput{page: page, epoch: 1, event: InputEvent{Kind: "mouse", Action: "move", X: 10, Y: 15}}, queue)
	if result.event.X != 40 || result.event.Y != 50 {
		t.Fatalf("移动没有收敛到最新坐标: %+v", result.event)
	}
	if pending == nil || pending.event.Action != "up" {
		t.Fatalf("离散事件没有保持在移动之后: %+v", pending)
	}
}

func TestCoalesceLiveInputsKeepsLatestTouchMove(t *testing.T) {
	page := &page{id: "page-a"}
	queue := make(chan liveInput, 3)
	queue <- liveInput{page: page, epoch: 1, event: InputEvent{Kind: "touch", Action: "move", Touches: []InputTouchPoint{{ID: 7, X: 20, Y: 30}}}}
	queue <- liveInput{page: page, epoch: 1, event: InputEvent{Kind: "touch", Action: "move", Touches: []InputTouchPoint{{ID: 7, X: 40, Y: 50}}}}
	queue <- liveInput{page: page, epoch: 1, event: InputEvent{Kind: "touch", Action: "end"}}

	result, pending := coalesceLiveInputs(liveInput{page: page, epoch: 1, event: InputEvent{Kind: "touch", Action: "move", Touches: []InputTouchPoint{{ID: 7, X: 10, Y: 15}}}}, queue)
	if len(result.event.Touches) != 1 || result.event.Touches[0].X != 40 || result.event.Touches[0].Y != 50 {
		t.Fatalf("触摸移动没有收敛到最新位置: %+v", result.event)
	}
	if pending == nil || pending.event.Action != "end" {
		t.Fatalf("触摸结束没有保持在移动之后: %+v", pending)
	}
}

func TestCoalesceLiveInputsAccumulatesTrackpadWheel(t *testing.T) {
	page := &page{id: "page-a"}
	queue := make(chan liveInput, 3)
	queue <- liveInput{page: page, epoch: 1, event: InputEvent{Kind: "mouse", Action: "wheel", X: 11, Y: 21, DeltaX: 2, DeltaY: 4}}
	queue <- liveInput{page: page, epoch: 1, event: InputEvent{Kind: "mouse", Action: "wheel", X: 12, Y: 22, DeltaX: -1, DeltaY: 8}}
	queue <- liveInput{page: page, epoch: 1, event: InputEvent{Kind: "key", Action: "down", Key: "Escape"}}

	result, pending := coalesceLiveInputs(liveInput{page: page, epoch: 1, event: InputEvent{Kind: "mouse", Action: "wheel", X: 10, Y: 20, DeltaX: 3, DeltaY: 5}}, queue)
	if result.event.DeltaX != 4 || result.event.DeltaY != 17 || result.event.X != 12 || result.event.Y != 22 {
		t.Fatalf("触摸板滚动没有正确聚合: %+v", result.event)
	}
	if pending == nil || pending.event.Kind != "key" {
		t.Fatalf("滚动之后的按键没有保持顺序: %+v", pending)
	}
}

func TestCoalesceLiveInputsDoesNotCrossPageEpoch(t *testing.T) {
	page := &page{id: "page-a"}
	queue := make(chan liveInput, 1)
	queue <- liveInput{page: page, epoch: 2, event: InputEvent{Kind: "mouse", Action: "move", X: 90, Y: 90}}

	result, pending := coalesceLiveInputs(liveInput{page: page, epoch: 1, event: InputEvent{Kind: "mouse", Action: "move", X: 10, Y: 10}}, queue)
	if result.epoch != 1 || pending == nil || pending.epoch != 2 {
		t.Fatalf("合并跨越了输入 epoch: result=%+v pending=%+v", result, pending)
	}
}

func TestManagerRejectsDiscreteInputWithoutBlockingWhenQueueIsFull(t *testing.T) {
	manager, session := fakeManager(t, Config{AllowPrivateNetwork: true})
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{
		URL: "https://example.com", Viewport: Viewport{Width: 800, Height: 600},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	defer close(blocked)
	session.page.inputBlock = blocked
	if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "text", Text: "active"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current := manager.sessions[opened.SessionID]
		current.mu.Lock()
		started := current.inputCancel != nil
		current.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("首条离散输入没有开始投递")
		}
		time.Sleep(time.Millisecond)
	}
	for range liveInputQueueSize {
		if err := manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "text", Text: "queued"}); err != nil {
			t.Fatal(err)
		}
	}

	started := time.Now()
	err = manager.DispatchLiveInput(context.Background(), opened.PageID, InputEvent{Kind: "shortcut", Action: "copy", Modifiers: 4})
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("满队列使离散输入阻塞了 %s", elapsed)
	}
	var overloaded *Error
	if !errors.As(err, &overloaded) || overloaded.Kind != "input_overloaded" {
		t.Fatalf("满队列错误=%v", err)
	}
}
