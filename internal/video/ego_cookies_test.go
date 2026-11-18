package video_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"serve-html-markdown/internal/execx"
	"serve-html-markdown/internal/video"
)

const egoPassword = "pw"

// egoRows is a fake ego database: what the Keychain says, the meta version and
// the rows the cookies query would print. It records every command it was asked to run.
type egoFake struct {
	password string
	version  string
	rows     [][]string
	calls    [][]string
	dbSeen   []string // sqlite3's db argument, as it was when asked
}

func (f *egoFake) run(_ context.Context, argv []string, opts execx.Options) ([]byte, []byte, error) {
	f.calls = append(f.calls, argv)
	if opts.Timeout != 10*time.Second {
		return nil, nil, fmt.Errorf("unexpected timeout %v", opts.Timeout)
	}
	if strings.HasSuffix(argv[0], "security") {
		return []byte(f.password + "\n"), nil, nil
	}
	f.dbSeen = append(f.dbSeen, argv[len(argv)-2])
	if strings.Contains(argv[len(argv)-1], "FROM meta") {
		return []byte(f.version + "\n"), nil, nil
	}
	var lines []string
	for _, r := range f.rows {
		lines = append(lines, strings.Join(r, "\t"))
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil, nil
}

func newFake(rows [][]string) *egoFake {
	return &egoFake{password: egoPassword, version: "24", rows: rows}
}

func (f *egoFake) exporter(db string) *video.EgoCookies {
	return &video.EgoCookies{DBPath: db, Runner: f.run}
}

func egoKey(t *testing.T, password string) []byte {
	t.Helper()
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// encryptCookie is the Ruby test's encrypt helper: "v10" + AES-128-CBC of
// (32 bytes of host hash) + value.
func encryptCookie(t *testing.T, password string, hostHash bool, value string) []byte {
	t.Helper()
	plain := []byte(value)
	if hostHash {
		plain = append(bytes.Repeat([]byte("h"), 32), plain...)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(egoKey(t, password))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{' '}, aes.BlockSize)).CryptBlocks(out, plain)
	return append([]byte("v10"), out...)
}

func hexUpper(b []byte) string { return strings.ToUpper(hex.EncodeToString(b)) }

func emptyDB(t *testing.T) (dir, db string) {
	t.Helper()
	dir = t.TempDir()
	db = filepath.Join(dir, "Cookies")
	if err := os.WriteFile(db, []byte("not really sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, db
}

// Ruby: EgoCookiesTest "decrypts cookies into a netscape file, dropping the host hash".
func TestEgoCookiesExportsANetscapeFile(t *testing.T) {
	rows := [][]string{
		{".youtube.com", "SID", "/", "1", "13400000000000000", "", hexUpper(encryptCookie(t, egoPassword, true, "abc"))},
		{"youtube.com", "PREF", "/", "0", "0", "", hexUpper(encryptCookie(t, egoPassword, true, "f6=1"))},
	}
	dir, db := emptyDB(t)
	dest := filepath.Join(dir, "cookies.txt")
	got, err := newFake(rows).exporter(db).Export(context.Background(), dest)
	if err != nil || got != dest {
		t.Fatalf("Export = %q, %v", got, err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if lines[0] != "# Netscape HTTP Cookie File" {
		t.Errorf("first line %q", lines[0])
	}
	wantExpiry := strconv.Itoa(13400000000000000/1_000_000 - 11_644_473_600)
	if want := []string{".youtube.com", "TRUE", "/", "TRUE", wantExpiry, "SID", "abc"}; fmt.Sprint(strings.Split(lines[1], "\t")) != fmt.Sprint(want) {
		t.Errorf("line 1 = %q", lines[1])
	}
	if want := []string{"youtube.com", "FALSE", "/", "FALSE", "0", "PREF", "f6=1"}; fmt.Sprint(strings.Split(lines[2], "\t")) != fmt.Sprint(want) {
		t.Errorf("line 2 = %q", lines[2])
	}
	if len(lines) != 3 || !strings.HasSuffix(string(raw), "\n") {
		t.Errorf("file = %q", raw)
	}
}

// Ruby: "raises when the keychain item can't be read".
func TestEgoCookiesKeychainFailure(t *testing.T) {
	dir, db := emptyDB(t)
	failing := func(_ context.Context, argv []string, _ execx.Options) ([]byte, []byte, error) {
		if strings.HasSuffix(argv[0], "security") {
			return []byte("sekret-leak\n"), []byte("sekret-stderr"), &execx.ExitError{Code: 44}
		}
		return []byte("24\n"), nil, nil
	}
	dest := filepath.Join(dir, "cookies.txt")
	_, err := (&video.EgoCookies{DBPath: db, Runner: failing}).Export(context.Background(), dest)
	var ee *video.EgoCookiesError
	if !errors.As(err, &ee) || err.Error() != `could not read the "ego safe storage" Keychain item` {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "sekret") || strings.Contains(fmt.Sprintf("%+v %#v", err, err), "sekret") {
		t.Errorf("the error leaks command output: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("no file should be written, stat: %v", err)
	}
}

// Ruby: "raises when the database is missing".
func TestEgoCookiesDatabaseMissing(t *testing.T) {
	for _, db := range []string{"/nope/Cookies", ""} {
		_, err := (&video.EgoCookies{DBPath: db, Runner: newFake(nil).run}).Export(context.Background(), filepath.Join(t.TempDir(), "x"))
		var ee *video.EgoCookiesError
		if !errors.As(err, &ee) || err.Error() != "ego lite cookie database not found" {
			t.Errorf("db %q: err = %v", db, err)
		}
	}
	// Nothing is run before the database is known to exist.
	f := newFake(nil)
	_, _ = f.exporter("/nope/Cookies").Export(context.Background(), "/tmp/x")
	if len(f.calls) != 0 {
		t.Errorf("ran %v", f.calls)
	}
}

// The commands, exactly as Ruby runs them, against a copy of the database.
func TestEgoCookiesCommands(t *testing.T) {
	dir, db := emptyDB(t)
	f := newFake([][]string{{".youtube.com", "SID", "/", "1", "0", "", hexUpper(encryptCookie(t, egoPassword, true, "abc"))}})
	if _, err := f.exporter(db).Export(context.Background(), filepath.Join(dir, "cookies.txt")); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("calls = %q", f.calls)
	}
	if got, want := fmt.Sprint(f.calls[0]), fmt.Sprint([]string{"/usr/bin/security", "find-generic-password", "-w", "-s", "ego safe storage"}); got != want {
		t.Errorf("security argv = %q", f.calls[0])
	}
	copyPath := f.dbSeen[0]
	if copyPath == db || f.dbSeen[1] != copyPath {
		t.Errorf("sqlite3 must read a copy of the database, got %q (original %q)", f.dbSeen, db)
	}
	meta := []string{"/usr/bin/sqlite3", "-readonly", "-separator", "\t", copyPath, "SELECT value FROM meta WHERE key = 'version'"}
	if fmt.Sprint(f.calls[1]) != fmt.Sprint(meta) {
		t.Errorf("meta argv = %q", f.calls[1])
	}
	wantSQL := "SELECT host_key, name, path, is_secure, expires_utc, top_frame_site_key, hex(encrypted_value) " +
		"FROM cookies WHERE host_key IN ('.youtube.com','youtube.com','.google.com','google.com') AND top_frame_site_key IN ('','https://youtube.com') " +
		"ORDER BY top_frame_site_key"
	cookies := []string{"/usr/bin/sqlite3", "-readonly", "-separator", "\t", copyPath, wantSQL}
	if fmt.Sprint(f.calls[2]) != fmt.Sprint(cookies) {
		t.Errorf("cookies argv = %q", f.calls[2])
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Errorf("the database copy %s was not removed (%v)", copyPath, err)
	}
}

// The copy lives in a private directory and the original is untouched.
func TestEgoCookiesCopyIsPrivate(t *testing.T) {
	dir, db := emptyDB(t)
	var mode os.FileMode
	var dirMode os.FileMode
	var content []byte
	f := newFake([][]string{{".youtube.com", "SID", "/", "1", "0", "", hexUpper(encryptCookie(t, egoPassword, true, "abc"))}})
	exp := &video.EgoCookies{DBPath: db, Runner: func(ctx context.Context, argv []string, o execx.Options) ([]byte, []byte, error) {
		if strings.HasSuffix(argv[0], "sqlite3") && mode == 0 {
			copyPath := argv[len(argv)-2]
			fi, err := os.Stat(copyPath)
			if err != nil {
				t.Fatal(err)
			}
			di, _ := os.Stat(filepath.Dir(copyPath))
			mode, dirMode = fi.Mode().Perm(), di.Mode().Perm()
			content, _ = os.ReadFile(copyPath)
		}
		return f.run(ctx, argv, o)
	}}
	if _, err := exp.Export(context.Background(), filepath.Join(dir, "cookies.txt")); err != nil {
		t.Fatal(err)
	}
	if mode != 0o600 || dirMode != 0o700 || string(content) != "not really sqlite" {
		t.Errorf("copy mode %v in a %v directory, content %q", mode, dirMode, content)
	}
	if raw, _ := os.ReadFile(db); string(raw) != "not really sqlite" {
		t.Error("the original database changed")
	}
}

// The temp copy is removed on every path, errors included.
func TestEgoCookiesCleansUpOnEveryPath(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	dir, db := emptyDB(t)
	dest := filepath.Join(dir, "cookies.txt")
	good := hexUpper(encryptCookie(t, egoPassword, true, "abc"))
	for name, f := range map[string]*egoFake{
		"ok":         newFake([][]string{{".youtube.com", "SID", "/", "1", "0", "", good}}),
		"no cookies": newFake(nil),
		"bad key":    {password: "other", version: "24", rows: [][]string{{".youtube.com", "SID", "/", "1", "0", "", good}}},
	} {
		_, _ = f.exporter(db).Export(context.Background(), dest)
		if left, _ := os.ReadDir(scratch); len(left) != 0 {
			t.Errorf("%s: left %d entries in the temp dir", name, len(left))
		}
	}
	failSQLite := &video.EgoCookies{DBPath: db, Runner: func(_ context.Context, argv []string, _ execx.Options) ([]byte, []byte, error) {
		if strings.HasSuffix(argv[0], "security") {
			return []byte("pw\n"), nil, nil
		}
		return nil, nil, &execx.ExitError{Code: 1}
	}}
	_, err := failSQLite.Export(context.Background(), dest)
	if err == nil || err.Error() != "could not read the ego lite cookie database" {
		t.Errorf("sqlite failure: %v", err)
	}
	if left, _ := os.ReadDir(scratch); len(left) != 0 {
		t.Errorf("sqlite failure left %d entries in the temp dir", len(left))
	}
}

func TestEgoCookiesNoCookiesIsAnErrorAndWritesNothing(t *testing.T) {
	dir, db := emptyDB(t)
	dest := filepath.Join(dir, "cookies.txt")
	// every blob is skipped: not v10, empty, wrong key
	f := newFake([][]string{
		{".youtube.com", "A", "/", "1", "0", "", hexUpper([]byte("v11" + strings.Repeat("x", 16)))},
		{".youtube.com", "B", "/", "1", "0", "", ""},
		{".youtube.com", "C", "/", "1", "0", "", hexUpper(encryptCookie(t, "wrong", true, "x"))},
	})
	_, err := f.exporter(db).Export(context.Background(), dest)
	if err == nil || err.Error() != "ego lite has no YouTube cookies" {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("stat: %v", err)
	}
	if _, err := newFake(nil).exporter(db).Export(context.Background(), dest); err == nil || err.Error() != "ego lite has no YouTube cookies" {
		t.Errorf("no rows: %v", err)
	}
}

func TestEgoCookiesFileMode(t *testing.T) {
	dir, db := emptyDB(t)
	dest := filepath.Join(dir, "cookies.txt")
	f := newFake([][]string{{".youtube.com", "SID", "/", "1", "0", "", hexUpper(encryptCookie(t, egoPassword, true, "abc"))}})
	if _, err := f.exporter(db).Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	// An older, world-readable file is tightened, and replaced, not appended to.
	if err := os.WriteFile(dest, []byte("old old old old old old old old old old old old old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exporter(db).Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(dest)
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o600 || strings.Contains(string(raw), "old") {
		t.Errorf("mode = %v, content %q", fi.Mode().Perm(), raw)
	}
	// A symbolic link is not followed.
	link := filepath.Join(dir, "link.txt")
	target := filepath.Join(dir, "target.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := f.exporter(db).Export(context.Background(), link); err == nil {
		t.Error("exporting through a symlink should fail")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("the symlink target was written (%v)", err)
	}
}

func TestEgoCookiesCommandFailures(t *testing.T) {
	dir, db := emptyDB(t)
	dest := filepath.Join(dir, "cookies.txt")
	run := func(err error) error {
		exp := &video.EgoCookies{DBPath: db, Runner: func(context.Context, []string, execx.Options) ([]byte, []byte, error) {
			return nil, nil, err
		}}
		_, got := exp.Export(context.Background(), dest)
		return got
	}
	timeout := fmt.Errorf("/usr/bin/security: %w", execx.ErrTimedOut)
	got := run(timeout)
	if got == nil || got.Error() != "/usr/bin/security timed out (Keychain prompt waiting for approval?)" || !errors.Is(got, execx.ErrTimedOut) {
		t.Errorf("timeout: %v", got)
	}
	var ee *video.EgoCookiesError
	// the binary is missing, or the context is cancelled: still the clean error type
	for _, cause := range []error{&os.PathError{Op: "fork/exec", Path: "/usr/bin/security", Err: os.ErrNotExist}, context.Canceled} {
		got = run(cause)
		if !errors.As(got, &ee) || !errors.Is(got, cause) {
			t.Errorf("cause %v: err = %v", cause, got)
		}
	}
}

// Without a runner the real commands are macOS-only; elsewhere it is a clean error.
func TestEgoCookiesNeedsMacOSForTheRealCommands(t *testing.T) {
	dir, db := emptyDB(t)
	exp := &video.EgoCookies{DBPath: db}
	video.SetEgoGOOS(exp, "linux")
	_, err := exp.Export(context.Background(), filepath.Join(dir, "cookies.txt"))
	var ee *video.EgoCookiesError
	if !errors.As(err, &ee) || err.Error() != "ego lite cookies can only be read on macOS" {
		t.Errorf("err = %v", err)
	}
}

func TestNewEgoCookiesPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, "Library", "Application Support", "Citro Labs", "ego lite", "Default", "Cookies")
	if got := video.NewEgoCookies().DBPath; got != want {
		t.Errorf("DBPath = %q, want %q", got, want)
	}
}

// Byte-for-byte against Ruby's own output (script/golden_ego_cookies.rb): its
// OpenSSL decryption and Netscape writer, over blobs it encrypted.
func TestEgoCookiesGoldenRuby(t *testing.T) {
	data, err := os.ReadFile("../../testdata/golden/video/ego_cookies.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string     `json:"name"`
		Password string     `json:"password"`
		Version  any        `json:"version"`
		Rows     [][]string `json:"rows"`
		Output   *string    `json:"output"`
		Error    *string    `json:"error"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("only %d golden cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			dir, db := emptyDB(t)
			f := &egoFake{password: c.Password, version: fmt.Sprint(c.Version), rows: c.Rows}
			dest := filepath.Join(dir, "cookies.txt")
			_, err := f.exporter(db).Export(context.Background(), dest)
			if c.Error != nil {
				if err == nil || err.Error() != *c.Error {
					t.Fatalf("err = %v, Ruby: %q", err, *c.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			want, _ := hex.DecodeString(*c.Output)
			got, _ := os.ReadFile(dest)
			if !bytes.Equal(got, want) {
				t.Errorf("file differs from Ruby's\n got %q\nwant %q", got, want)
			}
		})
	}
}

// End to end with the real sqlite3 and a real SQLite database: the SQL, the
// separator, hex() and the partition/host filters, with only the Keychain faked.
func TestEgoCookiesWithRealSQLite(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not installed: cannot build the fixture database")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "Cookies")
	blob := func(v string) string { return "x'" + hexUpper(encryptCookie(t, egoPassword, true, v)) + "'" }
	script := `
CREATE TABLE meta (key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR);
INSERT INTO meta VALUES ('version', '24');
CREATE TABLE cookies (creation_utc INTEGER, host_key TEXT, top_frame_site_key TEXT, name TEXT, value TEXT, encrypted_value BLOB, path TEXT, expires_utc INTEGER, is_secure INTEGER);
INSERT INTO cookies (host_key, top_frame_site_key, name, encrypted_value, path, expires_utc, is_secure) VALUES
 ('.youtube.com', 'https://youtube.com', 'SID', ` + blob("partitioned") + `, '/', 13400000000000000, 1),
 ('.youtube.com', '', 'SID', ` + blob("plain") + `, '/', 13400000000000000, 1),
 ('youtube.com', '', 'PREF', ` + blob("f6=1") + `, '/', 0, 0),
 ('.google.com', '', 'NID', ` + blob("ñandú") + `, '/', 13300000000000000, 1),
 ('google.com', '', 'EMPTY', ` + blob("") + `, '/x', 13300000000000000, 0),
 ('.youtube.com', 'https://evil.example', 'EVIL', ` + blob("evil") + `, '/', 0, 1),
 ('.example.com', '', 'OTHER', ` + blob("other") + `, '/', 0, 1),
 ('.youtube.com', '', 'OLD', x'7631307a7a7a', '/', 0, 1),
 ('.youtube.com', '', 'PLAINTEXT', x'', '/', 0, 1);
`
	if out, err := exec.Command(sqlite, db, script).CombinedOutput(); err != nil {
		t.Fatalf("building the fixture: %v\n%s", err, out)
	}
	real := func(ctx context.Context, argv []string, opts execx.Options) ([]byte, []byte, error) {
		if strings.HasSuffix(argv[0], "security") {
			return []byte(egoPassword + "\n"), nil, nil
		}
		argv = append([]string{sqlite}, argv[1:]...)
		return execx.Run(ctx, argv, opts)
	}
	dest := filepath.Join(dir, "cookies.txt")
	if _, err := (&video.EgoCookies{DBPath: db, Runner: real}).Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(dest)
	want := "# Netscape HTTP Cookie File\n" +
		".youtube.com\tTRUE\t/\tTRUE\t" + strconv.Itoa(13400000000000000/1_000_000-11_644_473_600) + "\tSID\tplain\n" +
		"youtube.com\tFALSE\t/\tFALSE\t0\tPREF\tf6=1\n" +
		".google.com\tTRUE\t/\tTRUE\t" + strconv.Itoa(13300000000000000/1_000_000-11_644_473_600) + "\tNID\tñandú\n" +
		"google.com\tFALSE\t/x\tFALSE\t" + strconv.Itoa(13300000000000000/1_000_000-11_644_473_600) + "\tEMPTY\t\n"
	// SQLite does not promise an order within a partition key, so compare as sets of lines.
	if !sameLines(string(raw), want) {
		t.Errorf("file:\n%s\nwant:\n%s", raw, want)
	}
	if !strings.HasPrefix(string(raw), "# Netscape HTTP Cookie File\n") || strings.Contains(string(raw), "partitioned") {
		t.Errorf("file:\n%s", raw)
	}
	// The browser's database was only read.
	if out, err := exec.Command(sqlite, "-readonly", db, "SELECT count(*) FROM cookies").Output(); err != nil || strings.TrimSpace(string(out)) != "9" {
		t.Errorf("fixture changed: %q %v", out, err)
	}
}

func sameLines(a, b string) bool {
	count := map[string]int{}
	for l := range strings.SplitSeq(a, "\n") {
		count[l]++
	}
	for l := range strings.SplitSeq(b, "\n") {
		count[l]--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true
}
