package browserkit

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const downloadCleanupTimeout = 2 * time.Second

const (
	DownloadInProgress    = "in_progress"
	DownloadCompleted     = "completed"
	DownloadFailed        = "failed"
	DownloadCanceled      = "canceled"
	DownloadOverLimit     = "over_limit"
	DownloadSessionClosed = "session_closed"
)

type downloadRecord struct {
	info          DownloadInfo
	begin         *proto.BrowserDownloadWillBegin
	done          chan struct{}
	stopReason    string
	cancelPending bool
	maxBytes      int64
}

func (session *rodSession) startDownloadWatcher() {
	wait := session.browser.EachEvent(
		func(event *proto.BrowserDownloadWillBegin) { session.downloadWillBegin(event) },
		func(event *proto.BrowserDownloadProgress) { session.downloadProgress(event) },
	)
	go wait()
}

func (session *rodSession) downloadWillBegin(event *proto.BrowserDownloadWillBegin) {
	if event == nil || event.GUID == "" {
		return
	}
	session.downloadMu.Lock()
	if session.downloadGuids[event.GUID] != nil || session.closed.Load() {
		session.downloadMu.Unlock()
		return
	}
	maxBytes := session.downloadMaxBytes
	if session.downloadLimit != nil {
		maxBytes = session.downloadLimit()
	}
	record := &downloadRecord{
		info:  DownloadInfo{ID: event.GUID, State: DownloadInProgress, FrameID: string(event.FrameID), URL: event.URL},
		begin: event, done: make(chan struct{}), maxBytes: maxBytes,
	}
	session.downloadGuids[event.GUID] = record
	session.downloadMu.Unlock()
	// 下载事件只给 frame ID。只按真实 frame tree 匹配，不把未知来源猜成当前 Tab。
	if session.browser != nil {
		go session.resolveDownloadTarget(event.GUID, string(event.FrameID))
	}
}

func (session *rodSession) resolveDownloadTarget(id, frameID string) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadCleanupTimeout)
	defer cancel()
	session.pagesMu.Lock()
	pages := make([]*rodPage, 0, len(session.pages))
	for _, page := range session.pages {
		pages = append(pages, page)
	}
	session.pagesMu.Unlock()
	targetID := ""
	for _, page := range pages {
		frames, err := pageFrameIDs(ctx, page)
		if err == nil && frames[frameID] {
			targetID = string(page.page.TargetID)
			break
		}
	}
	if targetID == "" {
		// 新弹出的下载 Tab 可能还没有 Manager Page 绑定。
		targets, err := (proto.TargetGetTargets{}).Call(session.browser.Context(ctx))
		if err == nil {
			for _, target := range targets.TargetInfos {
				if string(target.TargetID) == frameID && target.Type == proto.TargetTargetInfoTypePage {
					targetID = frameID
					break
				}
			}
		}
	}
	if targetID != "" {
		session.downloadMu.Lock()
		if record := session.downloadGuids[id]; record != nil {
			record.info.TargetID = targetID
		}
		session.downloadMu.Unlock()
	}
}

func (session *rodSession) downloadProgress(event *proto.BrowserDownloadProgress) {
	if event == nil {
		return
	}
	session.downloadMu.Lock()
	defer session.downloadMu.Unlock()
	record := session.downloadGuids[event.GUID]
	if record == nil || record.info.State != DownloadInProgress {
		return
	}
	// Chromium 的 canceled 事件可能把计数归零，保留已经观测的前缀。
	if received := int64(event.ReceivedBytes); received > record.info.Bytes {
		record.info.Bytes = received
	}
	if total := int64(event.TotalBytes); total > record.info.TotalBytes {
		record.info.TotalBytes = total
	}
	if record.maxBytes > 0 && record.info.Bytes > record.maxBytes && !record.cancelPending {
		record.stopReason = DownloadOverLimit
		record.cancelPending = true
		go session.stopDownload(event.GUID, DownloadOverLimit)
	}
	switch event.State {
	case proto.BrowserDownloadProgressStateCompleted:
		if record.stopReason == DownloadOverLimit {
			session.finishDownloadLocked(record, DownloadOverLimit, "下载超过 DownloadMaxBytes")
			session.removePartialDownload(event.GUID)
			return
		}
		info, err := finalizeDownloadTo(session.downloadTempDir, session.downloadDir, record.begin, record.maxBytes)
		if err != nil {
			state := DownloadFailed
			if errors.Is(err, errDownloadOverLimit) {
				state = DownloadOverLimit
			}
			session.finishDownloadLocked(record, state, err.Error())
			session.removePartialDownload(event.GUID)
			return
		}
		record.info.Filename, record.info.Path, record.info.Bytes = info.Filename, info.Path, info.Bytes
		session.finishDownloadLocked(record, DownloadCompleted, "")
	case proto.BrowserDownloadProgressStateCanceled:
		state, message := record.stopReason, ""
		if state == "" {
			state, message = DownloadFailed, "Chromium 终止了下载；CDP 未提供失败原因"
		}
		if state == DownloadOverLimit {
			message = "下载超过 DownloadMaxBytes"
		}
		session.finishDownloadLocked(record, state, message)
		session.removePartialDownload(event.GUID)
	}
}

