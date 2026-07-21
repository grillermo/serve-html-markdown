require "cgi"

class SelectionLinker
  class MarkdownMap
    FENCE = /\A(`{3,}|~{3,})/
    HEADING = /\A\#{1,6}[ \t]+/
    LIST_ITEM = /\A[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+/
    BLOCKQUOTE = /\A[ \t]{0,3}>[ \t]?/
    INDENTED_CODE = /\A(?: {4}|\t)/
    INLINE_TOKEN = %r(
      \\[!-/:-@\[-`{-~] |
      `+ |
      !?\[ |
      </?[A-Za-z][^>\n]*> |
      &(?:[A-Za-z][A-Za-z0-9]*|\#[0-9]+|\#x[0-9A-Fa-f]+); |
      \*{1,2} | _{1,2}
    )x

    def self.build(source)
      new(source).build
    end

    def initialize(source)
      @source = source
      @map = Map.new(plain: +"", starts: [], ends: [], block_ids: [],
                     inline_ranges: [], link_ranges: [], unlinkable_ranges: [])
      @block_id = 0
      @emphasis_stack = []
      @html_stack = []
    end

    def build
      offset = 0
      in_fence = false
      fence_char = nil
      previous_blank = true
      in_indented_code = false

      @source.each_line do |line|
        line_start = offset
        offset += line.length
        content = line.chomp
        content_end = line_start + content.length

        if in_fence
          if content.lstrip.match?(FENCE) && content.lstrip[0] == fence_char
            in_fence = false
            @block_id += 1
          else
            emit_verbatim(line_start, line_start + line.length)
            @map.unlinkable_ranges << (line_start...(line_start + line.length))
          end
          next
        end

        if (fence = content.lstrip[FENCE, 1])
          in_fence = true
          fence_char = fence[0]
          @block_id += 1
          previous_blank = false
          next
        end

        if content.strip.empty?
          @block_id += 1
          previous_blank = true
          in_indented_code = false
          next
        end

        if content.match?(INDENTED_CODE) && (previous_blank || in_indented_code)
          @block_id += 1 unless in_indented_code
          in_indented_code = true
          emit_verbatim(line_start, line_start + line.length)
          @map.unlinkable_ranges << (line_start...(line_start + line.length))
          previous_blank = false
          next
        end
        in_indented_code = false

        scan_start = line_start
        heading = false
        if (marker = content.match(HEADING))
          @block_id += 1
          heading = true
          scan_start = line_start + marker.end(0)
        elsif (marker = content.match(LIST_ITEM))
          @block_id += 1
          scan_start = line_start + marker.end(0)
        elsif (marker = content.match(BLOCKQUOTE))
          scan_start = line_start + marker.end(0)
        end

        scan_inline(scan_start, content_end)
        emit_char("\n", content_end, content_end + 1) if line.end_with?("\n")
        @block_id += 1 if heading
        previous_blank = false
      end
      @map
    end

    private
      def scan_inline(from, upto)
        position = from
        while position < upto
          token = @source.match(INLINE_TOKEN, position)
          if token.nil? || token.begin(0) >= upto
            emit_verbatim(position, upto)
            return
          end
          emit_verbatim(position, token.begin(0))
          position = handle_inline_token(token, upto)
        end
      end

      def handle_inline_token(token, upto)
        text = token[0]
        start = token.begin(0)
        finish = token.end(0)
        case text
        when /\A\\/ then escape(text, start, finish)
        when /\A`/ then code_span(start, text.length, upto)
        when "![" then image(start, upto)
        when "[" then link(start, upto)
        when /\A</ then html_tag(text, start, finish)
        when /\A&/ then entity(text, start, finish)
        else emphasis(text, start, finish)
        end
      end

      def escape(text, start, finish)
        emit_char(text[1], start, finish)
        finish
      end

      def code_span(start, tick_count, upto)
        opener_end = start + tick_count
        closer = @source.match(/(?<!`)`{#{tick_count}}(?!`)/, opener_end)
        if closer.nil? || closer.end(0) > upto
          emit_verbatim(start, opener_end)
          return opener_end
        end

        emit_verbatim(opener_end, closer.begin(0))
        @map.inline_ranges << (start...closer.end(0))
        closer.end(0)
      end

      def link(start, upto)
        label_end = find_label_end(start + 1, upto)
        if label_end && @source[label_end + 1] == "("
          close = @source.index(")", label_end + 2)
          if close && close < upto
            scan_inline(start + 1, label_end)
            @map.link_ranges << (start...(close + 1))
            return close + 1
          end
        end
        emit_verbatim(start, start + 1)
        start + 1
      end

      def image(start, upto)
        label_end = find_label_end(start + 2, upto)
        if label_end && @source[label_end + 1] == "("
          close = @source.index(")", label_end + 2)
          if close && close < upto
            @map.link_ranges << (start...(close + 1))
            return close + 1
          end
        end
        emit_verbatim(start, start + 1)
        start + 1
      end

      def find_label_end(from, upto)
        position = from
        while position < upto
          char = @source[position]
          return position if char == "]"

          position += char == "\\" ? 2 : 1
        end
        nil
      end

      def html_tag(text, start, finish)
        name = text[%r{\A</?([A-Za-z][A-Za-z0-9-]*)}, 1].downcase
        if name == "br"
          emit_char("\n", finish, finish)
        elsif text.start_with?("</")
          index = @html_stack.rindex { |entry| entry.first == name }
          if index
            _, open_start = @html_stack.delete_at(index)
            element = open_start...finish
            @map.inline_ranges << element
            @map.link_ranges << element if name == "a"
          end
        elsif HtmlMap::INLINE_TAGS.include?(name) && !text.end_with?("/>")
          @html_stack << [name, start]
        end
        finish
      end

      def entity(text, start, finish)
        CGI.unescapeHTML(text).each_char { |char| emit_char(char, start, finish) }
        finish
      end

      # Simplified emphasis pairing: a delimiter run closes the stack top when
      # it matches exactly (same char, same length) and is preceded by
      # non-space; otherwise it opens when followed by non-space; otherwise it
      # stays literal. Openers are emitted provisionally and removed from the
      # projection when their closer arrives — an opener that never closes
      # stays literal, matching how it renders.
      def emphasis(text, start, finish)
        top = @emphasis_stack.last
        if closer?(start) && top && top[0] == text
          _, open_start, plain_index = @emphasis_stack.pop
          remove_plain(plain_index, text.length)
          @map.inline_ranges << (open_start...finish)
        elsif opener?(finish)
          @emphasis_stack << [text, start, @map.plain.length]
          emit_verbatim(start, finish)
        else
          emit_verbatim(start, finish)
        end
        finish
      end

      def opener?(finish)
        following = @source[finish]
        !following.nil? && !following.match?(/\s/)
      end

      def closer?(start)
        preceding = start.positive? ? @source[start - 1] : nil
        !preceding.nil? && !preceding.match?(/\s/)
      end

      def remove_plain(index, count)
        @map.plain.slice!(index, count)
        @map.starts.slice!(index, count)
        @map.ends.slice!(index, count)
        @map.block_ids.slice!(index, count)
      end

      def emit_verbatim(from, upto)
        (from...upto).each { |i| emit_char(@source[i], i, i + 1) }
      end

      def emit_char(char, source_start, source_end)
        @map.plain << char
        @map.starts << source_start
        @map.ends << source_end
        @map.block_ids << @block_id
      end
  end
end
