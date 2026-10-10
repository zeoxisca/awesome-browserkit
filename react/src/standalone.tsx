import { createRoot, type Root } from 'react-dom/client'
import { createElement } from 'react'
import { BrowserView, type BrowserViewProps } from './BrowserView'
import './style.css'

/** 在已有宿主应用中挂载独立 BrowserKit React 根。 */
export function mountBrowserView(element: HTMLElement, props: BrowserViewProps) {
  const root: Root = createRoot(element)
  root.render(createElement(BrowserView, props))
  return {
    update(next: BrowserViewProps) { root.render(createElement(BrowserView, next)) },
    unmount() { root.unmount() },
  }
}
