require "cgi"

class SelectionLinker
  class MarkdownMap
    FENCE = /\A(`{3,}|~{3,})/
    HEADING = /\A\#{1,6}[ \t]+/
    LIST_ITEM = /\A[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+/
    BLOCKQUOTE = /\A[ \t]{0,3}>[ \t]?/
    INDENTED_CODE = /\A(?: {4}|\t)/

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
      # Replaced with a real inline scanner in the next task.
      def scan_inline(from, upto)
        emit_verbatim(from, upto)
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
