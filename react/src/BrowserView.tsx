import { Plus, Code2, ArrowRight, Keyboard, RefreshCw, X } from 'lucide-react'
import { useEffect, useRef, useState, type CSSProperties } from 'react'
import { decodeFrame, newBrowserTab, activateBrowserTab, browserStateStreamURL, browserStreamURL, closeBrowser, closeBrowserTab, fetchBrowserState, fetchCachedBrowserFrame, navigateBrowserTab, reloadBrowserTab, setBrowserTabViewport, takeBrowserControl, type BrowserControlState, type BrowserTabView, type BrowserViewState } from './client'
import { BrowserDevtoolsPanel } from './BrowserDevtoolsPanel'
import type { BrowserControls, BrowserInputOptions, BrowserDevtoolsOptions } from './options'

type DevtoolsResize = { pointerID: number }
type BrowserViewport = { width: number; height: number }
type ViewportTarget = BrowserViewport & { sessionID: string; pageID: string; generation: number; attempt: number; force: boolean }
type RemotePoint = { x: number; y: number; scaleX: number; scaleY: number }
type PendingMove = { pageID: string | undefined; input: BrowserInput }
type BrowserInput = {
  kind: 'mouse' | 'touch' | 'key' | 'shortcut' | 'composition' | 'text'
  action?: string
  x?: number
  y?: number
  delta_x?: number
  delta_y?: number
  button?: string
  buttons?: number
  click_count?: number
  pointer_type?: string
  touches?: Array<{ id: number; x: number; y: number; radius_x?: number; radius_y?: number; force?: number }>
  modifiers?: number
  key?: string
  code?: string
  key_code?: number
  location?: number
  auto_repeat?: boolean
  text?: string
  selection_start?: number
  selection_end?: number
}
const viewportResizeDebounceMS = 200
const viewportResizeRetryMS = 500
const browserInputBackpressureBytes = 16 << 10

const sameViewport = (left: BrowserViewport | undefined, right: BrowserViewport) =>
  left?.width === right.width && left.height === right.height

// ResizeObserver 的 contentRect 就是页面画布的 CSS content box：Tab 栏和 URL 栏
// 是它的兄弟节点，因此无需再猜测或减去任何栏位高度。CDP 只接受整数 CSS 像素。
const viewportFromContentRect = (rect: Pick<DOMRectReadOnly, 'width' | 'height'>): BrowserViewport | undefined => {
  const width = Math.round(rect.width)
  const height = Math.round(rect.height)
  return width > 0 && height > 0 ? { width, height } : undefined
}

