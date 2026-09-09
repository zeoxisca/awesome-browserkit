import { act, fireEvent, render, screen } from '@testing-library/react'
import { createElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { browserDevtoolsStreamURL, fetchBrowserAssets, fetchBrowserDevtoolsState, startBrowserDevtools, stopBrowserDevtools, type BrowserDevtoolsState } from './client'
import { BrowserDevtoolsPanel, mergeDevtoolsState } from './BrowserDevtoolsPanel'

vi.mock('./client', () => ({
  browserDevtoolsStreamURL: vi.fn(() => 'ws://localhost/browser/devtools/ws'),
  fetchBrowserAssets: vi.fn(),
  fetchBrowserDevtoolsState: vi.fn(),
  startBrowserDevtools: vi.fn(),
  stopBrowserDevtools: vi.fn(),
}))

const state = (value: Partial<BrowserDevtoolsState>): BrowserDevtoolsState => ({
  available: true,
  status: 'collecting',
  generation: 1,
  revision: 1,
  entries: [],
  next_sequence: 0,
  pending_requests: 0,
  dropped_before: 0,
  truncated: false,
  ...value,
})

describe('mergeDevtoolsState', () => {
  it('merges incremental records once and removes dropped history', () => {
    const current = mergeDevtoolsState({
      status: 'collecting', generation: 1, revision: 1,
      entries: [{ sequence: 1, kind: 'console', text: 'old', at: '' }],
      pendingRequests: 0, droppedBefore: 0,
    }, state({
      revision: 2,
      dropped_before: 1,
      entries: [
        { sequence: 1, kind: 'console', text: 'duplicate', at: '' },
        { sequence: 2, kind: 'network', url: 'https://example.com/api', at: '' },
      ],
    }))
    expect(current.entries).toEqual([{ sequence: 2, kind: 'network', url: 'https://example.com/api', at: '' }])
  })

  it('replaces the old generation instead of mixing sequence numbers', () => {
    const current = mergeDevtoolsState({
      status: 'stopped', generation: 1, revision: 9,
      entries: [{ sequence: 9, kind: 'console', text: 'previous', at: '' }],
      pendingRequests: 0, droppedBefore: 0,
    }, state({
      generation: 2,
      entries: [{ sequence: 1, kind: 'exception', text: 'current', at: '' }],
      next_sequence: 1,
    }))
    expect(current.generation).toBe(2)
    expect(current.entries).toEqual([{ sequence: 1, kind: 'exception', text: 'current', at: '' }])
  })

  it('ignores an older generation that arrives after the current state', () => {
    const current = {
      status: 'collecting' as const, generation: 2, revision: 3,
      entries: [{ sequence: 1, kind: 'console' as const, text: 'current', at: '' }],
      pendingRequests: 1, droppedBefore: 0,
    }
    expect(mergeDevtoolsState(current, state({
      generation: 1,
      revision: 99,
      status: 'stopped',
      pending_requests: 0,
      entries: [{ sequence: 9, kind: 'console', text: 'stale', at: '' }],
    }))).toEqual(current)
  })

  it('ignores an older revision from the same generation', () => {
    const current = {
      status: 'collecting' as const, generation: 2, revision: 3,
      entries: [{ sequence: 2, kind: 'network' as const, url: 'https://example.com/current', at: '' }],
      pendingRequests: 1, droppedBefore: 1,
    }
    expect(mergeDevtoolsState(current, state({
      generation: 2,
      revision: 2,
      status: 'stopped',
      pending_requests: 0,
      dropped_before: 0,
      entries: [{ sequence: 1, kind: 'console', text: 'stale', at: '' }],
    }))).toEqual(current)
  })
})

describe('BrowserDevtoolsPanel', () => {
  beforeEach(() => vi.clearAllMocks())
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('只用 Console 和异常记录计算 Console 筛选数量', async () => {
    vi.mocked(browserDevtoolsStreamURL).mockReturnValue('ws://localhost/browser/devtools/ws')
    vi.mocked(fetchBrowserAssets).mockResolvedValue({ page_id: 'page-a', assets: [], truncated: false })
    vi.mocked(fetchBrowserDevtoolsState).mockResolvedValue(state({
      revision: 4,
      entries: [
        { sequence: 1, kind: 'console', level: 'log', text: '普通日志', at: '' },
        { sequence: 2, kind: 'console', level: 'warning', text: '警告', at: '' },
        { sequence: 3, kind: 'exception', level: 'error', text: '异常', at: '' },
        { sequence: 4, kind: 'network', status: 500, url: 'https://example.com/failure', at: '' },
      ],
      next_sequence: 4,
    }))
    class TestWebSocket {
      onmessage: ((event: MessageEvent) => void) | null = null
      onclose: (() => void) | null = null
      onerror: (() => void) | null = null
      close() {}
    }
    vi.stubGlobal('WebSocket', TestWebSocket)

    render(createElement(BrowserDevtoolsPanel, { sessionID: 'session-a', pageID: 'page-a' }))
    await act(async () => {})

    expect(screen.getByRole('button', { name: '全部 3' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '问题 2' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '错误 1' })).toBeInTheDocument()
  })

  it('允许人工开启和停止与 Agent 共享的采集状态', async () => {
    vi.mocked(fetchBrowserDevtoolsState).mockResolvedValue(state({ status: 'idle' }))
    vi.mocked(startBrowserDevtools).mockResolvedValue(state({ status: 'collecting', generation: 1, revision: 1 }))
    vi.mocked(stopBrowserDevtools).mockResolvedValue(state({ status: 'stopped', generation: 1, revision: 2 }))
    class TestWebSocket {
      onmessage: ((event: MessageEvent) => void) | null = null
      onclose: (() => void) | null = null
      onerror: (() => void) | null = null
      close() {}
    }
    vi.stubGlobal('WebSocket', TestWebSocket)

    render(createElement(BrowserDevtoolsPanel, { sessionID: 'session-a', pageID: 'page-a' }))
    await act(async () => {})
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: '开始采集' })) })

    expect(startBrowserDevtools).toHaveBeenCalledWith('session-a', 'page-a', undefined)
    expect(screen.getByRole('button', { name: '停止采集' })).toHaveAttribute('aria-pressed', 'true')

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: '停止采集' })) })

    expect(stopBrowserDevtools).toHaveBeenCalledWith('session-a', 'page-a', undefined)
    expect(screen.getByRole('button', { name: '开始采集' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('观察者只能读取诊断信息，不能切换共享采集状态', async () => {
    vi.mocked(fetchBrowserDevtoolsState).mockResolvedValue(state({ status: 'idle' }))
    class TestWebSocket {
      onmessage: ((event: MessageEvent) => void) | null = null
      onclose: (() => void) | null = null
      onerror: (() => void) | null = null
      close() {}
    }
    vi.stubGlobal('WebSocket', TestWebSocket)

    render(createElement(BrowserDevtoolsPanel, { sessionID: 'session-a', pageID: 'page-a', viewerID: 'viewer-b', controlEnabled: false }))
    await act(async () => {})

    const button = screen.getByRole('button', { name: '开始采集' })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', '当前窗口没有浏览器操作权')
    fireEvent.click(button)
    expect(startBrowserDevtools).not.toHaveBeenCalled()
  })
})
