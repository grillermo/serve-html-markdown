# Regenerates expand_bootstrap_ruby.json, the output of the Rails helper that
# the Go port is tested against (Task 4.4). Run on a checkout that still has
# the Rails app:  RAILS_ENV=test bin/rails runner <this file> > expand_bootstrap_ruby.json
# The asset digest and CSRF token are stubbed; the Go test ignores both.
require "json"
h = ApplicationController.helpers
h.define_singleton_method(:form_authenticity_token) { "TOKEN" }
h.define_singleton_method(:asset_path) { |name, **| "/assets/#{name.sub(/\.js\z/, "-DIGEST.js")}" }
cases = {
  "plain_empty" => { scroll_anchor: nil, file_versions: [], expansion_mode: "create_new" },
  "anchor_versions" => { scroll_anchor: "section-2", expansion_mode: "edit_in_place",
    file_versions: [
      { name: "notes.md", url: "/notes.md", version: 1, current: false },
      { name: "notes-v2.md", url: "/notes-v2.md", version: 2, current: true }] },
  "nasty" => { scroll_anchor: "a\"b</script><!--&'\u00e9\u2028\u2029\u{1F600}", expansion_mode: "create_new",
    file_versions: [
      { name: "a b&c<d>\".md", url: "/#{ERB::Util.url_encode("a b&c<d>\".md")}", version: 1, current: true },
      { name: "é.md", url: "/#{ERB::Util.url_encode("é.md")}", version: 2, current: false }] }
}
out = cases.transform_values { |c| h.expand_bootstrap_tags(**c).to_str }
puts JSON.pretty_generate(out)
