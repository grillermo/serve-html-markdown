package video_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"serve-html-markdown/internal/execx"
	"serve-html-markdown/internal/video"
)

// recorder is a fake execx.Runner: it remembers the call and answers from fn.
type recorder struct {
	argv []string
	opts execx.Options
	runs int
	fn   func(argv []string) (stdout, stderr string, err error)
}

func (r *recorder) run(_ context.Context, argv []string, opts execx.Options) ([]byte, []byte, error) {
	r.argv, r.opts = argv, opts
	r.runs++
	stdout, stderr, err := r.fn(argv)
	return []byte(stdout), []byte(stderr), err
}

func (r *recorder) client() *video.YtdlpClient {
	c := video.NewYtdlpClient("yt-dlp")
	c.Runner = r.run
	return c
}

func ok(stdout string) func([]string) (string, string, error) {
	return func([]string) (string, string, error) { return stdout, "", nil }
}

func failing(stderr string) func([]string) (string, string, error) {
	return func([]string) (string, string, error) { return "", stderr, &execx.ExitError{Code: 1} }
}

// Ruby: YtdlpClientTest "download returns path and parsed title".
func TestYtdlpDownloadReturnsPathAndTitle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vid.mp4")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{fn: ok("TW2WL_FILE:" + path + "\nTW2WL_TITLE:Hello Tweet\n")}
	got, err := r.client().Download(context.Background(), "https://x.com/foo/status/1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != path || got.Title != "Hello Tweet" {
		t.Errorf("got %+v", got)
	}
	if !slicesContains(r.argv, "https://x.com/foo/status/1") {
		t.Errorf("argv %q lacks the url", r.argv)
	}
}

// The command line is Ruby's, argument for argument.
func TestYtdlpDownloadArgv(t *testing.T) {
	dir := t.TempDir()
	r := &recorder{fn: ok("TW2WL_FILE:/v/a.mp4\n")}
	if _, err := r.client().Download(context.Background(), "https://x.com/foo/status/1", dir); err != nil {
		t.Fatal(err)
	}
	want := []string{"yt-dlp", "-f", "best[ext=mp4]/best", "--no-playlist",
		"-o", dir + "/%(id)s.%(ext)s",
		"--print", "after_move:TW2WL_FILE:%(filepath)s",
		"--print", "after_move:TW2WL_TITLE:%(title)s",
		"--newline", "https://x.com/foo/status/1"}
	if !reflect.DeepEqual(r.argv, want) {
		t.Errorf("argv\n got %q\nwant %q", r.argv, want)
	}
	if r.opts.Timeout != video.DefaultDownloadTimeout || r.opts.Dir != dir {
		t.Errorf("opts = %+v", r.opts)
	}
}

