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

    test "resolves backslash escapes" do
      map = MarkdownMap.build("a \\* b\n")

      assert_equal "a * b\n", map.plain
      star_index = map.plain.index("*")
      assert_equal 2, map.starts[star_index]
      assert_equal 4, map.ends[star_index]
    end

    test "drops code span delimiters and records the element" do
      source = "this is `some` code\n"
      map = MarkdownMap.build(source)

      assert_equal "this is some code\n", map.plain
      tick_start = source.index("`")
      tick_end = source.rindex("`") + 1
      assert_includes map.inline_ranges, tick_start...tick_end
    end

    test "keeps link labels, drops urls, records the link range" do
      source = "see [beta](/old.html) end\n"
      map = MarkdownMap.build(source)

      assert_equal "see beta end\n", map.plain
      link_start = source.index("[")
      link_end = source.index(")") + 1
      assert_includes map.link_ranges, link_start...link_end
    end

    test "records images as links with no plain contribution" do
      source = "pic ![alt](i.png) end\n"
      map = MarkdownMap.build(source)

      assert_equal "pic  end\n", map.plain
      image_start = source.index("![")
      image_end = source.index(")") + 1
      assert_includes map.link_ranges, image_start...image_end
    end

    test "pairs emphasis delimiters and records the element" do
      source = "make *this bold* now\n"
      map = MarkdownMap.build(source)

      assert_equal "make this bold now\n", map.plain
      em_start = source.index("*")
      em_end = source.rindex("*") + 1
      assert_includes map.inline_ranges, em_start...em_end
    end

    test "leaves unmatched emphasis delimiters as literal text" do
      map = MarkdownMap.build("5 * 3 is fifteen\n")

      assert_equal "5 * 3 is fifteen\n", map.plain
    end

    test "handles raw inline html and anchors" do
      source = %(word <span>in span</span> and <a href="/o.html">old</a> end\n)
      map = MarkdownMap.build(source)

      assert_equal "word in span and old end\n", map.plain
      a_start = source.index("<a")
      a_end = source.index("</a>") + "</a>".length
      assert_includes map.link_ranges, a_start...a_end
    end

    test "decodes entities" do
      map = MarkdownMap.build("A &amp; B\n")

      assert_equal "A & B\n", map.plain
    end
  end
end
