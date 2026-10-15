package settings

import (
	"context"
	"sync"
)

// DownloadFunc performs the actual model download. It reports human-readable
// progress by calling emit for each line, and returns an error on failure. It
// is injected by ui so this package stays decoupled from the models package.
// ctx is cancelled if the manager is asked to stop.
type DownloadFunc func(ctx context.Context, emit func(line string)) error

// DownloadManager runs at most one model download at a time and exposes its
// cumulative log for polling. The settings page starts a download and then
// polls status to stream the log; this avoids SSE and keeps the client simple.
type DownloadManager struct {
	run DownloadFunc

	mu      sync.Mutex
	running bool
	done    bool
	errText string
	log     []string
	cancel  context.CancelFunc
}

// NewDownloadManager wraps an injected download function.
func NewDownloadManager(run DownloadFunc) *DownloadManager {
	return &DownloadManager{run: run}
}

// Start launches a download if one is not already running. It resets the log
// for the new run. Calling Start while running is a no-op.
func (m *DownloadManager) Start() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.running = true
	m.done = false
	m.errText = ""
	m.log = []string{"开始下载…"}
	m.cancel = cancel
	run := m.run
	m.mu.Unlock()

	go func() {
		var err error
		if run == nil {
			err = context.Canceled
		} else {
			err = run(ctx, m.emit)
		}
		m.mu.Lock()
		m.running = false
		m.done = true
		m.cancel = nil
		if err != nil {
			m.errText = err.Error()
			m.log = append(m.log, "下载失败: "+err.Error())
		} else {
			m.log = append(m.log, "全部完成 ✓")
		}
		m.mu.Unlock()
	}()
}

// emit appends one log line under the lock.
func (m *DownloadManager) emit(line string) {
	m.mu.Lock()
	m.log = append(m.log, line)
	m.mu.Unlock()
}

// Status returns a snapshot safe to serialize.
func (m *DownloadManager) Status() DownloadStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	logCopy := make([]string, len(m.log))
	copy(logCopy, m.log)
	return DownloadStatus{
		Running: m.running,
		Done:    m.done,
		Error:   m.errText,
		Log:     logCopy,
	}
}
