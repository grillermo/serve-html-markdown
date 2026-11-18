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
  c("added_html_uppercase_tags", ".html", "<P>Alpha <EM>beta</EM> gamma</P>", "beta gamma"),

  # --- Promoted from the Ruby-vs-Go differential fuzzing (random Markdown/HTML
  #     documents, 480k cases, no divergences): shortest-to-typical cases that exercise
  #     each feature. Appended after the cases above so those stay unchanged. ---
  c("fuzz_md_multi_tick_code_span", ".txt", "   > cat <span>&lt; é</span> ```the &gt;beta``` the é\n```ruby\n````", "the", 0),
  c("fuzz_md_bold_snap", ".txt", "> dog __and x1__\n  日本語\n\t&#x;beta *alpha **do ** alpha* [and $5]\n", "&#x", 7),
  c("fuzz_md_underscore_emphasis", ".md", "dog 😀 ` ![1.5](http://x/(y)) cat\r\n###### and é _1.5 dog_ delta <a\n", " and é ", 0),
  c("fuzz_md_around_image", ".txt", "> <!-- 日本語 --> delta **the delta**\r\n[x `](y)` z ![the]() &a cat\r\n", "delta x ``", 0),
  c("fuzz_md_nested_link_label", ".md", "```\r\n- ```the beta```` [a [b](c) d](e)\n\t[a [b](c) d](e) <!-- dog -->\n\n", "```", 0),
  c("fuzz_md_escaped_bracket_label", ".txt", "####### _&quot; the_ &a$5 [`&#xFFFE;delta\\](http:///(y)) a.b\r\n\tthe\r\n\r\n", "the", 3),
  c("fuzz_md_entities_numeric_hex", ".md", "café cat $5 é\n~~~\n> 1.5 <span>alpha delta</span> 😀 cat &#x;\n```\n", "cat", 2),
  c("fuzz_md_nbsp_entity", ".md", "[a [b](c) d](e) [<a href=\"/o.html\">&#X41;😀 &nbsp;</a>](http://x/(y))", "bsp;)", -1),
  c("fuzz_md_tilde_fence_unsafe", ".txt", "# x1 café ñandú\n~~~\n# é [delta a.b] ñandú\n__alpha foo-bar__\n~~~", "elt", 0),
  c("fuzz_md_indented_code", ".txt", "    &#x1F60\u00000;cat 1.5 _foo-bar\tcafé\n   ```\n``\n\n    1.5 *a.b* the &AMP;", "a.b", 2),
  c("fuzz_md_combining_mark", ".md", "###### café x1\n- <!-- ñandú --> café <a href=\"/o.html\">beta beta\n\n", "-->", 0),
  c("fuzz_md_inline_html_br", ".txt", "\r\n\r\n>[x `](y)` z <br> a.b\r\n  a.bé[**&bogus;dog the**](http://x/(y))\r\n", "x ``", 2),
  c("fuzz_md_heading_cross_block", ".md", "# gamma\nx1 *café **1.5** 日本語* *é **1.5** 1.5* &#1114111; 😀\n", "gamma x1", 2),
  c("fuzz_md_existing_link_overlap", ".md", "\r\n   > [a [b](c) d](e) [the\\](http://x/(y)) [a [b](c) d](e) &#X41;\r\n\r\n", ")) [a ", 2),
  c("fuzz_md_blockquote_list", ".md", "> alpha _the delta_ <a href=\"/o.html\">and 1.5\n- the &#xE9;foo-bar\n\n", "éfoo-bar", 0),
  c("fuzz_md_not_found_entity_literal", ".md", "&gt; gamma <!-- beta -->\n\t\n> &amp;\n<br>f!oo-bar [a [b](c) d](e) !́\n\u0000\n", "ar [a ", 7),
  c("fuzz_html_script_ignored", ".html", "<br/> and <a href=\"/old.htl\">usd) gamma</a> <script></script > <span/>", "and", 0),
  c("fuzz_html_style_ligature_close", ".html", "<div><p>ñandú $5 <style>.a{}</ﬆyle>cat (usd) &#233;alpha</p></div>", "$5 <s", 3),
  c("fuzz_html_long_s_script_close", ".html", "=<a <script>var a = 1;</ſcript> café dog <p>*!-- be&a --> <span/></p>", "/p>", 2),
  c("fuzz_html_comment_between", ".html", "<p><!-- the --> <a title=\"a > b\">delta</a> cat cat a.b</p>\n\r\n<P>é $5", "cat", 2),
  c("fuzz_html_nbsp_entity", ".html", "<blockquote><!-- &nbsp;foo-bar -->&#xE9;cat</blockquote>\r\n<h1>a.b</h1>", "écat", 1),
  c("fuzz_html_misnested_inline", ".html", "<pre>&#xFFFE; <b>dog<i>cat</b>&a</i> <em>dog a.b</em> 日本語</pre>", "dog", 0),
  c("fuzz_html_pre_and_list", ".html", "<ul><li>😀 café é</li><li>é</li></ul>\r\n<pre>日本語 &lt;</pre>", "caf", 3),
  c("fuzz_html_cross_block_unsafe", ".html", "<h1>delta <!-- foo-bar --> 1.5</h1><div>é cat delta delta</div><P>the", "lta\nthe", 0),
  c("fuzz_html_existing_anchor_overlap", ".html", "<div><p>cat cat gamma café <a href=\"/old.html\">the alpha</a></p></div>", "alpha", 7),
  c("fuzz_html_around_anchor_segments", ".html", "<P>cat <a href=\"/old.html\">the the</a> foo-bar &#X41;\n<P>and dog and", "cat the the foo-bar", 0),
  c("fuzz_html_uppercase_tag_close", ".html", "<blockquote><br/> <Em> <b>foo-bar<i>é</b>&quot;x1</i></blockquote>", "́\"x1\n", 3),
  c("fuzz_html_unclosed_script_swallows", ".html", "and\"cat <scipt></p><p><script>", "and\"cat <scipt></p><p><script>", 0),
  c("fuzz_html_occurrence_third", ".html", "<p><!----> <span class=\"x\">é a.b</span> &#1114111;a.b</p>\n<p>1.5</p>", "1.5", 3),
  c("fuzz_md_occurrence_out_of_range", ".md", ">_&#x;a.b é_ __&#x;gamma &a日本語__\r\n&#x; **ñandú (usd) the** `", "the", 7),
  c("fuzz_md_tab_separator_selection", ".md", "###### *&#xE9;dog 1.5 the*\n###### beta <span>dog é</span> \\# cat a.b\n", "og\t1.5\tthe", 7),
  c("fuzz_md_vertical_tab_selection", ".txt", "####### _&quot; the_ &a$5 [`&#xFFFE;delta\\](http:///(y)) a.b\r\n\tthe\r\n\r\n", "\vthe\v&", 0),

  # --- NBSP quirks: plain text keeps a literal "&nbsp;" (CGI.unescapeHTML does
  #     not decode it), while &#160; and a raw U+00A0 become a non-ASCII
  #     character that is not whitespace for the selection's \s+ matching. ---
  c("nbsp_html_literal_entity_in_selection", ".html", "<p>say one&nbsp;two now</p>", "one&nbsp;two"),
  c("nbsp_md_literal_entity_in_selection", ".md", "say one&nbsp;two now\n", "one&nbsp;two"),
  c("nbsp_html_entity_selected_with_two_words", ".html", "<p>say one&nbsp;two now</p>", "say one two"),
  c("nbsp_html_numeric_entity_matches_nbsp_char", ".html", "<p>one&#160;two three</p>", "one\u00a0two"),
  c("nbsp_html_numeric_entity_not_ascii_space", ".html", "<p>one&#160;two three</p>", "one two"),
  c("nbsp_md_selection_nbsp_ascii_space_source", ".md", "one two three\n", "one\u00a0two"),
  c("nbsp_html_selection_nbsp_ascii_space_source", ".html", "<p>one two three</p>", "one\u00a0two"),
  c("nbsp_md_nbsp_char_next_to_space_in_source", ".md", "one\u00a0 two three\n", "one\u00a0 two"),
  c("nbsp_html_uppercase_entity_stays_literal", ".html", "<p>a&NBSP;b &AMP; c</p>", "&AMP; c"),

  # --- Non-monotone Markdown maps: a raw tag inside a link label overshoots the
  #     label end, so later plain characters map to smaller source offsets than
  #     earlier ones (found by targeted differential testing; Map#plain_indices_within
  #     must filter every index, not assume sorted offsets). ---
  c("nonmonotone_br_overshoots_label", ".md", "b[<br]())>e\n", ")"),
  c("nonmonotone_br_overshoots_label_bracket", ".md", "[vamp;<br]()]> z)*w\n", "]")
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
