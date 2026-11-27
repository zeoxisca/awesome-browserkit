package browserkit

import "errors"

// SetDownloadMaxBytes updates the limit captured by subsequent downloads,
// including downloads started in existing sessions. Zero permits any size.
// In-progress downloads retain the limit captured when their download event began.
// Custom factories obtain this value through SessionConfig.CurrentDownloadMaxBytes.
func (manager *Manager) SetDownloadMaxBytes(limit int64) error {
	if manager == nil {
		return errors.New("Browser Manager 不存在")
	}
	if limit < 0 {
		return errors.New("Browser DownloadMaxBytes 不能为负数")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.stopped {
		return errors.New("Browser Manager 已关闭")
	}
	manager.downloadMaxBytes.Store(limit)
	return nil
}
