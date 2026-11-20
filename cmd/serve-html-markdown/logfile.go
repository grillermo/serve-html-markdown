package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
)

// maxLogBytes is the size past which openLogFile moves the log aside to
// <path>.1 (replacing the previous one) and starts a fresh file.
const maxLogBytes = 50 << 20

// openLogFile opens path for appending, creating its directory, after
// rotating it once if it has grown past maxLogBytes. It also points Go's
// crash output (unrecovered panics, fatal errors) at the file, so a crash
// that kills the process is on disk and not only in the tmux pane.
func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("log file: %w", err)
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, fmt.Errorf("log file: rotate: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("log file: %w", err)
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		f.Close()
		return nil, fmt.Errorf("log file: crash output: %w", err)
	}
	return f, nil
}
