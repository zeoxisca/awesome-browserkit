import { ChevronDown, ChevronRight, CircleAlert, File, Folder, FolderOpen, Info, Radio, TriangleAlert, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { browserDevtoolsStreamURL, fetchBrowserAssets, fetchBrowserDevtoolsState, startBrowserDevtools, stopBrowserDevtools, type BrowserAsset, type BrowserDevtoolsEntry, type BrowserDevtoolsState } from './client'

import type { BrowserDevtoolsTab } from './options'
type DiagnosticTab = BrowserDevtoolsTab
const allPanels: readonly DiagnosticTab[] = ['console', 'network', 'resources']
type DiagnosticFilter = 'all' | 'problems' | 'errors'
type ResourceFilter = 'code' | 'all' | 'script' | 'style' | 'document' | 'data' | 'image' | 'font' | 'other'
type NetworkFilter = { resourceType: string; method: string; status: string }

interface DiagnosticView {
  status: BrowserDevtoolsState['status']
  generation: number
  revision: number
  entries: BrowserDevtoolsEntry[]
  pendingRequests: number
  droppedBefore: number
}

interface ResourceTreeNode {
  key: string
  label: string
  children: ResourceTreeNode[]
  asset?: BrowserAsset
}

const emptyView: DiagnosticView = { status: 'idle', generation: 0, revision: 0, entries: [], pendingRequests: 0, droppedBefore: 0 }

export function BrowserDevtoolsPanel({ className = '', sessionID, pageID, viewerID, controlEnabled = true, hidden = false, panels = allPanels, captureControl = true, onClose }: { className?: string; sessionID: string; pageID?: string; viewerID?: string; controlEnabled?: boolean; hidden?: boolean; panels?: readonly DiagnosticTab[]; captureControl?: boolean; onClose?: () => void }) {
  const [view, setView] = useState<DiagnosticView>(emptyView)
  const [selectedTab, setTab] = useState<DiagnosticTab>(panels[0] ?? 'console')
  const tab = panels.includes(selectedTab) ? selectedTab : panels[0]
  const [filter, setFilter] = useState<DiagnosticFilter>('all')
  const [selected, setSelected] = useState<BrowserDevtoolsEntry>()
  const [networkFilter, setNetworkFilter] = useState<NetworkFilter>({ resourceType: 'all', method: 'all', status: 'all' })
  const [resourceFilter, setResourceFilter] = useState<ResourceFilter>('code')
  const [assets, setAssets] = useState<BrowserAsset[]>([])
  const [assetsLoading, setAssetsLoading] = useState(false)
  const [controlPending, setControlPending] = useState(false)
  const [controlError, setControlError] = useState<string>()
  const viewRef = useRef<DiagnosticView>(emptyView)

  useEffect(() => {
    setView(emptyView)
    setSelected(undefined)
    setFilter('all')
    setNetworkFilter({ resourceType: 'all', method: 'all', status: 'all' })
    setAssets([])
    setAssetsLoading(false)
    setControlPending(false)
    setControlError(undefined)
    viewRef.current = emptyView
    if (!pageID) return
    let active = true
    let socket: WebSocket | undefined
    let retry: number | undefined
    const applyState = (state: BrowserDevtoolsState) => {
      if (!active || !Array.isArray(state.entries)) return
      viewRef.current = mergeDevtoolsState(viewRef.current, state)
      setView(viewRef.current)
    }
    const refresh = () => {
      const after = viewRef.current.entries.at(-1)?.sequence ?? 0
      void fetchBrowserDevtoolsState(sessionID, pageID, after, 100).then(applyState).catch(() => undefined)
    }
    const connect = () => {
      if (!active) return
      socket = new WebSocket(browserDevtoolsStreamURL(sessionID, pageID))
      socket.onmessage = (event) => {
        if (!active || typeof event.data !== 'string') return
        try {
          const state = JSON.parse(event.data) as BrowserDevtoolsState
          applyState(state)
        } catch {
          // 丢弃单个损坏的临时视图；下一条增量仍可继续恢复。
        }
      }
      socket.onclose = () => {
        if (active) retry = window.setTimeout(connect, 500)
      }
      socket.onerror = () => socket?.close()
    }
    refresh()
    const poll = window.setInterval(refresh, 1500)
    connect()
    return () => {
      active = false
      window.clearInterval(poll)
      if (retry !== undefined) window.clearTimeout(retry)
      socket?.close()
    }
  }, [pageID, sessionID])

  useEffect(() => setSelected(undefined), [view.generation, tab])

  useEffect(() => {
    if (tab !== 'resources' || !pageID) return
    const controller = new AbortController()
    let active = true
    let first = true
    const refresh = () => {
      if (first) setAssetsLoading(true)
      void fetchBrowserAssets(sessionID, pageID, '', 500, controller.signal)
        .then((state) => { if (active) setAssets(state.assets) })
        .catch(() => { if (active) setAssets([]) })
        .finally(() => { if (active) { first = false; setAssetsLoading(false) } })
    }
    refresh()
    const timer = window.setInterval(refresh, 1500)
    return () => { active = false; window.clearInterval(timer); controller.abort() }
  }, [pageID, sessionID, tab])

  const consoleEntries = useMemo(() => view.entries.filter((entry) => entry.kind === 'console' || entry.kind === 'exception'), [view.entries])
  const networkEntries = useMemo(() => view.entries.filter((entry) => entry.kind === 'network'), [view.entries])
  const errorCount = useMemo(() => consoleEntries.filter(entryError).length, [consoleEntries])
  const problemCount = useMemo(() => consoleEntries.filter(entryProblem).length, [consoleEntries])
  const visibleConsole = useMemo(() => consoleEntries.filter((entry) => matchesFilter(entry, filter)), [consoleEntries, filter])
  const visibleNetwork = useMemo(() => networkEntries.filter((entry) => matchesNetworkFilter(entry, networkFilter)), [networkEntries, networkFilter])
  const visibleAssets = useMemo(() => assets.filter((asset) => matchesResourceFilter(asset, resourceFilter)), [assets, resourceFilter])
  const entries = tab === 'console' ? visibleConsole : visibleNetwork

  const toggleCollection = async () => {
    if (!captureControl || !pageID || !controlEnabled || controlPending) return
    setControlPending(true)
    setControlError(undefined)
    try {
      const state = view.status === 'collecting'
        ? await stopBrowserDevtools(sessionID, pageID, viewerID)
        : await startBrowserDevtools(sessionID, pageID, viewerID)
      viewRef.current = mergeDevtoolsState(viewRef.current, state)
      setView(viewRef.current)
    } catch (error) {
      setControlError(error instanceof Error ? error.message : '开发诊断操作失败')
    } finally {
      setControlPending(false)
    }
  }

  return <section data-part="devtools" className={`browser-devtools ${className}`} aria-label="开发诊断" hidden={hidden}>
    <header className="browser-devtools-header">
      <strong><i data-status={view.status} aria-hidden="true" />开发诊断</strong>
      <nav aria-label="诊断类型">
        {Array.from(new Set(panels)).map((panel) => <button key={panel} type="button" className={tab === panel ? 'active' : ''} onClick={() => { setTab(panel); setSelected(undefined) }}>
          {{ console: 'Console', network: 'Network', resources: 'Resources' }[panel]}
          {(panel === 'console' ? consoleEntries.length : panel === 'network' ? networkEntries.length : assets.length) > 0 && <span>{panel === 'console' ? consoleEntries.length : panel === 'network' ? networkEntries.length : assets.length}</span>}
        </button>)}
      </nav>
      <small title={controlError}>{controlError || statusText(view.status, view.pendingRequests)}</small>
      {captureControl && <button type="button" className="browser-devtools-toggle" aria-pressed={view.status === 'collecting'} disabled={!pageID || !controlEnabled || controlPending} title={!controlEnabled ? '当前窗口没有浏览器操作权' : undefined} onClick={() => void toggleCollection()}>{controlPending ? '处理中…' : view.status === 'collecting' ? '停止采集' : '开始采集'}</button>}
      {onClose && <button type="button" className="browser-devtools-panel-close" aria-label="关闭开发诊断面板" title="隐藏面板，不停止诊断采集" onClick={onClose}><X size={13} /></button>}
    </header>
    <div className="browser-devtools-filters" role="group" aria-label="诊断筛选">
      {tab === 'console' && <>
        <FilterButton active={filter === 'all'} onClick={() => { setFilter('all'); setSelected(undefined) }}>全部 {consoleEntries.length}</FilterButton>
        <FilterButton active={filter === 'problems'} onClick={() => { setFilter('problems'); setSelected(undefined) }}>问题 {problemCount}</FilterButton>
        <FilterButton active={filter === 'errors'} onClick={() => { setFilter('errors'); setSelected(undefined) }}>错误 {errorCount}</FilterButton>
      </>}
      {tab === 'network' && <NetworkFilters entries={networkEntries} value={networkFilter} onChange={(value) => { setNetworkFilter(value); setSelected(undefined) }} />}
      {tab === 'resources' && <ResourceFilters value={resourceFilter} onChange={setResourceFilter} />}
    </div>
    <div className="browser-devtools-content">
      {tab === 'resources' ? assetsLoading ? <DiagnosticEmpty status="collecting" tab="resources" /> : <ResourcesEntries assets={visibleAssets} total={assets.length} /> : entries.length === 0 ? <DiagnosticEmpty status={view.status} tab={tab} /> : tab === 'console'
        ? <ConsoleEntries entries={entries} selected={selected?.sequence} onSelect={setSelected} />
        : <NetworkEntries entries={entries} selected={selected?.sequence} onSelect={setSelected} />}
      {selected && <EntryDetail entry={selected} onClose={() => setSelected(undefined)} />}
    </div>
    <footer>
      <span>{view.entries.length} 条记录</span>
      {view.droppedBefore > 0 && <span>较早记录已裁剪至 #{view.droppedBefore}</span>}
      <span>#{view.revision}</span>
    </footer>
  </section>
}

function ResourcesEntries({ assets, total }: { assets: BrowserAsset[]; total: number }) {
  const tree = useMemo(() => buildResourceTree(assets), [assets])
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(tree.filter((node) => node.children.length > 0).map((node) => node.key)))
  useEffect(() => {
    setExpanded((current) => {
      const next = new Set(current)
      for (const node of tree) if (node.children.length > 0 && current.size === 0) next.add(node.key)
      return next
    })
  }, [tree])
  const toggle = (key: string) => setExpanded((current) => {
    const next = new Set(current)
    if (next.has(key)) next.delete(key); else next.add(key)
    return next
  })
  if (assets.length === 0) return <div className="browser-devtools-empty"><strong>{total === 0 ? '没有已观测资源' : '没有匹配的资源'}</strong><span>{total === 0 ? '当前页面尚未通过 Performance API 记录可见资源。' : '切换资源类型筛选后查看其他已观测文件。'}</span></div>
  return <div className="browser-devtools-list browser-resource-tree" role="tree" aria-label="页面资源文件树">
    {tree.map((node) => <ResourceTreeRow key={node.key} node={node} depth={0} expanded={expanded} onToggle={toggle} />)}
  </div>
}

function ResourceTreeRow({ node, depth, expanded, onToggle }: { node: ResourceTreeNode; depth: number; expanded: Set<string>; onToggle: (key: string) => void }) {
  const isExpanded = expanded.has(node.key)
  if (!node.asset) return <>
    <div role="treeitem" aria-expanded={isExpanded} className="browser-resource-tree-row browser-resource-tree-folder" style={{ '--resource-depth': depth } as CSSProperties} onClick={() => onToggle(node.key)}>
      {isExpanded ? <ChevronDown size={13} aria-hidden="true" /> : <ChevronRight size={13} aria-hidden="true" />} {isExpanded ? <FolderOpen size={14} aria-hidden="true" /> : <Folder size={14} aria-hidden="true" />}<strong>{node.label}</strong><small>{node.children.length} 项</small>
    </div>
    {isExpanded && node.children.map((child) => <ResourceTreeRow key={child.key} node={child} depth={depth + 1} expanded={expanded} onToggle={onToggle} />)}
  </>
  return <div role="treeitem" className="browser-resource-tree-row browser-resource-tree-file" style={{ '--resource-depth': depth } as CSSProperties} title={node.asset.url}>
    <File size={14} aria-hidden="true" /><code>{node.label}</code><span>{node.asset.initiator_type || 'resource'}</span><span>{node.asset.response_status || '—'}</span><span>{formatBytes(node.asset.transfer_bytes)}</span>
    <small>{node.asset.url}</small>
  </div>
}

function buildResourceTree(assets: BrowserAsset[]): ResourceTreeNode[] {
  const roots = new Map<string, ResourceTreeNode>()
  assets.forEach((asset, index) => {
    let parsed: URL | undefined
    try { parsed = new URL(asset.url) } catch { /* 保留原始地址作为不可解析资源 */ }
    const host = parsed?.host || '其他资源'
    const root = roots.get(host) ?? { key: `host:${host}`, label: host, children: [] }
    roots.set(host, root)
    const segments = (parsed?.pathname || '').split('/').filter(Boolean)
    const fileName = asset.name || segments.at(-1) || parsed?.hostname || asset.url
    const directories = segments.at(-1) === fileName ? segments.slice(0, -1) : segments
    let parent = root
    directories.forEach((segment, directoryIndex) => {
      const key = `${parent.key}/dir:${directoryIndex}:${segment}`
      let child = parent.children.find((item) => item.key === key)
      if (!child) { child = { key, label: segment, children: [] }; parent.children.push(child) }
      parent = child
    })
    parent.children.push({ key: `${parent.key}/file:${index}`, label: fileName, children: [], asset })
  })
  const sort = (nodes: ResourceTreeNode[]) => { nodes.sort((left, right) => Number(Boolean(right.asset)) - Number(Boolean(left.asset)) || left.label.localeCompare(right.label)); nodes.forEach((node) => sort(node.children)) }
  const result = Array.from(roots.values()); sort(result); return result
}

function FilterButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" className={active ? 'active' : ''} aria-pressed={active} onClick={onClick}>{children}</button>
}

