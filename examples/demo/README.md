# BrowserKit 演示

浏览器组件工作台和使用文档。
安装与集成步骤见仓库根目录的 [README](../../README.md)。

## 运行

需要 Go 1.25.8+、Node.js 22+ 和本机 Chrome / Chromium。
在仓库根目录执行：

```sh
npm ci --prefix react
npm run build --prefix react
npm ci --prefix examples/demo/web
npm run build --prefix examples/demo/web
go run ./examples/demo
```

打开 [浏览器工作台](http://127.0.0.1:8877/)，查看页面画面并操作标签页、地址栏、输入和诊断面板。
客户端配置区可实时调整组件功能，页面同时提供容器尺寸和主题切换。

快速开始中的输入框生成 CLI 命令；在本地执行命令后才会连接浏览器。
