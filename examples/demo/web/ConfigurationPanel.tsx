import type { BrowserControls, BrowserInputOptions, BrowserDevtoolsOptions, BrowserDevtoolsTab, BrowserViewProps } from '@browserkit/react'

export type ClientConfiguration = Pick<BrowserViewProps, 'controls' | 'input' | 'devtools' | 'readOnly'>

// 客户端场景由宿主组合；组件本身不按设备名称推断权限。
const presets: Record<string, ClientConfiguration> = {
  '桌面端': { controls: {}, input: {}, devtools: true },
  '移动端': { controls: { tabs: false, closeBrowser: false, devtools: false, devtoolsFloating: false }, input: { keyboardActivation: 'manual' }, devtools: false },
  '纯画布': { controls: false, input: { keyboardActivation: 'manual' }, devtools: false },
  '观察端': { readOnly: true, controls: { toolbar: false, newTab: false, closeTab: false, keyboard: false, takeover: false, controlStatus: false }, input: false, devtools: false },
}

const controlFields: Array<[keyof BrowserControls, string]> = [
  ['tabs', 'Tab 栏'], ['toolbar', '工具栏'], ['addressBar', '地址框'],
  ['newTab', '新建 Tab'], ['closeTab', '关闭 Tab'], ['reload', '刷新'],
  ['navigate', '前往'], ['closeBrowser', '关闭浏览器'], ['keyboard', '键盘按钮'],
  ['devtools', '诊断按钮'], ['devtoolsFloating', '悬浮诊断入口'], ['takeover', '获取操作权'], ['controlStatus', '观察状态提示'],
]
const inputFields: Array<[Exclude<keyof BrowserInputOptions, 'keyboardActivation'>, string]> = [
  ['mouse', '鼠标 / 触控笔'], ['touch', '触摸'], ['wheel', '滚轮'], ['keyboard', '键盘 / 中文输入'], ['clipboard', '复制 / 粘贴'],
]
const devtoolsFields: Array<[keyof Pick<BrowserDevtoolsOptions, 'shortcut' | 'resizable' | 'captureControl' | 'closeButton'>, string]> = [
  ['shortcut', 'F12 快捷键'], ['resizable', '调整面板高度'], ['captureControl', '开始 / 停止采集'], ['closeButton', '面板关闭按钮'],
]

export function ConfigurationPanel({ value, onChange }: { value: ClientConfiguration; onChange: (value: ClientConfiguration) => void }) {
  const controls = value.controls
  const input = value.input
  const devtools = typeof value.devtools === 'object' ? value.devtools : {}
  return <details className="configuration" open>
    <summary>客户端配置 <span>区域、输入与诊断</span></summary>
    <div className="configuration-presets" aria-label="客户端预设">
      {Object.entries(presets).map(([name, config]) => <button key={name} onClick={() => onChange(config)}>{name}</button>)}
      <label><input type="checkbox" checked={value.readOnly ?? false} onChange={(e) => onChange({ ...value, readOnly: e.target.checked })} />只读视图</label>
    </div>
    <div className="configuration-grid">
      <fieldset><legend>区域与按钮</legend>
        <label><input type="checkbox" checked={controls !== false} onChange={(e) => onChange({ ...value, controls: e.target.checked ? {} : false })} />显示内置控制区</label>
        <div className="configuration-fields">{controlFields.map(([key, label]) => <label key={key}><input type="checkbox" disabled={controls === false || key !== 'tabs' && ['newTab', 'closeTab'].includes(key) && controls?.tabs === false || ['addressBar', 'reload', 'navigate', 'closeBrowser', 'devtools'].includes(key) && controls?.toolbar === false}
          checked={controls !== false && controls?.[key] !== false} onChange={(e) => onChange({ ...value, controls: { ...(controls || {}), [key]: e.target.checked } })} />{label}</label>)}</div>
      </fieldset>
      <fieldset><legend>页面输入</legend>
        <label><input type="checkbox" checked={input !== false} onChange={(e) => onChange({ ...value, input: e.target.checked ? {} : false })} />允许页面输入</label>
        <div className="configuration-fields">{inputFields.map(([key, label]) => <label key={key}><input type="checkbox" disabled={input === false} checked={input !== false && input?.[key] !== false}
          onChange={(e) => onChange({ ...value, input: { ...(input || {}), [key]: e.target.checked } })} />{label}</label>)}</div>
        <label>键盘唤起 <select aria-label="键盘唤起方式" disabled={input === false || input?.keyboard === false} value={input && input.keyboardActivation || 'auto'} onChange={(e) => onChange({ ...value, input: { ...(input || {}), keyboardActivation: e.target.value as 'auto' | 'manual' } })}><option value="auto">点击画面自动聚焦</option><option value="manual">仅由键盘按钮唤起</option></select></label>
      </fieldset>
      <fieldset><legend>开发诊断</legend>
        <label><input type="checkbox" checked={Boolean(value.devtools)} onChange={(e) => onChange({ ...value, devtools: e.target.checked })} />启用开发诊断</label>
        <div className="configuration-fields">{(['console', 'network', 'resources'] as BrowserDevtoolsTab[]).map((panel) => <label key={panel}><input type="checkbox" disabled={!value.devtools} checked={(devtools.panels ?? ['console', 'network', 'resources']).includes(panel)} onChange={(e) => {
          const panels = devtools.panels ?? ['console', 'network', 'resources']
          onChange({ ...value, devtools: { ...devtools, panels: e.target.checked ? [...panels, panel] : panels.filter((item) => item !== panel) } })
        }} />{{ console: 'Console', network: 'Network', resources: 'Resources' }[panel]}</label>)}
        {devtoolsFields.map(([key, label]) => <label key={key}><input type="checkbox" disabled={!value.devtools} checked={devtools[key] !== false} onChange={(e) => onChange({ ...value, devtools: { ...devtools, [key]: e.target.checked } })} />{label}</label>)}</div>
      </fieldset>
    </div>
    <details className="configuration-code"><summary>查看当前配置代码</summary><pre><code>{`<BrowserView\n  endpoint="/browser"\n  controls={${JSON.stringify(value.controls ?? {}, null, 2)}}\n  input={${JSON.stringify(value.input ?? {}, null, 2)}}\n  devtools={${JSON.stringify(value.devtools ?? false, null, 2)}}\n  readOnly={${value.readOnly ?? false}}\n  style={{ height: 650 }}\n/>`}</code></pre></details>
  </details>
}
