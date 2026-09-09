package browserkit

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod/lib/proto"
)

const devtoolsResponseBodyTimeout = 2 * time.Second

// DevtoolsEvent 是页面开发诊断产生的一条临时事实。
// PendingDelta 只用于宿主维护进行中的请求数量，不应直接展示或持久化。
type DevtoolsEvent struct {
	Kind          string
	Level         string
	Text          string
	Stack         string
	SourceURL     string
	Line          int
	Column        int
	Method        string
	ResourceType  string
	URL           string
	Status        int
	MIMEType      string
	DurationMS    int64
	EncodedBytes  int64
	Failed        bool
	Error         string
	RequestBody   string
	ResponseBody  string
	BodyAvailable bool
	BodyTruncated bool
	At            time.Time
	PendingDelta  int
}

// DevtoolsPage 提供页面级 Console、异常和 Network（含有界文本正文）采集。
// stop 可以重复调用；回调必须尽快返回，并且可能在 stop 返回前与其并发。
type DevtoolsPage interface {
	StartDevtools(context.Context, func(DevtoolsEvent)) (stop func(), err error)
}

type devtoolsRequest struct {
	method        string
	resourceType  string
	url           string
	mimeType      string
	status        int
	started       float64
	requestBody   string
	responseBody  string
	bodyAvailable bool
	bodyTruncated bool
}

func (page *rodPage) StartDevtools(ctx context.Context, emit func(DevtoolsEvent)) (func(), error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if emit == nil {
		return nil, fmt.Errorf("DevTools 事件回调不能为空")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	requests := make(map[proto.NetworkRequestID]devtoolsRequest)
	var requestsMu sync.Mutex
	streamPage := page.page.Context(streamCtx)
	wait := streamPage.EachEvent(
		func(event *proto.RuntimeConsoleAPICalled) {
			entry := DevtoolsEvent{Kind: "console", Level: consoleLevel(event.Type), Text: consoleText(event.Args), At: runtimeTime(event.Timestamp)}
			entry.SourceURL, entry.Line, entry.Column, entry.Stack = stackLocation(event.StackTrace)
			emit(entry)
		},
		func(event *proto.RuntimeExceptionThrown) {
			if event.ExceptionDetails == nil {
				return
			}
			details := event.ExceptionDetails
			text := details.Text
			if details.Exception != nil && details.Exception.Description != "" {
				text = details.Exception.Description
			}
			entry := DevtoolsEvent{
				Kind: "exception", Level: "error", Text: text, SourceURL: details.URL,
				Line: details.LineNumber + 1, Column: details.ColumnNumber + 1, At: runtimeTime(event.Timestamp),
			}
			stackURL, stackLine, stackColumn, stack := stackLocation(details.StackTrace)
			entry.Stack = stack
			if entry.SourceURL == "" {
				entry.SourceURL, entry.Line, entry.Column = stackURL, stackLine, stackColumn
			}
			emit(entry)
		},
		func(event *proto.LogEntryAdded) {
			if event.Entry == nil {
				return
			}
			entry := DevtoolsEvent{
				Kind: "console", Level: string(event.Entry.Level), Text: event.Entry.Text,
				SourceURL: event.Entry.URL, At: runtimeTime(event.Entry.Timestamp),
			}
			if event.Entry.LineNumber != nil {
				entry.Line = *event.Entry.LineNumber + 1
			}
			stackURL, stackLine, stackColumn, stack := stackLocation(event.Entry.StackTrace)
			entry.Stack = stack
			if entry.SourceURL == "" {
				entry.SourceURL, entry.Line, entry.Column = stackURL, stackLine, stackColumn
			}
			emit(entry)
		},
		func(event *proto.NetworkRequestWillBeSent) {
			if event.Request == nil {
				return
			}
			requestsMu.Lock()
			previous, exists := requests[event.RequestID]
			if exists && event.RedirectResponse != nil {
				previous.status = event.RedirectResponse.Status
				previous.mimeType = event.RedirectResponse.MIMEType
			}
			requests[event.RequestID] = devtoolsRequest{method: event.Request.Method, resourceType: string(event.Type), url: event.Request.URL, started: float64(event.Timestamp), requestBody: event.Request.PostData}
			requestsMu.Unlock()
			if exists && event.RedirectResponse != nil {
				emit(networkEvent(previous, float64(event.Timestamp), event.RedirectResponse.EncodedDataLength, false, "", 0))
				return
			}
			if !exists {
				emit(DevtoolsEvent{PendingDelta: 1})
			}
		},
		func(event *proto.NetworkResponseReceived) {
			if event.Response == nil {
				return
			}
			requestsMu.Lock()
			request, ok := requests[event.RequestID]
			if ok {
				request.status = event.Response.Status
				request.mimeType = event.Response.MIMEType
				requests[event.RequestID] = request
			}
			requestsMu.Unlock()
		},
		func(event *proto.NetworkLoadingFinished) {
			requestsMu.Lock()
			request, ok := requests[event.RequestID]
			if ok {
				delete(requests, event.RequestID)
			}
			requestsMu.Unlock()
			if ok {
				requestID, finished, encoded := event.RequestID, float64(event.Timestamp), event.EncodedDataLength
				if request.mimeType == "" || !isTextMIME(request.mimeType) {
					emit(networkEvent(request, finished, encoded, false, "", -1))
					return
				}
				go func() {
					request.bodyAvailable = true
					body, truncated, err := readDevtoolsResponseBody(streamCtx, page.page, requestID, devtoolsResponseBodyTimeout)
					if err == nil {
						request.responseBody, request.bodyTruncated = body, truncated
					}
					emit(networkEvent(request, finished, encoded, false, "", -1))
				}()
			}
		},
		func(event *proto.NetworkLoadingFailed) {
			requestsMu.Lock()
			request, ok := requests[event.RequestID]
			if ok {
				delete(requests, event.RequestID)
			}
			requestsMu.Unlock()
			if ok {
				emit(networkEvent(request, float64(event.Timestamp), 0, true, event.ErrorText, -1))
			}
		},
	)
	go wait()
	var once sync.Once
	return func() { once.Do(cancel) }, nil
}

// readDevtoolsResponseBody 为一次 CDP 正文读取建立独立期限，避免异常请求
// 长期占用 goroutine，并保证调用方最终能够结算 pending request。
func readDevtoolsResponseBody(ctx context.Context, page interface {
	proto.Client
	proto.Sessionable
}, requestID proto.NetworkRequestID, timeout time.Duration) (string, bool, error) {
	bodyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := devtoolsBodyClient{Client: page, ctx: bodyCtx, sessionID: page.GetSessionID()}
	body, err := (proto.NetworkGetResponseBody{RequestID: requestID}).Call(client)
	if err != nil || body == nil {
		return "", false, err
	}
	if body.Base64Encoded {
		return "", false, nil
	}
	value, truncated := limitBody(body.Body)
	return value, truncated, nil
}

type devtoolsBodyClient struct {
	proto.Client
	ctx       context.Context
	sessionID proto.TargetSessionID
}

func (client devtoolsBodyClient) GetContext() context.Context         { return client.ctx }
func (client devtoolsBodyClient) GetSessionID() proto.TargetSessionID { return client.sessionID }

func consoleLevel(value proto.RuntimeConsoleAPICalledType) string {
	switch value {
	case proto.RuntimeConsoleAPICalledTypeError, proto.RuntimeConsoleAPICalledTypeAssert:
		return "error"
	case proto.RuntimeConsoleAPICalledTypeWarning:
		return "warning"
	case proto.RuntimeConsoleAPICalledTypeInfo:
		return "info"
	case proto.RuntimeConsoleAPICalledTypeDebug:
		return "debug"
	default:
		return "log"
	}
}

func consoleText(values []*proto.RuntimeRemoteObject) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		text := value.Value.Str()
		if value.Description != "" && (text == "<nil>" || text == "") {
			text = value.Description
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " ")
}