function ConsoleEntries({ entries, selected, onSelect }: { entries: BrowserDevtoolsEntry[]; selected?: number; onSelect: (entry: BrowserDevtoolsEntry) => void }) {
  return <div className="browser-devtools-list" role="list">
    {entries.map((entry) => <button type="button" role="listitem" key={entry.sequence} className={`browser-console-entry ${selected === entry.sequence ? 'selected' : ''}`} onClick={() => onSelect(entry)}>
      <EntryIcon entry={entry} />
      <code>{entry.text || entry.error || entry.kind}</code>
      <span>{sourceLabel(entry)}</span>
    </button>)}
  </div>
}

function NetworkEntries({ entries, selected, onSelect }: { entries: BrowserDevtoolsEntry[]; selected?: number; onSelect: (entry: BrowserDevtoolsEntry) => void }) {
  return <div className="browser-devtools-list" role="list">
    <div className="browser-network-head" aria-hidden="true"><span>名称</span><span>状态</span><span>类型</span><span>耗时</span><span>大小</span></div>
    {entries.map((entry) => <button type="button" role="listitem" key={entry.sequence} className={`browser-network-entry ${selected === entry.sequence ? 'selected' : ''}`} onClick={() => onSelect(entry)}>
      <code title={entry.url}>{requestName(entry.url)}</code>
      <span className={entryProblem(entry) ? 'problem' : ''}>{entry.failed ? '失败' : entry.status || '—'}</span>
      <span>{entry.resource_type || mimeLabel(entry.mime_type)}</span>
      <span>{entry.duration_ms === undefined ? '—' : `${entry.duration_ms} ms`}</span>
      <span>{formatBytes(entry.encoded_bytes)}</span>
    </button>)}
  </div>
}

