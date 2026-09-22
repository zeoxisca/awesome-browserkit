import { describe, expect, it, vi, afterEach } from 'vitest'
import { startBrowserDevtools, stopBrowserDevtools, takeBrowserControl, setBrowserTabViewport, browserStreamURL, decodeFrame } from './client'
const jsonResponse = (value: unknown) => new Response(JSON.stringify(value), {headers:{'Content-Type':'application/json'}})
afterEach(()=>vi.unstubAllGlobals())
describe('browser client',()=>{
  it('sends the browser viewer identity when changing DevTools collection', async () => {
    const request = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(String(input)).toBe('/api/sessions/session%2Fmain/browser/tabs/page%2Fchild/devtools')
      expect(new Headers(init?.headers).get('X-Browserkit-Viewer')).toBe('viewer-a')
      return jsonResponse({
        available: true, page_id: 'page/child', status: init?.method === 'POST' ? 'collecting' : 'stopped',
        generation: 1, revision: 1, entries: [], next_sequence: 0, pending_requests: 0, dropped_before: 0, truncated: false,
      })
    })
    vi.stubGlobal('fetch', request)

    await startBrowserDevtools('/api/sessions/session%2Fmain/browser', 'page/child', 'viewer-a')
    await stopBrowserDevtools('/api/sessions/session%2Fmain/browser', 'page/child', 'viewer-a')
    expect(request).toHaveBeenCalledTimes(2)
  })

  it('sends the browser viewer identity when taking control', async () => {
    const request = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(String(input)).toBe('/api/sessions/session%2Fmain/browser/control')
      expect(init?.method).toBe('POST')
      expect(new Headers(init?.headers).get('X-Browserkit-Viewer')).toBe('viewer-a')
      return jsonResponse({ type: 'browser_control', controller: true, can_takeover: false, revision: 2 })
    })
    vi.stubGlobal('fetch', request)

    await expect(takeBrowserControl('/api/sessions/session%2Fmain/browser', 'viewer-a')).resolves.toMatchObject({ controller: true, revision: 2 })
  })

 it('支持绝对 endpoint 和反向代理前缀',()=>{expect(browserStreamURL('https://browser.test/prefix/browser','viewer')).toBe('wss://browser.test/prefix/browser/ws?viewer_id=viewer')})
 it('视口请求同时携带控制身份和明确页面',async()=>{const fetcher=vi.fn(async (_url:unknown,init?:RequestInit)=>{expect(init?.method).toBe('PUT');expect(new Headers(init?.headers).get('X-Browserkit-Viewer')).toBe('viewer');return new Response(null,{status:204})});vi.stubGlobal('fetch',fetcher);await setBrowserTabViewport('/browser','page/a',{width:800,height:600},'viewer');expect(fetcher.mock.calls[0][0]).toBe('/browser/tabs/page%2Fa/viewport')})
 it('decodes frame identity separately from image bytes and rejects invalid headers',async()=>{
   const header=new TextEncoder().encode(JSON.stringify({page_id:'page-a',generation:7,mime:'image/jpeg'}))
   const bytes=new Uint8Array(4+header.length+3);new DataView(bytes.buffer).setUint32(0,header.length);bytes.set(header,4);bytes.set([1,2,3],4+header.length)
   const slices:Array<[number,number, string | undefined]>=[]
   const data={size:bytes.byteLength,slice:(start=0,end=bytes.byteLength,type?:string)=>{
     slices.push([start,end,type])
     const part=bytes.slice(start,end)
     return {size:part.byteLength,type:type||'',arrayBuffer:async()=>part.buffer} as Blob
   }} as Blob
   const frame=await decodeFrame(data)
   expect(frame.pageID).toBe('page-a');expect(frame.generation).toBe(7);expect(frame.blob.size).toBe(3)
   expect(frame.blob.type).toBe('image/jpeg')
   expect(slices).toEqual([[0,4,undefined],[4,4+header.length,undefined],[4+header.length,bytes.byteLength,'image/jpeg']])
   await expect(decodeFrame({size:3} as Blob)).rejects.toThrow('图帧头缺失')
 })

})
