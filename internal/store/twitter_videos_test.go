package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/db/testdb"
	"serve-html-markdown/internal/store"
)

func newVideo(t *testing.T, pool *pgxpool.Pool, n store.NewTwitterVideo) *store.TwitterVideo {
	t.Helper()
	v, err := store.CreateTwitterVideo(ctx, pool, n)
	must(t, err)
	return v
}

func videoIDs(vs []store.TwitterVideo) []int64 {
	ids := []int64{}
	for _, v := range vs {
		ids = append(ids, v.ID)
	}
	return ids
}

func TestCreateTwitterVideoDefaults(t *testing.T) {
	pool := testdb.New(t)
	clock(t, t0)
	v := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1"})
	if v.ID == 0 || v.SourceURL != "https://x.com/a/status/1" || v.Status != "downloading" || v.CaptionAttempts != 0 {
		t.Fatalf("got %+v", v)
	}
	if v.UploadCompletedAt != nil || v.VideoPath != nil || v.YoutubeID != nil || v.YoutubeTitle != nil ||
		v.HTMLFilename != nil || v.RulinkyLinkID != nil || v.ErrorDetail != nil {
		t.Fatalf("nullable columns should start NULL: %+v", v)
	}
	if !v.CreatedAt.Equal(t0) || !v.UpdatedAt.Equal(t0) {
		t.Fatalf("timestamps %v %v", v.CreatedAt, v.UpdatedAt)
	}
	got, err := store.FindTwitterVideo(ctx, pool, v.ID)
	must(t, err)
	if !reflect.DeepEqual(got, v) {
		t.Fatalf("find %+v != create %+v", got, v)
	}
	missing, err := store.FindTwitterVideo(ctx, pool, 999)
	must(t, err)
	if missing != nil {
		t.Fatalf("missing id got %+v", missing)
	}
}

func TestCreateTwitterVideoValidates(t *testing.T) {
	pool := testdb.New(t)
	cases := []struct {
		name  string
		in    store.NewTwitterVideo
		field string
		msg   string
	}{
		{"blank url", store.NewTwitterVideo{SourceURL: ""}, "source_url", "can't be blank"},
		{"whitespace url", store.NewTwitterVideo{SourceURL: "  "}, "source_url", "can't be blank"},
		{"NUL url", store.NewTwitterVideo{SourceURL: "a\x00b"}, "source_url", "is invalid"},
		{"unknown status", store.NewTwitterVideo{SourceURL: "u", Status: "bogus"}, "status", "is not included in the list"},
	}
	for _, c := range cases {
		v, err := store.CreateTwitterVideo(ctx, pool, c.in)
		var invalid *store.InvalidTwitterVideoError
		if v != nil || !errors.Is(err, store.ErrInvalidTwitterVideo) || !errors.As(err, &invalid) {
			t.Errorf("%s: got %v, %v", c.name, v, err)
			continue
		}
		if invalid.Field != c.field || invalid.Message != c.msg {
			t.Errorf("%s: %q %q, want %q %q", c.name, invalid.Field, invalid.Message, c.field, c.msg)
		}
	}
	for _, status := range store.TwitterVideoStatuses {
		newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: status})
	}
	want := []string{"downloading", "uploading", "awaiting_captions", "summarizing", "publishing", "done", "failed"}
	if !reflect.DeepEqual(store.TwitterVideoStatuses, want) {
		t.Fatalf("statuses = %v", store.TwitterVideoStatuses)
	}
}

func TestTwitterVideosForSourceURL(t *testing.T) {
	pool := testdb.New(t)
	plain := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1"})
	tracked := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1?t=x"})
	newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/12"})
	newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/12?s=1"})
	newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/b/status/1"})

	// The submitted URL's own query string is ignored; newest first.
	for _, submitted := range []string{"https://x.com/a/status/1?s=20", "https://x.com/a/status/1"} {
		got, err := store.TwitterVideosForSourceURL(ctx, pool, submitted)
		must(t, err)
		if want := []int64{tracked.ID, plain.ID}; !reflect.DeepEqual(videoIDs(got), want) {
			t.Errorf("%s: ids = %v, want %v", submitted, videoIDs(got), want)
		}
	}
	latest, err := store.LatestTwitterVideoForSourceURL(ctx, pool, "https://x.com/a/status/1?s=20")
	must(t, err)
	if latest == nil || latest.ID != tracked.ID {
		t.Fatalf("latest = %+v, want #%d", latest, tracked.ID)
	}
	none, err := store.LatestTwitterVideoForSourceURL(ctx, pool, "https://x.com/zzz/status/1")
	must(t, err)
	if none != nil {
		t.Fatalf("latest = %+v, want nil", none)
	}
}

