package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"serve-html-markdown/internal/auth"
	"serve-html-markdown/internal/expansion"
	"serve-html-markdown/internal/jobs"
	"serve-html-markdown/internal/store"
)

// Limits on what POST /expansions accepts. Ruby had none; they bound the work
// and the rows an authenticated client can create (the selection cap is the
// processor's, so a request that would only fail later is refused now).
const (
	// MaxExpansionBodyBytes caps the request body: a selection and a question at
	// their caps with JSON escaping fit in it with room to spare.
	MaxExpansionBodyBytes = 512 << 10
	// MaxQuestionBytes caps the question.
	MaxQuestionBytes = 10 << 10
	// MaxExpansionFileNameBytes caps file_name (file names are at most 255 bytes).
	MaxExpansionFileNameBytes = 1024
	// MaxFallbackAnchorBytes caps fallback_anchor (an element id).
	MaxFallbackAnchorBytes = 1024
)

// expansionWriteTimeout bounds the writes that follow the insert of an
// expansion. They run detached from the request so a client that hangs up
// does not leave a pending row that was never enqueued.
const expansionWriteTimeout = 15 * time.Second

const (
	msgMissingExpansionFields = "Missing file_name, selected_text, or question."
	msgUnknownMode            = "Unknown mode."
	msgSelectionTooLong       = "Selected text is too long."
	msgQuestionTooLong        = "Question is too long."
	msgFileNameTooLong        = "File name is too long."
	msgAnchorTooLong          = "Fallback anchor is too long."
	msgInvalidExpansionText   = "file_name, selected_text, question, and fallback_anchor must be valid text."
	msgExpansionNotFound      = "Expansion not found."
)

// WithEnqueuer sets where POST /expansions enqueues its job (default: the
// go_jobs table of the Server's pool). Tests use a fake.
func WithEnqueuer(e jobs.Enqueuer) Option {
	return func(s *Server) { s.enqueuer = e }
}

// createExpansion is POST /expansions (ExpansionsController#create): validate,
// store a pending expansion, remember the mode on the user, stamp the client
// and request times, enqueue the generation and answer 202 with the id.
//
// Deviations from Ruby, all refusals of input Ruby would have failed on later
// or not at all: the size caps above, text that Postgres cannot store (400
// instead of a 500), and an occurrence above int32 (clamped instead of a 500).
// Like Ruby the file is not looked up here; a missing one fails the job.
func (s *Server) createExpansion(w http.ResponseWriter, r *http.Request) {
	requestReceived := store.NowMS()
	user := auth.UserFrom(r.Context())
	if user == nil {
		s.unauthenticated(w, r)
		return
	}
	params, err := parseParams(w, r, MaxExpansionBodyBytes)
	if err != nil {
		bodyError(w, err)
		return
	}
	fileName, selected, question := params.get("file_name"), params.get("selected_text"), params.get("question")
	if blank(fileName) || blank(selected) || blank(question) {
		writeDetail(w, http.StatusBadRequest, msgMissingExpansionFields)
		return
	}
	mode := params.get("mode")
	if blank(mode) {
		mode = "create_new"
	}
	if !slices.Contains(store.ExpansionModes, mode) {
		writeDetail(w, http.StatusBadRequest, msgUnknownMode)
		return
	}
	fallback := params.get("fallback_anchor")
	if blank(fallback) {
		fallback = ""
	}
	switch {
	case len(selected) > expansion.MaxSelectedTextBytes:
		writeDetail(w, http.StatusBadRequest, msgSelectionTooLong)
		return
	case len(question) > MaxQuestionBytes:
		writeDetail(w, http.StatusBadRequest, msgQuestionTooLong)
		return
	case len(fileName) > MaxExpansionFileNameBytes:
		writeDetail(w, http.StatusBadRequest, msgFileNameTooLong)
		return
	case len(fallback) > MaxFallbackAnchorBytes:
		writeDetail(w, http.StatusBadRequest, msgAnchorTooLong)
		return
	}
	for _, text := range []string{fileName, selected, question, fallback} {
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			writeDetail(w, http.StatusBadRequest, msgInvalidExpansionText)
			return
		}
	}

	// From here the row exists: finish creating, enqueueing and stamping even if
	// the client goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), expansionWriteTimeout)
	defer cancel()
	e, err := store.CreateExpansion(ctx, s.pool, store.NewExpansion{
		UserID: user.ID, FileName: fileName, SelectedText: selected, Question: question,
		Occurrence:     occurrenceParam(params.get("occurrence")),
		Mode:           mode,
		FallbackAnchor: fallback,
		UseSotaLLMs:    castBoolean(params.lookup("use_sota_llms")),
	})
	if err != nil {
		var invalid *store.InvalidExpansionError
		if errors.As(err, &invalid) {
			slog.Warn("expansion rejected by the store", "err", err)
			writeDetail(w, http.StatusBadRequest, msgInvalidExpansionText)
			return
		}
		s.serverFailure(w, r, err)
		return
	}

	if user.ExpansionMode != mode {
		// A remembered preference: failing to save it must not lose the request.
		if err := store.SetExpansionMode(ctx, s.pool, user.ID, mode); err != nil {
			slog.Error("could not remember the expansion mode", "user_id", user.ID, "err", err)
		}
	}

	if clicked := clientClickedParam(params.get("client_clicked_at")); clicked > 0 {
		store.StampExpansion(ctx, s.pool, e.ID, "client_clicked", clicked)
	}
	store.StampExpansion(ctx, s.pool, e.ID, "request_received", requestReceived)

	if err := s.enqueuer.Enqueue(ctx, jobs.KindGenerateExpansion, jobs.Args{ID: e.ID}, 0); err != nil {
		// Without a job the expansion would stay pending, and the page poll forever.
		// FailExpansion never overwrites a finished row, so a job that somehow
		// ran already keeps its outcome.
		if ferr := store.FailExpansion(ctx, s.pool, e.ID, store.ExpansionFailedDetail); ferr != nil && !errors.Is(ferr, store.ErrExpansionFinished) {
			slog.Error("could not fail an expansion that was not enqueued", "expansion_id", e.ID, "err", ferr)
		}
		s.serverFailure(w, r, err)
		return
	}
	store.StampExpansion(ctx, s.pool, e.ID, "job_enqueued", store.NowMS())

	writeStatus(w, http.StatusAccepted, e)
}

