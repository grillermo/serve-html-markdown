package store_test

import (
	"testing"
	"time"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

// A session TimeZone other than UTC must not shift what is written or read:
// the columns are "timestamp without time zone" holding UTC.
func TestTimestampsAreNotShiftedBySessionTimeZone(t *testing.T) {
	pool := testdb.New(t)
	conn, err := pool.Acquire(ctx)
	must(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, `SET TIME ZONE 'Pacific/Kiritimati'`) // UTC+14
	must(t, err)
	defer func() { _, _ = conn.Exec(ctx, `RESET TIME ZONE`) }()

	var tz string
	must(t, conn.QueryRow(ctx, `SHOW TimeZone`).Scan(&tz))
	if tz != "Pacific/Kiritimati" {
		t.Fatalf("session TimeZone = %q; SET did not apply", tz)
	}

	before := time.Now().UTC().Add(-time.Second)
	must(t, store.RecordServedFile(ctx, conn, "tz.md"))
	must(t, store.StoreYoutubeCredential(ctx, conn, "tok"))
	after := time.Now().UTC().Add(time.Second)

	var created, obtained time.Time
	var text string
	must(t, conn.QueryRow(ctx, `SELECT created_at, created_at::text FROM served_files WHERE name = 'tz.md'`).Scan(&created, &text))
	must(t, conn.QueryRow(ctx, `SELECT obtained_at FROM youtube_credentials`).Scan(&obtained))

	for name, got := range map[string]time.Time{"served_files.created_at": created, "youtube_credentials.obtained_at": obtained} {
		if got.Before(before) || got.After(after) {
			t.Errorf("%s = %v, want between %v and %v", name, got, before, after)
		}
		if got.Location() != time.UTC {
			t.Errorf("%s location = %v, want UTC", name, got.Location())
		}
	}
	if want := created.Format("2006-01-02 15:04:05.999999"); text != want {
		t.Errorf("stored text %q != UTC rendering %q", text, want)
	}
}