func TestTwitterVideosForSourceURLTreatsLikeWildcardsLiterally(t *testing.T) {
	pool := testdb.New(t)
	want := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a_b/status/1"})
	newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/aXb/status/1"})
	newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/aXb/status/1?t=1"})
	got, err := store.TwitterVideosForSourceURL(ctx, pool, "https://x.com/a_b/status/1")
	must(t, err)
	if !reflect.DeepEqual(videoIDs(got), []int64{want.ID}) {
		t.Fatalf("ids = %v, want [%d]", videoIDs(got), want.ID)
	}
	got, err = store.TwitterVideosForSourceURL(ctx, pool, "https://x.com/100%/status/1")
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("got %v", videoIDs(got))
	}
	got, err = store.TwitterVideosForSourceURL(ctx, pool, "a\x00b")
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("got %v", videoIDs(got))
	}
}

func TestFindOrCreateTwitterVideo(t *testing.T) {
	pool := testdb.New(t)
	first, created, err := store.FindOrCreateTwitterVideo(ctx, pool, "https://x.com/a/status/1")
	must(t, err)
	if !created || first.Status != "downloading" {
		t.Fatalf("created=%v %+v", created, first)
	}
	again, created, err := store.FindOrCreateTwitterVideo(ctx, pool, "https://x.com/a/status/1?s=20")
	must(t, err)
	if created || again.ID != first.ID {
		t.Fatalf("created=%v again=%+v first=%+v", created, again, first)
	}
	if _, _, err := store.FindOrCreateTwitterVideo(ctx, pool, ""); !errors.Is(err, store.ErrInvalidTwitterVideo) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartYoutubeTwitterVideo(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	const watch = "https://www.youtube.com/watch?v=KpILHX2D-AA"

	v, existed, err := store.StartYoutubeTwitterVideo(ctx, pool, watch, "KpILHX2D-AA")
	must(t, err)
	if existed || v.Status != "awaiting_captions" || v.CaptionAttempts != 0 || v.YoutubeID == nil || *v.YoutubeID != "KpILHX2D-AA" ||
		v.UploadCompletedAt == nil || !v.UploadCompletedAt.Equal(t0) || v.ErrorDetail != nil {
		t.Fatalf("existed=%v %+v", existed, v)
	}

	// A retry resets the newest row: attempts, upload time and error.
	must(t, store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{
		Status: new("failed"), CaptionAttempts: new(3), ErrorDetail: new("boom")}))
	advance(time.Hour)
	again, existed, err := store.StartYoutubeTwitterVideo(ctx, pool, watch, "KpILHX2D-AA")
	must(t, err)
	if !existed || again.ID != v.ID || again.Status != "awaiting_captions" || again.CaptionAttempts != 0 ||
		again.ErrorDetail != nil || !again.UploadCompletedAt.Equal(t0.Add(time.Hour)) || !again.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("existed=%v %+v", existed, again)
	}
	var n int
	must(t, pool.QueryRow(ctx, `SELECT count(*) FROM twitter_videos`).Scan(&n))
	if n != 1 {
		t.Fatalf("%d rows, want 1", n)
	}
	// The exact URL is matched, not a prefix or a query-string variant.
	other, existed, err := store.StartYoutubeTwitterVideo(ctx, pool, watch+"&t=5", "KpILHX2D-AA")
	must(t, err)
	if existed || other.ID == v.ID {
		t.Fatalf("existed=%v %+v", existed, other)
	}
	for _, c := range []struct{ watch, id, field, msg string }{
		{"", "x", "source_url", "can't be blank"},
		{"  ", "x", "source_url", "can't be blank"},
		{"a\x00b", "x", "source_url", "is invalid"},
		{"a\xffb", "x", "source_url", "is invalid"},
		{watch, "", "youtube_id", "can't be blank"},
		{watch, " ", "youtube_id", "can't be blank"},
		{watch, "a\x00b", "youtube_id", "is invalid"},
	} {
		_, _, err := store.StartYoutubeTwitterVideo(ctx, pool, c.watch, c.id)
		var invalid *store.InvalidTwitterVideoError
		if !errors.Is(err, store.ErrInvalidTwitterVideo) || !errors.As(err, &invalid) || invalid.Field != c.field || invalid.Message != c.msg {
			t.Errorf("(%q, %q): err = %v, want %s %s", c.watch, c.id, err, c.field, c.msg)
		}
	}
}

