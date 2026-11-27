# 宿主管理的 Go SDK

本地 Chromium 使用 Rod/CDP；宿主持有 Manager，并在任务结束时显式关闭。示例中的路径均指 Chromium 所在容器内的路径。

```go
manager, err := browserkit.NewManager(browserkit.Config{
    Session: browserkit.SessionConfig{
        ExecPath: "/usr/bin/chromium",
        Proxy: "http://user:password@127.0.0.1:8080",
        ProxyBypassList: []string{"<-loopback>"},
        IgnoreCertificateErrors: true,
        OperationTimeout: 30 * time.Second,
    },
    DisableIdleReclaim: true,
    AllowPrivateNetwork: true,
    AllowEvaluate: true,
    UploadDir: "/workspace/uploads",
    DownloadDir: "/workspace/downloads",
    DownloadMaxBytes: 64 << 20,
})
if err != nil { return err }
defer manager.Close(context.Background())

opened, err := manager.Open(ctx, browserkit.OpenRequest{URL: "https://example.test/"})
if err != nil { return err }
snapshot, err := manager.Snapshot(ctx, browserkit.SnapshotRequest{
    SessionID: opened.SessionID, PageID: opened.PageID,
})
if err != nil { return err }
```

目录必须先存在。`DisableIdleReclaim` 默认关闭，未设置时仍在空闲 30 分钟后回收；`IdleTimeout` 可调整该时间。成功交付已有会话后，单次调用的 context、`OperationTimeout` 或 `timeout_ms` 到期只结束调用，不关闭 session、Tab 或 Cookie。新建会话在启动失败、取消且尚未交付时会清理其资源。关闭 Manager 才结束已交付会话的资源寿命；调用超时不能证明副作用未发生，不能自动重放。

`Proxy` 沿用 Chromium 代理地址语法；HTTP/HTTPS 认证代理可在 URL 中填写凭据，凭据从 launcher 参数中剥离，只回应匹配代理的 CDP Proxy challenge。Chromium 不支持的 SOCKS 代理认证会明确拒绝。`ProxyBypassList` 为空时保留 Chromium 的绕过规则，`<-loopback>` 使环回目标也经过代理。`IgnoreCertificateErrors` 默认 `false`，启用时仅为本地 Chromium 设置 `ignore-certificate-errors`；不管理 CA 或签发站点证书。远程 CDP 浏览器的代理、凭据与启动参数由远端宿主管理，本配置仅应用于本地启动的浏览器。

## 选择与上传

从最新快照中取得目标元素的 `ref`，始终一起传递该快照的 `revision` 与 `page_id`。

```go
values := []string{"red", "blue"}
selected, err := manager.SelectOption(ctx, browserkit.SelectOptionRequest{
    SessionID: opened.SessionID, PageID: opened.PageID,
    Ref: selectRef, Revision: snapshot.Revision, Values: &values,
})
```

`Values` 按 value 匹配，`Labels` 按可见标签匹配，两者互斥；`[]string{}` 清空，`[]string{""}` 选择空字符串选项，未提供条件会报错。单选不能同时选多个不同选项。`browserAct` 的 `select` 步骤使用相同的 `values` / `labels` 语义。

```go
uploaded, err := manager.UploadFile(ctx, browserkit.UploadFileRequest{
    SessionID: opened.SessionID, PageID: opened.PageID,
    Ref: fileInputRef, Revision: snapshot.Revision,
    Paths: []string{"/workspace/uploads/a.txt", "/workspace/uploads/b.txt"},
})

// ref 指向打开原生文件选择器的按钮。
uploaded, err = manager.UploadFile(ctx, browserkit.UploadFileRequest{
    SessionID: opened.SessionID, PageID: opened.PageID,
    Ref: chooserButtonRef, Revision: snapshot.Revision, Chooser: true,
    Paths: []string{"/workspace/uploads/a.txt"},
})
```

`Path` 与 `Paths` 二选一。每个文件都检查授权目录、符号链接解析、普通文件类型和 `10 << 20` 字节上限；传路径给 Chromium，不把文件读入 Go 内存或转成 base64。多文件目标必须支持 `multiple`。成功回执表示原生输入框已接受文件，站点随后是否上传成功需观察页面或网络。

## 下载

配置 `DownloadDir` 后，普通点击、表单提交、导航及显式 `Download(start)` 的下载都会进入同一会话列表。下载身份在本次 session 内稳定，不要求重新点击。

`Open`、`Navigate` 或新 Tab 的 URL 直接返回附件时，原生 Chromium `isDownload` 回执映射为 `load_status=download`。页面 URL 仍为原文档（首次打开则为 `about:blank`），session 和原文档引用保留；用 `browserDownload` 的 `list` 获取实际下载 ID。普通加载失败不会被猜成下载。

