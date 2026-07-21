class SelectionLinker
  module SelectionLocator
    NOT_FOUND_MESSAGE = "Selection not found in source — select a plainer run of text."

    def self.locate(map, selected_text, occurrence)
      tokens = selected_text.to_s.split(/\s+/).reject(&:empty?)
      raise NotFound, NOT_FOUND_MESSAGE if tokens.empty?

      pattern = Regexp.new(tokens.map { |token| Regexp.escape(token) }.join('\s+'))
      matches = []
      position = 0
      while (found = map.plain.match(pattern, position))
        matches << (found.begin(0)...found.end(0))
        position = found.begin(0) + 1
      end
      raise NotFound, NOT_FOUND_MESSAGE if matches.empty?

      matches.fetch(occurrence, matches.first)
    end
  end
end
