# 安全报告

通过 GitHub 仓库 Security 页的私密漏洞报告提交问题。
若私密报告入口尚未启用，可在 Issue 中请求私密联系方式，不附漏洞细节或访问凭据。

报告应包含版本、复现条件和影响范围，删除 Cookie、令牌及个人信息。
公开 Issue 用于不涉及敏感数据的普通问题。

## 部署

BrowserKit HTTP 服务不提供用户认证。
对外部署时，使用反向代理或宿主认证，同时保护 HTTP 和 WebSocket。
AllowedOrigins 控制跨源访问，不代替身份认证。

页面访问、JavaScript 执行和诊断权限由 Go 配置决定。
浏览器 profile 可能包含登录态，不应作为日志或问题附件上传。
