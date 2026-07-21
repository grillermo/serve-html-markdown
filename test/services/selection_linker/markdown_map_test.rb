require "test_helper"

class SelectionLinker
  class MarkdownMapTest < ActiveSupport::TestCase
    test "projects paragraph text with source offsets" do
      map = MarkdownMap.build("alpha beta\n")

      assert_equal "alpha beta\n", map.plain
      assert_equal 0, map.starts[0]
      assert_equal 0...5, map.source_range_for(0...5)
    end

    test "assigns different block ids across blank-line-separated paragraphs" do
      map = MarkdownMap.build("alpha\n\nbeta\n")

      alpha_id = map.block_ids[map.plain.index("alpha")]
      beta_id = map.block_ids[map.plain.index("beta")]
      refute_equal alpha_id, beta_id
    end

    test "keeps one block id across soft line breaks in a paragraph" do
      map = MarkdownMap.build("alpha\nbeta\n")

      assert_equal 1, map.block_ids.uniq.length
    end

    test "drops heading markers and isolates heading blocks" do
      map = MarkdownMap.build("# Title\ntext\n")

      assert_equal "Title\ntext\n", map.plain
      title_id = map.block_ids[map.plain.index("Title")]
      text_id = map.block_ids[map.plain.index("text")]
      refute_equal title_id, text_id
    end

    test "drops list markers and isolates each item" do
      map = MarkdownMap.build("- alpha\n- beta\n")

      assert_equal "alpha\nbeta\n", map.plain
      refute_equal map.block_ids[map.plain.index("alpha")],
                   map.block_ids[map.plain.index("beta")]
    end

    test "drops blockquote prefixes" do
      map = MarkdownMap.build("> quoted text\n")

      assert_equal "quoted text\n", map.plain
    end

    test "marks fenced code blocks unlinkable but keeps their text" do
      source = "before\n\n```\ncode here\n```\n"
      map = MarkdownMap.build(source)

      assert_includes map.plain, "code here"
      code_start = source.index("code here")
      zone = map.unlinkable_ranges.find { |r| r.cover?(code_start) }
      assert zone, "expected an unlinkable range covering the fenced code"
    end

    test "marks indented code blocks unlinkable" do
      source = "before\n\n    indented code\n"
      map = MarkdownMap.build(source)

      assert_includes map.plain, "indented code"
      code_start = source.index("indented code")
      assert map.unlinkable_ranges.any? { |r| r.cover?(code_start) }
    end
  end
end
