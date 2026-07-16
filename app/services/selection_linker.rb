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
    selection = @source[index, @selected_text.length]
    suffix = @source[(index + @selected_text.length)..]

    if @extension == ".html"
      ensure_safe_html!(prefix, selection)
      "#{prefix}<a href=\"#{@url}\">#{@selected_text}</a>#{suffix}"
    else
      ensure_safe_markdown!(prefix, index, index + @selected_text.length)
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

    def ensure_safe_html!(prefix, selection)
      if selection.match?(/<\s*\/?\s*[A-Za-z][^>]*(?:>|\z)/)
        raise UnsafeMatch, "Selection includes HTML markup."
      end

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

    def ensure_safe_markdown!(prefix, selection_start, selection_end)
      position = 0
      while (match = @source.match(/\[(?:\\.|[^\]])*\]\([^)]*\)/, position))
        match_start = match.begin(0)
        match_end = match.end(0)
        if match_start < selection_end && match_end > selection_start
          raise UnsafeMatch, "Selection overlaps an existing markdown link."
        end
        position = match_end
      end

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
