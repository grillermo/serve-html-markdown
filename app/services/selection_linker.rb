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
    map = build_map
    plain_range = SelectionLocator.locate(map, @selected_text, @occurrence)
    segments = Planner.plan(map, plain_range, source: @source)
    splice(segments)
  end

  private
    def markdown?
      @extension != ".html"
    end

    def build_map
      markdown? ? MarkdownMap.build(@source) : HtmlMap.build(@source)
    end

    def splice(segments)
      result = @source.dup
      segments.sort_by(&:begin).reverse_each do |segment|
        slice = result[segment]
        result[segment] = markdown? ? markdown_link(slice) : html_link(slice)
      end
      result
    end

    def html_link(slice)
      %(<a href="#{@url}">#{slice}</a>)
    end

    def markdown_link(slice)
      label = slice.gsub(/(?<!\\)\]/) { "\\]" }
      "[#{label}](#{@url})"
    end
end
