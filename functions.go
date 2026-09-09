package browserkit

import (
	"context"
	"errors"
	"time"
)

// Function 描述一个可由宿主封装成工具的浏览器函数。
// 请求与响应沿用根包的命名结构体，宿主可据此生成并校验 JSON Schema。
type Function struct {
	// Name 是模型调用时使用的稳定名称。
	Name string
	// Description 是供模型理解用途和约束的中文说明。
	Description string
	// Handler 的实际类型总是 func(context.Context, Request) (Response, error)。
	// Request 和 Response 均为导出的命名结构体；可交给宿主的函数工具构造器。
	Handler any
}

// Functions 返回绑定当前 Manager 的浏览器函数清单。
// 默认提供 14 个函数；AllowEvaluate 增加脚本执行，AllowDevtools 增加三个诊断函数。
// 每次返回独立切片，宿主可以筛选；同一 Manager 的操作与 HTTP/React 共用 session 和 Tab。
// 请求解码、输入/输出 schema 校验、审批、进度和模型协议由宿主工具框架处理。
func (manager *Manager) Functions() []Function {
	if manager == nil {
		return nil
	}
	functions := []Function{
		{Name: "browserOpen", Description: "启动可复用浏览器 session 并打开一个 HTTP/HTTPS 页面；返回前等待页面空闲，并在 load_status 中报告 loaded 或 timeout。返回 session_id 和 page_id；需要图片时再调用 browserScreenshot。完成操作后用 browserClose 释放 session。", Handler: manager.openFunction},
		{Name: "browserTabs", Description: "列出浏览器 session 中的网页 Tab，并返回当前激活的 page_id。", Handler: manager.tabsFunction},
		{Name: "browserActivateTab", Description: "激活指定浏览器 Tab，让后续直播画面和输入切换到该 Tab。", Handler: manager.activateTabFunction},
		{Name: "browserCloseTab", Description: "关闭浏览器 session 中指定的网页 Tab。", Handler: manager.closeTabFunction},
		{Name: "browserNavigate", Description: "在已有浏览器 page 中导航到 HTTP/HTTPS 地址，返回前等待页面空闲。", Handler: manager.Navigate},
		{Name: "browserSnapshot", Description: "获取页面文本摘要和绑定 revision 的可交互元素引用；先同步 Tab 状态。省略 page_id 时使用最新 active page，并返回 active_page_id、tabs_revision、tabs_changed 和 Tab 摘要；支持 max_elements 限量和 offset 分页，下一页使用 next_offset。", Handler: manager.Snapshot},
		{Name: "browserClick", Description: "点击指定 page_id 页面快照中的元素引用。ref 只能用于生成它的 page_id。", Handler: manager.Click},
		{Name: "browserHover", Description: "将指针移动到指定 page_id 的快照元素。ref 只能用于生成它的页面。", Handler: manager.Hover},
		{Name: "browserType", Description: "向指定 page_id 页面元素输入文本。ref 只能用于生成它的 page_id。默认覆盖原值，append=true 时追加。响应不会回显输入内容。", Handler: manager.Type},
		{Name: "browserPress", Description: "向指定 page_id 页面发送白名单按键。", Handler: manager.Press},
		{Name: "browserWait", Description: "等待指定 page_id 页面的状态。selector_visible/selector_hidden 可直接使用 browserSnapshot 返回的 ref 或 CSS selector；也支持 url_contains、title_contains、text_contains 和 load_complete。", Handler: manager.Wait},
		{Name: "browserScreenshot", Description: "把指定 page_id 页面截图保存为宿主可读取的 PNG 文件。", Handler: manager.Screenshot},
		{Name: "browserAssets", Description: "读取指定 page_id 当前页面已经观测到的资源列表；返回脚本、样式、图片、字体和 XHR/Fetch 等资源的名称、实际地址、发起类型、状态、大小与耗时；可用 host 过滤域名。不会主动爬取未访问页面。", Handler: manager.ListAssets},
		{Name: "browserClose", Description: "显式关闭指定浏览器 session。", Handler: manager.CloseSession},
	}
	if manager.config.AllowEvaluate {
		functions = append(functions, Function{Name: "browserEvaluate", Description: "在指定页面的 console 域执行一段 JavaScript，并返回有限 JSON 值。", Handler: manager.Evaluate})
	}
	if manager.config.AllowDevtools {
		functions = append(functions,
			Function{Name: "browserDevtoolsOpen", Description: "为指定 page_id 按需开启 Console、运行时异常和 Network 元数据采集；重复调用不会清空当前采集。", Handler: manager.OpenDevtools},
			Function{Name: "browserDevtoolsRead", Description: "读取指定 page_id 的有界开发诊断记录；支持 generation、after_sequence、kinds 和 limit 增量读取。", Handler: manager.ReadDevtools},
			Function{Name: "browserDevtoolsClose", Description: "停止指定 page_id 的开发诊断采集，并保留当前有界记录供最后读取。", Handler: manager.CloseDevtools},
		)
	}
	return functions
}

// 启动、导航和等待共用一次操作超时。
func (manager *Manager) openFunction(ctx context.Context, request OpenRequest) (OpenResponse, error) {
	if err := contextErr(ctx); err != nil {
		return OpenResponse{}, err
	}
	timeout := manager.config.Session.OperationTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return manager.Open(ctx, request)
}

// 宿主 UI 可以省略目标；模型函数必须明确 session，避免误操作另一个会话。
func (manager *Manager) tabsFunction(ctx context.Context, request TabsRequest) (TabsResponse, error) {
	if err := contextErr(ctx); err != nil {
		return TabsResponse{}, err
	}
	if request.SessionID == "" {
		return TabsResponse{}, errors.New("browserTabs 需要 session_id")
	}
	return manager.ListTabs(ctx, request)
}

func (manager *Manager) activateTabFunction(ctx context.Context, request TabRequest) (TabResponse, error) {
	if err := contextErr(ctx); err != nil {
		return TabResponse{}, err
	}
	if err := requirePage(request.SessionID, request.PageID, "browserActivateTab"); err != nil {
		return TabResponse{}, err
	}
	return manager.ActivateTab(ctx, request)
}

func (manager *Manager) closeTabFunction(ctx context.Context, request TabRequest) (CloseTabResponse, error) {
	if err := contextErr(ctx); err != nil {
		return CloseTabResponse{}, err
	}
	if err := requirePage(request.SessionID, request.PageID, "browserCloseTab"); err != nil {
		return CloseTabResponse{}, err
	}
	return manager.CloseTab(ctx, request)
}
