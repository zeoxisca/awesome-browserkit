//go:build browser_smoke

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// 使用临时 Chrome 验证 CLI；绝不连接开发者的日常浏览器。
func TestRealCLIModes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "browserkit")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", output, err)
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<title>Mode test</title><input aria-label="Name"><button>Save</button>`))
	}))
	defer site.Close()
	chrome, ok := launcher.LookPath()
	if !ok {
		t.Fatal("Chrome required")
	}
	launch := launcher.New().Bin(chrome).Headless(true).Leakless(false).UserDataDir(t.TempDir())
	ws, err := launch.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Kill()
	owner := rod.New().ControlURL(ws).NoDefaultDevice().Context(ctx)
	if err := owner.Connect(); err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	page := owner.MustPage(site.URL)
	page.MustWaitLoad()
	page.MustEval(`() => {window.modeMarker = 'original'; document.querySelector('input').value='kept'; localStorage.setItem('mode','owner')}`)

	runClient := func(args []string, check func(*exec.Cmd, func(string, map[string]any) map[string]any, map[string]any)) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		profileRoot := t.TempDir()
		cmd.Env = append(os.Environ(), "TMPDIR="+profileRoot)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = stdin.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()
		decoder := json.NewDecoder(bufio.NewReader(stdout))
		read := func() map[string]any {
			t.Helper()
			var response struct {
				OK     bool           `json:"ok"`
				Result map[string]any `json:"result"`
				Error  string         `json:"error"`
			}
			if err := decoder.Decode(&response); err != nil {
				t.Fatalf("CLI read: %v %s", err, stderr.String())
			}
			if !response.OK {
				t.Fatalf("CLI error: %s", response.Error)
			}
			return response.Result
		}
		opened := read()
		profiles, _ := filepath.Glob(filepath.Join(profileRoot, "browserkit-profile-*"))
		if args[1] == "new" && len(profiles) != 1 {
			t.Fatalf("new profile not created: %v", profiles)
		}
		if args[1] == "attach" && len(profiles) != 0 {
			t.Fatal("attach created a profile")
		}
		call := func(name string, args map[string]any) map[string]any {
			t.Helper()
			data, _ := json.Marshal(map[string]any{"function": name, "arguments": args})
			_, err := fmt.Fprintln(stdin, string(data))
			if err != nil {
				t.Fatal(err)
			}
			return read()
		}
		check(cmd, call, opened)
		_ = stdin.Close()
		_, _ = io.Copy(io.Discard, stdout)
		if err := cmd.Wait(); err != nil {
			t.Fatalf("CLI exit: %v %s", err, stderr.String())
		}
		profiles, _ = filepath.Glob(filepath.Join(profileRoot, "browserkit-profile-*"))
		if len(profiles) != 0 {
			t.Fatalf("profile not cleaned after exit: %v", profiles)
		}
	}
	for i, remote := range []string{ws, "http://" + strings.Split(strings.TrimPrefix(ws, "ws://"), "/")[0]} {
		runClient([]string{"--mode", "attach", "--remote", remote, "--url", site.URL + "/", "--allow-private-network"}, func(_ *exec.Cmd, call func(string, map[string]any) map[string]any, opened map[string]any) {
			result := call("browserSnapshot", map[string]any{"session_id": opened["session_id"], "page_id": opened["page_id"]})
			if result["title"] != "Mode test" {
				t.Fatalf("snapshot: %v", result)
			}
			call("browserClose", map[string]any{"session_id": opened["session_id"]})
		})
		if got := page.MustEval(`() => window.modeMarker + ':' + document.querySelector('input').value`).Str(); got != "original:kept" {
			t.Fatalf("attach reloaded page: %s", got)
		}
		if len(owner.MustPages()) != i+2 {
			t.Fatal("attach --url did not retain exactly one new tab")
		}
		freshCount := 0
		for _, candidate := range owner.MustPages() {
			if candidate.TargetID == page.TargetID {
				continue
			}
			freshCount++
			if candidate.MustInfo().URL != site.URL+"/" || candidate.MustEval(`() => localStorage.getItem('mode')`).Str() != "owner" || candidate.MustEval(`() => window.modeMarker === undefined`).Bool() != true {
				t.Fatal("new tab has wrong URL, profile or document state")
			}
		}
		if freshCount != i+1 {
			t.Fatal("wrong fresh tab count")
		}
	}

	for _, candidate := range owner.MustPages() {
		if candidate.TargetID != page.TargetID {
			candidate.MustClose()
		}
	}
	// 不提供 URL 时选择激活页，即使还有另一个 URL 不同的 Tab。
	other := owner.MustPage(site.URL + "/other")
	other.MustWaitLoad()
	page.MustActivate()
	runClient([]string{"--mode", "attach", "--remote", ws, "--allow-private-network"}, func(_ *exec.Cmd, call func(string, map[string]any) map[string]any, opened map[string]any) {
		if opened["url"] != site.URL+"/" {
			t.Fatalf("wrong active page: %v", opened)
		}
		call("browserClose", map[string]any{"session_id": opened["session_id"]})
	})
	if len(owner.MustPages()) != 2 || page.MustEval(`() => window.modeMarker`).Str() != "original" {
		t.Fatal("active attach altered tabs")
	}
	other.MustClose()
	// 两次 new 启动独立 profile；不继承 owner，也不继承上一次 new 的存储。
	for i := 0; i < 2; i++ {
		runClient([]string{"--mode", "new", "--url", site.URL + "/", "--allow-private-network", "--allow-evaluate"}, func(_ *exec.Cmd, call func(string, map[string]any) map[string]any, opened map[string]any) {
			result := call("browserEvaluate", map[string]any{"session_id": opened["session_id"], "page_id": opened["page_id"], "code": "localStorage.getItem('mode')"})
			if result["value"] != nil {
				t.Fatalf("profile storage inherited: %v", result)
			}
			call("browserEvaluate", map[string]any{"session_id": opened["session_id"], "page_id": opened["page_id"], "code": "localStorage.setItem('mode','new')"})
		})
	}
	// 已有页面仍然可用，外部 browser 未被 Browser.close 关闭。
	if page.MustEval(`() => localStorage.getItem('mode')`).Str() != "owner" {
		t.Fatal("owner storage changed")
	}
	// 参数拒绝也须有非零进程退出码。
	bad := exec.CommandContext(ctx, binary, "--mode", "attach", "--url", site.URL+"/")
	bad.Stdout = os.Stdout
	if err := bad.Run(); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
