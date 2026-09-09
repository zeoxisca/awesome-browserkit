package browserkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestResolveCDPAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/test"}`))
	}))
	defer server.Close()
	got, err := resolveCDPAddress(context.Background(), server.URL)
	if err != nil || got != "ws://127.0.0.1:9222/devtools/browser/test" {
		t.Fatalf("%s %v", got, err)
	}
	for _, address := range []string{"chrome://inspect", "ws://localhost:9222/devtools/page/123", "http://name:secret@localhost:9222"} {
		if _, err := resolveCDPAddress(context.Background(), address); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
}

func TestResolveCDPAddressCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := resolveCDPAddress(ctx, server.URL); err == nil {
		t.Fatal("expected timeout")
	}
}
