export interface BrowserViewState {
    available: boolean;
    closed?: boolean;
    session_id?: string;
    active_page_id?: string;
    tabs_revision?: number;
    tabs?: BrowserTabView[];
    url?: string;
    title?: string;
    viewport?: {
        width: number;
        height: number;
    };
}
export interface BrowserControlState {
    type: 'browser_control';
    controller: boolean;
    can_takeover: boolean;
    retry_after_ms?: number;
    revision: number;
}
async function responseError(response: Response): Promise<Error> { const body = await response.json().catch(() => ({})); return new Error(body.error || `浏览器请求失败 (${response.status})`); }
const checkedFetch = (url: string, options?: RequestInit) => fetch(url, { credentials: 'include', ...options });
async function request<T>(url: string, options?: RequestInit): Promise<T> { const response = await checkedFetch(url, options); if (!response.ok)
    throw await responseError(response); return response.json(); }
const jsonRequest = (method: string, value: unknown): RequestInit => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(value) });
const socketURL = (path: string) => { const url = new URL(path, window.location.href); url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'; return url.toString(); };
export interface BrowserTabView {
    page_id: string;
    url?: string;
    title?: string;
    viewport?: {
        width: number;
        height: number;
    };
    opener_page_id?: string;
    status: string;
    active: boolean;
}
export interface BrowserDevtoolsEntry {
    sequence: number;
    kind: 'console' | 'exception' | 'network';
    level?: string;
    text?: string;
    stack?: string;
    source_url?: string;
    line?: number;
    column?: number;
    method?: string;
    resource_type?: string;
    url?: string;
    status?: number;
    mime_type?: string;
    duration_ms?: number;
    encoded_bytes?: number;
    failed?: boolean;
    error?: string;
    request_body?: string;
    response_body?: string;
    body_available?: boolean;
    body_truncated?: boolean;
    at: string;
}
export interface BrowserDevtoolsState {
    available: boolean;
    session_id?: string;
    page_id?: string;
    status: 'idle' | 'collecting' | 'stopped';
    generation: number;
    revision: number;
    entries: BrowserDevtoolsEntry[];
    next_sequence: number;
    pending_requests: number;
    dropped_before: number;
    truncated: boolean;
}
export interface BrowserAsset {
    name: string;
    url: string;
    initiator_type?: string;
    response_status?: number;
    transfer_bytes?: number;
    encoded_bytes?: number;
    decoded_bytes?: number;
    duration_ms?: number;
}
export interface BrowserAssetsState {
    page_id: string;
    assets: BrowserAsset[];
    truncated: boolean;
}
export async function fetchBrowserState(sessionID: string, signal?: AbortSignal): Promise<BrowserViewState> {
    return request(`${sessionID.replace(/\/$/, "")}/state`, { signal });
}
export async function fetchBrowserFrame(sessionID: string, signal?: AbortSignal): Promise<Blob> {
    const response = await checkedFetch(`${sessionID.replace(/\/$/, "")}/frame`, {
        headers: { Accept: 'image/png,image/jpeg,image/*' },
        signal,
    });
    if (!response.ok)
        throw await responseError(response);
    const blob = await response.blob();
    return Object.assign(blob, { pageID: response.headers.get('X-Browser-Page') || undefined });
}
export async function fetchCachedBrowserFrame(sessionID: string, signal?: AbortSignal): Promise<Blob> {
    const response = await checkedFetch(`${sessionID.replace(/\/$/, "")}/cached-frame`, {
        headers: { Accept: 'image/png,image/jpeg,image/*' },
        signal,
    });
    if (!response.ok)
        throw await responseError(response);
    const blob = await response.blob();
    return Object.assign(blob, { pageID: response.headers.get('X-Browser-Page') || undefined });
}
export function browserStreamURL(sessionID: string, viewerID: string, readOnly = false): string {
    return socketURL(`${sessionID.replace(/\/$/, "")}/ws?viewer_id=${encodeURIComponent(viewerID)}${readOnly ? "&read_only=1" : ""}`);
}
export function browserStateStreamURL(sessionID: string): string {
    return socketURL(`${sessionID.replace(/\/$/, "")}/state/ws`);
}
export async function fetchBrowserDevtoolsState(sessionID: string, pageID: string, after = 0, limit = 100, signal?: AbortSignal): Promise<BrowserDevtoolsState> {
    const parameters = new URLSearchParams({ after: String(after), limit: String(limit) });
    return request(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}/devtools?${parameters}`, { signal });
}
export function browserDevtoolsStreamURL(sessionID: string, pageID: string): string {
    return socketURL(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}/devtools/ws`);
}
export async function startBrowserDevtools(sessionID: string, pageID: string, viewerID?: string): Promise<BrowserDevtoolsState> {
    return request(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}/devtools`, withBrowserViewer({ method: 'POST' }, viewerID));
}
export async function stopBrowserDevtools(sessionID: string, pageID: string, viewerID?: string): Promise<BrowserDevtoolsState> {
    return request(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}/devtools`, withBrowserViewer({ method: 'DELETE' }, viewerID));
}
export async function fetchBrowserAssets(sessionID: string, pageID: string, host = '', limit = 500, signal?: AbortSignal): Promise<BrowserAssetsState> {
    const parameters = new URLSearchParams({ limit: String(limit) });
    if (host)
        parameters.set('host', host);
    return request(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}/assets?${parameters}`, { signal });
}
const withBrowserViewer = (init: RequestInit, viewerID?: string): RequestInit => {
    const headers: Record<string, string> = {};
    new Headers(init.headers).forEach((value, name) => { headers[name] = value; });
    if (viewerID)
        headers['X-Browserkit-Viewer'] = viewerID;
    return { ...init, headers };
};
export async function takeBrowserControl(sessionID: string, viewerID: string): Promise<BrowserControlState> {
    return request(`${sessionID.replace(/\/$/, "")}/control`, withBrowserViewer({ method: 'POST' }, viewerID));
}
export async function closeBrowser(sessionID: string, viewerID: string): Promise<void> {
    const response = await checkedFetch(`${sessionID.replace(/\/$/, "")}/close`, withBrowserViewer({ method: 'POST' }, viewerID));
    if (!response.ok)
        throw await responseError(response);
}
export async function activateBrowserTab(sessionID: string, pageID: string, viewerID: string): Promise<void> {
    const response = await checkedFetch(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}/activate`, withBrowserViewer({ method: 'POST' }, viewerID));
    if (!response.ok)
        throw await responseError(response);
}
export async function navigateBrowserTab(sessionID: string, pageID: string, url: string, viewerID: string): Promise<void> {
    const response = await checkedFetch(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}/navigate`, withBrowserViewer(jsonRequest('POST', { url }), viewerID));
    if (!response.ok)
        throw await responseError(response);
}
export async function reloadBrowserTab(sessionID: string, pageID: string, viewerID: string): Promise<void> {
    const response = await checkedFetch(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}/reload`, withBrowserViewer({ method: 'POST' }, viewerID));
    if (!response.ok)
        throw await responseError(response);
}
export async function setBrowserTabViewport(sessionID: string, pageID: string, viewport: {
    width: number;
    height: number;
}, viewerID: string): Promise<void> {
    const response = await checkedFetch(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}/viewport`, withBrowserViewer(jsonRequest('PUT', viewport), viewerID));
    if (!response.ok)
        throw await responseError(response);
}
export async function closeBrowserTab(sessionID: string, pageID: string, viewerID: string): Promise<void> {
    const response = await checkedFetch(`${sessionID.replace(/\/$/, "")}/tabs/${encodeURIComponent(pageID)}`, withBrowserViewer({ method: 'DELETE' }, viewerID));
    if (!response.ok)
        throw await responseError(response);
}
export async function newBrowserTab(endpoint: string, viewerID: string, url = ''): Promise<void> {
    const response = await checkedFetch(`${endpoint.replace(/\/$/, '')}/tabs`, withBrowserViewer(jsonRequest('POST', { url }), viewerID));
    if (!response.ok)
        throw await responseError(response);
}
export interface BrowserFrame {
    blob: Blob;
    pageID: string;
    generation: number;
}
export async function decodeFrame(data: Blob): Promise<BrowserFrame> {
    if (data.size < 4)
        throw new Error('浏览器图帧头缺失');
    const prefix = await data.slice(0, 4).arrayBuffer();
    const length = new DataView(prefix).getUint32(0);
    if (length > 65536 || length + 4 > data.size)
        throw new Error('浏览器图帧头无效');
    const headerBytes = await data.slice(4, 4 + length).arrayBuffer();
    const header = JSON.parse(new TextDecoder().decode(headerBytes));
    if (typeof header.page_id !== 'string' || !header.page_id || !Number.isSafeInteger(header.generation) || typeof header.mime !== 'string')
        throw new Error('浏览器图帧身份无效');
    return { blob: data.slice(4 + length, data.size, header.mime), pageID: header.page_id, generation: header.generation };
}
