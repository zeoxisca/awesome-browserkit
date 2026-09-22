---
name: browserkit-cli
description: 通过 BrowserKit CLI 操作 Chrome 网页，支持连接已有浏览器或启动独立浏览器，以及快照、输入、点击、截图和 Tab 管理。
---

# BrowserKit

仓库根目录的 `install.sh` 同时安装 client 与本 Skill。Codex 使用默认安装；Pi Agent 使用 `./install.sh --agent pi`，并可通过 `/skill:browserkit-cli` 显式加载。
根据用户指定的浏览器选择模式；连接失败时检查 CDP 地址和目标页，不自动切换到另一个浏览器。

## 启动

### 已有 Chrome

1. 在 Chrome 打开 `chrome://inspect/#remote-debugging`，启用「允许调试本浏览器」。
   没有此选项时需要升级 Chrome；不要重启或替换用户的 profile。
2. 取得调试地址，切回目标 Tab。
3. 启动 client，Chrome 出现授权提示时由用户确认。

```sh
browserkit --mode attach --remote 'http://127.0.0.1:9222'
```

替换为实际调试地址：支持 HTTP `/json/version` 或 `/devtools/browser/…` WebSocket。
Discover network targets 仅用于发现调试目标，不开启当前浏览器的调试权限。

不传 `--url` 时接管激活 Tab；浏览器失焦时选择唯一可见 Tab。
无法确定目标时，请用户切回目标页并保持浏览器焦点后重试。
退出 client 或调用 browserClose 会断开连接，保留 Chrome 和 Tab。
browserCloseTab 会关闭指定 Tab。

### 独立 Chrome

```sh
browserkit --url 'https://example.com'
```

默认 `new` 模式在独立临时 profile 中启动无头浏览器，退出时关闭并清理。
不继承日常浏览器或上次 client 的 Cookie 和本地存储。
省略 `--url` 时，先调用 browserOpen 创建页面。

### 参数

- `--url`：所有模式均在所用浏览器中新建 Tab 并打开地址，不匹配或替换已有页面。
- `--chrome`：new 模式的 Chrome 可执行文件路径。
- `--devtools`：启用诊断函数。
- `--allow-evaluate`：启用 JavaScript 执行。
- `--allow-private-network`：允许访问用户任务涉及的本机或私网页面。

## 调用

CLI 为常驻进程，以 JSON Lines 交换请求和响应，每行一个 JSON 对象。
保持同一进程的 stdin/stdout，不能用多次独立启动维持会话。

attach 模式以及带 `--url` 的 new 模式会先输出一行 browserOpen 结果。
从结果获取 session_id 和 page_id；此时无需再次创建页面。

```json
{"function":"browserSnapshot","arguments":{"session_id":"从启动结果获取","page_id":"从启动结果获取"}}
```

先读取 Snapshot，再用返回的 ref 和 revision 操作元素。
ref 属于对应 page_id；引用失效时重新读取快照。
`ok=false` 时先检查错误；可能已产生副作用的操作不得自动重试。
截图返回文件路径，由宿主读取并展示。

`browserkit --list-functions` 列出函数名称和说明，browserTabs 列出 Tab。
每个进程最多一个 session；切换模式需结束当前进程，旧 session_id、page_id 和 ref 不跨进程复用。