// Ruby: "download raises on failure" ("ERROR: unavailable", not ok).
func TestYtdlpDownloadFailure(t *testing.T) {
	r := &recorder{fn: failing("ERROR: unavailable\n")}
	_, err := r.client().Download(context.Background(), "u", t.TempDir())
	var ye *video.YtdlpError
	if !errors.As(err, &ye) {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != "yt-dlp download failed: ERROR: unavailable" {
		t.Errorf("message = %q", err)
	}
}

func TestYtdlpDownloadNoFile(t *testing.T) {
	for _, stdout := range []string{"", "TW2WL_TITLE:Only a title\n", "noise\n", "TW2WL_FILE:\n", "TW2WL_FILE:   \n"} {
		r := &recorder{fn: ok(stdout)}
		_, err := r.client().Download(context.Background(), "u", t.TempDir())
		var ye *video.YtdlpError
		if !errors.As(err, &ye) || err.Error() != "yt-dlp produced no file" {
			t.Errorf("stdout %q: err = %v", stdout, err)
		}
	}
}

// parse_prefixed: the first line with the prefix, stripped; CRLF and spaces go.
func TestYtdlpDownloadParsing(t *testing.T) {
	r := &recorder{fn: ok("[download] noise\r\nTW2WL_FILE:  /v/a b.mp4 \r\nTW2WL_FILE:/v/other.mp4\nTW2WL_TITLE:  Hello: World  \r\nTW2WL_TITLE:second\n")}
	got, err := r.client().Download(context.Background(), "u", t.TempDir())
	if err != nil || got.Path != "/v/a b.mp4" || got.Title != "Hello: World" {
		t.Errorf("got %+v, %v", got, err)
	}
	// Ruby returns a nil title; the job turns "no title" into "Twitter video".
	r = &recorder{fn: ok("TW2WL_FILE:/v/a.mp4\n")}
	got, err = r.client().Download(context.Background(), "u", t.TempDir())
	if err != nil || got.Title != "" {
		t.Errorf("got %+v, %v", got, err)
	}
}

// Ruby: "fetch_auto_subs returns the produced vtt path".
func TestYtdlpFetchAutoSubsReturnsTheVttPath(t *testing.T) {
	dir := t.TempDir()
	vtt := filepath.Join(dir, "video.en.vtt")
	r := &recorder{fn: func(argv []string) (string, string, error) {
		for _, want := range []string{"--write-subs", "--write-auto-subs", "--skip-download"} {
			if !slicesContains(argv, want) {
				t.Errorf("argv %q lacks %s", argv, want)
			}
		}
		return "", "", os.WriteFile(vtt, []byte("WEBVTT"), 0o600)
	}}
	got, err := r.client().FetchAutoSubs(context.Background(), "https://youtube.com/watch?v=abc", dir, "")
	if err != nil || got != vtt {
		t.Errorf("got %q, %v", got, err)
	}
}

// Ruby: "fetch_auto_subs returns nil when no vtt produced".
func TestYtdlpFetchAutoSubsNoVtt(t *testing.T) {
	dir := t.TempDir()
	r := &recorder{fn: ok("")}
	got, err := r.client().FetchAutoSubs(context.Background(), "u", dir, "")
	if err != nil || got != "" {
		t.Errorf("got %q, %v", got, err)
	}
}

// Ruby: "fetch_auto_subs passes a cookies file when given one", and the whole
// command line without it.
func TestYtdlpFetchAutoSubsArgv(t *testing.T) {
	dir := t.TempDir()
	r := &recorder{fn: ok("")}
	if _, err := r.client().FetchAutoSubs(context.Background(), "u", dir, "/c.txt"); err != nil {
		t.Fatal(err)
	}
	i := indexOf(r.argv, "--cookies")
	if i < 0 || r.argv[i+1] != "/c.txt" {
		t.Errorf("argv %q has no --cookies /c.txt", r.argv)
	}
	if !slicesContains(r.argv, "--ignore-no-formats-error") {
		t.Errorf("argv %q lacks --ignore-no-formats-error", r.argv)
	}
	want := []string{"yt-dlp", "--write-subs", "--write-auto-subs", "--sub-langs", "en.*", "--sub-format", "vtt",
		"--skip-download", "--ignore-no-formats-error", "--no-playlist",
		"-o", dir + "/%(id)s.%(ext)s", "--cookies", "/c.txt", "u"}
	if !reflect.DeepEqual(r.argv, want) {
		t.Errorf("argv\n got %q\nwant %q", r.argv, want)
	}
	if r.opts.Timeout != video.DefaultSubsTimeout || r.opts.Dir != dir {
		t.Errorf("opts = %+v", r.opts)
	}

	if _, err := r.client().FetchAutoSubs(context.Background(), "u", dir, ""); err != nil {
		t.Fatal(err)
	}
	want = []string{"yt-dlp", "--write-subs", "--write-auto-subs", "--sub-langs", "en.*", "--sub-format", "vtt",
		"--skip-download", "--ignore-no-formats-error", "--no-playlist",
		"-o", dir + "/%(id)s.%(ext)s", "u"}
	if !reflect.DeepEqual(r.argv, want) {
		t.Errorf("without cookies\n got %q\nwant %q", r.argv, want)
	}
}

// Ruby: "fetch_auto_subs failure carries yt-dlp's ERROR lines".
func TestYtdlpFetchAutoSubsFailureCarriesErrorLines(t *testing.T) {
	r := &recorder{fn: failing("WARNING: noise\nERROR: [youtube] abc: Sign in to confirm you're not a bot\n")}
	_, err := r.client().FetchAutoSubs(context.Background(), "u", t.TempDir(), "")
	var ye *video.YtdlpError
	if !errors.As(err, &ye) {
		t.Fatalf("err = %v", err)
	}
	want := "yt-dlp subs fetch failed: ERROR: [youtube] abc: Sign in to confirm you're not a bot"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err, want)
	}
}

