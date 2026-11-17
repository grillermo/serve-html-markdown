# Captures golden input/output pairs from the real Ruby SelectionLinker so the
# Go port can be verified byte-for-byte.
#
#   bin/rails runner script/golden_linker.rb
#
# Writes testdata/golden/linker/cases.json. Each entry has the keys
# name, extension, source, selected_text, occurrence and either output or error.

def c(name, extension, source, selected_text, occurrence = 0)
  { name: name, extension: extension, source: source, selected_text: selected_text, occurrence: occurrence }
end

OLD_ANCHOR_P = %(<p>before <a href="/old.html">beta</a> after</p>)

cases = [
  # --- From test/services/selection_linker_test.rb ---
  c("md_wraps_selection", ".md", "Alpha beta gamma.", "beta"),
  c("html_wraps_selection", ".html", "<p>Alpha beta gamma.</p>", "beta"),
  c("html_greater_than_text", ".html", "<p>1 > 0</p>", ">", 1),
  c("md_picks_requested_occurrence", ".md", "cat dog cat bird cat", "cat", 2),
  c("md_occurrence_out_of_range_falls_back", ".md", "cat dog cat", "cat", 9),
  c("md_not_found_absent_text", ".md", "Alpha beta.", "missing"),
  c("md_escapes_closing_bracket", ".md", "a b] c", "b]"),
  c("md_unsafe_inside_existing_link_label", ".md", "see [beta gamma](/old.html) end", "beta"),
  c("md_not_found_text_only_in_link_url", ".md", "see [label](/beta.html) end", "beta"),
  c("html_not_found_text_only_in_tag", ".html", %(<p class="beta">x</p>), "beta"),
  c("html_not_found_text_only_in_script", ".html", "<script>var beta = 1;</script><p>x</p>", "beta"),
  c("html_unsafe_inside_existing_anchor", ".html", %(<a href="/old.html">beta</a>), "beta"),
  c("html_not_found_raw_anchor_markup", ".html", OLD_ANCHOR_P, %(<a href="/old.html">beta</a>)),
  c("html_not_found_raw_closing_anchor_markup", ".html", OLD_ANCHOR_P, "beta</a> after"),
  c("md_not_found_raw_link_syntax", ".md", "see [beta](/old.html) end", "[beta](/old.html)"),
  c("md_not_found_raw_link_syntax_escaped_bracket", ".md", "see [b\\]](/old.html) end", "[b\\]](/old.html)"),
  c("md_not_found_raw_syntax_crossing_link", ".md", "see [beta](/old.html) and more", "[beta](/old.html) and"),
  c("html_unsafe_occurrence_judged_as_is", ".html", %(<a href="/old.html">beta</a> and beta again), "beta"),
  c("html_spans_inline_span", ".html", "this is <span> some </span> code", "some code"),
  c("html_matches_across_differing_whitespace", ".html", "<p>some\ncode here</p>", "some code"),
  c("html_entity_amp_preserved", ".html", "<p>A &amp; B here</p>", "A & B"),
  c("html_snaps_to_cover_partial_em", ".html", "<p>x <em>alpha beta</em> gamma</p>", "beta gamma"),
  c("html_segments_around_existing_anchor", ".html", OLD_ANCHOR_P, "before beta after"),
  c("html_unsafe_spans_two_paragraphs", ".html", "<p>one</p><p>two</p>", "one two"),
  c("html_occurrence_counts_rendered_text", ".html", %(<p class="beta">beta one beta two</p>), "beta", 1),
  c("md_spans_inline_code", ".md", "this is `some` code", "some code"),
  c("md_snaps_to_cover_partial_emphasis", ".md", "make *this bold* now ok", "bold now"),
  c("md_segments_around_existing_link", ".md", "see [beta](/old.html) and more", "beta and"),
  c("md_segments_around_image", ".md", "pic ![alt](i.png) end", "pic end"),
  c("md_unsafe_spans_two_paragraphs", ".md", "alpha\n\nbeta", "alpha beta"),
  c("md_unsafe_inside_fenced_code_block", ".md", "before\n\n```\ncode here\n```\n", "code here"),
  c("md_links_heading_text", ".md", "# Title\ntext", "Title"),
  c("md_links_one_list_item", ".md", "- alpha\n- beta\n", "alpha"),
  c("md_unsafe_across_list_items", ".md", "- alpha\n- beta\n", "alpha beta"),

  # --- Derived from test/services/selection_linker/*_test.rb (Map, Planner, Locator
  #     tests rephrased as full-link cases; none of those tests call .link) ---
  c("sub_md_paragraph_trailing_newline", ".md", "alpha beta\n", "beta"),
  c("sub_md_soft_line_break_same_paragraph", ".md", "alpha\nbeta\n", "alpha beta"),
  c("sub_md_heading_then_text_across_blocks", ".md", "# Title\ntext\n", "Title text"),
  c("sub_md_blockquote_prefix_dropped", ".md", "> quoted text\n", "quoted text"),
  c("sub_md_indented_code_block_unsafe", ".md", "before\n\n    indented code\n", "indented code"),
  c("sub_md_backslash_escape", ".md", "a \\* b\n", "a * b"),
  c("sub_md_link_label_unsafe", ".md", "see [beta](/old.html) end\n", "beta"),
  c("sub_md_emphasis_partial", ".md", "make *this bold* now\n", "bold now"),
  c("sub_md_unmatched_emphasis_literal", ".md", "5 * 3 is fifteen\n", "5 * 3"),
  c("sub_md_raw_inline_html_and_anchor", ".md", %(word <span>in span</span> and <a href="/o.html">old</a> end\n), "word in span and old end"),
  c("sub_md_entity_decoded", ".md", "A &amp; B\n", "A & B"),
  c("sub_html_ab_paragraph", ".html", "<p>ab</p>", "b"),
  c("sub_html_entity_amp_atomic", ".html", "<p>A &amp; B</p>", "& B"),
  c("sub_html_skips_style_contents", ".html", "<script>var beta = 1;</script><style>.x{}</style><p>x</p>", "x"),
  c("sub_html_inline_tags_same_block", ".html", "<p>one <em>two</em> three</p>", "one two three"),
  c("sub_html_em_inside_selection", ".html", "<p>a <em>b</em> c</p>", "a b c"),
  c("sub_html_anchor_only_selection_unsafe", ".html", %(<p><a href="/old.html">alpha beta</a></p>), "beta"),
  c("sub_html_br_newline", ".html", "<p>some<br>code</p>", "some code"),
  c("sub_html_stray_angle_and_comment", ".html", "<p>1 > 0</p><!-- note -->", "1 > 0"),
  c("sub_html_plain_match_within_paragraph", ".html", "<p>alpha beta gamma</p>", "beta"),
  c("sub_html_span_wraps_whole_selection", ".html", "<p><span>alpha beta gamma</span></p>", "beta"),
  c("sub_html_trims_whitespace_around_anchor", ".html", %(<p>one <a href="/old.html">beta</a> two</p>), "one beta two"),
  c("sub_locator_collapsed_whitespace", ".md", "some  \n code", "some code"),
  c("sub_locator_second_occurrence", ".md", "cat dog cat", "cat", 1),
  c("sub_locator_blank_selection", ".md", "alpha", "   "),
  c("sub_locator_regex_metacharacters", ".md", "cost is $5 (usd)", "$5 (usd)"),

  # --- Added for the Go port (multi-byte, entities, structure) ---
  c("added_md_across_bold", ".md", "Hello **big** world\n", "big world"),
  c("added_md_bold_partial_start", ".md", "Hello **big** world\n", "Hello big"),
  c("added_md_inside_bold", ".md", "Hello **big** world\n", "big"),
  c("added_md_inside_inline_code", ".md", "run `git status` now\n", "status"),
  c("added_md_whole_inline_code", ".md", "run `git status` now\n", "git status"),
  c("added_md_across_existing_link", ".md", "go to [the docs](/d.html) for more help\n", "to the docs for"),
  c("added_md_across_existing_link_unsafe_inside", ".md", "go to [the docs](/d.html) for more help\n", "the docs"),
  c("added_md_entity_amp", ".md", "Tom &amp; Jerry went home\n", "Tom & Jerry"),
  c("added_md_entity_nbsp_selected_as_space", ".md", "one&nbsp;two three\n", "one two"),
  c("added_md_entity_nbsp_selected_as_char", ".md", "one&nbsp;two three\n", "one\u00a0two"),
  c("added_md_literal_nbsp_both", ".md", "one two three\n", "one two"),
  c("added_md_literal_nbsp_char_in_source", ".md", "one\u00a0two three\n", "one two"),
  c("added_md_entity_numeric_eacute", ".md", "un caf&#233; con leche\n", "café"),
  c("added_md_multibyte_quotes_and_dash", ".md", "Dijo “ñandú” — café con leche.\n", "“ñandú” — café"),
  c("added_md_multibyte_before_selection", ".md", "ñandú y café: hello world\n", "hello"),
  c("added_md_emoji_before_selection", ".md", "😀 happy 😀 days\n", "days"),
  c("added_md_second_occurrence", ".md", "the cat and the cat and the cat\n", "the cat", 1),
  c("added_md_cross_paragraph", ".md", "First paragraph here.\n\nSecond paragraph there.\n", "here. Second"),
  c("added_md_not_found_different_text", ".md", "alpha beta\n", "alpha  gamma"),
  c("added_md_multiple_closing_brackets", ".md", "x [a] and [b] y\n", "[a] and [b]"),
  c("added_md_negative_occurrence", ".md", "cat dog cat", "cat", -3),
  c("added_md_blockquote_selection", ".md", "> quoted text here\n", "text here"),
  c("added_md_ordered_list_item", ".md", "1. first item\n2. second item\n", "second"),
  c("added_md_no_trailing_newline_end", ".md", "tail word", "word"),
  c("added_md_crlf_line_endings", ".md", "alpha\r\nbeta gamma\r\n", "beta"),
  c("added_md_unknown_extension_treated_as_markdown", ".txt", "Alpha beta gamma.", "beta"),
  c("added_html_entity_nbsp_selected_as_space", ".html", "<p>one&nbsp;two three</p>", "one two"),
  c("added_html_entity_nbsp_selected_as_char", ".html", "<p>one&nbsp;two three</p>", "one\u00a0two"),
  c("added_html_literal_nbsp_char_in_source", ".html", "<p>one\u00a0two three</p>", "one two"),
  c("added_html_entity_numeric_eacute", ".html", "<p>un caf&#233; con leche</p>", "café"),
  c("added_html_entity_amp_selection_start", ".html", "<p>x &amp; y &amp; z</p>", "y & z"),
  c("added_html_multibyte_quotes_and_dash", ".html", "<p>Dijo “ñandú” — café con leche.</p>", "“ñandú” — café"),
  c("added_html_multibyte_inside_em", ".html", "<p>Un <em>ñandú</em> corre</p>", "ñandú corre"),
  c("added_html_emoji_before_selection", ".html", "<p>😀 happy 😀 days</p>", "days"),
  c("added_html_second_occurrence", ".html", "<p>the cat and the cat</p>", "the cat", 1),
  c("added_html_cross_paragraph_unsafe", ".html", "<p>First here.</p>\n<p>Second there.</p>", "here. Second"),
  c("added_html_em_and_br", ".html", "<p>Hello <em>big</em><br>wide world</p>", "big wide"),
  c("added_html_em_br_selection_inside_em", ".html", "<p>Hello <em>big</em><br>wide world</p>", "big"),
  c("added_html_partial_em_from_start", ".html", "<p>x <em>alpha beta</em> gamma</p>", "x alpha"),
  c("added_html_nested_inline_tags", ".html", "<p>a <strong>b <em>c d</em></strong> e</p>", "c d e"),
  c("added_html_not_found_entity_literal", ".html", "<p>A &amp; B here</p>", "A &amp; B"),
  c("added_html_attributes_with_gt", ".html", %(<p title="a > b">hello world</p>), "hello"),
  c("added_html_comment_ignored", ".html", "<p>one <!-- hidden two --> three</p>", "hidden"),
  c("added_html_style_block_ignored", ".html", "<style>.beta{}</style><p>beta here</p>", "beta"),
  c("added_html_blank_selection", ".html", "<p>alpha</p>", ""),
  c("added_html_full_document", ".html", "<!DOCTYPE html>\n<html><head><title>T</title></head>\n<body>\n<h1>Heading</h1>\n<p>Body text here.</p>\n</body></html>\n", "text here"),
  c("added_html_heading_then_paragraph_unsafe", ".html", "<h1>Heading</h1><p>Body</p>", "Heading Body"),
  c("added_html_list_items_unsafe", ".html", "<ul><li>alpha</li><li>beta</li></ul>", "alpha beta"),
  c("added_html_list_item", ".html", "<ul><li>alpha</li><li>beta</li></ul>", "beta"),
  c("added_html_pre_block", ".html", "<pre>code here</pre>", "code here"),
  c("added_html_uppercase_tags", ".html", "<P>Alpha <EM>beta</EM> gamma</P>", "beta gamma")
]

names = cases.map { |x| x[:name] }
dupes = names.tally.select { |_, n| n > 1 }.keys
abort "duplicate case names: #{dupes.inspect}" if dupes.any?

out = cases.map do |c|
  result = SelectionLinker.link(
    source: c[:source], extension: c[:extension], selected_text: c[:selected_text],
    occurrence: c[:occurrence], url: "/x--expand-1.html"
  )
  c.merge(output: result)
rescue SelectionLinker::Error => e
  c.merge(error: "#{e.class.name.demodulize}: #{e.message}")
end

File.write(Rails.root.join("testdata/golden/linker/cases.json"), JSON.pretty_generate(out) + "\n")
puts "wrote #{out.size} cases (#{out.count { |x| x.key?(:output) }} output, #{out.count { |x| x.key?(:error) }} error)"
