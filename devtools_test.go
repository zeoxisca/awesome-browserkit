package browserkit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

type blockingResponseBodyClient struct{}

func (client *blockingResponseBodyClient) GetSessionID() proto.TargetSessionID { return "session-1" }

func (client *blockingResponseBodyClient) Call(ctx context.Context, _ string, method string, _ any) ([]byte, error) {
	if method == (proto.NetworkGetResponseBody{}).ProtoReq() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []byte(`{}`), nil
}

func TestReadDevtoolsResponseBodyHasOperationTimeout(t *testing.T) {
	client := &blockingResponseBodyClient{}
	started := time.Now()
	_, _, err := readDevtoolsResponseBody(context.Background(), client, "request-1", 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("响应正文读取没有按操作期限退出: elapsed=%s err=%v", time.Since(started), err)
	}
}

func TestConsoleTextKeepsPrimitiveValuesAndDescriptions(t *testing.T) {
	values := []*proto.RuntimeRemoteObject{
		{Type: proto.RuntimeRemoteObjectTypeString, Value: gson.New("hello")},
		{Type: proto.RuntimeRemoteObjectTypeNumber, Value: gson.New(42)},
		{Type: proto.RuntimeRemoteObjectTypeObject, Description: "Object"},
	}
	if got := consoleText(values); got != "hello 42 Object" {
		t.Fatalf("consoleText=%q", got)
	}
}

func TestStackLocationUsesOneBasedSourceCoordinates(t *testing.T) {
	trace := &proto.RuntimeStackTrace{CallFrames: []*proto.RuntimeCallFrame{{FunctionName: "login", URL: "app.js", LineNumber: 4, ColumnNumber: 6}}}
	url, line, column, stack := stackLocation(trace)
	if url != "app.js" || line != 5 || column != 7 || stack != "login@app.js:5:7" {
		t.Fatalf("location=(%q,%d,%d,%q)", url, line, column, stack)
	}
}

func TestNetworkEventNormalizesDurationAndBytes(t *testing.T) {
	event := networkEvent(devtoolsRequest{method: "GET", url: "https://example.com", started: 2.5}, 2.583, 214.4, false, "", -1)
	if event.Kind != "network" || event.DurationMS != 83 || event.EncodedBytes != 214 || event.PendingDelta != -1 {
		t.Fatalf("event=%+v", event)
	}
	negative := networkEvent(devtoolsRequest{started: 3}, 2, -1, true, "failed", -1)
	if negative.DurationMS != 0 || negative.EncodedBytes != 0 || !negative.Failed {
		t.Fatalf("negative=%+v", negative)
	}
}

func TestNetworkBodyHelpersOnlyExposeBoundedText(t *testing.T) {
	for _, mime := range []string{"text/html; charset=utf-8", "application/json", "application/javascript", "application/xml"} {
		if !isTextMIME(mime) {
			t.Fatalf("文本 MIME 被拒绝: %s", mime)
		}
	}
	if isTextMIME("image/png") || isTextMIME("application/octet-stream") {
		t.Fatal("二进制 MIME 被误判为文本")
	}
	short, truncated := limitBody("hello")
	if short != "hello" || truncated {
		t.Fatalf("短正文=%q truncated=%v", short, truncated)
	}
	long, truncated := limitBody(strings.Repeat("x", 16<<10+1))
	if len(long) != 16<<10 || !truncated {
		t.Fatalf("长正文长度=%d truncated=%v", len(long), truncated)
	}
}

func TestRuntimeTimeUsesCDPMilliseconds(t *testing.T) {
	value := runtimeTime(proto.RuntimeTimestamp(1_500))
	if !value.Equal(time.Unix(1, 500*time.Millisecond.Nanoseconds())) {
		t.Fatalf("runtimeTime=%s", value)
	}
	if delta := time.Since(runtimeTime(0)); delta < 0 || delta > time.Second {
		t.Fatalf("零时间没有回退到当前时间: %s", delta)
	}
}

func TestConsoleLevelMapsProblemSeverities(t *testing.T) {
	if consoleLevel(proto.RuntimeConsoleAPICalledTypeAssert) != "error" || consoleLevel(proto.RuntimeConsoleAPICalledTypeWarning) != "warning" {
		t.Fatal("console level 映射错误")
	}
	if !strings.EqualFold(consoleLevel(proto.RuntimeConsoleAPICalledTypeLog), "log") {
		t.Fatal("普通日志映射错误")
	}
}
