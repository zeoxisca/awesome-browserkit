//go:build browser_smoke

package browserkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRealBrowserDownloadLimitUpdateExistingSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file" {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<!doctype html><link rel="icon" href="data:,"><a href="/file">file</a>`)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="live-limit.bin"`)
		for i := 0; i < 8; i++ {
			if _, err := w.Write(make([]byte, 8192)); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()
	manager, err := NewManager(Config{AllowPrivateNetwork: true, DownloadDir: t.TempDir(), Session: SessionConfig{ExecPath: "/usr/bin/chromium"}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	opened, err := manager.Open(context.Background(), OpenRequest{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := ""
	for index, limit := range []int64{8192, 0} {
		if err := manager.SetDownloadMaxBytes(limit); err != nil {
			t.Fatal(err)
		}
		snapshot, err := manager.Snapshot(context.Background(), SnapshotRequest{SessionID: opened.SessionID, PageID: opened.PageID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Click(context.Background(), ClickRequest{SessionID: opened.SessionID, PageID: opened.PageID, Ref: smokeElementRef(t, snapshot, "link", "file"), Revision: snapshot.Revision}); err != nil {
			t.Fatal(err)
		}
		downloads := awaitSmokeDownload(t, manager, opened.SessionID, index+1)
		id := ""
		for _, download := range downloads {
			if download.ID != previous {
				id = download.ID
				break
			}
		}
		if id == "" {
			t.Fatal("new download missing")
		}
		result, err := manager.Download(context.Background(), DownloadRequest{SessionID: opened.SessionID, Action: "wait", DownloadID: id, TimeoutMS: 5000})
		var browserErr *Error
		if err != nil && (limit == 0 || !errors.As(err, &browserErr) || browserErr.Kind != "download_over_limit") {
			t.Fatal(err)
		}
		if limit > 0 {
			if result.State != DownloadOverLimit || result.Path != "" {
				t.Fatalf("updated limit ignored: %+v", result)
			}
		} else if result.State != DownloadCompleted || result.Bytes != 65536 {
			t.Fatalf("restored unlimited download failed: %+v", result)
		}
		if result.SessionID != opened.SessionID {
			t.Fatal("limit update changed the session")
		}
		previous = id
	}
}