func TestYtdlpFailureMessage(t *testing.T) {
	cases := []struct{ name, stderr, want string }{
		{"several ERROR lines joined", "WARNING: w\nERROR: one\nnoise\nERROR: two\n", "yt-dlp download failed: ERROR: one | ERROR: two"},
		{"ERROR only at line start", "  ERROR: padded\nsee ERROR: inline\n", "yt-dlp download failed: ERROR: padded"},
		{"no ERROR line: last three non-empty", "a\n\nb\n\nc\nd\n\n", "yt-dlp download failed: b | c | d"},
		{"fewer than three lines", "only\n", "yt-dlp download failed: only"},
		{"blank stderr", "\n  \n", "yt-dlp download failed"},
		{"empty stderr", "", "yt-dlp download failed"},
		{"CRLF", "ERROR: x\r\nERROR: y\r\n", "yt-dlp download failed: ERROR: x | ERROR: y"},
		{"lowercase error is not an ERROR line", "error: x\nfoo\n", "yt-dlp download failed: error: x | foo"},
	}
	for _, c := range cases {
		r := &recorder{fn: failing(c.stderr)}
		_, err := r.client().Download(context.Background(), "u", t.TempDir())
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: message = %v, want %q", c.name, err, c.want)
		}
	}
	// A pathological stderr cannot make an unbounded message.
	r := &recorder{fn: failing("ERROR: " + strings.Repeat("x", 100000) + "\n")}
	_, err := r.client().Download(context.Background(), "u", t.TempDir())
	if err == nil || len(err.Error()) > 5000 || !strings.HasPrefix(err.Error(), "yt-dlp download failed: ERROR: xxx") {
		t.Errorf("len = %d", len(err.Error()))
	}
}

// The first *.vtt by name; hidden files, directories and other extensions are not candidates.
func TestYtdlpFetchAutoSubsPicksTheFirstVtt(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.en.vtt", "a.en.vtt", "a.en-US.vtt", ".hidden.vtt", "c.srt", "d.vtt.part", "z.vtt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "0.vtt"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &recorder{fn: ok("")}
	got, err := r.client().FetchAutoSubs(context.Background(), "u", dir, "")
	if want := filepath.Join(dir, "a.en-US.vtt"); err != nil || got != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
}

// A directory name with glob or template characters is neither a glob nor a template.
func TestYtdlpUnusualDirectoryNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "we[ir]d 100%(id)s*")
	r := &recorder{fn: func([]string) (string, string, error) {
		return "", "", os.WriteFile(filepath.Join(dir, "v.en.vtt"), nil, 0o600)
	}}
	got, err := r.client().FetchAutoSubs(context.Background(), "u", dir, "")
	if err != nil || got != filepath.Join(dir, "v.en.vtt") {
		t.Errorf("got %q, %v", got, err)
	}
	// the directory's own % is doubled so yt-dlp does not read it as a template field
	if want := strings.ReplaceAll(dir, "%", "%%") + "/%(id)s.%(ext)s"; !slicesContains(r.argv, want) {
		t.Errorf("argv %q lacks -o %q", r.argv, want)
	}
}

// A URL that begins with "-" would be read by yt-dlp as an option; it is refused
// before anything runs.
func TestYtdlpRefusesOptionLookingURLs(t *testing.T) {
	for _, u := range []string{"--exec=touch /tmp/x", "-o/etc/passwd", "-", "--"} {
		r := &recorder{fn: ok("TW2WL_FILE:/v/a.mp4\n")}
		if _, err := r.client().Download(context.Background(), u, t.TempDir()); err == nil || r.runs != 0 {
			t.Errorf("Download(%q): err = %v, runs = %d", u, err, r.runs)
		}
		if _, err := r.client().FetchAutoSubs(context.Background(), u, t.TempDir(), ""); err == nil || r.runs != 0 {
			t.Errorf("FetchAutoSubs(%q): err = %v, runs = %d", u, err, r.runs)
		}
	}
	r := &recorder{fn: ok("")}
	if _, err := r.client().Download(context.Background(), "", t.TempDir()); err == nil || r.runs != 0 {
		t.Errorf("empty url: err = %v, runs = %d", err, r.runs)
	}
}

// The destination directory is created when missing, private to the server.
func TestYtdlpCreatesTheDestinationPrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "videos")
	r := &recorder{fn: ok("TW2WL_FILE:/v/a.mp4\n")}
	if _, err := r.client().Download(context.Background(), "u", dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v, %v", fi, err)
	}
	// An existing directory keeps its mode.
	existing := t.TempDir()
	if err := os.Chmod(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.client().Download(context.Background(), "u", existing); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(existing); fi.Mode().Perm() != 0o755 {
		t.Errorf("existing dir mode = %v", fi.Mode().Perm())
	}
}

