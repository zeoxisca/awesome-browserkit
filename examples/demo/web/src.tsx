import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserView } from '@browserkit/react';
import '@browserkit/react/style.css';
import './style.css';
import { QuickStart } from './QuickStart';
import { Documentation, docPages } from './Documentation';
import { ConfigurationPanel, type ClientConfiguration } from './ConfigurationPanel';
function App() {
    const [configuration, setConfiguration] = useState<ClientConfiguration>({ controls: {}, input: {}, devtools: true });
    const [theme, setTheme] = useState('light'), [compact, setCompact] = useState(false), [observer, setObserver] = useState(false);
    const [route, setRoute] = useState(() => window.location.hash || '#/');
    const quickStart = route === '#/quickstart';
    const docPage = docPages.find(page => page.href === route);
    const title = quickStart ? '快速开始' : docPage?.title ?? 'BrowserView';
    useEffect(() => {
        const update = () => {
            setRoute(window.location.hash || '#/');
            window.scrollTo(0, 0);
        };
        window.addEventListener('hashchange', update);
        return () => window.removeEventListener('hashchange', update);
    }, []);
    useEffect(() => {
        document.title = title === 'BrowserView' ? 'BrowserKit — 可嵌入浏览器' : `${title} — BrowserKit`;
    }, [title]);
    return <div className="demo"><aside><a className="brand" href="#/">b<span>BrowserKit</span></a><div className="version">BrowserKit Demo</div><nav><a className={!quickStart && !docPage ? "selected" : ""} href="#/" aria-current={!quickStart && !docPage ? "page" : undefined}>◈ &nbsp; 浏览器工作台</a><a className={quickStart ? "selected" : ""} href="#/quickstart" aria-current={quickStart ? "page" : undefined}>↗ &nbsp; 快速开始</a><div className="docs-nav-group"><span className="docs-nav-label">详细文档</span>{docPages.map(page => <a key={page.href} href={page.href} className={route === page.href ? "selected" : ""} aria-current={route === page.href ? "page" : undefined}>{page.title}</a>)}</div></nav><div className="aside-bottom"><small>Go SDK · React</small></div></aside><main><header><div className="breadcrumb">组件库 <span>/</span> {docPage && <>详细文档 <span>/</span> </>}{title}</div><nav className="demo-page-nav" aria-label="页面导航"><a href="#/">工作台</a><a href="#/quickstart">快速开始</a>{docPages.map(page => <a key={page.href} href={page.href} aria-current={route === page.href ? "page" : undefined}>{page.title}</a>)}</nav><span className="badge">{docPage ? '文档' : quickStart ? '教程' : '预览'}</span></header>{docPage ? <Documentation page={docPage.id} /> : quickStart ? <QuickStart /> : <><section className="intro"><div><h1>浏览器组件工作台</h1><p>调整容器、主题和客户端配置，查看组件效果。</p></div><span className="release">v0.1.0</span></section><div className="controls"><div className="segmented"><button className={theme === 'light' ? 'active' : ''} onClick={() => setTheme('light')}>浅色</button><button className={theme === 'sage' ? 'active' : ''} onClick={() => setTheme('sage')}>鼠尾草</button></div><button className={compact ? 'toggle active' : 'toggle'} onClick={() => setCompact(!compact)}>↔ &nbsp;{compact ? '紧凑容器' : '宽屏容器'}</button><label><input type="checkbox" checked={observer} onChange={e => setObserver(e.target.checked)}/> 第二个嵌入视图</label></div><ConfigurationPanel value={configuration} onChange={setConfiguration} /><div className={`preview ${compact ? 'compact' : ''} ${theme}`}><BrowserView endpoint="/browser" {...configuration} style={{ height: 650 }}/></div>{observer && <section className="observer"><div><h2>只读观察视图</h2><p>通过 /observer 观看当前页面，服务端拒绝操作。</p></div><BrowserView endpoint="/observer" readOnly style={{ width: '100%', height: 360 }}/></section>}<footer><div className="code"><span>React</span><code>{'<BrowserView endpoint="/browser" {...configuration} />'}</code></div></footer></>}</main></div>;
}
createRoot(document.getElementById('root')!).render(<App />);
