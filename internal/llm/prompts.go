package llm

// AnchorSentinel is the marker a rewrite carries just before the expanded
// passage (ANCHOR_SENTINEL).
const AnchorSentinel = "⟦EXPANSION_ANCHOR⟧"

// ExpandPromptTemplate began as PROMPT_TEMPLATE of
// app/services/claude_expand_service.rb; since then it also asks for
// highlight.js-ready code blocks (see TestExpandPromptGolden). Its %{name} fields are filled by
// strings.Replacer, never by a format verb, so a "%" in a document is harmless.
const ExpandPromptTemplate = `You are given a document, a text selection from it, and a reader's question about that selection.

Write a complete standalone HTML page that answers the question and expands on the selected text with additional depth: background, context, related concepts, and concrete details the original document leaves out.

Requirements:
- Output ONLY the HTML document, starting with <!DOCTYPE html>. No markdown fences, no commentary.
- Dark theme, readable typography (max-width ~140ch, generous line-height), semantic HTML.
- Title the page after the selection.
- Ground the answer in the document's context, but bring in outside knowledge freely.
- Put every code sample, command, or config snippet in <pre><code class="language-LANG">...</code></pre>, where LANG is its highlight.js language name (python, javascript, typescript, ruby, go, bash, sql, json, yaml, html, css, plaintext, ...), with <, > and & escaped. The server loads highlight.js into the page to color these blocks, so do not include any syntax highlighting library or theme yourself, and do not color code by hand.

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

// RewritePromptTemplate began as REWRITE_PROMPT_TEMPLATE, with AnchorSentinel
// in place of its interpolation; it also asks for code blocks with a language.
const RewritePromptTemplate = `You are given a document, a text selection from it, and a reader's question about that selection.

Rewrite the document so the selected passage is expanded in light of the question: add the background, context, related concepts, and concrete details the original leaves out, woven into the document's own voice.

Requirements:
- Output the COMPLETE document, from its first line to its last. Never truncate, summarize, or elide with "...".
- Keep the document's original format exactly: Markdown stays Markdown, HTML stays HTML.
- Preserve every part of the document unrelated to the selection verbatim, including front matter, links, and code blocks.
- Give any code block you add a language: a fenced block names it (` + "```python" + `), an HTML one is <pre><code class="language-python">. The server highlights them with highlight.js.
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
