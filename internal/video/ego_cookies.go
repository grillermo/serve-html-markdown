package video

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"serve-html-markdown/internal/execx"
)

const (
	egoKeychainService = "ego safe storage"
	// chromeEpochOffset is the seconds between Chrome's cookie epoch (1601) and Unix's.
	chromeEpochOffset = 11_644_473_600
	// egoCommandTimeout bounds each of security and sqlite3 (Ruby's KEY_TIMEOUT):
	// the first can sit on a Keychain prompt waiting for approval.
	egoCommandTimeout = 10 * time.Second
	maxEgoOutput      = 16 << 20
)

// egoHosts and egoTopFrameSites are Ruby's HOSTS and TOP_FRAME_SITES. Partitioned
// (CHIPS) copies are keyed by the embedding site; only youtube.com's own count.
var (
	egoHosts         = []string{".youtube.com", "youtube.com", ".google.com", "google.com"}
	egoTopFrameSites = []string{"", "https://youtube.com"}
)

// EgoCookiesError is Ruby's EgoCookies::Error. Its message is fixed text: never
// cookie contents, the Keychain password or the output of the commands. Err is
// the underlying cause, if any (for example a timeout or a cancelled context).
type EgoCookiesError struct {
	Message string
	Err     error
}

func (e *EgoCookiesError) Error() string { return e.Message }
func (e *EgoCookiesError) Unwrap() error { return e.Err }

func egoError(cause error, format string, args ...any) error {
	return &EgoCookiesError{Message: fmt.Sprintf(format, args...), Err: cause}
}

// EgoCookies exports the YouTube/Google cookies of the ego lite browser (a
// Chromium clone) into a Netscape cookies.txt for yt-dlp. yt-dlp cannot read
// ego's cookies itself: on macOS it only knows the Keychain entries of the
// browsers it names ("Chrome Safe Storage", ...). Signed-in cookies keep YouTube
// from answering caption downloads with HTTP 429.
//
// Reading the key needs the "ego safe storage" Keychain item to allow
// /usr/bin/security ("Always Allow" on the first prompt). The database is
// copied to a private temporary directory first (the browser keeps it locked),
// and the Keychain password and the derived key never leave this process:
// they are not logged, not put in an error and are cleared after use.
type EgoCookies struct {
	// DBPath is the browser's Cookies database.
	DBPath string
	// Runner starts /usr/bin/security and /usr/bin/sqlite3; tests replace it.
	// Nil means execx.Run, which is only allowed on macOS.
	Runner execx.Runner

	goos string // runtime.GOOS, replaced in tests
}

// NewEgoCookies returns an exporter for the real ego lite database of the
// current user.
func NewEgoCookies() *EgoCookies {
	home, _ := os.UserHomeDir()
	db := ""
	if home != "" {
		db = filepath.Join(home, "Library", "Application Support", "Citro Labs", "ego lite", "Default", "Cookies")
	}
	return &EgoCookies{DBPath: db}
}