func TestDownloadedSiblings(t *testing.T) {
	pool := testdb.New(t)
	a := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1"})
	b := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1?s=20"})
	c := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1?s=21"})
	noFile := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1?s=22"})
	other := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/12"})
	for _, v := range []*store.TwitterVideo{a, b, c, other} {
		must(t, store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{VideoPath: new("/v/x.mp4")}))
	}
	got, err := store.DownloadedSiblings(ctx, pool, a)
	must(t, err)
	// The video itself first, then newest first; rows without a path and other tweets are left out.
	if want := []int64{a.ID, c.ID, b.ID}; !reflect.DeepEqual(videoIDs(got), want) {
		t.Fatalf("ids = %v, want %v", videoIDs(got), want)
	}
	got, err = store.DownloadedSiblings(ctx, pool, noFile)
	must(t, err)
	if want := []int64{c.ID, b.ID, a.ID}; !reflect.DeepEqual(videoIDs(got), want) {
		t.Fatalf("ids = %v, want %v", videoIDs(got), want)
	}
}

func TestUpdateTwitterVideo(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	v := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1"})
	advance(time.Minute)

	// What the ingest job does after downloading, then after uploading.
	must(t, store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{YoutubeTitle: new("A title"), VideoPath: new("/videos/a.mp4")}))
	must(t, store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{Status: new("uploading")}))
	uploaded := t0.Add(2 * time.Minute)
	must(t, store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{
		Status: new("awaiting_captions"), YoutubeID: new("KpILHX2D-AA"), UploadCompletedAt: &uploaded}))
	got, err := store.FindTwitterVideo(ctx, pool, v.ID)
	must(t, err)
	if got.Status != "awaiting_captions" || *got.YoutubeTitle != "A title" || *got.VideoPath != "/videos/a.mp4" ||
		*got.YoutubeID != "KpILHX2D-AA" || !got.UploadCompletedAt.Equal(uploaded) || !got.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("got %+v", got)
	}

	// What the captions job does when publishing.
	must(t, store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{
		Status: new("done"), HTMLFilename: new("summary.html"), RulinkyLinkID: new("42")}))
	got, err = store.FindTwitterVideo(ctx, pool, v.ID)
	must(t, err)
	if got.Status != "done" || *got.HTMLFilename != "summary.html" || *got.RulinkyLinkID != "42" || *got.YoutubeID != "KpILHX2D-AA" {
		t.Fatalf("got %+v", got)
	}

	// An empty string is stored as the empty string, not NULL.
	must(t, store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{ErrorDetail: new("")}))
	got, err = store.FindTwitterVideo(ctx, pool, v.ID)
	must(t, err)
	if got.ErrorDetail == nil || *got.ErrorDetail != "" {
		t.Fatalf("error_detail = %v", got.ErrorDetail)
	}

	var invalid *store.InvalidTwitterVideoError
	if err := store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{Status: new("bogus")}); !errors.As(err, &invalid) || invalid.Field != "status" {
		t.Errorf("bad status: %v", err)
	}
	if err := store.UpdateTwitterVideo(ctx, pool, v.ID, store.TwitterVideoChanges{VideoPath: new("a\x00b")}); !errors.As(err, &invalid) || invalid.Field != "video_path" {
		t.Errorf("NUL: %v", err)
	}
	if err := store.UpdateTwitterVideo(ctx, pool, 999, store.TwitterVideoChanges{Status: new("done")}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing row: %v", err)
	}
}

func TestFailTwitterVideoRecordsStatusAndDetail(t *testing.T) {
	pool := testdb.New(t)
	v := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1", Status: "uploading"})
	must(t, store.FailTwitterVideo(ctx, pool, v.ID, "boom"))
	got, err := store.FindTwitterVideo(ctx, pool, v.ID)
	must(t, err)
	if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != "boom" {
		t.Fatalf("got %+v", got)
	}
	if err := store.FailTwitterVideo(ctx, pool, 999, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing row: %v", err)
	}
}

// FailTwitterVideo is the jobs' last resort, so odd text is sanitised, not rejected.
func TestFailTwitterVideoSanitisesDetail(t *testing.T) {
	pool := testdb.New(t)
	for detail, want := range map[string]string{
		"a\x00b":        "ab",
		"bad\xffbytes":  "bad\uFFFDbytes",
		"\x00\xfe\x00z": "\uFFFDz",
		"plain é":       "plain é",
	} {
		v := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "https://x.com/a/status/1", Status: "uploading"})
		must(t, store.FailTwitterVideo(ctx, pool, v.ID, detail))
		got, err := store.FindTwitterVideo(ctx, pool, v.ID)
		must(t, err)
		if got.Status != "failed" || got.ErrorDetail == nil || *got.ErrorDetail != want {
			t.Errorf("detail %q: got %+v, want %q", detail, got, want)
		}
	}
}