```go
// 普通动作完成后，发现由浏览器实际开始的下载。
listed, err := manager.Download(ctx, browserkit.DownloadRequest{
    SessionID: opened.SessionID, Action: "list",
})
// 可加 PageID，只列出已核实归属该 Tab 的下载。

// 显式触发一次点击。当前 CDP 不提供动作到下载的因果身份，
// 因此返回 download_unconfirmed；先保存 Triggered，再处理 error，勿重放。
started, triggerErr := manager.Download(ctx, browserkit.DownloadRequest{
    SessionID: opened.SessionID, PageID: opened.PageID,
    Action: "start", Ref: downloadRef, Revision: snapshot.Revision, TimeoutMS: 1000,
})
_ = started.Triggered // true 仅表示原生点击已确认完成；false 不证明未发生。
_ = triggerErr

// list 是实际发生的下载，不表示由最近一次 start 导致。
listed, err = manager.Download(ctx, browserkit.DownloadRequest{
    SessionID: opened.SessionID, Action: "list",
})
// 按 URL、文件名和页面任务需要检查 listed.Downloads，选定实际 downloadID；
// 多个候选无法区分时保持未知，不把第一个事件当作本次点击的结果。
status, err := manager.Download(ctx, browserkit.DownloadRequest{
    SessionID: opened.SessionID, Action: "query", DownloadID: downloadID,
})
finished, err := manager.Download(ctx, browserkit.DownloadRequest{
    SessionID: opened.SessionID, Action: "wait", DownloadID: downloadID, TimeoutMS: 30000,
})
canceled, err := manager.Download(ctx, browserkit.DownloadRequest{
    SessionID: opened.SessionID, Action: "cancel", DownloadID: downloadID,
})
```

先检查每次返回的部分响应，再处理 error。`start` 不等待或关联下载，回执为 `state=unconfirmed`、`download_unconfirmed`，没有下载 ID、文件名或路径；`triggered=true` 仅表示原生点击回调确认完成，不证明开始了下载。弹窗抢先返回、超时或取消时，`triggered=false` 也不证明点击未发生。触发超时返回 `download_trigger_timeout`，保留 session/page/ref；不要自动重放。

Chromium 的 `downloadWillBegin` 只有下载身份、frame 和 URL，没有 request/action 身份。相同 frame、相同 URL、直接链接、新弹窗或事件先后均不能证明由此次点击触发。旧的“点击后取第一个匹配 frame 下载”的关联已移除；下载统一从会话列表发现，再按明确 ID 查询或续等。

`wait` 超时不取消下载；只有 `state=completed` 才有完整文件 `path`。实际下载状态为 `in_progress`、`completed`、`failed`、`canceled`、`over_limit`、`session_closed`。`canceled` 表示显式取消获浏览器终止事件确认；没有原因的 Chromium canceled 事件记为 `failed` 并说明原因不可得。取消与完成竞争时可能实际已经完成。

`DownloadMaxBytes=0` 不限大小；正数在 CDP 进度事件中检查并取消超限下载，完成时再按实际文件大小核对。进度事件有间隔，预算不是磁盘占用的硬上限，收到并取消前可能短暂超出。部分文件保存在 session 独立临时目录，失败/取消后删除，宿主关闭也会清理。完整文件归宿主持有，关闭不会删除完整文件。下载状态仅在 session 生命周期内保存；Manager 摘除已关闭 session 后不再提供其查询入口。

宿主可在保留现有 session、Tab 和 Cookie 的同时更新下载限额：

```go
if err := manager.SetDownloadMaxBytes(16 << 20); err != nil {
    return err
}
```

更新应用于已有和未来 session 中随后开始的下载，每个下载在收到 `downloadWillBegin` 时固定自己的限额；进行中的下载继续使用开始时的值。传 `0` 恢复不限大小，负数以及 Manager 关闭后的更新会报错。自定义 `SessionFactory` 应在每个下载开始时调用 `SessionConfig.CurrentDownloadMaxBytes()` 并保存该值；直接使用 `NewSession` 时，此方法返回原配置的 `DownloadMaxBytes`。

自动下载的 `TargetID` 通过真实 frame tree 异步核对；来源 frame 过快销毁时可能为空，仍保留实际 `FrameID`，不会猜成当前 Tab。按 `PageID` 筛选时不含尚未核实归属的下载。

## frame、观察与执行事实

常见嵌套 iframe/OOPIF 与开放 Shadow DOM 使用相同快照和 ref 入口。`frame_coverage` 按文档说明 `captured`、`partial`、`hidden` 或 `unavailable`，`shadow_dom=open_only` 明确闭合根不在快照覆盖范围。整体文字/元素预算作用于整个 Tab；元素可用 `next_offset` 继续读取，文字截断由 `text_truncated` 表示。不要将部分观察当成完整 DOM。

同源 frame 被定位到极远屏外、处于低透明度或零内容尺寸时，仍保留控件 ref、名称和角色，但不返回正文、值或选项；覆盖回执为 `partial / metadata_only`。普通页面滚动使 frame 离开视口时，仅隐藏正文、保留正常字段值，回执为 `partial / text_not_visible`。跨域隐藏 frame 继续遵循原有排除规则，不能把仅保留元数据理解为已验证实际可点击。

仿射 frame 变换使用 CDP content quad 映射真实坐标；透视变换、退化几何、裁剪/遮挡或无法核实命中点时返回明确错误。实际边界与可控复现见 [frame-tool-design.md](frame-tool-design.md)。闭合 Shadow DOM 尚无可靠的通用快照和 ref 操作支持，不宣称完整支持。

`Act` 的 Go error 为 `nil` 时仍须检查 `FailedStep`、`Cancelled` 及每步 `Success`。`Completed` 是已确认成功的步骤数，预检失败可能发生在执行任何步骤之前。`snapshot_error` 与 `screenshot_error` 分开表示动作之后的观察失败，不能据此重放动作。SDK 不自动重试副作用。

CDP Network 诊断保持现有有界诊断用途；完整 HTTP 原件、二进制正文和持续流量采集由外部代理承担。