var errDownloadOverLimit = errors.New("下载超过 DownloadMaxBytes")

func (session *rodSession) finishDownloadLocked(record *downloadRecord, state, message string) {
	if record.info.State != DownloadInProgress {
		return
	}
	record.info.State, record.info.Error = state, message
	close(record.done)
}

func (session *rodSession) removePartialDownload(id string) {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\`) {
		return
	}
	_ = os.Remove(filepath.Join(session.downloadTempDir, id))
	_ = os.Remove(filepath.Join(session.downloadTempDir, id+".crdownload"))
}

func (session *rodSession) stopDownload(id, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), downloadCleanupTimeout)
	defer cancel()
	var err error
	if session.browser == nil {
		err = errors.New("浏览器不可用")
	} else {
		err = (proto.BrowserCancelDownload{GUID: id}).Call(session.browser.Context(ctx))
	}
	if err != nil {
		session.downloadMu.Lock()
		if record := session.downloadGuids[id]; record != nil && record.info.State == DownloadInProgress {
			record.cancelPending = false
			record.info.Error = "取消下载失败: " + err.Error()
			if reason == DownloadCanceled && record.stopReason == DownloadCanceled {
				record.stopReason = ""
			}
		}
		session.downloadMu.Unlock()
	}
	return err
}

func (session *rodSession) Downloads(ctx context.Context) ([]DownloadInfo, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	session.downloadMu.Lock()
	defer session.downloadMu.Unlock()
	if session.downloadGuids == nil {
		return nil, newError("download_blocked", "当前 session 未启用下载", "配置 DownloadDir", true)
	}
	result := make([]DownloadInfo, 0, len(session.downloadGuids))
	for _, record := range session.downloadGuids {
		result = append(result, record.info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (session *rodSession) DownloadStatus(ctx context.Context, id string) (DownloadInfo, error) {
	if err := contextErr(ctx); err != nil {
		return DownloadInfo{}, err
	}
	session.downloadMu.Lock()
	defer session.downloadMu.Unlock()
	record := session.downloadGuids[id]
	if record == nil {
		return DownloadInfo{}, newError("download_not_found", "当前 session 没有此 download_id", "查询下载列表", true)
	}
	return record.info, nil
}

func (session *rodSession) WaitDownload(ctx context.Context, id string) (DownloadInfo, error) {
	operationCtx, cancel, err := session.ctx(ctx)
	if err != nil {
		info, _ := session.DownloadStatus(context.Background(), id)
		return info, err
	}
	defer cancel()
	session.downloadMu.Lock()
	record := session.downloadGuids[id]
	if record == nil {
		session.downloadMu.Unlock()
		return DownloadInfo{}, newError("download_not_found", "当前 session 没有此 download_id", "查询下载列表", true)
	}
	done := record.done
	session.downloadMu.Unlock()
	select {
	case <-done:
		return session.DownloadStatus(context.WithoutCancel(operationCtx), id)
	case <-operationCtx.Done():
		info, _ := session.DownloadStatus(context.WithoutCancel(operationCtx), id)
		return info, operationCtx.Err()
	}
}

func (session *rodSession) CancelDownload(ctx context.Context, id string) (DownloadInfo, error) {
	if err := contextErr(ctx); err != nil {
		info, _ := session.DownloadStatus(context.Background(), id)
		return info, err
	}
	session.downloadMu.Lock()
	record := session.downloadGuids[id]
	if record == nil {
		session.downloadMu.Unlock()
		return DownloadInfo{}, newError("download_not_found", "当前 session 没有此 download_id", "查询下载列表", true)
	}
	active := record.info.State == DownloadInProgress
	sendCancel := active && !record.cancelPending
	if sendCancel {
		if record.stopReason == "" {
			record.stopReason = DownloadCanceled
		}
		record.cancelPending = true
	}
	session.downloadMu.Unlock()
	if active {
		if sendCancel {
			if err := session.stopDownload(id, DownloadCanceled); err != nil {
				info, _ := session.DownloadStatus(context.WithoutCancel(ctx), id)
				return info, err
			}
		}
		return session.WaitDownload(ctx, id)
	}
	return session.DownloadStatus(ctx, id)
}

// beginClose records the host shutdown before closing tabs can emit cancellation.
func (session *rodSession) beginClose() {
	session.closed.Store(true)
	session.closeDownloads()
}

func (session *rodSession) closeDownloads() {
	session.downloadMu.Lock()
	defer session.downloadMu.Unlock()
	for _, record := range session.downloadGuids {
		session.finishDownloadLocked(record, DownloadSessionClosed, "浏览器 session 已关闭")
	}
}

func finalizeDownload(dir string, begin *proto.BrowserDownloadWillBegin) (DownloadInfo, error) {
	return finalizeDownloadTo(dir, dir, begin, 0)
}

func finalizeDownloadTo(sourceDir, dir string, begin *proto.BrowserDownloadWillBegin, maxBytes int64) (DownloadInfo, error) {
	if begin == nil || begin.GUID == "" {
		return DownloadInfo{}, errors.New("下载事件缺少 GUID")
	}
	if begin.GUID != filepath.Base(begin.GUID) || strings.ContainsAny(begin.GUID, `/\`) {
		return DownloadInfo{}, errors.New("下载 GUID 无效")
	}
	source := filepath.Join(sourceDir, begin.GUID)
	info, err := os.Lstat(source)
	if err != nil {
		return DownloadInfo{}, err
	}
	if !info.Mode().IsRegular() {
		return DownloadInfo{}, errors.New("下载结果不是普通文件")
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return DownloadInfo{}, errDownloadOverLimit
	}
	name := safeDownloadName(begin.SuggestedFilename, begin.GUID)
	path, err := moveDownloadFile(dir, source, name)
	if err != nil {
		return DownloadInfo{}, err
	}
	finalInfo, err := os.Lstat(path)
	if err != nil {
		return DownloadInfo{}, err
	}
	if !finalInfo.Mode().IsRegular() {
		return DownloadInfo{}, errors.New("下载结果不是普通文件")
	}
	return DownloadInfo{Filename: filepath.Base(path), Path: path, Bytes: finalInfo.Size()}, nil
}

// moveDownloadFile 用硬链接原子地占用唯一文件名，避免 Lstat+Rename 的竞态覆盖。
func moveDownloadFile(dir, source, name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for index := 0; index < 10_000; index++ {
		candidateName := name
		if index > 0 {
			candidateName = base + " (" + strconv.Itoa(index) + ")" + ext
		}
		candidate := filepath.Join(dir, candidateName)
		err := os.Link(source, candidate)
		if err == nil {
			_ = os.Remove(source)
			return candidate, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		// 不支持硬链接的文件系统：用 O_EXCL 占位后再 rename，
		// 避免覆盖其他并发下载。
		placeholder, createErr := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if createErr == nil {
			_ = placeholder.Close()
			if renameErr := os.Rename(source, candidate); renameErr != nil {
				_ = os.Remove(candidate)
				return "", renameErr
			}
			return candidate, nil
		}
		if errors.Is(createErr, fs.ErrExist) {
			continue
		}
		return "", createErr
	}
	return "", errors.New("无法生成唯一的下载文件名")
}

func safeDownloadName(name, fallback string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		name = fallback
	}
	const maxBytes = 240
	if len(name) > maxBytes {
		ext := filepath.Ext(name)
		if len(ext) > 40 {
			ext = ""
		}
		base := strings.TrimSuffix(name, ext)
		budget := maxBytes - len(ext)
		for budget > 0 && !utf8.RuneStart(base[budget]) {
			budget--
		}
		name = base[:budget] + ext
	}
	return name
}

func setDownloadBehavior(ctx context.Context, browser *rod.Browser, behavior proto.BrowserSetDownloadBehaviorBehavior, dir string) error {
	return (proto.BrowserSetDownloadBehavior{
		Behavior: behavior, DownloadPath: dir, EventsEnabled: true,
	}).Call(browser.Context(ctx))
}
