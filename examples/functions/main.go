// functions 示例演示普通宿主如何把浏览器函数适配成自己的工具函数。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/zeoxisca/awesome-browserkit"
)

// hostContext 模拟宿主已有的工具上下文；浏览器库不依赖这个类型。
type hostContext struct{ context.Context }

// wrap 只转换上下文；输入和输出类型完整保留，供宿主生成 schema。
func wrap[Request, Response any](fn func(context.Context, Request) (Response, error)) func(hostContext, Request) (Response, error) {
	return func(ctx hostContext, request Request) (Response, error) { return fn(ctx, request) }
}

func main() {
	address := flag.String("url", "", "可选：在真实 Chrome 中新建 Tab 打开此地址并读取快照；省略时只列出函数")
	flag.Parse()
	manager, err := browserkit.NewManager(browserkit.Config{})
	if err != nil {
		log.Fatal(err)
	}
	defer manager.Close(context.Background())
	var open func(hostContext, browserkit.OpenRequest) (browserkit.OpenResponse, error)
	var snapshot func(hostContext, browserkit.SnapshotRequest) (browserkit.SnapshotResponse, error)
	for _, function := range manager.Functions() {
		fmt.Printf("%s: %s\n", function.Name, function.Description)
		// 这里只演示两个绑定。支持标准 context.Context 的框架可直接接收 Handler。
		switch function.Name {
		case "browserOpen":
			open = wrap(function.Handler.(func(context.Context, browserkit.OpenRequest) (browserkit.OpenResponse, error)))
		case "browserSnapshot":
			snapshot = wrap(function.Handler.(func(context.Context, browserkit.SnapshotRequest) (browserkit.SnapshotResponse, error)))
		}
	}
	if *address == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	invocation := hostContext{Context: ctx}
	opened, err := open(invocation, browserkit.OpenRequest{URL: *address})
	if err != nil {
		log.Print(err)
		return
	}
	page, err := snapshot(invocation, browserkit.SnapshotRequest{SessionID: opened.SessionID, MaxElements: 10})
	if err != nil {
		log.Print(err)
		return
	}
	fmt.Printf("\n页面：%s\n%s\n", page.Title, page.Text)
}