// Export writes the cookies file (mode 0600) to destPath and returns destPath,
// or fails with an *EgoCookiesError. It ports EgoCookies#export; for the same
// database the file is byte for byte Ruby's. Cookies whose value could not be
// decrypted are left out, and so is any whose host, name, path or value holds a
// tab, a line break or a NUL (a Netscape line cannot carry them; Ruby would
// write a corrupt line).
func (c *EgoCookies) Export(ctx context.Context, destPath string) (string, error) {
	if _, err := os.Stat(c.DBPath); c.DBPath == "" || err != nil {
		return "", egoError(nil, "ego lite cookie database not found")
	}
	run := c.Runner
	if run == nil {
		goos := c.goos
		if goos == "" {
			goos = runtime.GOOS
		}
		if goos != "darwin" {
			return "", egoError(nil, "ego lite cookies can only be read on macOS")
		}
		run = execx.Run
	}

	// The browser keeps the database locked while it runs, so read a copy.
	dir, err := os.MkdirTemp("", "ego-cookies-")
	if err != nil {
		return "", egoError(err, "could not read the ego lite cookie database")
	}
	defer os.RemoveAll(dir)
	db := filepath.Join(dir, "Cookies")
	if err := copyPrivate(c.DBPath, db); err != nil {
		return "", egoError(err, "could not read the ego lite cookie database")
	}

	password, err := keychainPassword(ctx, run)
	if err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha1.New, string(password), []byte("saltysalt"), 1003, 16)
	clear(password)
	if err != nil {
		return "", egoError(err, "could not derive the ego lite cookie key")
	}
	defer clear(key)

	version, err := queryDB(ctx, run, db, "SELECT value FROM meta WHERE key = 'version'")
	if err != nil {
		return "", err
	}
	stripHostHash := rubyToI(string(version)) >= 24
	table, err := queryDB(ctx, run, db, cookiesSQL())
	if err != nil {
		return "", err
	}

	var content bytes.Buffer
	content.WriteString("# Netscape HTTP Cookie File\n")
	lines := 0
	for _, row := range dedupeRows(parseRows(string(table))) {
		if line, ok := netscapeLine(row, key, stripHostHash); ok {
			content.Write(line)
			content.WriteByte('\n')
			lines++
		}
	}
	if lines == 0 {
		return "", egoError(nil, "ego lite has no YouTube cookies")
	}
	if err := writePrivate(destPath, content.Bytes()); err != nil {
		return "", egoError(err, "could not write the cookies file")
	}
	return destPath, nil
}

// keychainPassword reads the "ego safe storage" Keychain item. The caller owns
// the returned slice and should clear it.
func keychainPassword(ctx context.Context, run execx.Runner) ([]byte, error) {
	argv := []string{"/usr/bin/security", "find-generic-password", "-w", "-s", egoKeychainService}
	stdout, _, err := run(ctx, argv, execx.Options{Timeout: egoCommandTimeout, MaxStdout: maxEgoOutput})
	if err != nil {
		clear(stdout)
		return nil, egoRunError(err, argv[0], fmt.Sprintf("could not read the %q Keychain item", egoKeychainService))
	}
	// String#chomp: one trailing "\n", "\r\n" or "\r".
	switch {
	case bytes.HasSuffix(stdout, []byte("\r\n")):
		return stdout[:len(stdout)-2], nil
	case bytes.HasSuffix(stdout, []byte("\n")), bytes.HasSuffix(stdout, []byte("\r")):
		return stdout[:len(stdout)-1], nil
	}
	return stdout, nil
}

func queryDB(ctx context.Context, run execx.Runner, db, sql string) ([]byte, error) {
	argv := []string{"/usr/bin/sqlite3", "-readonly", "-separator", "\t", db, sql}
	stdout, _, err := run(ctx, argv, execx.Options{Timeout: egoCommandTimeout, MaxStdout: maxEgoOutput})
	if err != nil {
		return nil, egoRunError(err, argv[0], "could not read the ego lite cookie database")
	}
	return stdout, nil
}

// egoRunError names the failure of one command without any of its output.
func egoRunError(err error, command, message string) error {
	if errors.Is(err, execx.ErrTimedOut) {
		return egoError(err, "%s timed out (Keychain prompt waiting for approval?)", command)
	}
	return egoError(err, "%s", message)
}

// cookiesSQL is Ruby's query: the YouTube and Google cookies, unpartitioned
// ones before partitioned ones.
func cookiesSQL() string {
	quote := func(list []string) string {
		quoted := make([]string, len(list))
		for i, s := range list {
			quoted[i] = "'" + s + "'"
		}
		return strings.Join(quoted, ",")
	}
	return "SELECT host_key, name, path, is_secure, expires_utc, top_frame_site_key, hex(encrypted_value) " +
		"FROM cookies WHERE host_key IN (" + quote(egoHosts) + ") AND top_frame_site_key IN (" + quote(egoTopFrameSites) + ") " +
		"ORDER BY top_frame_site_key"
}

// parseRows splits sqlite3's tab-separated output into rows of 7 fields (host,
// name, path, is_secure, expires_utc, top_frame_site_key, hex(encrypted_value));
// anything else (a field that held a tab or a line break) is dropped.
func parseRows(out string) [][]string {
	var rows [][]string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if fields := strings.Split(line, "\t"); len(fields) == 7 {
			rows = append(rows, fields)
		}
	}
	return rows
}

