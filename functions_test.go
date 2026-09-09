package browserkit

import (
	"context"
	"errors"
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

var updateFunctions = flag.Bool("update-functions", false, "更新浏览器函数清单 golden")

func functionByName(t *testing.T, manager *Manager, name string) Function {
	t.Helper()
	for _, function := range manager.Functions() {
		if function.Name == name {
			return function
		}
	}
	t.Fatalf("缺少函数 %s", name)
	return Function{}
}

func TestFunctionsContract(t *testing.T) {
	manager, _ := fakeManager(t, Config{AllowEvaluate: true, AllowDevtools: true})
	defer manager.Close(context.Background())
	var contract strings.Builder
	names := map[string]bool{}
	for _, function := range manager.Functions() {
		typ := reflect.TypeOf(function.Handler)
		if names[function.Name] || function.Description == "" {
			t.Fatalf("无效函数定义：%+v", function)
		}
		names[function.Name] = true
		if typ.Kind() != reflect.Func || typ.NumIn() != 2 || typ.In(0) != reflect.TypeFor[context.Context]() || typ.NumOut() != 2 || typ.Out(1) != reflect.TypeFor[error]() {
			t.Fatalf("%s 签名不符合约定：%v", function.Name, typ)
		}
		for _, structure := range []reflect.Type{typ.In(1), typ.Out(0)} {
			if structure.Kind() != reflect.Struct || structure.Name() == "" || structure.PkgPath() != reflect.TypeFor[OpenRequest]().PkgPath() {
				t.Fatalf("%s 不是根包命名结构体：%v", function.Name, structure)
			}
		}
		contract.WriteString(function.Name + "(" + typ.In(1).Name() + ") -> " + typ.Out(0).Name() + "\n" + function.Description + "\n\n")
	}
	actual := strings.TrimSpace(contract.String()) + "\n"
	path := "testdata/functions.golden"
	if *updateFunctions {
		if err := os.WriteFile(path, []byte(actual), 0644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual != string(expected) {
		t.Fatalf("函数契约与 %s 不一致，确认 API 变化后使用 -update-functions 更新", path)
	}
}

func TestFunctionsFollowCapabilitiesAndDoNotShareCatalog(t *testing.T) {
	for _, config := range []Config{{}, {AllowEvaluate: true}, {AllowDevtools: true}, {AllowEvaluate: true, AllowDevtools: true}} {
		manager, _ := fakeManager(t, config)
		functions := manager.Functions()
		expected := 14
		if config.AllowEvaluate {
			expected++
		}
		if config.AllowDevtools {
			expected += 3
		}
		if len(functions) != expected {
			t.Fatalf("函数数量=%d，期望=%d", len(functions), expected)
		}
		for _, function := range functions {
			if function.Name == "browserEvaluate" && !config.AllowEvaluate || strings.HasPrefix(function.Name, "browserDevtools") && !config.AllowDevtools {
				t.Fatal("暴露了未启用能力")
			}
		}
		functions[0].Name = "changed"
		if manager.Functions()[0].Name != "browserOpen" {
			t.Fatal("调用方修改了后续函数清单")
		}
		_ = manager.Close(context.Background())
	}
	var absent *Manager
	if len(absent.Functions()) != 0 {
		t.Fatal("nil Manager 应返回空清单")
	}
}

func TestFunctionsShareManagerAndRequireExplicitTargets(t *testing.T) {
	manager, _ := fakeManager(t, Config{})
	defer manager.Close(context.Background())
	open := functionByName(t, manager, "browserOpen").Handler.(func(context.Context, OpenRequest) (OpenResponse, error))
	opened, err := open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := manager.View(context.Background())
	if err != nil || view.ActivePageID != opened.PageID {
		t.Fatalf("函数与宿主视图没有共用状态：%+v %v", view, err)
	}
	tabs := functionByName(t, manager, "browserTabs").Handler.(func(context.Context, TabsRequest) (TabsResponse, error))
	if _, err := tabs(context.Background(), TabsRequest{}); err == nil {
		t.Fatal("模型函数接受了空 session_id")
	}
	for _, name := range []string{"browserActivateTab", "browserCloseTab"} {
		handler := reflect.ValueOf(functionByName(t, manager, name).Handler)
		for _, request := range []TabRequest{{}, {SessionID: opened.SessionID}, {PageID: opened.PageID}} {
			results := handler.Call([]reflect.Value{reflect.ValueOf(context.Background()), reflect.ValueOf(request)})
			if results[1].IsNil() {
				t.Fatalf("%s 接受了缺少目标的请求", name)
			}
		}
	}
	listed, err := tabs(context.Background(), TabsRequest{SessionID: opened.SessionID})
	if err != nil || len(listed.Tabs) != 1 {
		t.Fatalf("无效工具调用改变了会话：%+v %v", listed, err)
	}
	closeSession := functionByName(t, manager, "browserClose").Handler.(func(context.Context, CloseRequest) (CloseResponse, error))
	if _, err := closeSession(context.Background(), CloseRequest{SessionID: opened.SessionID}); err != nil {
		t.Fatal(err)
	}
	view, err = manager.View(context.Background())
	if err != nil || view.Available {
		t.Fatalf("工具关闭后宿主视图仍然可用：%+v %v", view, err)
	}
}

func TestOpenFunctionBoundsStartupAndHonorsCancellation(t *testing.T) {
	started := 0
	manager, err := NewManager(Config{Session: SessionConfig{OperationTimeout: 10 * time.Millisecond}, Factory: func(ctx context.Context, _ SessionConfig) (Session, error) {
		started++
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	open := functionByName(t, manager, "browserOpen").Handler.(func(context.Context, OpenRequest) (OpenResponse, error))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := open(ctx, OpenRequest{URL: "https://example.com"}); !errors.Is(err, context.Canceled) || started != 0 {
		t.Fatalf("取消请求仍启动浏览器：%v %d", err, started)
	}
	begin := time.Now()
	if _, err := open(context.Background(), OpenRequest{URL: "https://example.com"}); err == nil || started != 1 {
		t.Fatalf("启动未遵守超时：%v %d", err, started)
	}
	if time.Since(begin) > time.Second {
		t.Fatal("函数启动超时没有及时结束")
	}
}
