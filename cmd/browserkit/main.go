// browserkit 是 BrowserKit 的命令行 Function Tool 入口。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"

	browserkit "github.com/zeoxisca/awesome-browserkit"
)

type callRequest struct {
	Function  string          `json:"function"`
	Arguments json.RawMessage `json:"arguments"`
}

type callResponse struct {
	OK       bool   `json:"ok"`
	Function string `json:"function,omitempty"`
	Result   any    `json:"result,omitempty"`
	Error    string `json:"error,omitempty"`
}

func main() {
	if err := mainRun(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mainRun() error {
	var (
		modeFlag      = flag.String("mode", "", "new：新建独立 profile（默认）；attach：接管已有浏览器")
		pageURL       = flag.String("url", "", "可选：在所使用的浏览器中新建 Tab 打开此地址；attach 省略时接管激活 Tab")
		private       = flag.Bool("allow-private-network", false, "允许本地或私网页面")
		list          = flag.Bool("list-functions", false, "输出当前可用 Function Tool 的名称与说明")
		version       = flag.Bool("version", false, "输出 BrowserKit client 版本")
		chrome        = flag.String("chrome", "", "Chrome/Chromium 可执行文件路径")
		remote        = flag.String("remote", "", "已有 CDP 浏览器地址，例如 http://127.0.0.1:9222")
		allowEvaluate = flag.Bool("allow-evaluate", false, "启用 browserEvaluate")
		devtools      = flag.Bool("devtools", false, "启用开发诊断函数")
		screenshotDir = flag.String("screenshot-dir", "", "截图输出目录")
	)
	flag.Parse()
	if *version {
		fmt.Println("browserkit 0.1.0")
		return nil
	}
	if flag.NArg() != 0 {
		return errors.New("不支持位置参数，请使用 --help 查看选项")
	}
	mode, err := (modeOptions{mode: *modeFlag, remote: *remote, chrome: *chrome, pageURL: *pageURL}).validate()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sessionConfig := browserkit.SessionConfig{ExecPath: *chrome, RemoteAddress: *remote}
	if mode == "new" && !*list {
		dir, err := os.MkdirTemp("", "browserkit-profile-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		sessionConfig.UserDataDir = dir
		sessionConfig.CleanupUserDataDir = true
	}
	manager, err := browserkit.NewManager(browserkit.Config{
		AllowEvaluate:       *allowEvaluate,
		AllowPrivateNetwork: *private,
		MaxSessions:         1,
		AllowDevtools:       *devtools,
		ScreenshotDir:       *screenshotDir,
		Session:             sessionConfig,
	})
	if err != nil {
		return err
	}
	defer manager.Close(context.Background())
	functions := manager.Functions()
	if mode == "attach" {
		for i := range functions {
			if functions[i].Name == "browserClose" {
				functions[i].Description = "断开当前会话，保留外部浏览器和页面。"
			}
		}
	}
	if *list {
		for _, function := range functions {
			if err := writeJSON(os.Stdout, map[string]string{"name": function.Name, "description": function.Description}); err != nil {
				return err
			}
		}
		return nil
	}
	if mode == "attach" || *pageURL != "" {
		var opened browserkit.OpenResponse
		if mode == "attach" {
			fmt.Fprintln(os.Stderr, "正在连接 Chrome；若浏览器显示调试授权提示，请在浏览器中确认。")
			opened, err = manager.Attach(ctx, browserkit.AttachRequest{URL: *pageURL})
		} else {
			opened, err = manager.Open(ctx, browserkit.OpenRequest{URL: *pageURL})
		}
		if err != nil {
			return err
		}
		if err := writeJSON(os.Stdout, callResponse{OK: true, Function: "browserOpen", Result: opened}); err != nil {
			return err
		}
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = os.Stdin.Close()
		case <-done:
		}
	}()
	if err := run(ctx, os.Stdin, os.Stdout, functions); err != nil {
		return err
	}
	return nil
}

func run(ctx context.Context, input io.Reader, output io.Writer, functions []browserkit.Function) error {
	byName := make(map[string]browserkit.Function, len(functions))
	for _, function := range functions {
		byName[function.Name] = function
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		response := invoke(ctx, byName, []byte(line))
		if err := writeJSON(output, response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func invoke(ctx context.Context, functions map[string]browserkit.Function, data []byte) callResponse {
	var request callRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return callResponse{Error: "请求 JSON 无效: " + err.Error()}
	}
	response := callResponse{Function: request.Function}
	function, ok := functions[request.Function]
	if !ok {
		response.Error = "未知浏览器函数: " + request.Function
		return response
	}
	handler := reflect.ValueOf(function.Handler)
	if handler.Kind() != reflect.Func || handler.Type().NumIn() != 2 || handler.Type().NumOut() != 2 {
		response.Error = "浏览器函数 Handler 类型无效"
		return response
	}
	argument := reflect.New(handler.Type().In(1))
	if len(request.Arguments) != 0 && string(request.Arguments) != "null" {
		if err := json.Unmarshal(request.Arguments, argument.Interface()); err != nil {
			response.Error = "arguments 无法解码: " + err.Error()
			return response
		}
	}
	values := handler.Call([]reflect.Value{reflect.ValueOf(ctx), argument.Elem()})
	if errValue := values[1]; !errValue.IsNil() {
		response.Error = values[1].Interface().(error).Error()
		return response
	}
	response.OK = true
	response.Result = values[0].Interface()
	return response
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