// dedupeRows keeps the first row of each (host, name, path).
func dedupeRows(rows [][]string) [][]string {
	type id struct{ host, name, path string }
	seen := make(map[id]bool, len(rows))
	var out [][]string
	for _, row := range rows {
		k := id{row[0], row[1], row[2]}
		if !seen[k] {
			seen[k] = true
			out = append(out, row)
		}
	}
	return out
}

// netscapeLine is one cookies.txt line (without its newline), or false when the
// cookie cannot be used.
func netscapeLine(row []string, key []byte, stripHostHash bool) ([]byte, bool) {
	host, name, path, secure, expiresUTC, hexValue := row[0], row[1], row[2], row[3], row[4], row[6]
	blob, err := hex.DecodeString(hexValue)
	if err != nil {
		return nil, false
	}
	value, ok := decryptCookie(blob, key, stripHostHash)
	if !ok {
		return nil, false
	}
	for _, field := range []string{host, name, path} {
		if strings.ContainsAny(field, "\t\n\r\x00") {
			return nil, false
		}
	}
	if bytes.ContainsAny(value, "\t\n\r\x00") {
		return nil, false
	}
	var line []byte
	for i, field := range [][]byte{
		[]byte(host), []byte(netscapeFlag(strings.HasPrefix(host, "."))), []byte(path),
		[]byte(netscapeFlag(secure == "1")), []byte(strconv.FormatInt(chromeExpiry(expiresUTC), 10)), []byte(name), value,
	} {
		if i > 0 {
			line = append(line, '\t')
		}
		line = append(line, field...)
	}
	return line, true
}

func netscapeFlag(b bool) string {
	if b {
		return "TRUE"
	}
	return "FALSE"
}

// chromeExpiry converts Chrome's expires_utc (microseconds since 1601; "0" means
// a session cookie and stays 0) to Unix seconds, like Ruby's
// expires_utc.to_i / 1_000_000 - 11_644_473_600 (integer division rounds down).
func chromeExpiry(expiresUTC string) int64 {
	n := rubyToI(expiresUTC)
	if n == 0 {
		return 0
	}
	q := n / 1_000_000
	if n%1_000_000 < 0 {
		q--
	}
	return q - chromeEpochOffset
}

// decryptCookie decrypts a Chromium "v10" macOS cookie blob: AES-128-CBC with a
// fixed IV of 16 spaces and PKCS#7 padding. With stripHostHash (meta version 24
// and later) the first 32 bytes of the plaintext, a SHA-256 of the host, are
// dropped. It reports false for what Ruby's CipherError (or a nil result)
// skips: another prefix, a length that is no whole number of blocks, bad padding.
func decryptCookie(blob, key []byte, stripHostHash bool) ([]byte, bool) {
	body, ok := bytes.CutPrefix(blob, []byte("v10"))
	if !ok || len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return nil, false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false
	}
	plain := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte{' '}, aes.BlockSize)).CryptBlocks(plain, body)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(plain) {
		return nil, false
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return nil, false
		}
	}
	plain = plain[:len(plain)-pad]
	if stripHostHash {
		if len(plain) < 32 {
			return nil, false
		}
		plain = plain[32:]
	}
	return plain, true
}

// rubyToI is Ruby's String#to_i for a base-10 string: surrounding whitespace is
// skipped, then an optional sign and digits (single underscores between digits
// allowed); anything else ends the number, and no digits mean 0. Values past
// the int64 range are clamped.
func rubyToI(s string) int64 {
	s = rubyStrip(s)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	var n int64
	prevDigit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			d := int64(c - '0')
			if n > (1<<63-1-d)/10 {
				n = 1<<63 - 1
			} else {
				n = n*10 + d
			}
			prevDigit = true
		case c == '_' && prevDigit && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			prevDigit = false
		default:
			i = len(s)
		}
	}
	if neg {
		return -n
	}
	return n
}

// copyPrivate copies src to dst as a new file readable by this user only.
func copyPrivate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// writePrivate writes data to path with mode 0600, whatever mode the file had
// before, never following a symbolic link; a failed write leaves no file.
func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
