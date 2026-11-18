package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Settings of the expansion LLM, as ClaudeExpandService had them.
const (
	// DefaultClaudeModel is used when EXPANSION_CLAUDE_MODEL is not set.
	DefaultClaudeModel = "sonnet"
	// CodexModel is the model of the codex fallback.
	CodexModel = "earth"
	// OpenAIExpandModel is the model of the OpenAI path.
	OpenAIExpandModel = "gpt-5.6-terra"
	// OpenAIReasoningEffort is its reasoning.effort.
	OpenAIReasoningEffort = "medium"
	// CommandTimeout bounds each CLI call (claude, then codex).
	CommandTimeout = 120 * time.Second
	// maxCommandOutput caps the stdout and the codex output file that are read,
	// so a runaway process cannot exhaust memory.
	maxCommandOutput = 16 << 20
	// maxCommandStderr caps what is kept of stderr (which is never used).
	maxCommandStderr = 1 << 20
)

// Stages stamped on the expansion while it is generated.
const (
	StageRequestStart = "llm_request_start"
	StageFirstFailure = "llm_first_failure"
	StageResponse     = "llm_response"
)

// AnchorSentinel is the marker a rewrite carries just before the expanded
// passage (ANCHOR_SENTINEL).
const AnchorSentinel = "⟦EXPANSION_ANCHOR⟧"

// ExpandPromptTemplate is PROMPT_TEMPLATE of app/services/claude_expand_service.rb
// byte for byte (see expand_test.go). Its %{name} fields are filled by
// strings.Replacer, never by a format verb, so a "%" in a document is harmless.
const ExpandPromptTemplate = `You are given a document, a text selection from it, and a reader's question about that selection.

Write a complete standalone HTML page that answers the question and expands on the selected text with additional depth: background, context, related concepts, and concrete details the original document leaves out.

Requirements:
- Output ONLY the HTML document, starting with <!DOCTYPE html>. No markdown fences, no commentary.
- Dark theme, readable typography (max-width ~70ch, generous line-height), semantic HTML.
- Title the page after the selection.
- Ground the answer in the document's context, but bring in outside knowledge freely.

<document filename="%{file_name}">
%{document}
</document>

<selection>
%{selection}
</selection>

<question>
%{question}
</question>
`

// RewritePromptTemplate is REWRITE_PROMPT_TEMPLATE, with AnchorSentinel in place
// of its interpolation.
const RewritePromptTemplate = `You are given a document, a text selection from it, and a reader's question about that selection.

Rewrite the document so the selected passage is expanded in light of the question: add the background, context, related concepts, and concrete details the original leaves out, woven into the document's own voice.

Requirements:
- Output the COMPLETE document, from its first line to its last. Never truncate, summarize, or elide with "...".
- Keep the document's original format exactly: Markdown stays Markdown, HTML stays HTML.
- Preserve every part of the document unrelated to the selection verbatim, including front matter, links, and code blocks.
- Immediately before the expanded passage, emit the marker ⟦EXPANSION_ANCHOR⟧ on a line of its own. Emit it exactly once.
- Output ONLY the document. No markdown fences, no commentary.

<document filename="%{file_name}">
%{document}
</document>

<selection>
%{selection}
</selection>

<question>
%{question}
</question>
`

// ExpandError is a failed generation. Message is generic and safe to log; it
// never holds process output, a prompt, the API key or the upstream's words.
// The underlying cause, when there is one, is reachable with errors.Is/As but
// not printed by Error.
type ExpandError struct {
	Message string
	cause   error
}

func (e *ExpandError) Error() string { return e.Message }
func (e *ExpandError) Unwrap() error { return e.cause }

// ErrExpand makes an ExpandError with msg.
func ErrExpand(msg string) error { return &ExpandError{Message: msg} }

func expandErr(msg string, cause error) error { return &ExpandError{Message: msg, cause: cause} }

// Recorder is what the service reports to the expansion it works for: the
// stages of its life (see the Stage constants) and which provider answered.
// store-backed adapters live in the packages that own the store.
type Recorder interface {
	Stamp(stage string)
	RecordResponse(provider string, htmlBytes int)
}

// Runner starts argv (no shell) and returns what the process wrote. A non-nil
// error means it failed: it could not start, exited non-zero, timed out or
// wrote too much. The error text is never shown to users.
type Runner func(ctx context.Context, argv []string) (stdout, stderr []byte, err error)

// Params is one generation request.
type Params struct {
	// FileName is the base name of the document, embedded in the prompt.
	FileName  string
	Document  string
	Selection string
	Question  string
	// UseOpenAI sends the prompt to OpenAI only (no CLI, and no fallback).
	UseOpenAI bool
}

// ExpandService turns a document, a selection and a question into a page
// (Expand) or into the rewritten document (Rewrite): through the claude CLI,
// then the codex CLI when claude fails, or through OpenAI on request.
type ExpandService struct {
	// ClaudeModel is passed to claude --model; "" means DefaultClaudeModel.
	ClaudeModel string
	// OpenAI is the client of the OpenAI path, built with the
	// EXPANSION_LLM_API_KEY; nil or a blank key means "not configured".
	OpenAI *Client
	// Runner starts the CLIs; nil means the real process runner, which kills the
	// whole process group after CommandTimeout.
	Runner Runner
}