func stackLocation(value *proto.RuntimeStackTrace) (string, int, int, string) {
	if value == nil {
		return "", 0, 0, ""
	}
	lines := make([]string, 0, len(value.CallFrames))
	var sourceURL string
	var line, column int
	for _, frame := range value.CallFrames {
		if frame == nil {
			continue
		}
		if sourceURL == "" {
			sourceURL, line, column = frame.URL, frame.LineNumber+1, frame.ColumnNumber+1
		}
		name := frame.FunctionName
		if name == "" {
			name = "<anonymous>"
		}
		lines = append(lines, fmt.Sprintf("%s@%s:%d:%d", name, frame.URL, frame.LineNumber+1, frame.ColumnNumber+1))
	}
	return sourceURL, line, column, strings.Join(lines, "\n")
}

func networkEvent(request devtoolsRequest, finished, encoded float64, failed bool, failure string, pendingDelta int) DevtoolsEvent {
	duration := int64(math.Round((finished - request.started) * 1_000))
	if duration < 0 {
		duration = 0
	}
	bytes := int64(math.Round(encoded))
	if bytes < 0 {
		bytes = 0
	}
	return DevtoolsEvent{
		Kind: "network", Method: request.method, ResourceType: request.resourceType, URL: request.url, Status: request.status,
		MIMEType: request.mimeType, DurationMS: duration, EncodedBytes: bytes,
		Failed: failed, Error: failure, RequestBody: request.requestBody, ResponseBody: request.responseBody, BodyAvailable: request.bodyAvailable, BodyTruncated: request.bodyTruncated, At: time.Now(), PendingDelta: pendingDelta,
	}
}

func isTextMIME(value string) bool {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return strings.HasPrefix(value, "text/") || strings.Contains(value, "json") || strings.Contains(value, "javascript") || strings.Contains(value, "xml") || strings.Contains(value, "x-www-form-urlencoded")
}

func limitBody(value string) (string, bool) {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= 16<<10 {
		return value, false
	}
	value = value[:16<<10]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func runtimeTime(value proto.RuntimeTimestamp) time.Time {
	if value <= 0 {
		return time.Now()
	}
	seconds := float64(value) / 1_000
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*float64(time.Second)))
}