func TestIncrementCaptionAttempts(t *testing.T) {
	pool := testdb.New(t)
	advance := clock(t, t0)
	v := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "awaiting_captions", CaptionAttempts: 2})
	advance(time.Minute)
	n, err := store.IncrementCaptionAttempts(ctx, pool, v.ID)
	must(t, err)
	if n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
	got, err := store.FindTwitterVideo(ctx, pool, v.ID)
	must(t, err)
	if got.CaptionAttempts != 3 || !got.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("got %+v", got)
	}
	if _, err := store.IncrementCaptionAttempts(ctx, pool, 999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing row: %v", err)
	}
}

func TestCheckpoints(t *testing.T) {
	long := []int{5, 10, 20, 30, 40, 60, 120, 1440, 2880}
	short := []int{5, 10, 20, 30, 40, 60, 120}
	cases := map[string][]int{
		"https://x.com/a/status/1": long,
		"u":                        long,
		"https://www.youtube.com/watch?v=KpILHX2D-AA":    short,
		"https://youtube.com/watch?v=KpILHX2D-AA&t=5":    short,
		"https://youtu.be/KpILHX2D-AA":                   short,
		"https://www.youtube.com/watch?v=tooshort":       long,
		"https://www.youtube.com/watch":                  long,
		"https://notyoutube.com/watch?v=KpILHX2D-AA":     long,
		"https://x.com/a/status/1?u=youtube.com/watch":   long,
		"https://example.com/?next=youtu.be/KpILHX2D-AA": long,
	}
	for u, want := range cases {
		v := &store.TwitterVideo{SourceURL: u}
		if got := v.Checkpoints(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", u, got, want)
		}
	}
	if !reflect.DeepEqual(store.CaptionCheckpoints, long) || !reflect.DeepEqual(store.YoutubeLinkCheckpoints, short) {
		t.Fatal("exported schedules changed")
	}
}

func TestNextCaptionWait(t *testing.T) {
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	video := func(source string, attempts int, uploaded *time.Time) *store.TwitterVideo {
		return &store.TwitterVideo{SourceURL: source, Status: "awaiting_captions", CaptionAttempts: attempts, UploadCompletedAt: uploaded}
	}
	check := func(name string, v *store.TwitterVideo, now time.Time, want time.Duration, wantOK bool) {
		t.Helper()
		got, ok := v.NextCaptionWait(now)
		if got != want || ok != wantOK {
			t.Errorf("%s: (%v, %v), want (%v, %v)", name, got, ok, want, wantOK)
		}
	}
	// 5 min checkpoint, 1 min already elapsed.
	check("first checkpoint", video("u", 0, &base), base.Add(time.Minute), 240*time.Second, true)
	// 5 min checkpoint, 2 min elapsed.
	check("two minutes in", video("u", 0, &base), base.Add(2*time.Minute), 180*time.Second, true)
	// Never negative: the 5 min checkpoint passed 5 min ago.
	check("overdue", video("u", 0, &base), base.Add(10*time.Minute), 0, true)
	// The 2nd checkpoint is 10 min; 15 min elapsed clamps to 0.
	check("second overdue", video("u", 1, &base), base.Add(15*time.Minute), 0, true)
	// Whole seconds, truncated toward zero.
	check("fractional", video("u", 0, &base), base.Add(time.Minute+500*time.Millisecond), 239*time.Second, true)

	// Checks again one and two days after upload.
	n := len(store.CaptionCheckpoints)
	check("one day", video("u", n-2, &base), base, 24*time.Hour, true)
	check("two days", video("u", n-1, &base), base, 48*time.Hour, true)
	// Exhausted.
	now := time.Now().UTC()
	check("exhausted", video("u", n, &now), now, 0, false)
	check("negative attempts", video("u", -1, &base), base, 0, false)
	check("no upload time", video("u", 0, nil), base, 0, false)

	// A YouTube link gives up two hours after it was sent.
	yt := "https://www.youtube.com/watch?v=KpILHX2D-AA"
	m := len(store.YoutubeLinkCheckpoints)
	check("youtube last", video(yt, m-1, &base), base, 2*time.Hour, true)
	check("youtube exhausted", video(yt, m, &base), base, 0, false)

	if video("u", 0, &base).HasUploadTime() == false || video("u", 0, nil).HasUploadTime() {
		t.Error("HasUploadTime")
	}
}

// A row read back from Postgres works with NextCaptionWait (UTC timestamps).
func TestNextCaptionWaitFromStoredRow(t *testing.T) {
	pool := testdb.New(t)
	uploaded := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	v := newVideo(t, pool, store.NewTwitterVideo{SourceURL: "u", Status: "awaiting_captions", UploadCompletedAt: &uploaded})
	got, err := store.FindTwitterVideo(ctx, pool, v.ID)
	must(t, err)
	if wait, ok := got.NextCaptionWait(uploaded.Add(2 * time.Minute)); wait != 3*time.Minute || !ok {
		t.Fatalf("(%v, %v)", wait, ok)
	}
}