const createBrowserViewerID = () => {
  return globalThis.crypto?.randomUUID?.() ?? `viewer-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
}

/** 浏览器组件参数。 */
export interface BrowserViewProps {
 endpoint: string
 className?: string
 style?: CSSProperties
 classNames?: Partial<Record<'toolbar' | 'tabs' | 'tab' | 'viewport' | 'devtools', string>>
 readOnly?: boolean
 /** 暂停页面交互，但保留控制方的关闭入口。 */
 busy?: boolean
 /** 用户请求关闭时立即通知宿主，不等待浏览器回收。 */
 onClose?: () => void
 /** 区域与按钮显示开关；false 隐藏全部内置控制区域。 */
 controls?: false | BrowserControls
 /** 页面输入开关；false 停止所有页面输入，仍可使用导航和 Tab。 */
 input?: false | BrowserInputOptions
 devtools?: boolean | BrowserDevtoolsOptions
 liveView?: boolean
 viewport?: 'container' | { width: number; height: number }
 viewerID?: string
 onStateChange?: (state: BrowserViewState) => void
 onError?: (error: Error) => void
}

export function BrowserView(props: BrowserViewProps) { return <BrowserSurface key={props.endpoint} {...props} /> }

function BrowserSurface({ endpoint: sessionID, className = '', style, classNames, readOnly = false, busy = false, onClose, controls, input, devtools = false, liveView: liveViewEnabled = true, viewport: configuredViewport = 'container', viewerID: suppliedViewerID, onStateChange, onError }: BrowserViewProps) {
 const visible = (name: keyof BrowserControls) => controls !== false && controls?.[name] !== false
 const mouseEnabled = input !== false && input?.mouse !== false
 const touchEnabled = input !== false && input?.touch !== false
 const wheelEnabled = input !== false && input?.wheel !== false
 const keyboardEnabled = input !== false && input?.keyboard !== false
 const clipboardEnabled = input !== false && input?.clipboard !== false
 const manualKeyboard = input !== false && input?.keyboardActivation === 'manual'
 const mouseEnabledRef = useRef(mouseEnabled); mouseEnabledRef.current = mouseEnabled
 const devtoolsOptions = typeof devtools === 'object' ? devtools : undefined
 const devtoolsEnabled = Boolean(devtools) && devtoolsOptions?.panels?.length !== 0
 const [generatedViewerID] = useState(createBrowserViewerID)
 const viewerID = suppliedViewerID || generatedViewerID
 const stateCallback = useRef(onStateChange); stateCallback.current = onStateChange

  const [pageURL, setPageURL] = useState<string | undefined>(undefined)
  const [addressValue, setAddressValue] = useState('')
  const [viewport, setViewport] = useState<BrowserViewport | undefined>(undefined)
  const [tabs, setTabs] = useState<BrowserTabView[]>([])
  const [activePageID, setActivePageID] = useState<string | undefined>(undefined)
  const [hasFrame, setHasFrame] = useState(false)
  const [waitingForFrame, setWaitingForFrame] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [closing, setClosing] = useState(false)
  const closingRef = useRef(false)
  const [notice, setNotice] = useState<string>()
  const errorCallback = useRef(onError); errorCallback.current = onError
  useEffect(() => { if (notice) errorCallback.current?.(new Error(notice)) }, [notice])
  const [panelHost, setPanelHost] = useState<HTMLElement | null>(null)
  const [sideViewportSize, setSideViewportSize] = useState<BrowserViewport>()
  const [mobilePanel, setMobilePanel] = useState<'page' | 'devtools'>(devtoolsEnabled && (devtoolsOptions?.open ?? devtoolsOptions?.defaultOpen) ? 'devtools' : 'page')
  const [mobileInputActive, setMobileInputActive] = useState(false)
  const [devtoolsHeight, setDevtoolsHeight] = useState<string>()
  const [internalDevtoolsOpen, setInternalDevtoolsOpen] = useState(devtoolsOptions?.defaultOpen ?? false)
  const devtoolsPanelOpen = devtoolsEnabled && (devtoolsOptions?.open ?? internalDevtoolsOpen)
  const setDevtoolsPanelOpen = (open: boolean) => {
    if (devtoolsOptions?.open === undefined) setInternalDevtoolsOpen(open)
    devtoolsOptions?.onOpenChange?.(open)
  }
  useEffect(() => { setMobilePanel(devtoolsPanelOpen ? 'devtools' : 'page') }, [devtoolsPanelOpen])
  const [browserControl, setBrowserControl] = useState<BrowserControlState>()
  const [controlRetryAfterMS, setControlRetryAfterMS] = useState(0)
  const [takingControl, setTakingControl] = useState(false)
  const seenRef = useRef(false)
  const activePageRef = useRef<string | undefined>(undefined)
  const stateRevisionRef = useRef(0)
  const tabSelectionRef = useRef(0)
  const addressEditingRef = useRef(false)
  // 浏览器 Tab 切换期间先复用该 Tab 的本地最后一帧，避免等待 HTTP/WS 往返。
  // Blob 本身不可变；Tab 关闭时从 Map 删除即可释放引用。
  const frameGenerations = useRef(new Map<string, number>())
  const frameCacheRef = useRef(new Map<string, Blob>())
  const framePageRef = useRef<string | undefined>(undefined)
  const frameSequenceRef = useRef(0)
  const devtoolsResizeRef = useRef<DevtoolsResize | undefined>(undefined)
  const socketRef = useRef<WebSocket | undefined>(undefined)
  const frameRef = useRef<HTMLDivElement | null>(null)
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const inputRef = useRef<HTMLTextAreaElement | null>(null)
  const touchesRef = useRef(new Map<number, RemotePoint & { id: number; radiusX: number; radiusY: number; force: number }>())
  const nextTouchIDRef = useRef(0)
  const pendingTouchMoveRef = useRef<PendingMove | undefined>(undefined)
  const touchMoveFrameRef = useRef<number | undefined>(undefined)
  const pendingMouseMoveRef = useRef<PendingMove | undefined>(undefined)
  const mouseMoveFrameRef = useRef<number | undefined>(undefined)
  const mobileInputToggleRef = useRef(false)
  const composingRef = useRef(false)
  const suppressInputRef = useRef(false)
  const shortcutKeysRef = useRef(new Set<string>())
  const suppressedKeyRefs = useRef(new Set<string>())
  const clickRef = useRef<{ button: number; x: number; y: number; at: number; count: number } | undefined>(undefined)
  const heldKeysRef = useRef(new Map<string, { pageID: string; input: BrowserInput }>())
  const heldMouseRef = useRef(new Map<string, { pageID: string; input: BrowserInput }>())
  const mouseButtonsRef = useRef(new Map<number, number>())
  // CDP 需要在 mouseMoved 中同时带上上一次按下的 button。仅传 buttons
  // 位掩码在普通页面上通常可用，但 Chromium 的原生滚动条拖动会因此
  // 把移动识别成未按键移动，导致滚动条只高亮、不跟随指针。
  const mouseButtonRef = useRef(new Map<number, number>())
  const expectedFrameRef = useRef<(BrowserViewport & { pageID: string }) | undefined>(undefined)
  const sideViewportSizeRef = useRef<BrowserViewport | undefined>(undefined)
  const viewportGenerationRef = useRef(0)
  const viewportPendingRef = useRef<ViewportTarget | undefined>(undefined)
  const viewportInFlightRef = useRef(false)
  const viewportAppliedRef = useRef(new Map<string, BrowserViewport>())
  const viewportDebounceTimerRef = useRef<number | undefined>(undefined)
  const viewportRetryTimerRef = useRef<number | undefined>(undefined)
  const flushViewportRef = useRef<() => void>(() => undefined)
  const correctViewportRef = useRef<(pageID: string) => void>(() => undefined)
  const browserControllerRef = useRef(false)
  const readOnlyRef = useRef(readOnly); readOnlyRef.current = readOnly || busy || closing
  const browserControlRevisionRef = useRef(0)
  sideViewportSizeRef.current = sideViewportSize
  browserControllerRef.current = !readOnlyRef.current && browserControl?.controller === true

  useEffect(() => {
    if (!visible('toolbar') || !visible('addressBar')) addressEditingRef.current = false
    if (!addressEditingRef.current) setAddressValue(pageURL || '')
  }, [pageURL, controls === false ? false : controls?.toolbar, controls === false ? false : controls?.addressBar])

  const clearKeyboardState = () => {
    heldKeysRef.current.clear()
    shortcutKeysRef.current.clear()
    suppressedKeyRefs.current.clear()
    composingRef.current = false
    suppressInputRef.current = false
  }

  const cancelPendingTouchMove = () => {
    if (touchMoveFrameRef.current !== undefined) cancelAnimationFrame(touchMoveFrameRef.current)
    touchMoveFrameRef.current = undefined
    pendingTouchMoveRef.current = undefined
  }

  const cancelPendingMouseMove = () => {
    if (mouseMoveFrameRef.current !== undefined) cancelAnimationFrame(mouseMoveFrameRef.current)
    mouseMoveFrameRef.current = undefined
    pendingMouseMoveRef.current = undefined
  }

  const cancelPendingMoves = () => {
    cancelPendingTouchMove()
    cancelPendingMouseMove()
  }

  // 操作权丢失和直播连接断开具有相同的输入边界：两者都必须取消尚未
  // 提交的 viewport/触摸操作，并清除本地按键状态，避免重连后续接旧手势。
  const loseBrowserControl = () => {
    browserControllerRef.current = false
    viewportGenerationRef.current += 1
    viewportPendingRef.current = undefined
    expectedFrameRef.current = undefined
    if (viewportDebounceTimerRef.current !== undefined) window.clearTimeout(viewportDebounceTimerRef.current)
    if (viewportRetryTimerRef.current !== undefined) window.clearTimeout(viewportRetryTimerRef.current)
    viewportDebounceTimerRef.current = undefined
    viewportRetryTimerRef.current = undefined
    cancelPendingMoves()
    touchesRef.current.clear()
    heldMouseRef.current.clear()
    mouseButtonsRef.current.clear()
    mouseButtonRef.current.clear()
    clickRef.current = undefined
    inputRef.current?.blur()
    setMobileInputActive(false)
    clearKeyboardState()
  }

  useEffect(() => {
    if (busy) loseBrowserControl()
  }, [busy])

  const applyBrowserControl = (state: BrowserControlState) => {
    if (state.type !== 'browser_control') return
    if (state.revision < browserControlRevisionRef.current) return
    browserControlRevisionRef.current = state.revision
    const wasController = browserControllerRef.current
    browserControllerRef.current = !readOnlyRef.current && state.controller
    setBrowserControl(state)
    setControlRetryAfterMS(Math.max(0, state.retry_after_ms || 0))
    if (wasController && !state.controller) loseBrowserControl()
  }

  useEffect(() => {
    if (controlRetryAfterMS <= 0) return
    const startedAt = Date.now()
    const initial = controlRetryAfterMS
    const timer = window.setInterval(() => {
      const remaining = Math.max(0, initial - (Date.now() - startedAt))
      setControlRetryAfterMS(remaining)
      if (remaining === 0) window.clearInterval(timer)
    }, 250)
    return () => window.clearInterval(timer)
  }, [browserControl?.revision])

  // HTTP 初始读取、独立状态 WebSocket 和用户操作后的读取可能乱序到达。
  // tabs_revision 只在 Manager 的可见 Tab 投影变化时递增；旧状态不能
  // 把输入重新绑定到已经离开的 page_id，也不能回滚 Tab 的加载状态。
  const acceptState = (state: BrowserViewState) => {
    const revision = state.tabs_revision || 0
    if (revision && revision < stateRevisionRef.current) return false
    if (revision) stateRevisionRef.current = revision
    return true
  }

  const clearFrame = () => {
    cancelPendingMoves()
    touchesRef.current.clear()
    frameSequenceRef.current += 1
    framePageRef.current = undefined
    setHasFrame(false)
    setWaitingForFrame(true)
  }

  const drawFrame = async (blob: Blob, pageID = activePageRef.current, streamed = true): Promise<boolean> => {
    if (closingRef.current) return false
    const knownPage = (blob as Blob & { pageID?: string }).pageID
    if (knownPage && pageID !== knownPage) return false
    const sequence = ++frameSequenceRef.current
    try {
      const bitmap = await createImageBitmap(blob)
      if (pageID && activePageRef.current && pageID !== activePageRef.current) {
        bitmap.close()
        return false
      }
      if (pageID) frameCacheRef.current.set(pageID, blob)
      if (sequence !== frameSequenceRef.current) {
        bitmap.close()
        return false
      }
      const canvas = canvasRef.current
      const context = canvas?.getContext('2d')
      if (!canvas || !context) {
        bitmap.close()
        return false
      }
      if (canvas.width !== bitmap.width) canvas.width = bitmap.width
      if (canvas.height !== bitmap.height) canvas.height = bitmap.height
      context.drawImage(bitmap, 0, 0)
      const actualViewport = { width: bitmap.width, height: bitmap.height }
      setViewport(actualViewport)
      framePageRef.current = pageID
      const expected = expectedFrameRef.current
      const desired = sideViewportSizeRef.current
      if (browserControllerRef.current && pageID && activePageRef.current === pageID && desired) {
        if (bitmap.width === desired.width && bitmap.height === desired.height) {
          if (sessionID) viewportAppliedRef.current.set(pageID, desired)
          const pending = viewportPendingRef.current
          if (pending?.pageID === pageID && sameViewport(pending, desired)) {
            viewportPendingRef.current = undefined
            if (viewportDebounceTimerRef.current !== undefined) window.clearTimeout(viewportDebounceTimerRef.current)
            viewportDebounceTimerRef.current = undefined
          }
          if (expected?.pageID === pageID) expectedFrameRef.current = undefined
          setViewport(desired)
        } else {
          // 旧尺寸帧仍然有显示价值；保留并绘制它，同时在独立防抖窗口后
          // 重新校正远端 viewport。后续错误帧不会不断推迟这个计时器。
          correctViewportRef.current(pageID)
        }
      } else if (expected && expected.pageID === pageID && bitmap.width === expected.width && bitmap.height === expected.height) {
        expectedFrameRef.current = undefined
      }
      bitmap.close()
      setHasFrame(true)
      if (streamed) setWaitingForFrame(false)
      return true
    } catch {
      // 页面切换期间可能收到已经失效或不完整的帧，下一帧会继续覆盖。
      return false
    }
  }

  const flushViewport = () => {
    if (viewportInFlightRef.current) return
    const target = viewportPendingRef.current
    if (!target) return
    if (!target.force && sameViewport(viewportAppliedRef.current.get(target.pageID), target)) {
      viewportPendingRef.current = undefined
      return
    }
    viewportPendingRef.current = undefined
    viewportInFlightRef.current = true
    if (target.generation === viewportGenerationRef.current && activePageRef.current === target.pageID) {
      expectedFrameRef.current = { width: target.width, height: target.height, pageID: target.pageID }
      touchesRef.current.clear()
      clickRef.current = undefined
      clearKeyboardState()
    }
    void setBrowserTabViewport(target.sessionID, target.pageID, { width: target.width, height: target.height }, viewerID).then(() => {
      if (target.generation !== viewportGenerationRef.current) return
      viewportAppliedRef.current.set(target.pageID, { width: target.width, height: target.height })
      if (activePageRef.current !== target.pageID) return
      const nextViewport = { width: target.width, height: target.height }
      setTabs((current) => current.map((tab) => tab.page_id === target.pageID ? { ...tab, viewport: nextViewport } : tab))
      setViewport(nextViewport)
      setNotice(undefined)
    }).catch((error) => {
      const stillCurrent = target.generation === viewportGenerationRef.current && activePageRef.current === target.pageID
      if (stillCurrent && target.attempt === 0 && !viewportPendingRef.current) {
        viewportPendingRef.current = { ...target, attempt: 1 }
        viewportRetryTimerRef.current = window.setTimeout(() => {
          viewportRetryTimerRef.current = undefined
          flushViewportRef.current()
        }, viewportResizeRetryMS)
        return
      }
      if (stillCurrent) {
        expectedFrameRef.current = undefined
        setNotice(error instanceof Error ? `调整浏览器视口失败：${error.message}` : '调整浏览器视口失败')
      }
    }).finally(() => {
      viewportInFlightRef.current = false
      if (viewportPendingRef.current && viewportDebounceTimerRef.current === undefined && viewportRetryTimerRef.current === undefined) flushViewportRef.current()
    })
  }
  flushViewportRef.current = flushViewport

  const scheduleViewport = (pageID: string, size: BrowserViewport, force: boolean, restartDebounce: boolean) => {
    if (!browserControllerRef.current || !sessionID || activePageRef.current !== pageID) return
    if (force) viewportAppliedRef.current.delete(pageID)
    viewportPendingRef.current = {
      sessionID,
      pageID,
      width: size.width,
      height: size.height,
      generation: viewportGenerationRef.current,
      attempt: 0,
      force,
    }
    if (viewportRetryTimerRef.current !== undefined) {
      window.clearTimeout(viewportRetryTimerRef.current)
      viewportRetryTimerRef.current = undefined
    }
    if (restartDebounce && viewportDebounceTimerRef.current !== undefined) {
      window.clearTimeout(viewportDebounceTimerRef.current)
      viewportDebounceTimerRef.current = undefined
    }
    if (viewportDebounceTimerRef.current === undefined) {
      viewportDebounceTimerRef.current = window.setTimeout(() => {
        viewportDebounceTimerRef.current = undefined
        flushViewportRef.current()
      }, viewportResizeDebounceMS)
    }
  }
  correctViewportRef.current = (pageID) => {
    const size = sideViewportSizeRef.current
    if (size) scheduleViewport(pageID, size, true, false)
  }

  useEffect(() => {
    viewportGenerationRef.current += 1
    viewportPendingRef.current = undefined
    expectedFrameRef.current = undefined
    setSideViewportSize(undefined)
    if (viewportDebounceTimerRef.current !== undefined) window.clearTimeout(viewportDebounceTimerRef.current)
    if (viewportRetryTimerRef.current !== undefined) window.clearTimeout(viewportRetryTimerRef.current)
    viewportDebounceTimerRef.current = undefined
    viewportRetryTimerRef.current = undefined
  }, [sessionID])

  useEffect(() => {
    viewportAppliedRef.current.clear()
    browserControllerRef.current = false
    browserControlRevisionRef.current = 0
    setBrowserControl(undefined)
    setControlRetryAfterMS(0)
    setTakingControl(false)
  }, [sessionID])

  useEffect(() => {
    if (!liveViewEnabled || !panelHost || typeof ResizeObserver === 'undefined') return
    const frame = frameRef.current
    if (!frame) return
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect
      if (!rect) return
      const size = configuredViewport === 'container' ? viewportFromContentRect(rect) : configuredViewport
      if (!size) return
      setSideViewportSize((current) => sameViewport(current, size) ? current : size)
    })
    observer.observe(frame)
    return () => observer.disconnect()
  }, [liveViewEnabled, panelHost])

  useEffect(() => {
    if (readOnly || busy || closing || !browserControl?.controller || !liveViewEnabled || !sessionID || !activePageID || !sideViewportSize) return
    scheduleViewport(activePageID, sideViewportSize, false, true)
  }, [activePageID, browserControl?.controller, liveViewEnabled, sessionID, sideViewportSize, readOnly, busy, closing])

  useEffect(() => {
    // 浮窗、侧栏和最小化状态会重建 canvas；旧 backing store 不能复用。
    clearFrame()
  }, [])

  useEffect(() => {
    inputRef.current?.blur()
    setMobileInputActive(false)
    cancelPendingMoves()
    touchesRef.current.clear()
  }, [activePageID, liveViewEnabled, mobilePanel, sessionID])

  useEffect(() => {

    setPageURL(undefined)
    setAddressValue('')
    addressEditingRef.current = false
    setViewport(undefined)
    setTabs([])
    setActivePageID(undefined)
    frameCacheRef.current.clear()
    seenRef.current = false
    activePageRef.current = undefined
    stateRevisionRef.current = 0
    tabSelectionRef.current += 1

    setSyncing(false)
    setWaitingForFrame(false)

    clearKeyboardState()
    heldMouseRef.current.clear()
    mouseButtonsRef.current.clear()
    mouseButtonRef.current.clear()
    clearFrame()
  }, [sessionID])

  useEffect(() => {
    if (!sessionID) {

      seenRef.current = false

      setSyncing(false)
      setWaitingForFrame(false)
      setPageURL(undefined)
      setAddressValue('')
      addressEditingRef.current = false
      setViewport(undefined)
      activePageRef.current = undefined
      stateRevisionRef.current = 0
      tabSelectionRef.current += 1
      setTabs([])
      setActivePageID(undefined)
      frameCacheRef.current.clear()

      clearKeyboardState()
      heldMouseRef.current.clear()
      mouseButtonsRef.current.clear()
      mouseButtonRef.current.clear()
      clearFrame()
      return
    }
    let active = true
    const applyState = (state: BrowserViewState) => {
      if (!active) return
      if (!acceptState(state)) return
      stateCallback.current?.(state)
      if (state.closed) {
        seenRef.current = false

        setSyncing(false)
        setWaitingForFrame(false)

        setPageURL(undefined)
        setAddressValue('')
        addressEditingRef.current = false
        setViewport(undefined)
        activePageRef.current = undefined
        stateRevisionRef.current = 0
        tabSelectionRef.current += 1
        setTabs([])
        setActivePageID(undefined)
        frameCacheRef.current.clear()
        viewportAppliedRef.current.clear()

        clearKeyboardState()
        heldMouseRef.current.clear()
        mouseButtonsRef.current.clear()
        mouseButtonRef.current.clear()
        clearFrame()

        return
      }

      setSyncing(!state.available && seenRef.current)
      const nextTabs = state.tabs ?? []
      const activeChanged = activePageRef.current !== undefined && activePageRef.current !== state.active_page_id
      if (activeChanged && state.active_page_id) setWaitingForFrame(true)
      if (Array.isArray(state.tabs) && nextTabs.length === 0) {
        // 服务端明确返回 tabs: [] 即表示所有 Tab 已回收；清空画布和
        // 所有本地帧缓存，不依赖 available 状态。
        frameCacheRef.current.clear()
        viewportAppliedRef.current.clear()
        activePageRef.current = undefined
        setActivePageID(undefined)
        setTabs([])
        setPageURL(undefined)
        setViewport(undefined)
        clearFrame()
        setWaitingForFrame(false)
      }
      if (state.available) {
        if (activePageRef.current !== undefined && activePageRef.current !== state.active_page_id && sessionID) {
          // 先显示 Manager 按 Tab 保存的最后帧；新 screencast 首帧到达后再覆盖，避免黑屏。
          void fetchCachedBrowserFrame(sessionID).then((cached) => drawFrame(cached, state.active_page_id, false)).catch(() => undefined)
        }
        activePageRef.current = state.active_page_id
        if (state.tabs) {
          const availablePages = new Set(state.tabs.map((tab) => tab.page_id))
          for (const pageID of frameCacheRef.current.keys()) {
            if (!availablePages.has(pageID)) frameCacheRef.current.delete(pageID)
          }
          for (const pageID of viewportAppliedRef.current.keys()) {
            if (!availablePages.has(pageID)) viewportAppliedRef.current.delete(pageID)
          }
        }
        seenRef.current = true

        setPageURL(state.url)
        setTabs(nextTabs)
        setActivePageID(state.active_page_id)
        if (state.viewport?.width && state.viewport?.height) {
          const expected = expectedFrameRef.current
          if (!expected || expected.pageID !== state.active_page_id || expected.width === state.viewport.width && expected.height === state.viewport.height) setViewport(state.viewport)
        }

      }
    }
    const refresh = () => void fetchBrowserState(sessionID).then(applyState).catch(() => {
      // 状态查询失败可能只是 CDP 在切换 target；保留已经打开的浮窗和最后一帧。
      if (active) setSyncing(seenRef.current)
    })
    refresh()
    let retry: number | undefined
    let socket: WebSocket | undefined
    const connect = () => {
      if (!active) return
      socket = new WebSocket(browserStateStreamURL(sessionID))
      socket.onmessage = (event) => {
        if (!active || typeof event.data !== 'string') return
        try { applyState(JSON.parse(event.data) as BrowserViewState) } catch { setSyncing(seenRef.current) }
      }
      socket.onclose = () => {
        if (!active) return
        // 状态连接结束可能正是 Agent/Plugin runtime 被关闭。主动读取一次
        // 权威终态；仅重连会在新连接被策略拒绝时永久保留旧 tabs 和画面。
        refresh()
        retry = window.setTimeout(connect, 500)
      }
      socket.onerror = () => socket?.close()
    }
    connect()
    return () => { active = false; if (retry !== undefined) window.clearTimeout(retry); socket?.close() }
  }, [devtoolsEnabled, liveViewEnabled, sessionID])

  useEffect(() => {
    if ((!liveViewEnabled && !devtoolsEnabled) || !sessionID || !panelHost) return
    let active = true
    let retry: number | undefined
    let socket: WebSocket | undefined
    const connect = () => {
      if (!active) return
      socket = new WebSocket(browserStreamURL(sessionID, viewerID, readOnly))
      socketRef.current = socket
      socket.binaryType = 'blob'
      socket.onmessage = (event) => {
        if (!active) return
        if (typeof event.data === 'string') {
          try {
            const message = JSON.parse(event.data)
            if (message.type === 'browser_input_error') {
              setNotice(message.error.message)
            } else {
              applyBrowserControl(message as BrowserControlState)
            }
          } catch { /* 下一条控制消息会恢复。 */ }
          return
        }
        if (!(event.data instanceof Blob)) return
        void decodeFrame(event.data).then((frame) => {
          if (!active || frame.pageID !== activePageRef.current) return false
          if (frame.generation < (frameGenerations.current.get(frame.pageID) ?? 0)) return false
          frameGenerations.current.set(frame.pageID, frame.generation)
          return drawFrame(frame.blob, frame.pageID, true)
        }).catch(() => undefined)
      }
      socket.onclose = () => {
        if (socketRef.current === socket) socketRef.current = undefined
        if (active) {
          loseBrowserControl()
          setBrowserControl(undefined)
          retry = window.setTimeout(connect, 50)
        }
      }
      socket.onerror = () => socket?.close()
    }
    connect()
    return () => {
      active = false
      if (retry !== undefined) window.clearTimeout(retry)
      if (socketRef.current === socket) socketRef.current = undefined
      socket?.close()
    }
  }, [devtoolsEnabled, liveViewEnabled, panelHost, sessionID, viewerID, readOnly])

  useEffect(() => () => {
    frameSequenceRef.current += 1
    cancelPendingMoves()
  }, [])

  useEffect(() => {
    if (!notice) return
    const timer = window.setTimeout(() => setNotice(undefined), 3_500)
    return () => window.clearTimeout(timer)
  }, [notice])

  const startDevtoolsResize = (event: React.PointerEvent<HTMLDivElement>) => {
    if ((panelHost?.clientWidth ?? 0) <= 520) return
    devtoolsResizeRef.current = { pointerID: event.pointerId }
    event.currentTarget.setPointerCapture?.(event.pointerId)
    event.preventDefault()
  }

  const moveDevtoolsResize = (event: React.PointerEvent<HTMLDivElement>) => {
    if (devtoolsResizeRef.current?.pointerID !== event.pointerId) return
    const body = event.currentTarget.parentElement
    if (!body) return
    const rect = body.getBoundingClientRect()
    const height = Math.min(Math.max(190, rect.bottom - event.clientY), Math.max(190, rect.height - 150 - 8))
    setDevtoolsHeight(`${Math.round(height)}px`)
  }

  const endDevtoolsResize = (event: React.PointerEvent<HTMLDivElement>) => {
    if (devtoolsResizeRef.current?.pointerID !== event.pointerId) return
    devtoolsResizeRef.current = undefined
    event.currentTarget.releasePointerCapture?.(event.pointerId)
  }

  const openDevtoolsPanel = () => {
    if (devtoolsEnabled) setDevtoolsPanelOpen(true)
  }

  const closeDevtoolsPanel = () => {
    setDevtoolsPanelOpen(false)
  }

  const sendInput = (input: BrowserInput) => {
    // 图流仅用于显示；输入目标只取独立 state 通道确认过的
    // active page，并由 Manager 在入队和投递前再次校验。
    if (!liveViewEnabled || !browserControllerRef.current) return
    if (input.kind === 'mouse' && (input.action === 'wheel' ? !wheelEnabled : !mouseEnabled)) return
    if (input.kind === 'touch' && !touchEnabled) return
    if (['key', 'shortcut', 'composition', 'text'].includes(input.kind) && !keyboardEnabled) return
    if (input.kind === 'shortcut' && ['copy', 'paste'].includes(input.action || '') && !clipboardEnabled) return
    const pageID = activePageRef.current
    if (!hasFrame || !pageID) return
    const expected = expectedFrameRef.current
    if (expected && expected.pageID === activePageRef.current) return
    const socket = socketRef.current
    if (socket?.readyState !== 1) return
    const mouseMove = input.kind === 'mouse' && input.action === 'move'
    if (mouseMove && (socket.bufferedAmount || 0) >= browserInputBackpressureBytes) {
      // 链路健康时逐样本发送以保留窄 hover 轨迹；只有实际拥塞时才合并，
      // 并保留最终位置，避免用户停止移动后远端指针永久停在旧位置。
      pendingMouseMoveRef.current = { pageID, input }
      if (mouseMoveFrameRef.current === undefined) {
        const flush = () => {
          mouseMoveFrameRef.current = undefined
          const pending = pendingMouseMoveRef.current
          if (!pending) return
          const pendingSocket = socketRef.current
          if (!mouseEnabledRef.current || pending.pageID !== activePageRef.current || !browserControllerRef.current || pendingSocket?.readyState !== 1) {
            pendingMouseMoveRef.current = undefined
            return
          }
          if ((pendingSocket.bufferedAmount || 0) >= browserInputBackpressureBytes) {
            mouseMoveFrameRef.current = requestAnimationFrame(flush)
            return
          }
          pendingMouseMoveRef.current = undefined
          try { pendingSocket.send(JSON.stringify({ ...pending.input, page_id: pending.pageID })) } catch { /* best effort */ }
        }
        mouseMoveFrameRef.current = requestAnimationFrame(flush)
      }
      return
    }
    if (input.kind === 'mouse') cancelPendingMouseMove()
    // 快捷键、按键边界、触控和输入法最终文本不可重建，始终立即入队。
    // 浏览器图帧走相反方向，不再用帧写超时重连来干扰这些输入。
    try {
      socket.send(JSON.stringify({ ...input, page_id: pageID }))
      if (input.kind === 'key') {
        const key = input.code || input.key || ''
        if (input.action === 'down') heldKeysRef.current.set(key, { pageID, input })
        else heldKeysRef.current.delete(key)
      }
      if (input.kind === 'mouse' && input.button) {
        if (input.action === 'down') heldMouseRef.current.set(input.button, { pageID, input })
        else if (input.action === 'up') heldMouseRef.current.delete(input.button)
      }
    } catch { /* best effort */ }
  }

  // 动态关闭输入时补齐已经发送的按下边界，并取消尚未发送的采样。
  // 只发送释放事件，目标仍是按下时的页面，不会把旧手势带入新 Tab。
  useEffect(() => {
    const release = (pageID: string, input: BrowserInput) => {
      const socket = socketRef.current
      if (!browserControllerRef.current || pageID !== activePageRef.current || socket?.readyState !== 1) return
      try { socket.send(JSON.stringify({ ...input, page_id: pageID })) } catch { /* 连接结束时由服务端回收。 */ }
    }
    if (!mouseEnabled) {
      cancelPendingMouseMove()
      for (const { pageID, input } of heldMouseRef.current.values()) release(pageID, { ...input, action: 'up', buttons: 0 })
      heldMouseRef.current.clear()
      mouseButtonsRef.current.clear()
      mouseButtonRef.current.clear()
      clickRef.current = undefined
    }
    if (!touchEnabled) {
      cancelPendingTouchMove()
      if (touchesRef.current.size && activePageRef.current) release(activePageRef.current, { kind: 'touch', action: 'cancel', touches: [] })
      touchesRef.current.clear()
    }
    if (!keyboardEnabled) {
      for (const { pageID, input } of heldKeysRef.current.values()) release(pageID, { ...input, action: 'up', text: undefined, auto_repeat: false })
      heldKeysRef.current.clear()
    }
  }, [mouseEnabled, touchEnabled, keyboardEnabled])

  const flushPendingTouchMove = () => {
    if (touchMoveFrameRef.current !== undefined) cancelAnimationFrame(touchMoveFrameRef.current)
    touchMoveFrameRef.current = undefined
    const pending = pendingTouchMoveRef.current
    pendingTouchMoveRef.current = undefined
    if (pending && pending.pageID === activePageRef.current) sendInput(pending.input)
  }

  const scheduleTouchMove = (input: BrowserInput) => {
    pendingTouchMoveRef.current = { pageID: activePageRef.current, input }
    if (touchMoveFrameRef.current !== undefined) return
    touchMoveFrameRef.current = requestAnimationFrame(() => {
      touchMoveFrameRef.current = undefined
      const pending = pendingTouchMoveRef.current
      pendingTouchMoveRef.current = undefined
      if (pending && pending.pageID === activePageRef.current) sendInput(pending.input)
    })
  }

  const pointAt = (clientX: number, clientY: number, clamp: boolean): RemotePoint | undefined => {
    const frame = frameRef.current
    if (!frame || !viewport) return undefined
    const expected = expectedFrameRef.current
    if (expected && expected.pageID === activePageRef.current) return undefined
    const canvas = canvasRef.current
    if (!canvas || canvas.width !== viewport.width || canvas.height !== viewport.height) return undefined
    const rect = frame.getBoundingClientRect()
    if (rect.width <= 0 || rect.height <= 0) return undefined
    const scale = Math.min(rect.width / viewport.width, rect.height / viewport.height)
    const width = viewport.width * scale
    const height = viewport.height * scale
    const left = rect.left + (rect.width - width) / 2
    const top = rect.top + (rect.height - height) / 2
    if (!clamp && (clientX < left || clientX > left + width || clientY < top || clientY > top + height)) return undefined
    const x = Math.min(viewport.width - 0.001, Math.max(0, (clientX - left) / width * viewport.width))
    const y = Math.min(viewport.height - 0.001, Math.max(0, (clientY - top) / height * viewport.height))
    return { x, y, scaleX: viewport.width / width, scaleY: viewport.height / height }
  }

  const modifiers = (event: { altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }) =>
    (event.altKey ? 1 : 0) | (event.ctrlKey ? 2 : 0) | (event.metaKey ? 4 : 0) | (event.shiftKey ? 8 : 0)

  const mouseButton = (button: number) => ['left', 'middle', 'right', 'back', 'forward'][button] || 'none'

  const focusInput = () => {
    if (!keyboardEnabled || !browserControllerRef.current || !inputRef.current) return
    // 同一用户事件中先更新 inputMode 再聚焦，确保移动端可以弹出键盘。
    inputRef.current.inputMode = 'text'
    inputRef.current.focus({ preventScroll: true })
  }

  const toggleMobileInput = () => {
    mobileInputToggleRef.current = false
    if (!keyboardEnabled || !browserControllerRef.current) return
    if (mobileInputActive) {
      inputRef.current?.blur()
      clearKeyboardState()
      setMobileInputActive(false)
      return
    }
    setMobileInputActive(true)
    // 必须在用户点击的同一个事件中聚焦，iOS 才会可靠地打开软键盘。
    focusInput()
  }

  useEffect(() => {
    inputRef.current?.blur()
    setMobileInputActive(false)
    clearKeyboardState()
  }, [keyboardEnabled, manualKeyboard])

  useEffect(() => {
    if (!visible('keyboard') && mobileInputActive) {
      inputRef.current?.blur()
      setMobileInputActive(false)
    }
  }, [controls === false ? false : controls?.keyboard])

  const onInputBlur = (event: React.FocusEvent<HTMLTextAreaElement>) => {
    // 点击角标本身会先让 textarea 失焦，再触发 button click；此时保留
    // 状态交给 toggleMobileInput 处理，避免一次点击被误判为系统收起键盘。
    const next = event.relatedTarget
    if (mobileInputToggleRef.current || (next instanceof HTMLElement && next.closest('.browser-live-window-mobile-input'))) return
    clearKeyboardState()
    setMobileInputActive(false)
  }

  const touchInput = (action: 'start' | 'move' | 'end' | 'cancel', event: React.TouchEvent<HTMLDivElement>) => {
    // start/end/cancel 是顺序边界，必须先提交上一帧尚未发送的最终位置；
    // 连续 move 则每个本地渲染帧只发送最新触点，避免 WebSocket 先积压旧轨迹。
    if (action !== 'move') flushPendingTouchMove()
    const active = new Set<number>()
    for (const touch of Array.from(event.touches)) {
      const contact = touch as Touch & { radiusX?: number; radiusY?: number; force?: number }
      const point = pointAt(touch.clientX, touch.clientY, action === 'move')
      if (!point) continue
      const previous = touchesRef.current.get(touch.identifier)
      if (action === 'move' && !previous) continue
      active.add(touch.identifier)
      touchesRef.current.set(touch.identifier, {
        ...point,
        id: previous?.id ?? nextTouchIDRef.current++,
        radiusX: Math.max(1, (contact.radiusX || .5) * point.scaleX),
        radiusY: Math.max(1, (contact.radiusY || .5) * point.scaleY),
        force: contact.force && contact.force > 0 ? Math.min(1, contact.force) : 1,
      })
    }
    for (const identifier of touchesRef.current.keys()) {
      if (!active.has(identifier)) touchesRef.current.delete(identifier)
    }
    const touches = action === 'cancel' ? [] : Array.from(touchesRef.current.values(), (touch) => ({
      id: touch.id, x: touch.x, y: touch.y, radius_x: touch.radiusX, radius_y: touch.radiusY, force: touch.force,
    }))
    // CDP 的 touchEnd 只接受空触点；多指操作中单指离开时用 move 更新
    // 剩余触点，最后一个触点离开时再发送真正的 end。
    const inputAction = action === 'end' && touches.length > 0 ? 'move' : action
    if ((inputAction === 'start' || inputAction === 'move') && touches.length === 0) return
    const input: BrowserInput = { kind: 'touch', action: inputAction, touches, modifiers: modifiers(event) }
    if (inputAction === 'move') scheduleTouchMove(input)
    else sendInput(input)
    if (action === 'cancel') touchesRef.current.clear()
    if ((action === 'end' || action === 'cancel') && touchesRef.current.size === 0) nextTouchIDRef.current = 0
  }

  const onPageTouch = (action: 'start' | 'move' | 'end' | 'cancel', event: React.TouchEvent<HTMLDivElement>) => {
    if (!touchEnabled || !browserControllerRef.current || !liveViewEnabled || (event.target as HTMLElement).closest('button')) return
    event.preventDefault()
    touchInput(action, event)
  }

  const onPagePointerDown = (event: React.PointerEvent<HTMLDivElement>) => {

    // 触摸统一由 TouchEvent 处理。荣耀等 Android WebView 会同时派发
    // PointerEvent 与 TouchEvent，若两条路径并存会产生重复 touchStart。
    if (!mouseEnabled || !browserControllerRef.current || !liveViewEnabled || event.pointerType === 'touch') return
    const point = pointAt(event.clientX, event.clientY, false)
    if (!point) return
    if (manualKeyboard || !keyboardEnabled) frameRef.current?.focus({ preventScroll: true })
    else focusInput()
    event.preventDefault()
    // WebKit/WebView 偶尔会在 pointerdown 中对仍然有效的指针抛
    // NotFoundError。Pointer capture 只用于把越界 move/up 留在画面上，
    // 失败不能阻断当前鼠标或触控笔输入本身。
    try { event.currentTarget.setPointerCapture?.(event.pointerId) } catch { /* 继续传递当前输入。 */ }
    const previous = clickRef.current
    const close = previous && previous.button === event.button && event.timeStamp - previous.at < 500 && Math.hypot(previous.x - point.x, previous.y - point.y) < 5
    const count = close ? Math.min(3, previous.count + 1) : 1
    clickRef.current = { button: event.button, x: point.x, y: point.y, at: event.timeStamp, count }
    const pressedButtons = event.buttons || (1 << Math.max(0, event.button))
    mouseButtonsRef.current.set(event.pointerId, pressedButtons)
    mouseButtonRef.current.set(event.pointerId, event.button)
    sendInput({
      kind: 'mouse', action: 'down', x: point.x, y: point.y, button: mouseButton(event.button),
      buttons: pressedButtons, click_count: count, pointer_type: event.pointerType === 'pen' ? 'pen' : 'mouse', modifiers: modifiers(event),
    })
  }

  const onPagePointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    if (!mouseEnabled || !browserControllerRef.current || !liveViewEnabled || event.pointerType === 'touch') return
    const captured = event.currentTarget.hasPointerCapture?.(event.pointerId) || false
    const coalesced = event.nativeEvent.getCoalescedEvents?.()
    const samples = coalesced?.length ? coalesced : [event.nativeEvent]
    for (const sample of samples) {
      const point = pointAt(sample.clientX, sample.clientY, captured)
      if (!point) continue
      const buttons = sample.buttons || mouseButtonsRef.current.get(sample.pointerId) || 0
      const button = mouseButtonRef.current.get(sample.pointerId)
      sendInput({
        kind: 'mouse', action: 'move', x: point.x, y: point.y, buttons,
        button: button === undefined ? undefined : mouseButton(button),
        pointer_type: sample.pointerType === 'pen' ? 'pen' : 'mouse', modifiers: modifiers(sample),
      })
    }
  }

  const finishPagePointer = (_action: 'up' | 'cancel', event: React.PointerEvent<HTMLDivElement>) => {

    if (!mouseEnabled || !browserControllerRef.current || !liveViewEnabled || event.pointerType === 'touch') return
    const point = pointAt(event.clientX, event.clientY, true)
    if (point) {
      const button = mouseButtonRef.current.get(event.pointerId) ?? event.button
      sendInput({
        kind: 'mouse', action: 'up', x: point.x, y: point.y, button: mouseButton(button), buttons: event.buttons,
        click_count: clickRef.current?.count || 1, pointer_type: event.pointerType === 'pen' ? 'pen' : 'mouse', modifiers: modifiers(event),
      })
    }
    mouseButtonsRef.current.delete(event.pointerId)
    mouseButtonRef.current.delete(event.pointerId)
    try { event.currentTarget.releasePointerCapture?.(event.pointerId) } catch { /* 捕获未建立时无需释放。 */ }
    event.preventDefault()
  }

  const sendKey = (action: 'down' | 'up', event: React.KeyboardEvent<HTMLDivElement>) => {
    if (!keyboardEnabled || !browserControllerRef.current) return
    if (composingRef.current || event.key === 'Process' || event.keyCode === 229) return
    const keyTokens = Array.from(new Set([event.code?.toLowerCase(), event.key.toLowerCase()].filter(Boolean) as string[]))
    const keyToken = keyTokens[0] || ''
    const shortcutActions: Record<string, string> = {
      keya: 'select_all', keyc: 'copy', keyv: 'paste',
      a: 'select_all', c: 'copy', v: 'paste',
    }
    const shortcutAction = shortcutActions[keyToken]
    if (action === 'down' && (event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey && shortcutAction) {
      if (!event.repeat) {
        keyTokens.forEach((token) => shortcutKeysRef.current.add(token))
        sendInput({ kind: 'shortcut', action: shortcutAction, modifiers: event.metaKey ? 4 : 2 })
      }
      event.preventDefault()
      event.stopPropagation()
      return
    }
    if (action === 'up' && (keyTokens.some((token) => shortcutKeysRef.current.delete(token)) || keyTokens.some((token) => suppressedKeyRefs.current.delete(token)))) {
      event.preventDefault()
      event.stopPropagation()
      return
    }
    // 不把 Ctrl/Cmd/Alt/Win 自身或未允许的组合键发送到远端，避免
    // 修饰键丢失 keyup 后在 Chrome 中形成粘滞状态。
    if (event.ctrlKey || event.metaKey || event.altKey || ['Control', 'Meta', 'OS', 'Alt', 'AltGraph'].includes(event.key)) {
      if (action === 'down' && !event.repeat) keyTokens.forEach((token) => suppressedKeyRefs.current.add(token))
      event.preventDefault()
      event.stopPropagation()
      return
    }
    const hasCommandModifier = event.altKey || event.ctrlKey || event.metaKey
    sendInput({
      kind: 'key', action, key: event.key, code: event.code, key_code: event.keyCode || event.which,
      location: event.location, auto_repeat: action === 'down' && event.repeat, modifiers: modifiers(event),
      text: action === 'down' && event.key.length === 1 && !hasCommandModifier ? event.key : undefined,
    })
    event.preventDefault()
    event.stopPropagation()
  }

  const sendText = (text: string) => {
    if (text) sendInput({ kind: 'text', text })
  }

  const sendVirtualKey = (key: string, code: string, keyCode: number) => {
    sendInput({ kind: 'key', action: 'down', key, code, key_code: keyCode })
    sendInput({ kind: 'key', action: 'up', key, code, key_code: keyCode })
  }

  const onBeforeInput = (event: React.FormEvent<HTMLDivElement>) => {
    if (!keyboardEnabled || !browserControllerRef.current || composingRef.current) return
    const native = event.nativeEvent as InputEvent
    if (!clipboardEnabled && native.inputType === 'insertFromPaste') { event.preventDefault(); return }
    if (native.inputType === 'deleteContentBackward') {
      sendVirtualKey('Backspace', 'Backspace', 8)
      event.preventDefault()
    } else if (native.inputType === 'deleteContentForward') {
      sendVirtualKey('Delete', 'Delete', 46)
      event.preventDefault()
    } else if (native.data && native.inputType?.startsWith('insert')) {
      sendText(native.data)
      suppressInputRef.current = true
      event.preventDefault()
    }
  }

  const onLocalInput = (event: React.FormEvent<HTMLDivElement>) => {
    const target = event.target as HTMLTextAreaElement
    if (composingRef.current) return
    if (suppressInputRef.current) suppressInputRef.current = false
    else sendText(target.value)
    target.value = ''
  }

  const onCompositionStart = () => {
    composingRef.current = true
  }

  const onCompositionUpdate = () => {
    // 输入法候选态可能每个字都产生多次 update；只在本地记录，避免把
    // 未完成的候选文本排队发送到 CDP，快速输入时造成请求堆积。
  }

  const onCompositionEnd = (event: React.CompositionEvent<HTMLDivElement>) => {
    composingRef.current = false
    suppressInputRef.current = true
    // compositionend.data 才是输入法最终提交的文本；候选态被取消时为空，
    // 不能回退使用之前的 compositionupdate，否则会把未确认的拼音误提交。
    sendText(event.data)
    if (inputRef.current) inputRef.current.value = ''
  }

  // React 在部分 Chromium 版本会把委托的 wheel listener 注册为 passive；
  // 直播画面需要阻止本地页面滚动，因此在实际画面节点上安装 non-passive listener。
  useEffect(() => {
    const frame = frameRef.current
    if (!frame || !viewport || !wheelEnabled || !liveViewEnabled) return
    const onWheel = (event: WheelEvent) => {
      if (!browserControllerRef.current) return
      const point = pointAt(event.clientX, event.clientY, false)
      if (!point) return
      const unit = event.deltaMode === WheelEvent.DOM_DELTA_LINE ? 16 : event.deltaMode === WheelEvent.DOM_DELTA_PAGE ? frame.clientHeight : 1
      sendInput({
        kind: 'mouse', action: 'wheel', x: point.x, y: point.y,
        delta_x: event.deltaX * unit * point.scaleX, delta_y: event.deltaY * unit * point.scaleY,
        buttons: event.buttons, modifiers: modifiers(event),
      })
      event.preventDefault()
    }
    frame.addEventListener('wheel', onWheel, { passive: false })
    return () => frame.removeEventListener('wheel', onWheel)
  }, [hasFrame, viewport, wheelEnabled, liveViewEnabled])

  const selectTab = async (pageID: string) => {
    if (!browserControllerRef.current || !liveViewEnabled || !sessionID || pageID === activePageID) return
    const selection = ++tabSelectionRef.current
    try {
      await activateBrowserTab(sessionID, pageID, viewerID)
      if (selection !== tabSelectionRef.current) return
      setWaitingForFrame(true)
      // 同一浏览器窗口内切换不清空画布：目标 Tab 有本地缓存时立即替换，
      // 没有缓存时继续显示上一帧，直到服务端首帧到达。
      const local = frameCacheRef.current.get(pageID)
      if (local) void drawFrame(local, pageID)
      const state = await fetchBrowserState(sessionID)
      if (selection !== tabSelectionRef.current || !acceptState(state)) return
      activePageRef.current = state.active_page_id
      setTabs(state.tabs ?? [])
      setActivePageID(state.active_page_id)
      setPageURL(state.url)
      if (state.viewport?.width && state.viewport?.height) {
        const expected = expectedFrameRef.current
        if (!expected || expected.pageID !== state.active_page_id || expected.width === state.viewport.width && expected.height === state.viewport.height) setViewport(state.viewport)
      }
      if (state.active_page_id) {
        void fetchCachedBrowserFrame(sessionID).then((cached) => drawFrame(cached, state.active_page_id, false)).catch(() => undefined)
      }
      setNotice(undefined)
    } catch (error) {
      setNotice(error instanceof Error ? `切换浏览器 Tab 失败：${error.message}` : '切换浏览器 Tab 失败')
    }
  }

  const syncAfterNavigation = async (pageID: string) => {
    if (!sessionID) return
    const state = await fetchBrowserState(sessionID)
    if (!acceptState(state)) return
    activePageRef.current = state.active_page_id || pageID
    setTabs(state.tabs ?? [])
    setActivePageID(state.active_page_id || pageID)
    setPageURL(state.url)
    if (state.viewport?.width && state.viewport.height) setViewport(state.viewport)
    setWaitingForFrame(true)
    void fetchCachedBrowserFrame(sessionID).then((cached) => drawFrame(cached, state.active_page_id || pageID, false)).catch(() => undefined)
  }

  const navigateAddress = async () => {
    const address = addressValue.trim()
    const pageID = activePageRef.current || activePageID
    if (!browserControllerRef.current || !liveViewEnabled || !sessionID || !pageID || !address) return
    try {
      addressEditingRef.current = false
      setWaitingForFrame(true)
      await navigateBrowserTab(sessionID, pageID, address, viewerID)
      await syncAfterNavigation(pageID)
      setNotice(undefined)
    } catch (error) {
      setNotice(error instanceof Error ? `前往页面失败：${error.message}` : '前往页面失败')
    }
  }

  const reloadActivePage = async () => {
    const pageID = activePageRef.current || activePageID
    if (!browserControllerRef.current || !liveViewEnabled || !sessionID || !pageID) return
    try {
      setWaitingForFrame(true)
      await reloadBrowserTab(sessionID, pageID, viewerID)
      await syncAfterNavigation(pageID)
      setNotice(undefined)
    } catch (error) {
      setNotice(error instanceof Error ? `刷新页面失败：${error.message}` : '刷新页面失败')
    }
  }

  const removeTab = async (pageID: string) => {
    if (!browserControllerRef.current || !liveViewEnabled || !sessionID) return
    try {
      await closeBrowserTab(sessionID, pageID, viewerID)
      frameCacheRef.current.delete(pageID)
      viewportAppliedRef.current.delete(pageID)
      const state = await fetchBrowserState(sessionID)
      if (!acceptState(state)) return
      if (state.active_page_id) activePageRef.current = state.active_page_id
      setTabs(state.tabs ?? [])
      setActivePageID(state.active_page_id)
      setPageURL(state.url)
      if (state.viewport?.width && state.viewport?.height) {
        const expected = expectedFrameRef.current
        if (!expected || expected.pageID !== state.active_page_id || expected.width === state.viewport.width && expected.height === state.viewport.height) setViewport(state.viewport)
      }
      if (state.active_page_id) {
        const local = frameCacheRef.current.get(state.active_page_id)
        if (local) void drawFrame(local, state.active_page_id)
        void fetchCachedBrowserFrame(sessionID).then((cached) => drawFrame(cached, state.active_page_id, false)).catch(() => undefined)
      } else {
        frameCacheRef.current.clear()
        activePageRef.current = undefined
        setPageURL(undefined)
        setViewport(undefined)
        clearFrame()
        setWaitingForFrame(false)
      }
    } catch (error) {
      setNotice(error instanceof Error ? `关闭浏览器 Tab 失败：${error.message}` : '关闭浏览器 Tab 失败')
    }
  }

  const claimBrowserControl = async () => {
    if (!sessionID || browserControllerRef.current || takingControl || controlRetryAfterMS > 0) return
    setTakingControl(true)
    try {
      const state = await takeBrowserControl(sessionID, viewerID)
      applyBrowserControl(state)
      setNotice(undefined)
    } catch (error) {
      setNotice(error instanceof Error ? `获取浏览器操作权失败：${error.message}` : '获取浏览器操作权失败')
    } finally {
      setTakingControl(false)
    }
  }

  const close = async () => {
    if (readOnly || !browserControl?.controller || closingRef.current) return
    closingRef.current = true
    setClosing(true)
    loseBrowserControl()
    clearFrame()
    frameCacheRef.current.clear()
    viewportAppliedRef.current.clear()
    onClose?.()
    try {
      await closeBrowser(sessionID, viewerID)
      setNotice('浏览器已关闭')
    } catch (error) {
      setNotice(error instanceof Error ? `关闭浏览器失败：${error.message}` : '关闭浏览器失败')
    } finally {
      closingRef.current = false
      setClosing(false)
    }
  }

  const activeTab = tabs.find((tab) => tab.page_id === activePageID)
  const controller = !readOnly && !busy && !closing && browserControl?.controller === true
  const showAddress = visible('toolbar') && visible('addressBar')
  const showReload = visible('toolbar') && visible('reload')
  const showNavigate = showAddress && visible('navigate')
  const showClose = visible('toolbar') && visible('closeBrowser') && liveViewEnabled && !readOnly
  const showDevtools = visible('toolbar') && visible('devtools') && devtoolsEnabled
  const showToolbar = showAddress || showReload || showClose || showDevtools
  const takeoverSeconds = Math.ceil(controlRetryAfterMS / 1000)

  const content = <section ref={setPanelHost} data-part="root" className={`browserkit ${className} browser-live-window browser-live-window-side-panel ${devtoolsEnabled ? 'browser-live-window-devtools-enabled' : ''} ${devtoolsPanelOpen ? 'browser-live-window-devtools-open' : ''} ${devtoolsOptions?.resizable === false ? 'browser-live-window-devtools-fixed' : ''}   ${syncing ? 'browser-live-window-syncing' : ''} `} style={style} aria-busy={busy || closing || undefined} aria-label={liveViewEnabled ? syncing ? '浏览器页面直播（同步中）' : '浏览器页面直播' : '浏览器开发诊断'} onKeyDownCapture={(event) => { if (devtoolsEnabled && devtoolsOptions?.shortcut !== false && event.key === "F12") { event.preventDefault(); event.stopPropagation(); devtoolsPanelOpen ? closeDevtoolsPanel() : openDevtoolsPanel() } }}>
    {visible('tabs') && <div data-part="tabs" className={`browser-live-tab-list ${classNames?.tabs || ''}`} role="tablist" aria-label="浏览器 Tab 列表" inert={busy || closing || undefined}>
      {tabs.length === 0 && <span className="browser-live-tab-empty">没有可用页面</span>}
      {tabs.map((tab) => <div key={tab.page_id} data-part="tab" className={`browser-live-tab-row ${classNames?.tab || ''} ${tab.page_id === activePageID ? 'browser-live-tab-row-active' : ''} ${tab.status === 'loading' ? 'browser-live-tab-row-loading' : ''}`}>
        <button type="button" role="tab" disabled={!liveViewEnabled || !controller} aria-selected={tab.page_id === activePageID} aria-busy={tab.status === 'loading' || undefined} onClick={() => void selectTab(tab.page_id)} title={tab.url || tab.title || tab.page_id}>
          <i aria-hidden="true" />
          <span>{tab.status === 'loading' ? `${tab.title || tab.url || '新页面'}（加载中）` : tab.title || tab.url || '未命名页面'}</span>
        </button>
        {liveViewEnabled && visible('closeTab') && <button type="button" className="browser-live-tab-close" disabled={!controller} aria-label={`关闭 ${tab.title || '浏览器 Tab'}`} onClick={() => void removeTab(tab.page_id)}><X size={12} /></button>}
      </div>)}
      {!readOnly && liveViewEnabled && visible('newTab') && <button type="button" className="browser-new-tab" aria-label="新建浏览器 Tab" disabled={!controller} onClick={() => void newBrowserTab(sessionID, viewerID).catch((error: Error) => setNotice(error.message))}><Plus size={14} /></button>}
    </div>}
    {showToolbar && <header data-part="toolbar" className={`browser-live-window-header ${classNames?.toolbar || ''}`}>
      <div className="browser-live-address-row" inert={busy || closing || undefined} onPointerDown={(event) => { event.stopPropagation() }}>
        {showAddress && <i aria-hidden="true" />}
        {showAddress && <input
          aria-label="浏览器页面地址"
          disabled={!liveViewEnabled || !controller}
          value={addressValue}
          placeholder="输入页面地址"
          onFocus={() => { addressEditingRef.current = true }}
          onChange={(event) => setAddressValue(event.target.value)}
          onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); void navigateAddress() } }}
          onBlur={() => { addressEditingRef.current = false; if (!addressValue.trim()) setAddressValue(pageURL || '') }}
          title={pageURL || '浏览器直播'}
        />}
        {showReload && <button type="button" aria-label="刷新当前页面" disabled={!liveViewEnabled || !controller || !activePageID} onClick={() => void reloadActivePage()}><RefreshCw size={13} /></button>}
        {showNavigate && <button type="button" aria-label="前往页面" disabled={!liveViewEnabled || !controller || !activePageID || !addressValue.trim()} onClick={() => void navigateAddress()}><ArrowRight size={13} /></button>}
      </div>
      <div>
        {showClose && <button type="button" aria-label="关闭浏览器" disabled={!browserControl?.controller || closing} onClick={() => void close()}><X size={14} /></button>}
        {showDevtools && <button type="button" aria-label="切换开发诊断 F12" aria-pressed={devtoolsPanelOpen} onClick={() => devtoolsPanelOpen ? closeDevtoolsPanel() : openDevtoolsPanel()}><Code2 size={15} /></button>}
      </div>
    </header>}
    {<div className={`browser-live-panel-body browser-live-panel-${mobilePanel}`} inert={busy || closing || undefined} style={devtoolsHeight ? { '--browser-devtools-height': devtoolsHeight } as CSSProperties : undefined}>
      {devtoolsEnabled && devtoolsPanelOpen && <div className="browser-live-mobile-switch" role="tablist" aria-label="浏览器面板视图">
        <button type="button" role="tab" aria-selected={mobilePanel === 'page'} onClick={() => setMobilePanel('page')}>页面</button>
        <button type="button" role="tab" aria-selected={mobilePanel === 'devtools'} onClick={() => setMobilePanel('devtools')}>开发诊断</button>
      </div>}
      <div
      ref={frameRef}
      data-part="viewport" className={`browser-live-window-frame ${classNames?.viewport || ''} ${mobileInputActive ? 'browser-live-window-frame-input-active' : ''}`}
      style={{ touchAction: touchEnabled && controller ? 'none' : 'auto' }}
      role="application"
      aria-label="可交互浏览器页面"
      aria-disabled={!liveViewEnabled || !controller || undefined}
      tabIndex={liveViewEnabled && controller ? 0 : -1}
      onPointerDown={onPagePointerDown}
      onPointerMove={onPagePointerMove}
      onPointerUp={(event) => finishPagePointer('up', event)}
      onPointerCancel={(event) => finishPagePointer('cancel', event)}
      onTouchStart={(event) => onPageTouch('start', event)}
      onTouchMove={(event) => onPageTouch('move', event)}
      onTouchEnd={(event) => onPageTouch('end', event)}
      onTouchCancel={(event) => onPageTouch('cancel', event)}
      onKeyDown={(event) => sendKey('down', event)}
      onKeyUp={(event) => sendKey('up', event)}
      onBeforeInput={onBeforeInput}
      onInput={onLocalInput}
      onCompositionStart={onCompositionStart}
      onCompositionUpdate={onCompositionUpdate}
      onCompositionEnd={onCompositionEnd}
      onPaste={(event) => { if (keyboardEnabled && clipboardEnabled) sendText(event.clipboardData.getData('text')); event.preventDefault() }}
      onContextMenu={(event) => event.preventDefault()}
    >
      {keyboardEnabled && <textarea ref={inputRef} className="browser-live-window-ime" aria-label="浏览器直播键盘输入" tabIndex={-1} autoCapitalize="off" autoCorrect="off" spellCheck={false} onBlur={onInputBlur} inputMode={manualKeyboard && !mobileInputActive ? "none" : "text"} />}
      <canvas ref={canvasRef} className={hasFrame ? 'browser-live-window-canvas-ready' : ''} aria-label="浏览器页面当前画面" />
      {visible('keyboard') && keyboardEnabled && liveViewEnabled && hasFrame && controller && <button
        type="button"
        className="browser-live-window-mobile-input"
        aria-label={mobileInputActive ? '关闭页面输入' : '开启页面输入'}
        aria-pressed={mobileInputActive}
        title={mobileInputActive ? '关闭输入模式' : '开启输入模式'}
        onPointerDown={(event) => { mobileInputToggleRef.current = true; event.stopPropagation() }}
        onPointerMove={(event) => event.stopPropagation()}
        onPointerUp={(event) => { mobileInputToggleRef.current = false; event.stopPropagation() }}
        onPointerCancel={(event) => { mobileInputToggleRef.current = false; event.stopPropagation() }}
        onKeyDown={(event) => event.stopPropagation()}
        onKeyUp={(event) => event.stopPropagation()}
        onClick={toggleMobileInput}
      ><Keyboard size={13} /><span>{mobileInputActive ? '完成' : '输入'}</span></button>}
      {(visible('controlStatus') || visible('takeover') && !readOnly) && liveViewEnabled && hasFrame && !controller && !busy && !closing && <div className="browser-live-window-control-overlay" role="status" aria-label="浏览器观察模式">
        <div className="browser-live-window-control-copy">
          {visible('controlStatus') && <span>{readOnly ? '只读浏览器视图' : browserControl ? '当前浏览器正在由其他窗口控制' : '正在确认浏览器操作权…'}</span>}
          {visible('takeover') && browserControl && !readOnly && <button
            type="button"
            disabled={takingControl || controlRetryAfterMS > 0}
            onPointerDown={(event) => event.stopPropagation()}
            onPointerUp={(event) => event.stopPropagation()}
            onClick={() => void claimBrowserControl()}
          >{takingControl ? '获取中…' : takeoverSeconds > 0 ? `${takeoverSeconds} 秒后可获取` : '获取操作权'}</button>}
        </div>
      </div>}
      {!liveViewEnabled ? <span className="browser-live-window-loading">页面直播未启用</span> : (!hasFrame || waitingForFrame || activeTab?.status === 'loading') && <span className={`browser-live-window-loading ${waitingForFrame || activeTab?.status === 'loading' ? 'browser-live-window-loading-active' : ''}`}><i aria-hidden="true" />{activeTab?.status === 'loading' || waitingForFrame ? '页面加载中…' : '没有可用页面'}</span>}
      </div>
      {visible('devtoolsFloating') && devtoolsEnabled && !devtoolsPanelOpen && <button type="button" className="browser-devtools-panel-trigger" aria-label="打开开发诊断面板" onClick={openDevtoolsPanel}><span aria-hidden="true" />开发诊断</button>}
      {devtoolsEnabled && devtoolsPanelOpen && devtoolsOptions?.resizable !== false && <div className="browser-devtools-resizer" role="separator" aria-label="调整开发诊断高度" aria-orientation="horizontal" onPointerDown={startDevtoolsResize} onPointerMove={moveDevtoolsResize} onPointerUp={endDevtoolsResize} onPointerCancel={endDevtoolsResize} />}
      {devtoolsEnabled && sessionID && <BrowserDevtoolsPanel className={classNames?.devtools} sessionID={sessionID} pageID={activePageID} viewerID={viewerID} controlEnabled={controller} hidden={!devtoolsPanelOpen} panels={devtoolsOptions?.panels} captureControl={devtoolsOptions?.captureControl} onClose={devtoolsOptions?.closeButton === false ? undefined : closeDevtoolsPanel} />}
    </div>}
    {notice && <div role="alert" className="browser-live-window-notice">{notice}</div>}

    </section>
  return content
}
