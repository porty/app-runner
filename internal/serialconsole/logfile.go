package serialconsole

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// logFile appends a VM's serial output to disk so it survives App Runner
// restarts. It is strictly best effort: a file that cannot be opened or
// written is reported once and then ignored, so it never blocks or breaks
// the console itself.
type logFile struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// openLog opens path for appending. A new run (rotate) first moves the
// previous log to path.1, replacing any older one, and starts an empty
// file; resuming an existing run appends. It returns nil when path is empty
// or the file cannot be opened.
func openLog(path string, rotate bool) *logFile {
	if path == "" {
		return nil
	}
	file, err := openLogFile(path, rotate)
	if err != nil {
		slog.Warn("serial console log unavailable; output is still captured in memory", "path", path, "error", err)
		return nil
	}
	return &logFile{path: path, file: file}
}

func openLogFile(path string, rotate bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if rotate {
		if err := os.Rename(path, path+".1"); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("keep previous serial log: %w", err)
		}
		flags |= os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o600)
}

// Write appends contents. After the first failure the file is closed and
// further output is dropped from the log only.
func (l *logFile) Write(contents []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	if _, err := l.file.Write(contents); err != nil {
		slog.Warn("stop writing serial console log; output is still captured in memory", "path", l.path, "error", err)
		_ = l.file.Close()
		l.file = nil
	}
}

func (l *logFile) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}
