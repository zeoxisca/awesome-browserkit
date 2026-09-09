import { useState, type ReactNode } from 'react'

const sourceSetup = `git clone https://github.com/zeoxisca/awesome-browserkit.git
cd awesome-browserkit
go mod download`

const skillSetup = `# 在 BrowserKit 仓库根目录执行
./install.sh
export PATH="$HOME/.local/bin:$PATH"
browserkit --version`

const reactPackage = `# 在 BrowserKit 仓库根目录生成 React 安装包
npm ci --prefix react
make pack`

const remoteSetup = `# 编译独立服务
make server

# 启动 HTTP 服务，监听所有网卡的 8787 端口
./browserkit-server --addr 0.0.0.0:8787 \\
  --url https://example.com \\
  --allowed-origins https://app.example.com`

const remoteReact = `import { BrowserView } from '@browserkit/react'
import '@browserkit/react/style.css'

export function RemoteBrowser() {
  return (
    <BrowserView
      endpoint="https://browser.example.com/browser"
      input={{ keyboardActivation: 'manual' }}
      style={{ width: '100%', height: 640 }}
    />
  )
}`

const embeddedGo = `go get github.com/zeoxisca/awesome-browserkit`

const embeddedReactSetup = `# 在 React 项目目录执行，替换为安装包的实际路径
npm install /path/to/awesome-browserkit/react/browserkit-react-0.1.0.tgz
npm install react@^19 react-dom@^19`

const embeddedCode = `import (
  "context"
  "net/http"

  browserkit "github.com/zeoxisca/awesome-browserkit"
  "github.com/zeoxisca/awesome-browserkit/browserhttp"
)

func mountBrowser(ctx context.Context, mux *http.ServeMux) (*browserkit.Manager, error) {
  manager, err := browserkit.NewManager(browserkit.Config{
    AllowDevtools: true,
  })
  if err != nil { return nil, err }
  if _, err = manager.Open(ctx, browserkit.OpenRequest{
    URL: "https://example.com",
  }); err != nil {
    _ = manager.Close(context.Background())
    return nil, err
  }
  mux.Handle("/browser/", http.StripPrefix("/browser",
    browserhttp.NewHandler(manager, browserhttp.Options{})))
  return manager, nil
}`

const embeddedReact = `import { BrowserView } from '@browserkit/react'
import '@browserkit/react/style.css'

export function BrowserPanel() {
  return <BrowserView
    endpoint="/browser"
    controls={{ tabs: false }}
    style={{ height: 560 }}
  />
}`

function CodeBlock({ title, language, code }: { title: string; language: string; code: string }) {
  const [copyStatus, setCopyStatus] = useState('')
  return <div className="quick-code">
    <div className="quick-code-header">
      <span>{title}</span><small>{language}</small>
      <span className="quick-copy-status" role="status">{copyStatus}</span>
      <button type="button" aria-label={`复制 ${title}`} onClick={async () => {
        try { await navigator.clipboard.writeText(code); setCopyStatus('已复制') }
        catch { setCopyStatus('请选中代码复制') }
      }}>复制代码</button>
    </div>
    <pre tabIndex={0} aria-label={title}><code>{code}</code></pre>
  </div>
}

function GuideSection({ number, eyebrow, title, description, children }: { number: string; eyebrow: string; title: string; description: string; children: ReactNode }) {
  return <section className="quick-guide" aria-labelledby={`guide-${number}`}>
    <div className="quick-guide-heading"><span>{number} / {eyebrow}</span><h2 id={`guide-${number}`}>{title}</h2><p>{description}</p></div>
    {children}
  </section>
}

function SkillModes() {
  const [mode, setMode] = useState<'attach' | 'new'>('attach')
  const [remote, setRemote] = useState('http://127.0.0.1:9222')
  const [pageURL, setPageURL] = useState('')
  const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'"
  const command = (mode === 'attach'
    ? `browserkit --mode attach --remote ${quote(remote)}`
    : 'browserkit') + (pageURL.trim() ? ` --url ${quote(pageURL.trim())}` : '')
  return <div className="skill-modes">
    <div className="skill-mode-choices" role="group" aria-label="本地浏览器模式">
      <button type="button" aria-pressed={mode === 'attach'} onClick={() => setMode('attach')}><strong>1. 接管已有浏览器</strong><span>使用已打开页面和已有登录态</span></button>
      <button type="button" aria-pressed={mode === 'new'} onClick={() => setMode('new')}><strong>2. 新建独立浏览器</strong><span>独立用户数据目录、Cookie 与本地存储</span></button>
    </div>
    {mode === 'attach' ? <div className="skill-mode-instructions">
      <h3>允许 Chrome 调试连接</h3>
      <ol>
        <li>地址栏输入 <code>chrome://inspect/#remote-debugging</code>。也可打开 <code>chrome://inspect</code> 后选择 Remote debugging。</li>
        <li>勾选「允许调试本浏览器」（Allow remote debugging for this browser）。如果没有此项，需要升级到支持它的 Chrome。</li>
        <li>记录调试地址并切回目标 Tab。启动 client 后，确认 Chrome 的授权提示。</li>
      </ol>
      <p>仅勾选 Discover network targets 不能开启当前浏览器调试。9222 是示例端口，以实际连接地址为准。</p>
    </div> : <div className="skill-mode-instructions">
      <h3>使用独立用户数据目录</h3>
      <p>new 模式以无头方式运行，使用独立临时 profile。</p>
      <p>退出时关闭浏览器并删除临时 profile。</p>
    </div>}
    <div className="skill-mode-fields">
      {mode === 'attach' && <label>Chrome 调试地址（--remote）<input value={remote} onChange={event => setRemote(event.target.value)} spellCheck={false} /><small>支持 /json/version 的 HTTP 入口，或 /devtools/browser/… WebSocket 地址。</small></label>}
      <label>在新 Tab 打开的 URL（--url，可选）<input value={pageURL} onChange={event => setPageURL(event.target.value)} spellCheck={false} /><small>填写后在新 Tab 打开地址。{mode === 'attach' ? '留空接管激活 Tab。' : '留空由 Agent 创建页面。'}</small></label>
    </div>
    <CodeBlock title={mode === 'attach' ? '接管已有页面的启动命令' : '新建独立浏览器的启动命令'} language="Shell" code={command} />
    <p className="quick-note">在本地执行上方命令。{mode === 'attach' ? '退出时断开连接，保留 Chrome 和 Tab。' : ''}切换模式需结束当前进程。</p>
  </div>
}

