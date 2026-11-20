package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Settings of the expansion LLM, as ClaudeExpandService had them.
const (
	// DefaultClaudeModel is used when EXPANSION_CLAUDE_MODEL is not set.
	DefaultClaudeModel = "sonnet"
	// CodexModel is the model of the codex fallback.
	CodexModel = "earth"
	// OpenAIExpandModel is the model of the OpenAI path (now sota-llms' agy model).
	OpenAIExpandModel = DefaultModel
)

// Stages stamped on the expansion while it is generated.
const (
	StageRequestStart = "llm_request_start"
	StageFirstFailure = "llm_first_failure"
	StageResponse     = "llm_response"
)

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
	if !p.UseOpenAI && len(prompt) > MaxPromptArgBytes {
		// The CLIs take the prompt as an argument, which the OS limits (see
		// promptArgLimit): refuse now rather than spawn two doomed processes.
		slog.Warn("[ClaudeExpandService] prompt too large for the CLIs", "file", p.FileName, "prompt_bytes", len(prompt))
		return "", ErrPromptTooLarge
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
		Model: OpenAIExpandModel,
		Input: prompt,
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
