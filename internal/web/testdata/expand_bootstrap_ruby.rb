# Regenerates expand_bootstrap_ruby.json: for each case, the input handed to the
# Rails helper (ExpandBootstrapHelper#expand_bootstrap_tags) and the exact string
# it returned. views_test.go feeds the same inputs to the Go port, so the two
# cannot drift. Run on a checkout that still has the Rails app:
#   RAILS_ENV=test bin/rails runner <this file> > expand_bootstrap_ruby.json
# The asset digest and CSRF token are stubbed; the Go test ignores both.
require "json"
h = ApplicationController.helpers
h.define_singleton_method(:form_authenticity_token) { "TOKEN" }
h.define_singleton_method(:asset_path) { |name, **| "/assets/#{name.sub(/\.js\z/, "-DIGEST.js")}" }

def v(name, version, current)
  { name: name, url: "/#{ERB::Util.url_encode(name)}", version: version, current: current }
end

NASTY_NAME = "a b&c<d>\".md"
cases = {
  "plain_empty" => { scroll_anchor: nil, file_versions: [], expansion_mode: "create_new" },
  "anchor_versions" => { scroll_anchor: "section-2", expansion_mode: "edit_in_place",
    file_versions: [v("notes.md", 1, false), v("notes-v2.md", 2, true)] },
  "nasty" => { scroll_anchor: "a\"b</script><!--&'\u00e9\u2028\u2029\u{1F600}", expansion_mode: "create_new",
    file_versions: [v(NASTY_NAME, 1, true), v("\u00e9.md", 2, false)] },
  "anchor_backslash_newline" => { scroll_anchor: "a\\b\nc\td\r\\n\u0000\u001f", expansion_mode: "create_new", file_versions: [] },
  "anchor_cdata_comment" => { scroll_anchor: "x]]>y<!--z-->w<![CDATA[q", expansion_mode: "create_new", file_versions: [] },
  "anchor_line_separators_only" => { scroll_anchor: "\u2028\u2029", expansion_mode: "create_new", file_versions: [] },
  "anchor_emoji_cjk" => { scroll_anchor: "\u{1F600}\u{1F469}\u200d\u{1F4BB} \u4e2d\u6587", expansion_mode: "create_new", file_versions: [] },
  "mode_with_quotes_and_script" => { scroll_anchor: nil, expansion_mode: "\"</script><b>'\\", file_versions: [] },
  "odd_version_names" => { scroll_anchor: nil, expansion_mode: "create_new",
    file_versions: [v("</script>.md", 1, false), v("%41 +&=?#.md", 2, true), v("  spaces  .md", 3, false),
                    v("\u{1F600}\u2028.md", 4, false), v("quote'\"back\\slash.md", 5, false)] },
  "blank_anchor_space" => { scroll_anchor: " ", expansion_mode: "create_new", file_versions: [] },
  "blank_anchor_spaces" => { scroll_anchor: "   ", expansion_mode: "create_new", file_versions: [] },
  "blank_anchor_crlf" => { scroll_anchor: "\r\n", expansion_mode: "create_new", file_versions: [] },
  "blank_anchor_tab_nbsp" => { scroll_anchor: "\t\u00a0\u3000", expansion_mode: "create_new", file_versions: [] },
  "blank_anchor_empty" => { scroll_anchor: "", expansion_mode: "create_new", file_versions: [] },
  "anchor_with_surrounding_space" => { scroll_anchor: " x ", expansion_mode: "create_new", file_versions: [] }
}
out = cases.transform_values do |c|
  { input: c, output: h.expand_bootstrap_tags(**c).to_str }
end
puts JSON.pretty_generate(out)