function NetworkFilters({ entries, value, onChange }: { entries: BrowserDevtoolsEntry[]; value: NetworkFilter; onChange: (value: NetworkFilter) => void }) {
  const types = Array.from(new Set(entries.map((entry) => entry.resource_type).filter(Boolean) as string[])).sort()
  const methods = Array.from(new Set(entries.map((entry) => entry.method || 'GET'))).sort()
  const statuses = Array.from(new Set(entries.map((entry) => entry.status).filter((status): status is number => Boolean(status)))).sort((left, right) => left - right)
  return <>
    <select aria-label="请求类型筛选" value={value.resourceType} onChange={(event) => onChange({ ...value, resourceType: event.target.value })}><option value="all">类型：全部</option>{types.map((type) => <option key={type} value={type}>{type}</option>)}</select>
    <select aria-label="请求方法筛选" value={value.method} onChange={(event) => onChange({ ...value, method: event.target.value })}><option value="all">方法：全部</option>{methods.map((method) => <option key={method} value={method}>{method}</option>)}</select>
    <select aria-label="状态码筛选" value={value.status} onChange={(event) => onChange({ ...value, status: event.target.value })}><option value="all">状态：全部</option><option value="2xx">2xx</option><option value="3xx">3xx</option><option value="4xx">4xx</option><option value="5xx">5xx</option>{statuses.map((status) => <option key={status} value={String(status)}>{status}</option>)}</select>
  </>
}

