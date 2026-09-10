# 安全报告

通过 GitHub 仓库 Security 页的私密漏洞报告提交问题。
若私密报告入口尚未启用，可在 Issue 中请求私密联系方式，不附漏洞细节或访问凭据。

报告应包含版本、复现条件和影响范围，删除 Cookie、令牌及个人信息。
公开 Issue 用于不涉及敏感数据的普通问题。

## 部署

BrowserKit HTTP 服务不内置特定用户认证协议。嵌入服务可通过 `browserhttp.Options.AuthMiddleware` 注册宿主认证，它会同时保护 HTTP 和 WebSocket 握手。
对外部署时必须配置该中间件，或使用同时保护 HTTP 和 WebSocket 的认证反向代理。
AllowedOrigins 控制跨源访问，不代替身份认证。

页面访问、JavaScript 执行和诊断权限由 Go 配置决定。内置 Rod/CDP 会话默认在浏览器请求边界拒绝解析到本机、私网、链路本地及其他非公网地址，并重新检查重定向和子资源；自定义 `SessionFactory` 需要自行提供等价策略。
该应用层检查属于纵深防御。对抗恶意权威 DNS 在 BrowserKit 检查与 Chromium 建连之间切换解析结果的场景时，还应使用出口防火墙或能够解析并固定目标 IP 的可信代理，在网络层拒绝私网和元数据网段。
浏览器 profile 可能包含登录态，不应作为日志或问题附件上传。
