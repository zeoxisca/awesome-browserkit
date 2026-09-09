# @browserkit/react

`BrowserView` 连接 BrowserKit HTTP 服务，提供页面画面、Tab 管理、输入和开发诊断。
需要 React 19。

## 安装

组件尚未发布到 npm。在 BrowserKit 仓库根目录生成安装包：

```sh
npm ci --prefix react
make pack
```

在使用组件的 React 项目中安装，路径替换为安装包实际位置：

```sh
npm install /path/to/awesome-browserkit/react/browserkit-react-0.1.0.tgz
```

组件要求 React 19。尚未安装时执行：

```sh
npm install react@^19 react-dom@^19
```

## 基本用法

指定 HTTP 服务入口和容器高度。以下示例启用诊断面板，需要 Go 端同时配置 `AllowDevtools: true`：

```tsx
import { BrowserView } from '@browserkit/react'
import '@browserkit/react/style.css'

export function Workspace() {
  return <BrowserView
    endpoint="/browser"
    devtools
    className="my-browser"
    style={{ width: '100%', height: 640 }}
    onError={(error) => console.error(error.message)}
  />
}
```

## 组件参数

| 参数 | 说明 |
| --- | --- |
| `endpoint` | 必填，指向 browserhttp 挂载地址；支持相对路径或完整 URL |
| `className`、`style` | 根节点外观及容器尺寸 |
| `classNames` | `toolbar`、`tabs`、`tab`、`viewport`、`devtools` 的自定义 class |
| `busy` | 默认 false；暂停页面交互，保留控制方的关闭入口 |
| `onClose` | 用户请求关闭时立即回调，宿主可先收起容器 |
| `readOnly` | 默认 false；观察模式，不接管、不提交操作 |
| `controls` | 区域及按钮配置；默认全部显示，`false` 隐藏外层内置控制区 |
| `input` | 页面输入配置；默认全部允许，`false` 停止所有页面输入 |
| `devtools` | 默认 false；也可传诊断配置对象，服务端必须授权 |
| `liveView` | 默认 true；可以只显示诊断 |
| `viewport` | 默认 `"container"`；也可传 `{ width, height }` 固定远端尺寸 |
| `viewerID` | 默认每实例生成；同一实例重连保持身份 |
| `onStateChange` | 浏览器完整状态更新回调 |
| `onError` | 请求或操作错误回调 |

同一应用页面可以嵌入多个组件实例。
`endpoint` 变化时，组件清理上一目标的连接、输入和画面。
只有控制方调整远端 viewport，观察者按容器缩放已有图片。
请求使用 Cookie 凭据；跨源部署需要服务端明确配置允许的 Origin。

## 布局与样式

默认样式单独导出，作用域限制在 `.browserkit` 内。
不使用 Shadow DOM 或全局 portal，可选择不导入默认 CSS，完全自行编写。

```css
.my-browser {
  --browser-background: #f3f7f1;
  --browser-toolbar-background: #e4eee0;
  --browser-border: #c9d6c3;
  --browser-radius: 16px;
  --browser-color: #213326;
  --browser-font: system-ui, sans-serif;
}
.my-browser [data-part="toolbar"] { padding-inline: 16px; }
```

组件聚焦时，F12 切换诊断面板；工具栏按钮提供相同入口。
诊断面板目前支持 Console、Network 和 Resources，不提供 Elements 编辑和 JavaScript 断点调试。
宿主浏览器保留的快捷键可能无法覆盖。

## 开发与验证

在 BrowserKit 仓库根目录执行：

```sh
npm ci --prefix react
npm test --prefix react
npm run build --prefix react
```

构建产物包含 ESM、TypeScript 类型及独立 CSS。
仓库内的消费者可使用 `file:` 依赖，外部项目按安装章节使用生成的 `.tgz` 文件。
`prepack` 自动构建；仓库的 UI 和示例必须先构建库再构建消费者。

## 按客户端配置

所有字段均可在运行中更新；只调整按钮和输入时不会重新连接或打开浏览器。

