package session

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// recordLockName is the lock file used to guarantee that only one recording is
// active system-wide, across both the daemon and standalone one-shot processes.
const recordLockName = "linux-voice-input.record.lock"

func recordLockPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = os.TempDir()
	}
	return filepath.Join(runtimeDir, recordLockName)
}

// recordLock is a system-wide advisory lock (flock) held for the duration of a
// recording session. Acquire returns an error if another process already holds
// it, which is how a second concurrent recording is prevented.
type recordLock struct {
	file *os.File
}

// acquireRecordLock tries to take the exclusive, non-blocking recording lock.
// It fails immediately if another recording is already in progress.
func acquireRecordLock() (*recordLock, error) {
	file, err := os.OpenFile(recordLockPath(), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open record lock: %w", err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another recording is already in progress")
	}

	return &recordLock{file: file}, nil
}

// release unlocks and closes the lock file. Safe to call on a nil receiver.
func (l *recordLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
}