// showExpansion is GET /expansions/{id} (ExpansionsController#show): the
// status of one of the signed-in user's expansions. Another user's, a missing
// one and an id too large for int64 are all the same 404.
func (s *Server) showExpansion(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFrom(r.Context())
	if user == nil {
		s.unauthenticated(w, r)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeDetail(w, http.StatusNotFound, msgExpansionNotFound)
		return
	}
	e, err := store.FindUserExpansion(r.Context(), s.pool, user.ID, id)
	if err != nil {
		s.serverFailure(w, r, err)
		return
	}
	if e == nil {
		writeDetail(w, http.StatusNotFound, msgExpansionNotFound)
		return
	}
	writeStatus(w, http.StatusOK, e)
}

func (s *Server) serverFailure(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeDetail(w, http.StatusInternalServerError, msgInternal)
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// occurrenceParam is [params[:occurrence].to_i, 0].max, clamped to what the
// integer column holds.
func occurrenceParam(v string) int {
	return int(min(max(rubyToI(v), 0), math.MaxInt32))
}

// clientClickedParam is params[:client_clicked_at].to_i (epoch milliseconds);
// 0 means absent or garbage, which is not stamped.
func clientClickedParam(v string) int64 {
	if blank(v) {
		return 0
	}
	return rubyToI(v)
}

// rubyToI is String#to_i: optional leading whitespace and sign, then decimal
// digits (single underscores between digits allowed), stopping at the first
// other character; 0 when there are none. Too-big values saturate.
func rubyToI(s string) int64 {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	var n uint64
	saturated := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' && i > 0 && i+1 < len(s) && s[i-1] != '_' && s[i+1] >= '0' && s[i+1] <= '9' {
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		if n > (math.MaxInt64-uint64(c-'0'))/10 {
			saturated = true
			break
		}
		n = n*10 + uint64(c-'0')
	}
	switch {
	case saturated && neg:
		return math.MinInt64
	case saturated:
		return math.MaxInt64
	case neg:
		return -int64(n)
	}
	return int64(n)
}

// castBoolean is ActiveModel::Type::Boolean#cast(value) || false: an absent,
// null or empty value is false, so are the falsy spellings "0", "f", "F",
// "false", "FALSE", "off" and "OFF" (and JSON false and 0, which arrive as
// the same text), and anything else is true.
func castBoolean(v string, present bool) bool {
	if !present || v == "" {
		return false
	}
	switch v {
	case "0", "f", "F", "false", "FALSE", "off", "OFF":
		return false
	}
	return true
}

// writeStatus answers the status payload: {"id","status"}, plus "url" once
// completed and "detail" once failed, in that key order (Ruby's status_payload).
func writeStatus(w http.ResponseWriter, code int, e *store.Expansion) {
	var b bytes.Buffer
	b.WriteString(`{"id":`)
	b.WriteString(strconv.FormatInt(e.ID, 10))
	b.WriteString(`,"status":`)
	b.Write(marshalJSON(e.Status))
	switch e.Status {
	case "completed":
		b.WriteString(`,"url":`)
		b.Write(marshalJSON(e.URL))
	case "failed":
		b.WriteString(`,"detail":`)
		b.Write(marshalJSON(e.ErrorDetail))
	}
	b.WriteString(`}`)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if code != http.StatusOK {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(code)
	_, _ = w.Write(b.Bytes())
}
