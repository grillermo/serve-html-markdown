class SelectionLinker
  module Planner
    CROSS_BLOCK_MESSAGE = "Selection spans multiple paragraphs — select within one."

    def self.plan(map, plain_range, source:)
      reject_cross_block!(map.block_ids[plain_range].uniq)
      range = map.source_range_for(plain_range)
      reject_unlinkable!(map, range)
      range = snap(map, range)
      reject_cross_block!(block_ids_within(map, range))

      segments = segment(map, range)
      segments = segments.select { |seg| meaningful?(map, seg) }
      segments = segments.filter_map { |seg| trim(seg, source) }
      raise UnsafeMatch, "Selection overlaps an existing link." if segments.empty?

      segments
    end

    def self.reject_cross_block!(ids)
      raise UnsafeMatch, CROSS_BLOCK_MESSAGE if ids.length > 1
    end

    def self.reject_unlinkable!(map, source_range)
      map.unlinkable_ranges.each do |zone|
        next if zone.begin >= source_range.end || zone.end <= source_range.begin
        raise UnsafeMatch, "Selection is inside a code block."
      end
    end

    # Extend the range until no inline element is partially overlapped: an
    # element with exactly one of its tags inside the range gets pulled in
    # whole. Elements fully inside or fully containing the range are fine.
    def self.snap(map, range)
      loop do
        changed = false
        map.inline_ranges.each do |el|
          next if el.begin >= range.end || el.end <= range.begin
          next if el.begin >= range.begin && el.end <= range.end
          next if el.begin < range.begin && el.end > range.end

          range = [range.begin, el.begin].min...[range.end, el.end].max
          changed = true
        end
        break unless changed
      end
      range
    end

    def self.block_ids_within(map, source_range)
      map.plain_indices_within(source_range).map { |i| map.block_ids[i] }.uniq
    end

    def self.segment(map, range)
      segments = [range]
      map.link_ranges.sort_by(&:begin).each do |link|
        segments = segments.flat_map do |seg|
          next [seg] if link.begin >= seg.end || link.end <= seg.begin

          pieces = []
          pieces << (seg.begin...link.begin) if link.begin > seg.begin
          pieces << (link.end...seg.end) if link.end < seg.end
          pieces
        end
      end
      segments
    end

    def self.meaningful?(map, seg)
      map.plain_indices_within(seg).any? { |i| !map.plain[i].match?(/\s/) }
    end

    def self.trim(seg, source)
      from = seg.begin
      upto = seg.end
      from += 1 while from < upto && source[from].match?(/\s/)
      upto -= 1 while upto > from && source[upto - 1].match?(/\s/)
      return nil if from == upto

      from...upto
    end
  end
end
