import type { ReactNode } from 'react'

export const docPages = [
  { id: 'principles', href: '#/docs/principles', title: '工作原理' },
  { id: 'scenarios', href: '#/docs/scenarios', title: '使用场景示例' },
  { id: 'features', href: '#/docs/features', title: '特性说明' },
] as const

type Page = typeof docPages[number]['id']

function Section({ title, children }: { title: string; children: ReactNode }) {
  return <section className="docs-section"><h2>{title}</h2>{children}</section>
}
function Code({ children }: { children: string }) {
  return <pre className="docs-code" tabIndex={0}><code>{children}</code></pre>
}

function Principles() {
  return <>
    <Section title="浏览器运行与连接">
      <p>BrowserKit 在运行 Go 程序的机器上启动 Chrome / Chromium，或连接已有的 CDP 浏览器。网页脚本和网络请求在该浏览器中执行，页面状态也由该浏览器维护。React 组件接收并展示页面画面。</p>
      <div className="docs-flow" aria-label="浏览器控制与画面传输流程">
        <div><b>React / Agent</b><span>展示画面、提交操作</span></div><span aria-hidden="true">⇄</span>
        <div><b>Go Manager</b><span>管理会话、Tab 与策略</span></div><span aria-hidden="true">⇄</span>
        <div><b>Chrome / CDP</b><span>加载网页、执行交互</span></div>
      </div>
      <p>React 经 HTTP 和 WebSocket 访问 Handler；Go SDK 与本地 CLI 直接调用 Manager。browserkit-server 将同一个 HTTP Handler 作为独立服务运行。</p>
    </Section>
    <Section title="运行时与生命周期">
      <dl className="docs-definitions">
        <div><dt>Manager</dt><dd>管理浏览器会话、页面、图流、输入与开发诊断，供 SDK 与 HTTP Handler 使用。</dd></div>
        <div><dt>Session 与 Tab</dt><dd>一次打开返回 session_id 和 page_id。自动化操作用它们定位页面，前端默认展示当前活动会话与 Tab。</dd></div>
        <div><dt>宿主生命周期</dt><dd>Go 宿主在退出时调用 Manager.Close。React 卸载或网络断开仅释放连接，浏览器会话仍由 Go 端管理。</dd></div>
      </dl>
    </Section>
    <Section title="画面传输与输入处理">
      <ol><li>Chrome 产生页面帧，Go 管理图流订阅并通过 WebSocket 发送给组件。</li><li>组件在容器中绘制画面，把点击、滚轮、键盘与输入法事件传回 Go。</li><li>Go 检查页面身份和操作权，再将输入发给目标页面。</li><li>页面变化产生后续画面；慢连接跳过旧帧，避免持续堆积。</li></ol>
      <p>多个组件可以观看同一浏览器。操作权在观看者之间协调，观察者可请求接管；服务端只读入口拒绝操作与接管。</p>
    </Section>
    <Section title="自动化与开发诊断">
      <p>Agent 先读取 Snapshot，获取页面文字和元素 ref，再点击或输入。ref 绑定产生它的页面与快照版本；页面变化导致引用过期时，应重新读取快照。</p>
      <p>诊断通过 CDP 收集 Console、运行时异常和 Network 记录。前端的 F12 打开 BrowserKit 诊断面板，提供 Console、Network 和 Resources；目前不提供 Chrome 原生 DevTools 的 Elements 编辑和 JavaScript 断点调试。记录保留在有界缓冲区中，可分页读取。</p>
    </Section>
  </>
}

function Scenarios() {
  return <>
    <Section title="01 · 本地 Agent 浏览网页">
      <p>使用已登录的 Chrome 填写表单，或启动独立浏览器检查网页。</p>
      <div className="docs-example"><strong>给 Agent 的任务示例</strong><p>“用 BrowserKit 打开我的测试页面，检查搜索框是否可输入，搜索后确认结果标题，并保存截图。”</p></div>
      <p>Agent 按 Skill 中的约定保持 CLI 进程，复用同一浏览器会话。安装路径与接入步骤见<a href="#/quickstart">快速开始 · 本地 Skill</a>。</p>
    </Section>
    <Section title="02 · 在远程服务器上运行浏览器">
      <p>浏览器部署在服务器上，前端通过服务地址提供操作界面。启动命令见快速开始。</p>
      <Code>{`<BrowserView
  endpoint="https://browser.example.com/browser"
  style={{ width: '100%', height: 640 }}
/>`}</Code>
    </Section>
    <Section title="03 · 在业务应用中嵌入浏览器">
      <p>在业务应用的侧栏或分屏中放置浏览器。</p>
      <Code>{`// manager 由宿主创建，并在应用退出时 Close。
mux.Handle("/browser/", http.StripPrefix("/browser",
  browserhttp.NewHandler(manager, browserhttp.Options{})))`}</Code>
      <Code>{`<BrowserView
  endpoint="/browser"
  controls={{ tabs: false, closeBrowser: false }}
  input={{ keyboardActivation: 'manual' }}
  style={{ width: '100%', height: 560 }}
/>`}</Code>
      <p>示例隐藏标签栏和关闭浏览器按钮，保留页面操作，输入法由键盘按钮唤起。默认配置与自定义 CSS 可在<a href="#/">浏览器工作台</a>中实时预览。</p>
    </Section>
    <Section title="04 · 共享浏览器与只读观察">
      <p>用只读视图展示 Agent 的操作过程。观察入口与控制入口使用同一个 Manager。</p>
      <Code>{`mux.Handle("/observer/", http.StripPrefix("/observer",
  browserhttp.NewHandler(manager, browserhttp.Options{ReadOnly: true})))`}</Code>
      <Code>{`<BrowserView
  endpoint="/observer"
  readOnly
  controls={false}
  input={false}
  style={{ height: 360 }}
/>`}</Code>
      <p>前端 readOnly、controls 和 input 用于界面与输入体验；服务端 Options.ReadOnly 才负责拒绝修改请求。</p>
    </Section>
  </>
}

