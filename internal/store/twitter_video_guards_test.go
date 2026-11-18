package store_test

import (
	"testing"
	"time"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

func mustFind(t *testing.T, db store.DB, id int64) *store.TwitterVideo {
	t.Helper()
	v, err := store.FindTwitterVideo(ctx, db, id)
	must(t, err)
	if v == nil {
		t.Fatalf("video %d vanished", id)
	}
	return v
}

func TestTransitionTwitterVideoIsGuardedByStatus(t *testing.T) {
	pool := testdb.New(t)
	v := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1", Status: "downloading"})

	ok, err := store.TransitionTwitterVideo(ctx, pool, v.ID, []string{"done", "failed"}, store.TwitterVideoChanges{Status: new("uploading")})
	must(t, err)
	if ok || mustFind(t, pool, v.ID).Status != "downloading" {
		t.Fatalf("guard let a row through: ok=%v", ok)
	}
	ok, err = store.TransitionTwitterVideo(ctx, pool, v.ID, []string{"downloading", "failed"},
		store.TwitterVideoChanges{Status: new("uploading"), VideoPath: new("/v/x.mp4")})
	must(t, err)
	got := mustFind(t, pool, v.ID)
	if !ok || got.Status != "uploading" || got.VideoPath == nil || *got.VideoPath != "/v/x.mp4" {
		t.Fatalf("ok=%v %+v", ok, got)
	}
	// A missing row is "no row matched", not an error.
	if ok, err = store.TransitionTwitterVideo(ctx, pool, 999, []string{"uploading"}, store.TwitterVideoChanges{Status: new("done")}); ok || err != nil {
		t.Fatalf("missing row: %v %v", ok, err)
	}
	// Bad input is rejected up front.
	if _, err = store.TransitionTwitterVideo(ctx, pool, v.ID, nil, store.TwitterVideoChanges{Status: new("done")}); err == nil {
		t.Error("empty from accepted")
	}
	if _, err = store.TransitionTwitterVideo(ctx, pool, v.ID, []string{"bogus"}, store.TwitterVideoChanges{Status: new("done")}); err == nil {
		t.Error("unknown from status accepted")
	}
	if _, err = store.TransitionTwitterVideo(ctx, pool, v.ID, []string{"uploading"}, store.TwitterVideoChanges{Status: new("bogus")}); err == nil {
		t.Error("unknown status accepted")
	}
}

func TestFailTwitterVideoFromIsGuardedAndSanitised(t *testing.T) {
	pool := testdb.New(t)
	done := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "done"})
	ok, err := store.FailTwitterVideoFrom(ctx, pool, done.ID, []string{"summarizing", "publishing"}, "late")
	must(t, err)
	if ok || mustFind(t, pool, done.ID).Status != "done" {
		t.Fatalf("a terminal row was regressed (ok=%v)", ok)
	}
	pub := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "publishing"})
	ok, err = store.FailTwitterVideoFrom(ctx, pool, pub.ID, []string{"summarizing", "publishing"}, "bad\x00\xffbytes")
	must(t, err)
	got := mustFind(t, pool, pub.ID)
	if !ok || got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "bad�bytes" {
		t.Fatalf("ok=%v %+v", ok, got)
	}
}

func TestAdvanceCaptionAttemptCountsOncePerAttempt(t *testing.T) {
	pool := testdb.New(t)
	v := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "awaiting_captions", CaptionAttempts: 2})

	n, ok, err := store.AdvanceCaptionAttempt(ctx, pool, v.ID, 2)
	must(t, err)
	if !ok || n != 3 {
		t.Fatalf("first advance: %d %v", n, ok)
	}
	// A second writer that read the same attempt loses.
	n, ok, err = store.AdvanceCaptionAttempt(ctx, pool, v.ID, 2)
	must(t, err)
	if ok || mustFind(t, pool, v.ID).CaptionAttempts != 3 {
		t.Fatalf("double increment: %d %v", n, ok)
	}
	// Only awaiting_captions rows count.
	must(t, store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{Status: new("failed")}))
	if _, ok, err = store.AdvanceCaptionAttempt(ctx, pool, v.ID, 3); err != nil || ok {
		t.Fatalf("failed row advanced: %v %v", ok, err)
	}
}

