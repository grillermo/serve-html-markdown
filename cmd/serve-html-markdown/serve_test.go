package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"serve-html-markdown/internal/config"
	"serve-html-markdown/internal/jobs"
	"serve-html-markdown/internal/store"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.FromEnv()
	cfg.SecretKeyBase = "test-secret-key-base-test-secret-key-base"
	cfg.FilesDir = t.TempDir()
	cfg.VideosDir = t.TempDir()
	cfg.JobConcurrency = 1
	return cfg
}

func TestNewRunnerRegistersEveryJobKind(t *testing.T) {
	pool := useTestDatabase(t)
	r, err := newRunner(testConfig(t), pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{jobs.KindGenerateExpansion, jobs.KindTwitterVideoIngest, jobs.KindTwitterVideoCaptions} {
		if r.Handlers[kind] == nil {
			t.Errorf("no handler for %s", kind)
		}
	}
	if len(r.Handlers) != 3 {
		t.Errorf("%d handlers, want 3", len(r.Handlers))
	}
	for _, kind := range []string{jobs.KindTwitterVideoIngest, jobs.KindTwitterVideoCaptions} {
		if r.Timeouts[kind] <= 0 {
			t.Errorf("no timeout for %s", kind)
		}
	}
	if len(r.Sweepers) < 2 {
		t.Errorf("%d sweepers, want the expansion and video ones", len(r.Sweepers))
	}
}

func startServer(t *testing.T, withJobs bool) (addr string, stop func() error) {
	t.Helper()
	pool := useTestDatabase(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cfg := testConfig(t)
	go func() { done <- runServer(ctx, cfg, pool, ln, nil, withJobs) }()
	return ln.Addr().String(), func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(20 * time.Second):
			t.Fatal("runServer did not return after the context was cancelled")
			return nil
		}
	}
}

func TestRunServerServesAndShutsDown(t *testing.T) {
	for _, withJobs := range []bool{true, false} {
		addr, stop := startServer(t, withJobs)
		req, _ := http.NewRequest(http.MethodHead, "http://"+addr+"/health", nil)
		var resp *http.Response
		var err error
		for range 50 {
			if resp, err = http.DefaultClient.Do(req); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("withJobs=%v: %v", withJobs, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("withJobs=%v: /health = %d", withJobs, resp.StatusCode)
		}
		if err := stop(); err != nil {
			t.Errorf("withJobs=%v: runServer: %v", withJobs, err)
		}
		if _, err := http.Get("http://" + addr + "/health"); err == nil {
			t.Errorf("withJobs=%v: still listening after shutdown", withJobs)
		}
	}
}

func TestServeRejectsBadInvocations(t *testing.T) {
	cases := []struct {
		args []string
		cfg  config.Config
		code int
		err  string
	}{
		{[]string{"--bogus"}, config.Config{SecretKeyBase: "x"}, 2, "flag provided but not defined"},
		{[]string{"extra"}, config.Config{SecretKeyBase: "x"}, 2, "unexpected argument"},
		{nil, config.Config{}, 1, "SECRET_KEY_BASE is required"},
	}
	for _, c := range cases {
		var errb strings.Builder
		if got := serve(c.cfg, c.args, &errb); got != c.code {
			t.Errorf("%v: exit %d, want %d", c.args, got, c.code)
		}
		if !strings.Contains(errb.String(), c.err) {
			t.Errorf("%v: stderr %q missing %q", c.args, errb.String(), c.err)
		}
	}
}

func TestSyncFilesMatchesTheDirectory(t *testing.T) {
	pool := useTestDatabase(t)
	dir := t.TempDir()
	t.Setenv("FILES_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO served_files (name, created_at, updated_at) VALUES ('gone.md', now() - interval '1 day', now() - interval '1 day')`); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCmd("sync-files")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "Synced") {
		t.Errorf("stdout %q", out)
	}
	rows, err := store.ServedFilesByUpdated(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "a.md" {
		t.Errorf("served_files = %+v, want only a.md", rows)
	}
}
