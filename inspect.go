package browserkit

import (
	"context"
	"encoding/json"
	"errors"
)

// InspectPointRequest 查询页面视口坐标处的元素，不向页面发送输入。
type InspectPointRequest struct {
	SessionID string  `json:"session_id"`
	PageID    string  `json:"page_id"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
}

// InspectPointJSON 供不直接依赖选区类型的宿主插件调用同一 ref 登记流程。
func (manager *Manager) InspectPointJSON(ctx context.Context, sessionID, pageID string, x, y float64) (json.RawMessage, error) {
	element, err := manager.InspectPoint(ctx, InspectPointRequest{SessionID: sessionID, PageID: pageID, X: x, Y: y})
	if err != nil {
		return nil, err
	}
	return json.Marshal(element)
}

// ElementBounds 是页面视口内的元素边框。
type ElementBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// InspectedElement 带有可用于后续 Tool 操作的页面引用。
type InspectedElement struct {
	Ref      string        `json:"ref"`
	Revision uint64        `json:"revision"`
	Role     string        `json:"role"`
	Name     string        `json:"name,omitempty"`
	URL      string        `json:"url"`
	Bounds   ElementBounds `json:"bounds"`
}

type pointInspector interface {
	InspectPoint(context.Context, float64, float64) (InspectedElement, error)
}

// InspectPoint 在真实页面命中元素并登记 ref；不会触发点击、滚动或键盘输入。
func (manager *Manager) InspectPoint(ctx context.Context, request InspectPointRequest) (InspectedElement, error) {
	if request.PageID == "" {
		return InspectedElement{}, errors.New("browserInspectPoint 需要 page_id")
	}
	if err := validateInputPoint(request.X, request.Y); err != nil {
		return InspectedElement{}, err
	}
	current, target, err := manager.get(ctx, request.SessionID, request.PageID)
	if err != nil {
		return InspectedElement{}, err
	}
	inspector, ok := target.page.(pointInspector)
	if !ok {
		return InspectedElement{}, errors.New("当前浏览器页面不支持元素选择")
	}
	element, err := inspector.InspectPoint(ctx, request.X, request.Y)
	if err != nil {
		return InspectedElement{}, err
	}
	current.mu.Lock()
	if target.refs == nil || target.refRevision != element.Revision {
		target.refs = make(map[string]struct{})
		target.refRevision = element.Revision
	}
	target.refs[element.Ref] = struct{}{}
	current.mu.Unlock()
	return element, nil
}
