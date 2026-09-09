package browserkit

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/cdp"
)

const maxCDPWriteTimeout = 500 * time.Millisecond

type closeableWebSocket interface {
	cdp.WebSocketable
	Close() error
}

type cdpWrite struct {
	data []byte
	done chan error
}

// boundedWebSocket 串行写入 CDP 帧，并为 Rod 没有 context 的 Send 补上硬
// 时限。写入超时说明连接已经无法可靠传输，直接关闭连接可同时唤醒全部
// pending CDP 请求，避免遗留永久阻塞的写 goroutine。
type boundedWebSocket struct {
	socket  closeableWebSocket
	timeout time.Duration
	writes  chan cdpWrite
	done    chan struct{}
	once    sync.Once
}

func startCDPClient(ctx context.Context, address string, header http.Header, operationTimeout time.Duration) (*cdp.Client, *boundedWebSocket, error) {
	socket := &cdp.WebSocket{}
	if err := socket.Connect(ctx, address, header); err != nil {
		return nil, nil, err
	}
	writeTimeout := maxCDPWriteTimeout
	if operationTimeout > 0 && operationTimeout < writeTimeout {
		writeTimeout = operationTimeout
	}
	transport := newBoundedWebSocket(socket, writeTimeout)
	return cdp.New().Start(transport), transport, nil
}

func newBoundedWebSocket(socket closeableWebSocket, timeout time.Duration) *boundedWebSocket {
	if timeout <= 0 {
		timeout = maxCDPWriteTimeout
	}
	transport := &boundedWebSocket{socket: socket, timeout: timeout, writes: make(chan cdpWrite), done: make(chan struct{})}
	go transport.writeLoop()
	return transport
}

func (transport *boundedWebSocket) Send(data []byte) error {
	request := cdpWrite{data: data, done: make(chan error, 1)}
	timer := time.NewTimer(transport.timeout)
	defer timer.Stop()
	select {
	case <-transport.done:
		return errors.New("CDP WebSocket 已关闭")
	case transport.writes <- request:
	case <-timer.C:
		transport.close()
		return errors.New("CDP WebSocket 写入排队超时")
	}
	select {
	case <-transport.done:
		select {
		case err := <-request.done:
			return err
		default:
			return errors.New("CDP WebSocket 已关闭")
		}
	case err := <-request.done:
		return err
	case <-timer.C:
		transport.close()
		return errors.New("CDP WebSocket 写入超时")
	}
}

func (transport *boundedWebSocket) Read() ([]byte, error) {
	data, err := transport.socket.Read()
	if err != nil {
		transport.close()
	}
	return data, err
}

func (transport *boundedWebSocket) Close() error {
	return transport.close()
}

func (transport *boundedWebSocket) close() error {
	var err error
	transport.once.Do(func() {
		close(transport.done)
		err = transport.socket.Close()
	})
	return err
}

func (transport *boundedWebSocket) writeLoop() {
	for {
		select {
		case <-transport.done:
			return
		case request := <-transport.writes:
			err := transport.socket.Send(request.data)
			request.done <- err
			if err != nil {
				transport.close()
				return
			}
		}
	}
}
