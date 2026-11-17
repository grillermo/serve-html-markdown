package store_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/db"
	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

// A session TimeZone other than UTC, and a non-UTC time.Local, must not shift
// what is written or read: the columns are "timestamp without time zone"
// holding UTC. Run over the simple protocol (production, PgBouncer) and the
// extended protocol.
func TestTimestampsAreNotShiftedByTimeZones(t *testing.T) {
	// Set before the pools exist and restored after they close (cleanups run
	// last-in first-out): pool goroutines read time.Local, so changing it while
	// they run would be a data race.
	prevLocal := time.Local
	time.Local = time.FixedZone("x", -6*3600) // a dropped .UTC() would now shift
	t.Cleanup(func() { time.Local = prevLocal })

	simple := testdb.New(t)
	extended, err := db.Open(ctx, simple.Config().ConnConfig.ConnString(), 2, true)
	must(t, err)
	t.Cleanup(extended.Close)

	for name, pool := range map[string]*pgxpool.Pool{"simple": simple, "extended": extended} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `TRUNCATE served_files, youtube_credentials`)
			must(t, err)
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

			for col, got := range map[string]time.Time{"served_files.created_at": created, "youtube_credentials.obtained_at": obtained} {
				if got.Before(before) || got.After(after) {
					t.Errorf("%s = %v, want between %v and %v", col, got, before, after)
				}
				if got.Location() != time.UTC {
					t.Errorf("%s location = %v, want UTC", col, got.Location())
				}
			}
			if want := created.Format("2006-01-02 15:04:05.999999"); text != want {
				t.Errorf("stored text %q != UTC rendering %q", text, want)
			}
		})
	}
}
