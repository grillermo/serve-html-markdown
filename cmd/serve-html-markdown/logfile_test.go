package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWritesLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log", "production.log")
	t.Setenv("LOG_FILE", path)

	if got := run([]string{"bogus"}); got != 2 {
		t.Fatalf("exit %d, want 2", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "unknown command") {
		t.Errorf("log file %q missing the command's stderr", data)
	}
}

func TestOpenLogFileRotatesLargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "production.log")
	if err := os.WriteFile(path, make([]byte, maxLogBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := openLogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Errorf("new log: %v, err %v; want an empty file", info, err)
	}
	if info, err := os.Stat(path + ".1"); err != nil || info.Size() != maxLogBytes+1 {
		t.Errorf("rotated log: %v, err %v; want the old file", info, err)
	}
}
