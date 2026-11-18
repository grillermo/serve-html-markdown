package llm

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
