package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
)

// goldenResult is one scenario of testdata/golden/video/messages.json, what the
// real Ruby jobs said and stored (script/golden_video_messages.rb). The record
// id is "{id}" in the texts.
type goldenResult struct {
	Status          string   `json:"status"`
	ErrorDetail     *string  `json:"error_detail"`
	CaptionAttempts int      `json:"caption_attempts"`
	HTMLFilename    *string  `json:"html_filename"`
	RulinkyLinkID   *string  `json:"rulinky_link_id"`
	SlackSuccess    []string `json:"slack_success"`
	SlackFailure    []string `json:"slack_failure"`
	NextCheckMin    *int     `json:"next_check_in_minutes"`
	UploadTitle     *string  `json:"upload_title,omitempty"`
}

func (e *videoEnv) goldenResult(v *store.TwitterVideo, uploadTitle *string) goldenResult {
	got := e.reload(v.ID)
	norm := func(texts []string) []string {
		out := []string{}
		for _, t := range texts {
			t = strings.ReplaceAll(t, fmt.Sprintf("#%d]", v.ID), "#{id}]")
			t = strings.ReplaceAll(t, fmt.Sprintf("video_id=%d", v.ID), "video_id={id}")
			out = append(out, t)
		}
		return out
	}
	r := goldenResult{
		Status: got.Status, ErrorDetail: got.ErrorDetail, CaptionAttempts: got.CaptionAttempts,
		HTMLFilename: got.HTMLFilename, RulinkyLinkID: got.RulinkyLinkID,
		SlackSuccess: norm(e.slack.successes), SlackFailure: norm(e.slack.failures), UploadTitle: uploadTitle,
	}
	if q := e.queued(KindTwitterVideoCaptions, v.ID); len(q) > 0 {
		m := int(math.Round(q[len(q)-1].Delay.Minutes()))
		r.NextCheckMin = &m
	}
	return r
}

// Every scenario the Ruby script runs, with the same inputs. (A RuntimeError
// scenario is not here: Go's class name for an untyped error is its Go type.)
var goldenScenarios = map[string]func(e *videoEnv) goldenResult{
	"ingest_happy": func(e *videoEnv) goldenResult { return goldenIngest(e) },
	"ingest_no_title": func(e *videoEnv) goldenResult {
		e.ytdlp.downloadFn = func(_ context.Context, _, dir string) (video.Downloaded, error) {
			p := filepath.Join(dir, "x.mp4")
			return video.Downloaded{Path: p}, os.WriteFile(p, nil, 0o600)
		}
		must(e.t, os.MkdirAll(e.videosDir, 0o700))
		return goldenIngest(e)
	},
	"ingest_download_error": func(e *videoEnv) goldenResult {
		e.ytdlp.downloadFn = func(context.Context, string, string) (video.Downloaded, error) {
			return video.Downloaded{}, &video.YtdlpError{Message: "gone"}
		}
		return goldenIngest(e)
	},
	"ingest_reauth": func(e *videoEnv) goldenResult {
		e.uploader.fn = func(context.Context, string, string) (string, error) {
			return "", &video.YoutubeError{Message: "invalid_grant", Expired: true}
		}
		return goldenIngest(e)
	},
	"ingest_upload_error": func(e *videoEnv) goldenResult {
		e.uploader.fn = func(context.Context, string, string) (string, error) {
			return "", &video.YoutubeError{Message: "YouTube upload failed: boom"}
		}
		return goldenIngest(e)
	},

	"captions_hit": func(e *videoEnv) goldenResult { return goldenCaptions(e, tweetURL, 0) },
	"captions_miss": func(e *videoEnv) goldenResult {
		e.ytdlp.subsFn = noCaptions
		return goldenCaptions(e, tweetURL, 0)
	},
	"captions_ytdlp_error": func(e *videoEnv) goldenResult {
		e.ytdlp.subsFn = func(context.Context, string, string, string) (string, error) {
			return "", &video.YtdlpError{Message: "HTTP 429"}
		}
		return goldenCaptions(e, tweetURL, 0)
	},
	"captions_exhausted_twitter": func(e *videoEnv) goldenResult {
		e.ytdlp.subsFn = noCaptions
		return goldenCaptions(e, tweetURL, len(store.CaptionCheckpoints)-1)
	},
	"captions_exhausted_youtube": func(e *videoEnv) goldenResult {
		e.ytdlp.subsFn = noCaptions
		return goldenCaptions(e, "https://www.youtube.com/watch?v=dQw4w9WgXcQ", len(store.YoutubeLinkCheckpoints)-1)
	},
	"captions_summarizer_error": func(e *videoEnv) goldenResult {
		e.summ.fn = func(context.Context, string) (llm.Summary, error) {
			return llm.Summary{}, llm.ErrSotaLLMs("sota-llms summarization failed.")
		}
		return goldenCaptions(e, tweetURL, 0)
	},
	"captions_rulinky_error": func(e *videoEnv) goldenResult {
		e.rulinky.fn = func(context.Context, string, string) (string, error) {
			return "", &video.RulinkyError{Message: "rulinky link creation failed."}
		}
		return goldenCaptions(e, tweetURL, 0)
	},
}

func noCaptions(context.Context, string, string, string) (string, error) { return "", nil }

func goldenIngest(e *videoEnv) goldenResult {
	e.deps.YoutubeReauthURL = "https://serve.chiq.me/youtube/reauth"
	v := e.create(store.NewTwitterVideo{SourceURL: "https://x.com/golden/status/1", Status: "downloading"})
	must(e.t, e.ingest(v.ID))
	var title *string
	if len(e.uploader.titles) > 0 {
		title = &e.uploader.titles[0]
	}
	return e.goldenResult(v, title)
}

func goldenCaptions(e *videoEnv, source string, attempts int) goldenResult {
	v := e.awaiting(source)
	e.update(v.ID, store.TwitterVideoChanges{CaptionAttempts: &attempts})
	must(e.t, e.captions(v.ID))
	return e.goldenResult(v, nil)
}

// TestVideoMessagesGolden compares what the Go jobs say and store with what the
// real Ruby jobs produced for the same scenarios (Slack texts, error_detail,
// status, the title handed to YouTube, the delay of the next caption check).
// It runs without Ruby; regenerate the file with
// bin/rails runner -e test script/golden_video_messages.rb.
func TestVideoMessagesGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "video", "messages.json"))
	must(t, err)
	var golden map[string]goldenResult
	must(t, json.Unmarshal(raw, &golden))
	for name := range golden {
		if goldenScenarios[name] == nil {
			t.Errorf("golden scenario %q has no Go counterpart", name)
		}
	}
	for name, run := range goldenScenarios {
		want, ok := golden[name]
		if !ok {
			t.Errorf("scenario %q is missing from the golden file", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			got := run(newVideoEnv(t))
			norm := func(r goldenResult) goldenResult {
				if r.SlackSuccess == nil {
					r.SlackSuccess = []string{}
				}
				if r.SlackFailure == nil {
					r.SlackFailure = []string{}
				}
				return r
			}
			if !reflect.DeepEqual(norm(got), norm(want)) {
				g, _ := json.MarshalIndent(got, "", "  ")
				w, _ := json.MarshalIndent(want, "", "  ")
				t.Fatalf("Go differs from Ruby.\ngot:\n%s\nruby:\n%s", g, w)
			}
		})
	}
}
