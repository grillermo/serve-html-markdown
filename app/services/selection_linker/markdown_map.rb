class SelectionLinker
  # Temporary: markdown routes through the HTML tokenizer until the real
  # markdown map lands. Replaced in the markdown-map task.
  class MarkdownMap
    def self.build(source)
      HtmlMap.build(source)
    end
  end
end