export function QuickStart() {
  return <div className="quick-start">
    <section className="intro">
      <div><div className="eyebrow">GETTING STARTED</div><h1>快速开始</h1><p>选择一种接入方式。</p></div>
      <a className="quick-demo-link" href="#/">打开工作台 ↗</a>
    </section>
    <div className="quick-choice" aria-label="接入方式选择">
      <span><b>01</b> 本地 Skill</span><span><b>02</b> 独立远程服务</span><span><b>03</b> 嵌入程序</span>
    </div>

    <section className="quick-guide" aria-labelledby="requirements-title">
      <div className="quick-guide-heading"><h2 id="requirements-title">环境准备</h2><p>源码编译需要 Go 1.25.8+。启动浏览器的机器需要 Chrome / Chromium；构建 React 组件还需要 Node.js 22+，组件支持 React 19。</p></div>
      <CodeBlock title="获取源码" language="Shell" code={sourceSetup} />
      <p className="quick-note">以下 CLI 编译和组件打包命令均在 BrowserKit 仓库根目录执行；Go 和 React 依赖安装命令在各自的应用项目中执行。</p>
    </section>

    <GuideSection number="01" eyebrow="LOCAL SKILL" title="作为 Skill：本地使用" description="安装 client 与 Skill，让 Agent 操作本地浏览器。">
      <CodeBlock title="安装 CLI 与 Skill" language="Shell" code={skillSetup} />
      <div className="quick-callout"><strong>安装结果</strong><span><code>browserkit</code> 默认安装到 <code>~/.local/bin</code>；Skill 安装到 <code>${'{CODEX_HOME:-~/.codex}'}/skills/browserkit-cli</code>。可通过 BROWSERKIT_BIN_DIR 和 BROWSERKIT_SKILL_DIR 修改安装目录；自定义 CLI 目录需加入 PATH。</span></div>
      <SkillModes />
    </GuideSection>

    <GuideSection number="02" eyebrow="REMOTE SERVICE" title="作为独立服务：远程 HTTP" description="服务器运行浏览器，React 连接 HTTP 服务。">
      <CodeBlock title="编译并启动 HTTP 服务" language="Shell" code={remoteSetup} />
      <div className="quick-callout"><strong>服务地址</strong><span>服务默认监听 <code>127.0.0.1:8787</code>；示例通过 <code>--addr</code> 监听所有网卡。<code>/browser</code> 提供浏览器服务，<code>/healthz</code> 提供健康检查。<code>--allowed-origins</code> 指定前端 Origin。</span></div>
      <p className="quick-note">先按本页末尾的「React 安装」安装组件。</p>
      <CodeBlock title="React 连接远程服务" language="TSX" code={remoteReact} />
    </GuideSection>

    <GuideSection number="03" eyebrow="EMBEDDED PROGRAM" title="嵌入程序：Go 与 React" description="在 Go 服务中挂载 Handler，在 React 中嵌入 BrowserView。">
      <CodeBlock title="安装 Go 包" language="Shell" code={embeddedGo} />
      <CodeBlock title="创建 Manager 并挂载 HTTP Handler" language="Go" code={embeddedCode} />
      <p className="quick-note">由应用生命周期管理 ctx，保留返回的 Manager，并在应用退出时调用 manager.Close(context.Background())。</p>
      <p className="quick-note">React 安装命令见下方。</p>
      <CodeBlock title="React 嵌入组件" language="TSX" code={embeddedReact} />
      <div className="quick-callout"><strong>组件配置</strong><span>通过 style 指定容器尺寸，通过 className 和 CSS 定制外观。按客户端需求分别配置 <code>controls</code>、<code>input</code> 和 <code>devtools</code>。</span></div>
    </GuideSection>

    <section className="quick-guide" aria-labelledby="react-install-title">
      <div className="quick-guide-heading"><h2 id="react-install-title">React 安装</h2><p>远程服务与嵌入应用使用同一个组件包。当前通过源码打包安装，尚未发布到 npm。</p></div>
      <CodeBlock title="仓库目录：生成安装包" language="Shell" code={reactPackage} />
      <CodeBlock title="React 项目：安装组件" language="Shell" code={embeddedReactSetup} />
      <p className="quick-note">替换 .tgz 路径。项目已有 React 19 时省略第二条命令。参数与 CSS 变量见 react/README.md。</p>
    </section>
  </div>
}
