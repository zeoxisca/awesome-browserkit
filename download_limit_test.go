package browserkit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func TestManagerDownloadLimitUpdatesExistingSession(t *testing.T) {
	dir := t.TempDir()
	var raw *rodSession
	var configs []SessionConfig
	manager, err := NewManager(Config{AllowPrivateNetwork: true, DownloadDir: dir, Factory: func(_ context.Context, config SessionConfig) (Session, error) {
		configs = append(configs, config)
		raw = &rodSession{downloadDir: dir, downloadTempDir: dir, downloadMaxBytes: config.DownloadMaxBytes, downloadLimit: config.CurrentDownloadMaxBytes, downloadGuids: map[string]*downloadRecord{}}
		return &fakeSession{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	first, err := manager.Open(context.Background(), OpenRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	raw.downloadWillBegin(&proto.BrowserDownloadWillBegin{GUID: "before", SuggestedFilename: "before.bin"})
	if err := manager.SetDownloadMaxBytes(8); err != nil {
		t.Fatal(err)
	}
	raw.downloadWillBegin(&proto.BrowserDownloadWillBegin{GUID: "after", SuggestedFilename: "after.bin"})
	if raw.downloadGuids["before"].maxBytes != 0 || raw.downloadGuids["after"].maxBytes != 8 {
		t.Fatal("download did not freeze its start-time limit")
	}
	if err := os.WriteFile(filepath.Join(dir, "before"), make([]byte, 16), 0600); err != nil {
		t.Fatal(err)
	}
	raw.downloadProgress(&proto.BrowserDownloadProgress{GUID: "before", State: proto.BrowserDownloadProgressStateCompleted, ReceivedBytes: 16})
	if info, _ := raw.DownloadStatus(context.Background(), "before"); info.State != DownloadCompleted {
		t.Fatalf("prior download changed retroactively: %+v", info)
	}
	if err := os.WriteFile(filepath.Join(dir, "after"), make([]byte, 16), 0600); err != nil {
		t.Fatal(err)
	}
	raw.downloadProgress(&proto.BrowserDownloadProgress{GUID: "after", State: proto.BrowserDownloadProgressStateCompleted, ReceivedBytes: 16})
	if info, _ := raw.DownloadStatus(context.Background(), "after"); info.State != DownloadOverLimit || info.Path != "" {
		t.Fatalf("new download ignored updated budget: %+v", info)
	}
	snapshot, err := manager.Snapshot(context.Background(), SnapshotRequest{SessionID: first.SessionID, PageID: first.PageID})
	if err != nil || snapshot.SessionID != first.SessionID {
		t.Fatalf("limit update replaced session: %+v %v", snapshot, err)
	}
	if _, err = manager.Open(context.Background(), OpenRequest{URL: "https://example.com/new"}); err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 || configs[1].DownloadMaxBytes != 8 {
		t.Fatalf("future session got stale config: %+v", configs)
	}
	if err := manager.SetDownloadMaxBytes(-1); err == nil {
		t.Fatal("negative limit accepted")
	}
	if manager.downloadMaxBytes.Load() != 8 {
		t.Fatal("invalid limit changed current budget")
	}
	if err := manager.SetDownloadMaxBytes(0); err != nil {
		t.Fatal(err)
	}
	raw.downloadWillBegin(&proto.BrowserDownloadWillBegin{GUID: "unlimited"})
	if raw.downloadGuids["unlimited"].maxBytes != 0 {
		t.Fatal("zero did not restore unlimited downloads")
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDownloadMaxBytes(4); err == nil {
		t.Fatal("closed manager accepted update")
	}
}

func TestManagerDownloadLimitConcurrentUpdates(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	raw := &rodSession{downloadLimit: manager.downloadMaxBytes.Load, downloadGuids: map[string]*downloadRecord{}}
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		for i := 0; i < 100; i++ {
			if err := manager.SetDownloadMaxBytes(int64(i)); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer wait.Done()
		for i := 0; i < 100; i++ {
			raw.downloadWillBegin(&proto.BrowserDownloadWillBegin{GUID: fmt.Sprint(i)})
		}
	}()
	wait.Wait()
	if len(raw.downloadGuids) != 100 {
		t.Fatal("lost downloads during updates")
	}
}
