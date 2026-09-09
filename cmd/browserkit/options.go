package main

import (
	"errors"
	"net/url"
)

type modeOptions struct{ mode, remote, chrome, pageURL string }

func (o modeOptions) validate() (string, error) {
	mode := o.mode
	if mode == "" {
		mode = "new"
		if o.remote != "" {
			mode = "attach"
		}
	}
	switch mode {
	case "new":
		if u, err := url.Parse(o.chrome); err == nil && u.Host != "" {
			return "", errors.New("--chrome 必须是本地可执行文件路径，接管使用 --mode attach --remote")
		}
		if o.remote != "" {
			return "", errors.New("new 模式不能设置 --remote；接管请使用 --mode attach")
		}
	case "attach":
		if o.chrome != "" {
			return "", errors.New("attach 模式不能设置 --chrome，不会启动另一个 Chrome")
		}
		u, err := url.Parse(o.remote)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss") {
			return "", errors.New("attach 模式需要 --remote 指定 Chrome 调试 HTTP 地址或 browser WebSocket")
		}
	default:
		return "", errors.New("--mode 只支持 new 或 attach")
	}
	if o.pageURL != "" {
		u, err := url.Parse(o.pageURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "", errors.New("--url 必须是完整 HTTP/HTTPS 页面地址，不是 chrome://inspect")
		}
	}
	return mode, nil
}
