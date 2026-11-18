package video

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"fmt"
	"testing"
)

// SetEgoGOOS lets tests pretend to run on another OS.
func SetEgoGOOS(e *EgoCookies, goos string) { e.goos = goos }

func testKey(t *testing.T, password string) []byte {
	t.Helper()
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// seal encrypts plain (already padded or not, as the test wants) with the fixed IV.
func seal(t *testing.T, key, plain []byte, pad bool) []byte {
	t.Helper()
	if pad {
		n := aes.BlockSize - len(plain)%aes.BlockSize
		plain = append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(n)}, n)...)
	}
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{' '}, 16)).CryptBlocks(out, plain)
	return append([]byte("v10"), out...)
}

func TestKeyDerivationMatchesRubyOpenSSL(t *testing.T) {
	// PBKDF2-HMAC-SHA1("peanuts", "saltysalt", 1003, 16), as printed by Ruby's
	// OpenSSL::PKCS5.pbkdf2_hmac_sha1 (the call in ego_cookies.rb).
	if got := fmt.Sprintf("%x", testKey(t, "peanuts")); got != "d9a09d499b4e1b7461f28e67972c6dbd" {
		t.Errorf("key = %s", got)
	}
}

func TestDecryptCookie(t *testing.T) {
	key := testKey(t, "pw")
	hostHash := bytes.Repeat([]byte("h"), 32)
	cases := []struct {
		name  string
		blob  []byte
		strip bool
		want  string
		ok    bool
	}{
		{"with host hash", seal(t, key, append(append([]byte{}, hostHash...), "abc"...), true), true, "abc", true},
		{"without host hash (version < 24)", seal(t, key, []byte("abc"), true), false, "abc", true},
		{"empty value", seal(t, key, hostHash, true), true, "", true},
		{"full block value", seal(t, key, []byte("0123456789abcdef"), true), false, "0123456789abcdef", true},
		{"shorter than the host hash", seal(t, key, []byte("abc"), true), true, "", false},
		{"other prefix", append([]byte("v11"), seal(t, key, []byte("abc"), true)[3:]...), false, "", false},
		{"no prefix", seal(t, key, []byte("abc"), true)[3:], false, "", false},
		{"empty blob", nil, false, "", false},
		{"prefix only", []byte("v10"), false, "", false},
		{"not whole blocks", append(seal(t, key, []byte("abc"), true), 'x'), false, "", false},
		{"wrong key", seal(t, testKey(t, "other"), []byte("abc"), true), false, "", false},
		{"padding byte 0", seal(t, key, append(bytes.Repeat([]byte("a"), 15), 0), false), false, "", false},
		{"padding byte 17", seal(t, key, append(bytes.Repeat([]byte("a"), 15), 17), false), false, "", false},
		{"inconsistent padding", seal(t, key, append(bytes.Repeat([]byte("a"), 14), 1, 2), false), false, "", false},
		{"padding 2 with right bytes", seal(t, key, append(bytes.Repeat([]byte("a"), 14), 2, 2), false), false, string(bytes.Repeat([]byte("a"), 14)), true},
		{"padding 16", seal(t, key, bytes.Repeat([]byte{16}, 16), false), false, "", true},
	}
	for _, c := range cases {
		got, ok := decryptCookie(c.blob, key, c.strip)
		if ok != c.ok || string(got) != c.want {
			t.Errorf("%s: got %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestChromeExpiry(t *testing.T) {
	for in, want := range map[string]int64{
		"0":                  0,
		"":                   0,
		"abc":                0,
		"00":                 0,
		"13400000000000000":  13400000000 - chromeEpochOffset,
		" 13400000000000000": 13400000000 - chromeEpochOffset,
		"13400000000000000x": 13400000000 - chromeEpochOffset,
		"1_0000000000000000": 10000000000 - chromeEpochOffset,
		"11644473600000000":  0 - 0, // exactly the Unix epoch
		"11644473599999999":  -1,    // rounds down, like Ruby's integer division
		"999999":             -chromeEpochOffset,
		"1":                  -chromeEpochOffset,
		"-5":                 -chromeEpochOffset - 1,
		"+13400000000000000": 13400000000 - chromeEpochOffset,
	} {
		if got := chromeExpiry(in); got != want {
			t.Errorf("chromeExpiry(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestRubyToI(t *testing.T) {
	for in, want := range map[string]int64{
		"24": 24, "24\n": 24, "  7": 7, "": 0, "x": 0, "12x": 12, "-3": -3, "+3": 3, "1_000": 1000,
		"1__0": 1, "_1": 0, "1_": 1, "0x10": 0, "3.9": 3, "99999999999999999999999": 1<<63 - 1,
	} {
		if got := rubyToI(in); got != want {
			t.Errorf("rubyToI(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestDedupeRowsKeepsTheFirst(t *testing.T) {
	rows := [][]string{
		{".youtube.com", "SID", "/", "1", "0", "", "A"},
		{".youtube.com", "SID", "/", "1", "0", "https://youtube.com", "B"},
		{".youtube.com", "SID", "/x", "1", "0", "", "C"},
		{"youtube.com", "SID", "/", "1", "0", "", "D"},
		{".youtube.com", "sid", "/", "1", "0", "", "E"},
		{".youtube.com", "SID", "/", "0", "5", "", "F"},
	}
	var got []string
	for _, r := range dedupeRows(rows) {
		got = append(got, r[6])
	}
	if fmt.Sprint(got) != "[A C D E]" {
		t.Errorf("kept %v", got)
	}
}

func TestParseRows(t *testing.T) {
	out := ".youtube.com\tSID\t/\t1\t0\t\tAB\r\n" + // CRLF is chomped
		"broken\tline\n" + // too few fields: dropped
		"\n" + // blank: dropped
		"youtube.com\tPREF\t/\t0\t0\thttps://youtube.com\tCD\n"
	rows := parseRows(out)
	if len(rows) != 2 || rows[0][6] != "AB" || rows[1][5] != "https://youtube.com" || rows[1][6] != "CD" {
		t.Errorf("rows = %q", rows)
	}
	if len(parseRows("")) != 0 {
		t.Error("empty output has no rows")
	}
}

// A line of the Netscape file has seven tab-separated fields, so a value that
// carries a tab or a line break (which Ruby would write out, corrupting the
// file) makes the cookie unusable.
func TestNetscapeLineRefusesFieldsThatBreakTheFormat(t *testing.T) {
	key := testKey(t, "pw")
	row := func(host, name, path, value string) []string {
		blob := seal(t, key, []byte(value), true)
		return []string{host, name, path, "1", "0", "", fmt.Sprintf("%X", blob)}
	}
	if line, ok := netscapeLine(row(".youtube.com", "SID", "/", "ok"), key, false); !ok || string(line) != ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tok" {
		t.Errorf("good row: %q %v", line, ok)
	}
	for _, bad := range [][]string{
		row(".youtube.com", "SID", "/", "a\tb"),
		row(".youtube.com", "SID", "/", "a\nb"),
		row(".youtube.com", "SID", "/", "a\rb"),
		row(".youtube.com", "SID", "/", "a\x00b"),
		row(".youtube.com", "S\rID", "/", "v"),
		row(".youtube.com", "SID", "/a\rb", "v"),
		row(".youtube\r.com", "SID", "/", "v"),
		{".youtube.com", "SID", "/", "1", "0", "", "ZZ"}, // not hex
	} {
		if line, ok := netscapeLine(bad, key, false); ok {
			t.Errorf("row %q gave %q", bad, line)
		}
	}
}

func TestEgoCookiesSQLIsTheRubyQuery(t *testing.T) {
	want := "SELECT host_key, name, path, is_secure, expires_utc, top_frame_site_key, hex(encrypted_value) " +
		"FROM cookies WHERE host_key IN ('.youtube.com','youtube.com','.google.com','google.com') AND top_frame_site_key IN ('','https://youtube.com') " +
		"ORDER BY top_frame_site_key"
	if got := cookiesSQL(); got != want {
		t.Errorf("sql = %q", got)
	}
}
