// demo 提供组件工作台、文档和示例网页。
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zeoxisca/awesome-browserkit"
	"github.com/zeoxisca/awesome-browserkit/browserhttp"
)

func main() {
	address := flag.String("addr", "127.0.0.1:8877", "演示监听地址")
	chrome := flag.String("chrome", "", "Chrome 可执行文件，省略时使用系统安装")
	directory := flag.String("dir", "examples/demo", "示例静态文件目录")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manager, err := browserkit.NewManager(browserkit.Config{Session: browserkit.SessionConfig{ExecPath: *chrome}, AllowDevtools: true, DevtoolsAutoStart: true, AllowPrivateNetwork: true, AllowEvaluate: true})
	if err != nil {
		log.Fatal(err)
	}
	defer manager.Close(context.Background())
	mux := http.NewServeMux()
	mux.Handle("/browser/", http.StripPrefix("/browser", browserhttp.NewHandler(manager, browserhttp.Options{})))
	mux.Handle("/observer/", http.StripPrefix("/observer", browserhttp.NewHandler(manager, browserhttp.Options{ReadOnly: true})))
	mux.Handle("/site/", http.StripPrefix("/site/", http.FileServer(http.Dir(filepath.Join(*directory, "site")))))
	mux.HandleFunc("GET /site/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","message":"请求成功","items":3}`))
	})
	mux.Handle("/", http.FileServer(http.Dir(filepath.Join(*directory, "web/dist"))))
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Print(err)
			stop()
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	origin := "http://" + listener.Addr().String()
	opened, err := manager.Open(ctx, browserkit.OpenRequest{URL: origin + "/site/", Viewport: browserkit.Viewport{Width: 1100, Height: 680}})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("独立浏览器示例：%s；页面 %s", origin, opened.PageID)
	<-ctx.Done()
}
