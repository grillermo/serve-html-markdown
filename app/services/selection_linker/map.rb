class SelectionLinker
  # Offset-preserving projection of a source document.
  #
  # plain      - String, rendered-text projection of the source
  # starts     - source char offset where plain char i begins
  # ends       - source char offset (exclusive) where plain char i ends;
  #              multi-char source runs (entities) map every plain char to the
  #              full run so a splice can never split one
  # block_ids  - block id for plain char i; a match touching two ids crosses a
  #              block boundary
  # inline_ranges     - full source ranges of inline elements (snap targets)
  # link_ranges       - full source ranges of existing links/images
  # unlinkable_ranges - source ranges where a link must not be spliced
  Map = Struct.new(
    :plain, :starts, :ends, :block_ids,
    :inline_ranges, :link_ranges, :unlinkable_ranges,
    keyword_init: true
  ) do
    def source_range_for(plain_range)
      starts[plain_range.begin]...ends[plain_range.end - 1]
    end

    def plain_indices_within(source_range)
      starts.each_index.select do |i|
        starts[i] >= source_range.begin && ends[i] <= source_range.end
      end
    end
  end
end