func TestRecordTwitterVideoUploadWritesOneStatementOnce(t *testing.T) {
	pool := testdb.New(t)
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	store.SetNow(t, func() time.Time { return at })
	v := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1", Status: "uploading", CaptionAttempts: 4})

	ok, err := store.RecordTwitterVideoUpload(ctx, pool, v.ID, "YT9")
	must(t, err)
	got := mustFind(t, pool, v.ID)
	if !ok || got.Status != "awaiting_captions" || got.YoutubeID == nil || *got.YoutubeID != "YT9" ||
		got.CaptionAttempts != 0 || got.UploadCompletedAt == nil || !got.UploadCompletedAt.Equal(at) {
		t.Fatalf("ok=%v %+v", ok, got)
	}
	// An id is never replaced: a duplicate upload cannot overwrite the first.
	ok, err = store.RecordTwitterVideoUpload(ctx, pool, v.ID, "OTHER")
	must(t, err)
	if ok || *mustFind(t, pool, v.ID).YoutubeID != "YT9" {
		t.Fatalf("second upload replaced the id (ok=%v)", ok)
	}
	// ... but a blank id column counts as unset, and a failed row is revived
	// (the sweeper may have failed it during a long upload; the video exists).
	b := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "failed"})
	must(t, store.UpdateTwitterVideo(ctx, pool, b.ID, store.TwitterVideoChanges{YoutubeID: new("")}))
	if ok, err = store.RecordTwitterVideoUpload(ctx, pool, b.ID, "YT1"); err != nil || !ok {
		t.Fatalf("blank id: %v %v", ok, err)
	}
	if ok, err = store.RecordTwitterVideoUpload(ctx, pool, 999, "YT1"); err != nil || ok {
		t.Fatalf("missing row: %v %v", ok, err)
	}
}

func TestReclaimTwitterVideoTakesOverOnlyStaleInFlightRows(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	summ := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "summarizing"})
	pub := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "publishing"})
	other := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "done"})
	advance(10 * time.Minute)

	for _, v := range []*store.TwitterVideo{summ, pub, other} {
		if ok, err := store.ReclaimTwitterVideo(ctx, pool, v.ID, 15*time.Minute); err != nil || ok {
			t.Fatalf("#%d fresh row reclaimed: %v %v", v.ID, ok, err)
		}
	}
	advance(10 * time.Minute)
	for _, v := range []*store.TwitterVideo{summ, pub} {
		ok, err := store.ReclaimTwitterVideo(ctx, pool, v.ID, 15*time.Minute)
		must(t, err)
		if !ok || mustFind(t, pool, v.ID).Status != "awaiting_captions" {
			t.Fatalf("#%d stale row not reclaimed: %v", v.ID, ok)
		}
	}
	if ok, err := store.ReclaimTwitterVideo(ctx, pool, other.ID, 15*time.Minute); err != nil || ok || mustFind(t, pool, other.ID).Status != "done" {
		t.Fatalf("a done row was reclaimed: %v %v", ok, err)
	}
}

func TestFailStaleTwitterVideos(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	stuck := map[string]*store.TwitterVideo{}
	for _, s := range []string{"downloading", "uploading", "summarizing", "publishing", "awaiting_captions", "done", "failed"} {
		stuck[s] = newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: s})
	}
	advance(2 * time.Hour)
	fresh := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "downloading"})
	advance(2 * time.Hour) // the first batch is 4h old, the fresh one 2h

	n, err := store.FailStaleTwitterVideos(ctx, pool, 3*time.Hour)
	must(t, err)
	if n != 4 {
		t.Fatalf("failed %d rows, want 4", n)
	}
	for s, v := range stuck {
		got := mustFind(t, pool, v.ID)
		switch s {
		case "downloading", "uploading", "summarizing", "publishing":
			if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != store.TwitterVideoFailedDetail {
				t.Errorf("%s: %+v", s, got)
			}
		default: // awaiting_captions legitimately waits for days; terminal rows stay as they are
			if got.Status != s || got.ErrorDetail != nil {
				t.Errorf("%s was touched: %+v", s, got)
			}
		}
	}
	if got := mustFind(t, pool, fresh.ID); got.Status != "downloading" {
		t.Errorf("fresh row failed: %+v", got)
	}
}

func TestAwaitingCaptionVideos(t *testing.T) {
	pool := testdb.New(t)
	at := t0
	a := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "awaiting_captions", UploadCompletedAt: &at})
	newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "done"})
	newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "downloading"})
	got, err := store.AwaitingCaptionVideos(ctx, pool, 10)
	must(t, err)
	if len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("got %+v", got)
	}
}
