class SelectionLinker
  Error = Class.new(StandardError)
  NotFound = Class.new(Error)
  UnsafeMatch = Class.new(Error)

  def self.link(source:, extension:, selected_text:, occurrence:, url:)
    new(source, extension, selected_text, occurrence, url).link
  end

  def initialize(source, extension, selected_text, occurrence, url)
    @source = source
    @extension = extension
    @selected_text = selected_text
    @occurrence = [occurrence.to_i, 0].max
    @url = url
  end

  def link
    index = match_index
    prefix = @source[0, index]
    suffix = @source[(index + @selected_text.length)..]

    if @extension == ".html"
      ensure_safe_html!(prefix)
      "#{prefix}<a href=\"#{@url}\">#{@selected_text}</a>#{suffix}"
    else
      ensure_safe_markdown!(prefix)
      label = @selected_text.gsub("]", "\\]")
      "#{prefix}[#{label}](#{@url})#{suffix}"
    end
  end

  private
    def match_index
      indices = []
      position = 0
      while (found = @source.index(@selected_text, position))
        indices << found
        position = found + 1
      end
      if indices.empty?
        raise NotFound, "Selection not found in source — select a plainer run of text."
      end

      indices.fetch(@occurrence, indices.first)
    end

    def ensure_safe_html!(prefix)
      last_lt = prefix.rindex("<")
      last_gt = prefix.rindex(">")
      if last_lt && (last_gt.nil? || last_lt > last_gt)
        raise UnsafeMatch, "Selection falls inside an HTML tag."
      end
      %w[script style a].each do |tag|
        opens = prefix.scan(/<#{tag}\b/i).length
        closes = prefix.scan(%r{</#{tag}\s*>}i).length
        raise UnsafeMatch, "Selection falls inside a <#{tag}> element." if opens > closes
      end
    end

    def ensure_safe_markdown!(prefix)
      last_open = prefix.rindex("[")
      last_close = prefix.rindex("]")
      if last_open && (last_close.nil? || last_open > last_close)
        raise UnsafeMatch, "Selection falls inside a markdown link label."
      end

      last_link_open = prefix.rindex("](")
      last_paren_close = prefix.rindex(")")
      if last_link_open && (last_paren_close.nil? || last_link_open > last_paren_close)
        raise UnsafeMatch, "Selection falls inside a markdown link URL."
      end
    end
end