```tsx
// 移动端：单页显示、隐藏关闭浏览器入口、显式唤起键盘。
<BrowserView
  endpoint="/browser"
  controls={{ tabs: false, closeBrowser: false }}
  input={{ keyboardActivation: 'manual' }}
  style={{ height: 560 }}
/>

// 仅显示页面画面，保留页面操作。
<BrowserView endpoint="/browser" controls={false} />

// 观察端：服务端 /observer 同时设置 Options.ReadOnly。
<BrowserView endpoint="/observer" controls={false} input={false} readOnly />
```

`controls` 的所有字段默认 true：

| 字段 | 控制内容 |
| --- | --- |
| `tabs` | 整个 Tab 栏，含新建及关闭 Tab 按钮 |
| `toolbar` | 整个工具栏，含地址框、刷新、前往、关闭和诊断按钮 |
| `addressBar` | 页面地址输入框；隐藏时也不展示前往按钮 |
| `newTab` / `closeTab` | 新建 / 关闭 Tab 按钮 |
| `reload` / `navigate` | 刷新 / 前往按钮；隐藏前往按钮后地址栏仍可回车导航 |
| `closeBrowser` | 关闭整个浏览器按钮 |
| `devtools` / `devtoolsFloating` | 工具栏诊断按钮 / 画面内悬浮诊断入口 |
| `keyboard` | 键盘唤起和收起按钮；隐藏不等于禁用键盘输入 |
| `takeover` / `controlStatus` | 获取操作权按钮 / 观察模式提示文字，可分别关闭 |

父区域关闭时，其内部按钮不会显示；工具栏没有可显示内容时自动移除，不留空栏。
`controls` 控制外层界面；诊断面板内部按钮及 F12 由 `devtools` 配置。
只读权限由 Go Handler 的 ReadOnly 控制，诊断授权由 Manager 配置；组件开关仅控制界面。

`input` 的布尔字段默认 true：

| 字段 | 控制内容 |
| --- | --- |
| `mouse` | 鼠标、触控笔及拖动 |
| `touch` | 触摸输入；关闭时允许手势滚动宿主页面 |
| `wheel` | 滚轮输入；关闭时允许滚动宿主页面 |
| `keyboard` | 实体键盘、输入法及软键盘；false 会移除输入框和键盘入口 |
| `clipboard` | 页面复制、粘贴快捷键和本地粘贴事件 |
| `keyboardActivation` | `auto`（默认）点击画面聚焦输入框；`manual` 仅键盘按钮唤起输入法 |

`manual` 下实体键盘仍可操作已聚焦的画布，移动端输入法由按钮触发。
触摸沿用显式键盘按钮，不因点击远端页面中的输入框而自动弹出系统键盘。
软键盘是否弹出最终由客户端 WebView/浏览器决定。
`input={false}` 不影响 Tab 和地址栏，观察端请同时使用 `readOnly`。

## 定制开发诊断

```tsx
const [open, setOpen] = useState(false)

<BrowserView
  endpoint="/browser"
  controls={{ devtools: false, devtoolsFloating: false }}
  devtools={{
    panels: ['console', 'network'],
    open,
    onOpenChange: setOpen,
    shortcut: false,
    captureControl: false,
    resizable: false,
  }}
/>
// 宿主自己的按钮调用 setOpen(true) 打开面板。
```

| 字段 | 默认值与用途 |
| --- | --- |
| `panels` | 全部 Console / Network / Resources；按数组顺序展示，空数组关闭诊断 |
| `defaultOpen` | false，非受控模式下的初始展开状态 |
| `open` / `onOpenChange` | 受控展开状态与用户请求回调，省略 open 时由组件维护 |
| `shortcut` | true，是否响应 F12 |
| `resizable` | true，是否允许拖动调整面板高度 |
| `captureControl` | true，是否显示开始/停止采集按钮 |
| `closeButton` | true，是否显示诊断面板关闭按钮 |

仅关闭面板不会停止已经开启的诊断采集。
类型 `BrowserControls`、`BrowserInputOptions`、`BrowserDevtoolsOptions` 和 `BrowserDevtoolsTab` 可从包根导入。

## 许可证

[Apache-2.0](LICENSE)。