// Failure kinds: a yt-dlp failure, a timeout and a runaway output are
// YtdlpErrors (the captions job counts them as "not ready yet"); a binary that
// cannot start is not, like Ruby's Errno::ENOENT.
func TestYtdlpErrorKinds(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		kind    bool
		message string
	}{
		{"exit", &execx.ExitError{Code: 1}, true, "yt-dlp download failed"},
		{"timeout", fmt.Errorf("yt-dlp: %w", execx.ErrTimedOut), true, "yt-dlp download timed out"},
		{"too large", fmt.Errorf("yt-dlp: %w", execx.ErrOutputTooLarge), true, "yt-dlp download output was too large"},
		{"cancelled", fmt.Errorf("yt-dlp: %w", context.Canceled), false, ""},
		{"cannot start", &os.PathError{Op: "fork/exec", Path: "yt-dlp", Err: syscall.ENOENT}, false, ""},
	} {
		r := &recorder{fn: func([]string) (string, string, error) { return "", "", c.err }}
		_, err := r.client().Download(context.Background(), "u", t.TempDir())
		var ye *video.YtdlpError
		if got := errors.As(err, &ye); got != c.kind {
			t.Errorf("%s: YtdlpError = %v (%v)", c.name, got, err)
		}
		if c.kind && err.Error() != c.message {
			t.Errorf("%s: message = %q, want %q", c.name, err, c.message)
		}
		if !errors.Is(err, c.err) {
			t.Errorf("%s: cause lost: %v", c.name, err)
		}
	}
}

// The whole thing against a real process: a script stands in for yt-dlp.
func TestYtdlpWithARealProcess(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "yt-dlp")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$ARGS_FILE"
env > "$ENV_FILE"
pwd -P > "$PWD_FILE"
case "$*" in
  *--skip-download*) printf WEBVTT > "$DEST/abc.en.vtt"; echo "WARNING: x" >&2; exit 0 ;;
  *FAILME*) echo "ERROR: boom" >&2; echo "ERROR: second" >&2; exit 1 ;;
esac
printf 'TW2WL_FILE:%s/abc.mp4\nTW2WL_TITLE:Real Title\n' "$DEST"
`
	dest := t.TempDir()
	scratch := t.TempDir()
	// The process gets only the allowlisted environment, so the paths are baked in.
	script = strings.NewReplacer("$DEST", dest, "$ARGS_FILE", filepath.Join(scratch, "args"),
		"$ENV_FILE", filepath.Join(scratch, "env"), "$PWD_FILE", filepath.Join(scratch, "pwd")).Replace(script)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_PASSWORD", "sekret-db")
	t.Setenv("SLACK_TOKEN", "sekret-slack")
	c := video.NewYtdlpClient(bin)

	got, err := c.Download(context.Background(), "https://x.com/foo/status/1", dest)
	if err != nil || got.Path != dest+"/abc.mp4" || got.Title != "Real Title" {
		t.Fatalf("got %+v, %v", got, err)
	}
	args, _ := os.ReadFile(filepath.Join(scratch, "args"))
	if !strings.Contains(string(args), "https://x.com/foo/status/1\n") || !strings.Contains(string(args), "--no-playlist\n") {
		t.Errorf("args:\n%s", args)
	}
	env, _ := os.ReadFile(filepath.Join(scratch, "env"))
	if strings.Contains(string(env), "sekret") || !strings.Contains(string(env), "PATH=") {
		t.Errorf("yt-dlp must get PATH but no server secrets:\n%s", env)
	}
	pwd, _ := os.ReadFile(filepath.Join(scratch, "pwd"))
	if want, _ := filepath.EvalSymlinks(dest); strings.TrimSpace(string(pwd)) != want {
		t.Errorf("ran in %q, want %q", pwd, want)
	}

	vtt, err := c.FetchAutoSubs(context.Background(), "https://www.youtube.com/watch?v=abc", dest, "")
	if err != nil || vtt != dest+"/abc.en.vtt" {
		t.Errorf("vtt = %q, %v", vtt, err)
	}

	_, err = c.Download(context.Background(), "FAILME", dest)
	if err == nil || err.Error() != "yt-dlp download failed: ERROR: boom | ERROR: second" {
		t.Errorf("err = %v", err)
	}

	missing := video.NewYtdlpClient(filepath.Join(scratch, "nope"))
	_, err = missing.Download(context.Background(), "u", dest)
	var ye *video.YtdlpError
	if err == nil || errors.As(err, &ye) {
		t.Errorf("a missing binary should be a plain error, got %v", err)
	}
}

// Cancelling the context kills yt-dlp (and anything it started) and returns promptly.
func TestYtdlpContextCancelKillsTheProcess(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := video.NewYtdlpClient(bin).Download(ctx, "u", t.TempDir())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}

func TestNewYtdlpClientDefaults(t *testing.T) {
	c := video.NewYtdlpClient("/opt/homebrew/bin/yt-dlp")
	if c.Bin != "/opt/homebrew/bin/yt-dlp" || c.Runner == nil || c.DownloadTimeout != video.DefaultDownloadTimeout || c.SubsTimeout != video.DefaultSubsTimeout {
		t.Errorf("client = %+v", c)
	}
}

func slicesContains(list []string, s string) bool { return indexOf(list, s) >= 0 }

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