function Features() {
  return <>
    <Section title="页面操作与展示">
      <div className="docs-table"><table><thead><tr><th>能力</th><th>说明</th></tr></thead><tbody>
        <tr><td>Tab 管理</td><td>新建、激活、关闭 Tab，导航与刷新；自动化通过 SessionID / PageID 定位。</td></tr>
        <tr><td>页面输入</td><td>鼠标、触摸、滚轮、实体键盘、中文输入法与剪贴板；可分别启用或关闭。</td></tr>
        <tr><td>图流与截图</td><td>实时帧用于交互预览；Screenshot 生成 PNG 文件，JSON 结果提供文件路径与元信息。</td></tr>
        <tr><td>开发诊断</td><td>Console、Network、Resources。Go 需允许诊断，组件再决定面板、入口与 F12 行为。</td></tr>
        <tr><td>共享观看</td><td>多个组件连接同一 Manager，使用活动 Tab 与操作权协调；支持只读入口。</td></tr>
      </tbody></table></div>
    </Section>
    <Section title="客户端配置">
      <dl className="docs-definitions">
        <div><dt>controls</dt><dd>控制 Tab 栏、工具栏、地址栏、新建/关闭、刷新、键盘、诊断、接管等区域与按钮。false 隐藏外层控制区。</dd></div>
        <div><dt>input</dt><dd>分别配置 mouse、touch、wheel、keyboard、clipboard。keyboardActivation: 'manual' 由按钮唤起输入法；false 停止页面输入。</dd></div>
        <div><dt>devtools</dt><dd>选择 panels，配置 open / onOpenChange、shortcut、resizable、captureControl 与 closeButton。默认不启用。</dd></div>
        <div><dt>布局与 CSS</dt><dd>通过 style 设置容器尺寸，className、classNames、CSS 变量与 data-part 定制样式。建议明确设置高度。</dd></div>
      </dl>
      <p>配置可运行时更新。显示开关不代替服务端授权；隐藏地址栏或按钮也不等于禁止对应的 API 操作。</p>
    </Section>
    <Section title="默认行为与使用边界">
      <ul><li>本地启动需要已安装 Chrome / Chromium，也可以配置 RemoteAddress 连接已有 CDP 浏览器。</li><li>页面导航使用 HTTP / HTTPS。私网访问、脚本执行与诊断由 Go Config 显式授权。</li><li>HTTP 默认同源；远程前端用 browserkit-server 的 --allowed-origins 或 Handler 的 AllowedOrigins 配置。</li><li>软键盘是否弹出最终由客户端浏览器或 WebView 决定。</li><li>诊断历史有容量上限；进程内的浏览器会话不会在服务重启后自动恢复。</li></ul>
    </Section>
  </>
}

export function Documentation({ page }: { page: Page }) {
  const index = docPages.findIndex(item => item.id === page)
  const current = docPages[index]
  const descriptions = {
    principles: '浏览器连接、画面传输和输入处理。',
    scenarios: '表单操作、远程工作台和只读观察。',
    features: '功能、配置与限制。',
  }
  return <article className="docs-page">
    <section className="intro"><div><div className="eyebrow">BROWSERKIT DOCUMENTATION · 0{index + 1}</div><h1>{current.title}</h1><p>{descriptions[page]}</p></div></section>
    {page === 'principles' ? <Principles /> : page === 'scenarios' ? <Scenarios /> : <Features />}
    <nav className="docs-pagination" aria-label="文档翻页">
      {index > 0 ? <a href={docPages[index - 1].href}>← {docPages[index - 1].title}</a> : <a href="#/quickstart">← 快速开始</a>}
      {index < docPages.length - 1 ? <a href={docPages[index + 1].href}>{docPages[index + 1].title} →</a> : <a href="#/">打开工作台 →</a>}
    </nav>
  </article>
}
