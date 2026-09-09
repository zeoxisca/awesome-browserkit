/** 浏览器区域和按钮的显示配置，未指定的字段默认显示。 */
export interface BrowserControls {
  /** Tab 栏；隐藏后同时隐藏栏内的新建、关闭按钮。 */
  tabs?: boolean
  /** 工具栏；隐藏后同时隐藏地址栏及工具栏内按钮。 */
  toolbar?: boolean
  /** 页面地址输入框。 */
  addressBar?: boolean
  /** 新建 Tab 按钮。 */
  newTab?: boolean
  /** 各 Tab 的关闭按钮。 */
  closeTab?: boolean
  /** 刷新按钮。 */
  reload?: boolean
  /** 地址栏的前往按钮；地址栏回车仍可导航。 */
  navigate?: boolean
  /** 关闭整个浏览器的按钮。 */
  closeBrowser?: boolean
  /** 工具栏中的开发诊断按钮。 */
  devtools?: boolean
  /** 画面内悬浮的开发诊断入口。 */
  devtoolsFloating?: boolean
  /** 唤起或收起页面键盘的按钮。 */
  keyboard?: boolean
  /** 获取操作权按钮。 */
  takeover?: boolean
  /** 观察模式提示文字；不影响独立配置的获取操作权按钮。 */
  controlStatus?: boolean
}

/** 页面输入配置，未指定的输入类型默认允许；不影响地址栏和 Tab 操作。 */
export interface BrowserInputOptions {
  /** 鼠标和触控笔输入。 */
  mouse?: boolean
  /** 触摸输入；关闭时允许手势滚动宿主页面。 */
  touch?: boolean
  /** 鼠标滚轮；关闭时允许滚动宿主页面。 */
  wheel?: boolean
  /** 实体键盘、输入法和软键盘输入；关闭时移除键盘入口。 */
  keyboard?: boolean
  /** 页面复制、粘贴快捷键与本地粘贴事件。 */
  clipboard?: boolean
  /** auto 沿用点击画面聚焦输入框的行为；manual 仅由键盘按钮唤起输入法。 */
  keyboardActivation?: 'auto' | 'manual'
}

/** 可展示的开发诊断面板。 */
export type BrowserDevtoolsTab = 'console' | 'network' | 'resources'

/** 开发诊断配置；传入对象即启用。 */
export interface BrowserDevtoolsOptions {
  /** 面板及排列顺序；默认全部，空数组关闭开发诊断。 */
  panels?: readonly BrowserDevtoolsTab[]
  /** 初始展开状态，默认 false。 */
  defaultOpen?: boolean
  /** 受控展开状态，可由宿主按钮控制。 */
  open?: boolean
  /** 用户请求展开或关闭时回调，受控模式由宿主更新 open。 */
  onOpenChange?: (open: boolean) => void
  /** 是否响应 F12，默认 true。 */
  shortcut?: boolean
  /** 是否允许拖动调整高度，默认 true。 */
  resizable?: boolean
  /** 是否显示开始/停止采集按钮，默认 true。 */
  captureControl?: boolean
  /** 是否显示诊断面板关闭按钮，默认 true。 */
  closeButton?: boolean
}
