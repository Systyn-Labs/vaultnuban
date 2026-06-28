package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// dailyFileWriter writes to a date-stamped file and rotates at midnight.
// Old files beyond retainDays are pruned on each rotation.
type dailyFileWriter struct {
	mu         sync.Mutex
	dir        string
	retainDays int
	current    *os.File
	date       string // "2006-01-02"
}

func (w *dailyFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if w.current == nil || today != w.date {
		if w.current != nil {
			_ = w.current.Close()
		}
		f, err := os.OpenFile(
			filepath.Join(w.dir, "api-"+today+".log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644,
		)
		if err != nil {
			return 0, fmt.Errorf("logger: open log file: %w", err)
		}
		w.current = f
		w.date = today
		go w.pruneOld() // async; errors are non-fatal
	}
	return w.current.Write(p)
}

func (w *dailyFileWriter) pruneOld() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	type logFile struct {
		name string
		date string
	}
	var logs []logFile
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "api-") && strings.HasSuffix(e.Name(), ".log") {
			date := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "api-"), ".log")
			logs = append(logs, logFile{e.Name(), date})
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].date > logs[j].date })
	for i, lf := range logs {
		if i >= w.retainDays {
			_ = os.Remove(filepath.Join(w.dir, lf.name))
		}
	}
}

// EnableFileLogging starts writing all log output to daily-rotating files in dir.
// Files are named api-YYYY-MM-DD.log and the most recent retainDays files are kept.
// Console output is preserved via a MultiWriter.
func EnableFileLogging(dir string, retainDays int) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("logger: create log dir: %w", err)
	}
	fw := &dailyFileWriter{dir: dir, retainDays: retainDays}
	// Write a test byte to surface any permission errors early.
	if _, err := fw.Write(nil); err != nil {
		return err
	}

	out = io.MultiWriter(os.Stdout, fw)
	errOut = io.MultiWriter(os.Stderr, fw)
	// File output is never a TTY; useColor stays as-is for the console half.
	return nil
}
