require "cgi"

class SelectionLinker
  class HtmlMap
    INLINE_TAGS = %w[
      a abbr b cite code del em i ins kbd mark q s samp small span strong sub sup time u var
    ].freeze
    VOID_TAGS = %w[area base br col embed hr img input link meta param source track wbr].freeze
    RAW_TEXT_TAGS = %w[script style].freeze
    TOKEN = %r{
      <!--.*?--> |
      </?[A-Za-z][^>]*> |
      <![^>]*> |
      &(?:[A-Za-z][A-Za-z0-9]*|\#[0-9]+|\#x[0-9A-Fa-f]+);
    }xm

    def self.build(source)
      new(source).build
    end

    def initialize(source)
      @source = source
      @map = Map.new(plain: +"", starts: [], ends: [], block_ids: [],
                     inline_ranges: [], link_ranges: [], unlinkable_ranges: [])
      @block_id = 0
      @stack = []
    end

    def build
      position = 0
      while (token = @source.match(TOKEN, position))
        emit_text(position, token.begin(0))
        position = handle_token(token)
      end
      emit_text(position, @source.length)
      @map
    end

    private
      def emit_text(from, upto)
        (from...upto).each { |i| emit_char(@source[i], i, i + 1) }
      end

      def emit_char(char, source_start, source_end)
        @map.plain << char
        @map.starts << source_start
        @map.ends << source_end
        @map.block_ids << @block_id
      end

      def handle_token(token)
        text = token[0]
        range = token.begin(0)...token.end(0)
        return handle_entity(text, range) if text.start_with?("&")
        return range.end unless text.match?(%r{\A</?[A-Za-z]})

        name = text[%r{\A</?([A-Za-z][A-Za-z0-9-]*)}, 1].downcase
        text.start_with?("</") ? close_tag(name, range) : open_tag(name, text, range)
      end

      def handle_entity(text, range)
        CGI.unescapeHTML(text).each_char { |char| emit_char(char, range.begin, range.end) }
        range.end
      end

      def open_tag(name, text, range)
        if RAW_TEXT_TAGS.include?(name)
          close = @source.match(%r{</#{name}\s*>}i, range.end)
          return close ? close.end(0) : @source.length
        end
        if name == "br" || name == "hr"
          emit_char("\n", range.end, range.end)
          return range.end
        end
        return range.end if VOID_TAGS.include?(name)

        if text.end_with?("/>")
          unless INLINE_TAGS.include?(name)
            emit_block_separator(range.begin)
            @block_id += 1
          end
        elsif INLINE_TAGS.include?(name)
          @stack << [name, range.begin]
        else
          emit_block_separator(range.begin)
          @block_id += 1
          @stack << [name, range.begin]
        end
        range.end
      end

      def close_tag(name, range)
        index = @stack.rindex { |entry| entry.first == name }
        if index
          _, open_start = @stack.delete_at(index)
          if INLINE_TAGS.include?(name)
            element = open_start...range.end
            @map.inline_ranges << element
            @map.link_ranges << element if name == "a"
          else
            emit_block_separator(range.begin)
            @block_id += 1
          end
        end
        range.end
      end

      # Rendered text has line breaks between block elements even when the
      # source has no whitespace there (<p>one</p><p>two</p> renders as
      # "one\ntwo"). Emit a zero-width newline so selections spanning blocks
      # still match — and then get rejected as cross-block, not NotFound.
      def emit_block_separator(position)
        return if @map.plain.empty? || @map.plain.end_with?("\n")

        emit_char("\n", position, position)
      end
  end
end
