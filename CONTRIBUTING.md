# 贡献指南

## 开发

需要 Go 1.25.8+、Node.js 22+；浏览器测试另需 Chrome / Chromium。

```sh
make install
make test
make build
```

`make demo` 在 `http://127.0.0.1:8877` 启动演示。
React 组件修改后，先构建 `react`，再构建 demo。

## 验证

- `make test`：Go 与 React 测试。
- `make race`：Go 竞态检查。
- `make smoke`：临时 Chrome 中的 SDK 和 CLI 测试，不连接日常浏览器。
- `make pack`：生成 React 安装包。

修改工具名称、签名或说明后，检查 `testdata/functions.golden` 的差异。
使用 `go test . -run TestFunctionsContract -update-functions` 更新预期结果。

## 提交变更

说明问题、修改后的行为和验证结果。
Go 代码使用 gofmt；公共注释与用户文案使用中文。
新增依赖需说明用途；安装方式或 API 变化需更新对应教程。

不要提交密钥、浏览器 profile、Cookie、日志或构建产物。
报告问题时提供版本、操作系统、复现步骤和已脱敏的错误信息。