function ResourceFilters({ value, onChange }: { value: ResourceFilter; onChange: (value: ResourceFilter) => void }) {
  return <select aria-label="资源类型筛选" value={value} onChange={(event) => onChange(event.target.value as ResourceFilter)}>
    <option value="code">资源：代码文件</option>
    <option value="all">资源：全部</option>
    <option value="script">资源：JavaScript</option>
    <option value="style">资源：CSS</option>
    <option value="document">资源：HTML</option>
    <option value="data">资源：数据请求</option>
    <option value="image">资源：图片</option>
    <option value="font">资源：字体</option>
    <option value="other">资源：其他</option>
  </select>
}

function matchesResourceFilter(asset: BrowserAsset, filter: ResourceFilter) {
  if (filter === 'all') return true
  const kind = resourceKind(asset)
  if (filter === 'code') return kind === 'script' || kind === 'style' || kind === 'document' || kind === 'data'
  return kind === filter
}

function resourceKind(asset: BrowserAsset): Exclude<ResourceFilter, 'code' | 'all'> {
  const initiator = (asset.initiator_type || '').toLowerCase()
  let extension = ''
  try { extension = new URL(asset.url).pathname.split('/').at(-1)?.split('.').at(-1)?.toLowerCase() || '' } catch { extension = asset.name.split('.').at(-1)?.toLowerCase() || '' }
  if (initiator === 'script' || ['js', 'mjs', 'cjs', 'jsx', 'ts', 'tsx', 'wasm'].includes(extension)) return 'script'
  if (initiator === 'css' || (initiator === 'link' && extension === 'css') || extension === 'css') return 'style'
  if (initiator === 'img' || ['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'avif', 'ico'].includes(extension)) return 'image'
  if (initiator === 'font' || ['woff', 'woff2', 'ttf', 'otf', 'eot'].includes(extension)) return 'font'
  if (initiator === 'fetch' || initiator === 'xmlhttprequest' || initiator === 'eventsource' || ['json', 'xml', 'csv'].includes(extension)) return 'data'
  if (['html', 'htm', 'xhtml'].includes(extension)) return 'document'
  return 'other'
}

function EntryDetail({ entry, onClose }: { entry: BrowserDevtoolsEntry; onClose: () => void }) {
  const network = entry.kind === 'network'
  return <aside className="browser-devtools-detail" aria-label="诊断详情">
    <i className="browser-devtools-detail-grabber" aria-hidden="true" />
    <header><EntryIcon entry={entry} /><strong>{network ? `${entry.method || 'GET'} ${requestName(entry.url)}` : entry.kind === 'exception' ? '运行时异常' : entry.level || 'Console'}</strong><button type="button" aria-label="关闭诊断详情" onClick={onClose}><X size={14} /></button></header>
    <dl>
      {network ? <>
        <dt>状态</dt><dd className={entryProblem(entry) ? 'problem' : ''}>{entry.failed ? entry.error || '请求失败' : entry.status || '—'}</dd>
        <dt>方法</dt><dd>{entry.method || '—'}</dd><dt>地址</dt><dd>{entry.url || '—'}</dd>
        <dt>类型</dt><dd>{entry.resource_type || entry.mime_type || '—'}</dd><dt>耗时</dt><dd>{entry.duration_ms ?? 0} ms</dd><dt>传输</dt><dd>{formatBytes(entry.encoded_bytes)}</dd>
        {(entry.body_available || entry.request_body || entry.response_body) && <><dt>请求体</dt><dd><pre className="browser-devtools-body">{entry.request_body || '—'}</pre></dd><dt>响应体</dt><dd><pre className="browser-devtools-body">{entry.response_body || '—'}</pre></dd>{entry.body_truncated && <><dt>正文</dt><dd>已截断至 16 KiB</dd></>}</>}
      </> : <>
        <dt>级别</dt><dd className={entryProblem(entry) ? 'problem' : ''}>{entry.level || 'error'}</dd>
        <dt>内容</dt><dd>{entry.text || entry.error || '—'}</dd><dt>位置</dt><dd>{sourceLabel(entry) || '—'}</dd>
        {entry.stack && <><dt>堆栈</dt><dd className="browser-devtools-stack">{entry.stack}</dd></>}
      </>}
      <dt>序号</dt><dd>#{entry.sequence}</dd>
    </dl>
  </aside>
}

function EntryIcon({ entry }: { entry: BrowserDevtoolsEntry }) {
  if (entryError(entry)) return <CircleAlert size={14} className="error" aria-hidden="true" />
  if (entryProblem(entry)) return <TriangleAlert size={14} className="warning" aria-hidden="true" />
  return entry.kind === 'network' ? <Radio size={14} aria-hidden="true" /> : <Info size={14} aria-hidden="true" />
}

function DiagnosticEmpty({ status, tab }: { status: DiagnosticView['status']; tab: DiagnosticTab }) {
  return <div className="browser-devtools-empty">
    <strong>{status === 'idle' ? 'Agent 尚未开始开发诊断' : status === 'collecting' ? '正在等待开发信息' : '采集已停止'}</strong>
    <span>{status === 'idle' ? '你可以手动开始采集；需要排障时，Agent 也会在复现操作前按需开启。' : status === 'collecting' ? `复现页面问题后，${tab === 'console' ? 'Console 和异常' : tab === 'network' ? 'Network 请求' : '页面资源'}会显示在这里。` : '本次采集没有符合当前筛选的记录。'}</span>
  </div>
}

export function mergeDevtoolsState(current: DiagnosticView, state: BrowserDevtoolsState): DiagnosticView {
  if (state.generation < current.generation || (state.generation === current.generation && state.revision < current.revision)) return current
  const reset = state.generation > current.generation
  const entries = reset ? state.entries : mergeEntries(current.entries, state.entries)
  return {
    status: state.status, generation: state.generation, revision: state.revision,
    entries: entries.filter((entry) => entry.sequence > state.dropped_before).slice(-500),
    pendingRequests: state.pending_requests, droppedBefore: state.dropped_before,
  }
}

function mergeEntries(current: BrowserDevtoolsEntry[], next: BrowserDevtoolsEntry[]) {
  if (next.length === 0) return current
  const known = new Set(current.map((entry) => entry.sequence))
  return [...current, ...next.filter((entry) => !known.has(entry.sequence))].sort((left, right) => left.sequence - right.sequence)
}

function entryError(entry: BrowserDevtoolsEntry) {
  return entry.kind === 'exception' || entry.level === 'error' || entry.failed === true || (entry.status ?? 0) >= 500
}

function entryProblem(entry: BrowserDevtoolsEntry) {
  return entryError(entry) || entry.level === 'warning' || (entry.status ?? 0) >= 400
}

function matchesFilter(entry: BrowserDevtoolsEntry, filter: DiagnosticFilter) {
  if (filter === 'errors') return entryError(entry)
  if (filter === 'problems') return entryProblem(entry)
  return true
}

function matchesNetworkFilter(entry: BrowserDevtoolsEntry, filter: NetworkFilter) {
  if (filter.resourceType !== 'all' && entry.resource_type !== filter.resourceType) return false
  if (filter.method !== 'all' && (entry.method || 'GET') !== filter.method) return false
  if (filter.status === 'all') return true
  const status = entry.status || 0
  if (filter.status.endsWith('xx')) return Math.floor(status / 100) === Number(filter.status[0])
  return status === Number(filter.status)
}

function statusText(status: DiagnosticView['status'], pending: number) {
  if (status === 'collecting') return pending > 0 ? `采集中 · ${pending} 个请求` : '采集中'
  if (status === 'stopped') return '采集已停止'
  return '未开始'
}

function sourceLabel(entry: BrowserDevtoolsEntry) {
  if (!entry.source_url) return ''
  const name = requestName(entry.source_url)
  return `${name}${entry.line ? `:${entry.line}${entry.column ? `:${entry.column}` : ''}` : ''}`
}

function requestName(value?: string) {
  if (!value) return '请求'
  try {
    const url = new URL(value)
    return url.pathname.split('/').filter(Boolean).at(-1) || url.hostname
  } catch {
    return value.split('/').filter(Boolean).at(-1) || value
  }
}

function mimeLabel(value?: string) {
  if (!value) return '—'
  return value.split('/').at(-1)?.split(';')[0] || value
}

function formatBytes(value?: number) {
  if (value === undefined) return '—'
  if (value < 1024) return `${value} B`
  return `${(value / 1024).toFixed(value < 10 * 1024 ? 1 : 0)} kB`
}
