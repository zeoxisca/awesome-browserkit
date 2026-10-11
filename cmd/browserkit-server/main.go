// browserkit-server 将 BrowserKit 作为独立 HTTP 服务运行。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	browserkit "github.com/zeoxisca/awesome-browserkit"
	"github.com/zeoxisca/awesome-browserkit/browserhttp"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8787", "HTTP 监听地址")
	initialURL := flag.String("url", "", "启动时在所使用的浏览器中新建 Tab 打开此地址；省略时等待客户端新建 Tab")
	chrome := flag.String("chrome", "", "Chrome/Chromium 可执行文件路径")
	remote := flag.String("remote", "", "已有浏览器的 CDP 地址")
	origins := flag.String("allowed-origins", "", "允许的前端 Origin，逗号分隔，例如 http://localhost:5173")
	readOnly := flag.Bool("read-only", false, "只允许观察，拒绝页面操作和诊断启停")
	devtools := flag.Bool("devtools", false, "启用开发诊断")
	private := flag.Bool("allow-private-network", false, "允许打开私网和本地页面")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("不支持位置参数，请使用 --help 查看选项")
	}
	allowed, err := parseOrigins(*origins)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manager, err := browserkit.NewManager(browserkit.Config{
		Session:       browserkit.SessionConfig{ExecPath: *chrome, RemoteAddress: *remote},
		AllowDevtools: *devtools, AllowPrivateNetwork: *private,
	})
	if err != nil {
		return err
	}
	defer manager.Close(context.Background())
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if *initialURL != "" {
		if _, err := manager.Open(ctx, browserkit.OpenRequest{URL: *initialURL}); err != nil {
			return err
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/browser/", http.StripPrefix("/browser", browserhttp.NewHandler(manager, browserhttp.Options{
		ReadOnly: *readOnly, AllowedOrigins: allowed,
	})))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Printf("BrowserKit HTTP：http://%s/browser/", listener.Addr())
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func parseOrigins(value string) ([]string, error) {
	var origins []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		u, err := url.Parse(item)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return nil, fmt.Errorf("无效前端 Origin %q，需使用 scheme://host[:port]", item)
		}
		origins = append(origins, item)
	}
	return origins, nil
}