// NewExpandService returns a service for the real CLIs and the OpenAI endpoint.
func NewExpandService(claudeModel, openAIKey string) *ExpandService {
	return &ExpandService{ClaudeModel: claudeModel, OpenAI: NewClient(openAIKey)}
}

// Expand returns a standalone HTML page that answers the question. rec may be
// nil. Failures are *ExpandError.
func (s *ExpandService) Expand(ctx context.Context, p Params, rec Recorder) (string, error) {
	return s.generate(ctx, ExpandPromptTemplate, ensureHTML, false, p, rec)
}

// Rewrite returns the whole document rewritten with the selection expanded,
// carrying AnchorSentinel before the expanded passage. For an .html document the
// result must look like HTML; for any other it must be non-blank, and its
// surrounding whitespace is kept.
func (s *ExpandService) Rewrite(ctx context.Context, p Params, rec Recorder) (string, error) {
	validate := ensurePresent
	if strings.EqualFold(rubyExt(p.FileName), ".html") {
		validate = ensureHTML
	}
	return s.generate(ctx, RewritePromptTemplate, validate, true, p, rec)
}

type validator func(string) (string, error)

func (s *ExpandService) generate(ctx context.Context, template string, validate validator,
	preserveWhitespace bool, p Params, rec Recorder) (string, error) {
	prompt := strings.NewReplacer(
		"%{file_name}", p.FileName,
		"%{document}", p.Document,
		"%{selection}", p.Selection,
		"%{question}", p.Question,
	).Replace(template)
	finish := func(text string) (string, error) {
		return validate(stripFence(text, preserveWhitespace))
	}
	stamp := func(stage string) {
		if rec != nil {
			rec.Stamp(stage)
		}
	}
	done := func(provider, html string) (string, error) {
		slog.Info("[ClaudeExpandService] succeeded", "provider", provider, "bytes", len(html))
		stamp(StageResponse)
		if rec != nil {
			rec.RecordResponse(provider, len(html))
		}
		return html, nil
	}

	slog.Info("[ClaudeExpandService] generating", "file", p.FileName, "selection_bytes", len(p.Selection),
		"question_bytes", len(p.Question), "use_openai", p.UseOpenAI)
	stamp(StageRequestStart)

	if p.UseOpenAI {
		text, err := s.runOpenAI(ctx, prompt)
		if err == nil {
			text, err = finish(text)
		}
		if err != nil {
			return "", err
		}
		return done("openai", text)
	}

	text, err := s.runClaude(ctx, prompt)
	if err == nil {
		text, err = finish(text)
	}
	if err == nil {
		return done("claude", text)
	}
	if ctx.Err() != nil {
		return "", expandErr("generation was cancelled", ctx.Err())
	}
	slog.Warn("[ClaudeExpandService] claude failed, falling back to codex", "reason", err.Error())
	stamp(StageFirstFailure)
	text, err = s.runCodex(ctx, prompt)
	if err == nil {
		text, err = finish(text)
	}
	if err != nil {
		return "", err
	}
	return done("codex", text)
}

func (s *ExpandService) run(ctx context.Context, argv []string) ([]byte, []byte, error) {
	if s.Runner != nil {
		return s.Runner(ctx, argv)
	}
	return execRunner(ctx, argv)
}

func (s *ExpandService) runOpenAI(ctx context.Context, prompt string) (string, error) {
	if s.OpenAI == nil {
		return "", expandErr("EXPANSION_LLM_API_KEY is not configured.", ErrConfiguration)
	}
	text, err := s.OpenAI.Generate(ctx, Request{
		Model:           OpenAIExpandModel,
		Input:           prompt,
		ReasoningEffort: OpenAIReasoningEffort,
	})
	if errors.Is(err, ErrConfiguration) {
		return "", expandErr("EXPANSION_LLM_API_KEY is not configured.", err)
	}
	if err != nil {
		return "", expandErr("openai request failed", err)
	}
	return text, nil
}

