# The two render paths (a layout for Markdown, string injection for raw HTML)
# both need the same client bootstrap. Keeping it here means expand.js gets
# identical data whichever way the document was served.
module ExpandBootstrapHelper
  def expand_bootstrap_tags(scroll_anchor:, file_versions:, expansion_mode:)
    script = +""
    script << "window.__scrollAnchor = #{scroll_anchor.to_json};" if scroll_anchor.present?
    script << "window.__fileVersions = #{file_versions.to_json};"
    script << "window.__expansionMode = #{expansion_mode.to_json};"

    safe_join([
      tag.meta(name: "csrf-token", content: form_authenticity_token),
      tag.script(raw(script)),
      raw(%(<script src="#{asset_path("expand.js")}" defer></script>))
    ])
  end
end