func (s *ExpandService) runClaude(ctx context.Context, prompt string) (string, error) {
	model := s.ClaudeModel
	if model == "" {
		model = DefaultClaudeModel
	}
	stdout, _, err := s.run(ctx, []string{
		"claude", "-p", prompt,
		"--model", model,
		"--output-format", "json",
		"--tools", "",
	})
	if err != nil {
		return "", commandError("claude", err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &parsed); err != nil || parsed == nil {
		return "", ErrExpand("claude output was not JSON")
	}
	if rubyTruthy(parsed["is_error"]) {
		return "", ErrExpand("claude returned error")
	}
	return rubyToS(parsed["result"]), nil
}

func (s *ExpandService) runCodex(ctx context.Context, prompt string) (string, error) {
	out, err := os.CreateTemp("", "expansion*.html")
	if err != nil {
		return "", expandErr("codex CLI could not be started", err)
	}
	path := out.Name()
	defer os.Remove(path)
	_ = out.Close()

	_, _, err = s.run(ctx, []string{
		"codex", "exec",
		"-m", CodexModel,
		"-s", "read-only",
		"--skip-git-repo-check",
		"--color", "never",
		"-o", path,
		prompt,
	})
	if err != nil {
		return "", commandError("codex", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", expandErr("codex CLI failed", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCommandOutput+1))
	if err != nil || len(data) > maxCommandOutput {
		return "", ErrExpand("codex output was unreadable")
	}
	return string(data), nil
}

// commandError turns a Runner error into the generic ExpandError of the
// command; the error text itself (which may hold paths or output) is dropped.
func commandError(name string, err error) error {
	var exitErr *exec.ExitError
	var execErr *exec.Error
	var pathErr *fs.PathError
	var errno syscall.Errno
	switch {
	case errors.Is(err, errTimedOut):
		return expandErr(fmt.Sprintf("%s timed out after %ds", name, int(CommandTimeout/time.Second)), err)
	case errors.Is(err, errOutputTooLarge):
		return expandErr(name+" output was too large", err)
	case errors.Is(err, syscall.E2BIG):
		return expandErr(name+" CLI could not be started: the prompt is too large", err)
	case errors.As(err, &exitErr):
		return expandErr(name+" CLI failed", err)
	case errors.As(err, &execErr), errors.As(err, &pathErr), errors.As(err, &errno):
		return expandErr(name+" CLI could not be started", err)
	}
	return expandErr(name+" CLI failed", err)
}

var (
	errTimedOut       = errors.New("command timed out")
	errOutputTooLarge = errors.New("command output too large")
)

var execRunner = newExecRunner(CommandTimeout)

// newExecRunner returns the real Runner: it runs argv directly (no shell) with
// the inherited environment and no stdin, in its own process group, and kills
// that whole group when timeout passes, the context ends or stdout exceeds
// maxCommandOutput, so claude's and codex's children never linger.
func newExecRunner(timeout time.Duration) Runner {
	return func(ctx context.Context, argv []string) ([]byte, []byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin = nil
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			// The leader has not been reaped yet, so its pid is still the group's.
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			return nil
		}
		// A grandchild that keeps the pipes open must not hold Wait forever.
		cmd.WaitDelay = 2 * time.Second
		out := &capWriter{limit: maxCommandOutput, fail: true, cancel: cancel}
		errb := &capWriter{limit: maxCommandStderr}
		cmd.Stdout, cmd.Stderr = out, errb
		err := cmd.Run()
		switch {
		case out.exceeded:
			return nil, nil, fmt.Errorf("%s: %w", argv[0], errOutputTooLarge)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, nil, fmt.Errorf("%s: %w", argv[0], errTimedOut)
		}
		return out.buf.Bytes(), errb.buf.Bytes(), err
	}
}

// capWriter keeps at most limit bytes. Past it, a failing writer cancels the
// command and errors (the process gets SIGPIPE/SIGKILL), a plain one discards.
type capWriter struct {
	buf      bytes.Buffer
	limit    int
	fail     bool
	cancel   context.CancelFunc
	exceeded bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); len(p) <= room {
		return w.buf.Write(p)
	} else if room > 0 {
		w.buf.Write(p[:room])
	}
	if w.fail {
		w.exceeded = true
		w.cancel()
		return 0, errOutputTooLarge
	}
	return len(p), nil
}

// rubyStrip is String#strip: ASCII whitespace and NUL only.
func rubyStrip(s string) string {
	return strings.Trim(s, "\x00\t\n\v\f\r ")
}

var (
	fenceOpen = regexp.MustCompile("(?i)\\A```[a-z]*\n")
	fenceEnd  = regexp.MustCompile("\n```\\z")
	htmlTag   = regexp.MustCompile(`(?i)<html`)
)

// stripFence unwraps a ```lang fence. Other text is stripped, unless
// preserveWhitespace, which keeps it as is (a fenced text is always stripped).
func stripFence(text string, preserveWhitespace bool) string {
	stripped := rubyStrip(text)
	if strings.HasPrefix(stripped, "```") {
		return fenceEnd.ReplaceAllString(fenceOpen.ReplaceAllString(stripped, ""), "")
	}
	if preserveWhitespace {
		return text
	}
	return stripped
}

func ensureHTML(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", ErrExpand("output was not valid UTF-8")
	}
	if !htmlTag.MatchString(text) {
		return "", ErrExpand("output does not look like HTML")
	}
	return text, nil
}

func ensurePresent(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", ErrExpand("output was not valid UTF-8")
	}
	if rubyStrip(text) == "" {
		return "", ErrExpand("output was empty")
	}
	return text, nil
}

// rubyTruthy is Ruby truthiness of a JSON value: everything but null, false and
// an absent key.
func rubyTruthy(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "false"
}

// rubyToS is `value.to_s` for the "result" field: a string as is, nil as "".
func rubyToS(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	if t := strings.TrimSpace(string(raw)); t != "null" {
		return t
	}
	return ""
}

// rubyExt is File.extname of a file name.
func rubyExt(name string) string {
	return filepath.Ext(strings.TrimLeft(filepath.Base(name), "."))
}
