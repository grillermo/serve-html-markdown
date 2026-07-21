require "test_helper"

class SelectionLinker
  class HtmlMapTest < ActiveSupport::TestCase
    test "projects text and drops tags" do
      map = HtmlMap.build("<p>alpha <span>beta</span></p>")

      assert_equal "alpha beta\n", map.plain
    end

    test "maps plain chars back to source offsets" do
      source = "<p>ab</p>"
      map = HtmlMap.build(source)

      assert_equal "ab\n", map.plain
      assert_equal 3, map.starts[0]
      assert_equal 5, map.ends[1]
      assert_equal 3...5, map.source_range_for(0...2)
    end

    test "decodes entities as atomic runs" do
      source = "<p>A &amp; B</p>"
      map = HtmlMap.build(source)

      assert_equal "A & B\n", map.plain
      amp_index = map.plain.index("&")
      assert_equal 5, map.starts[amp_index]
      assert_equal 10, map.ends[amp_index]
    end

    test "skips script and style contents" do
      map = HtmlMap.build("<script>var beta = 1;</script><style>.x{}</style><p>x</p>")

      assert_equal "x\n", map.plain
    end

    test "separates block elements with a zero-width newline and distinct block ids" do
      map = HtmlMap.build("<p>one</p><p>two</p>")

      assert_equal "one\ntwo\n", map.plain
      one_id = map.block_ids[map.plain.index("one")]
      two_id = map.block_ids[map.plain.index("two")]
      refute_equal one_id, two_id
      separator = map.plain.index("\n")
      assert_equal map.starts[separator], map.ends[separator]
    end

    test "keeps one block id inside a paragraph with inline tags" do
      map = HtmlMap.build("<p>one <em>two</em> three</p>")

      assert_equal 1, map.block_ids.uniq.length
    end

    test "records inline element ranges" do
      source = "<p>a <em>b</em> c</p>"
      map = HtmlMap.build(source)

      em_start = source.index("<em>")
      em_end = source.index("</em>") + "</em>".length
      assert_includes map.inline_ranges, em_start...em_end
    end

    test "records anchor elements as both inline and link ranges" do
      source = %(<p><a href="/old.html">beta</a></p>)
      map = HtmlMap.build(source)

      a_start = source.index("<a")
      a_end = source.index("</a>") + "</a>".length
      assert_includes map.link_ranges, a_start...a_end
      assert_includes map.inline_ranges, a_start...a_end
    end

    test "emits a newline for br tags" do
      map = HtmlMap.build("<p>some<br>code</p>")

      assert_equal "some\ncode\n", map.plain
    end

    test "treats stray angle brackets and comments correctly" do
      map = HtmlMap.build("<p>1 > 0</p><!-- note -->")

      assert_equal "1 > 0\n", map.plain
    end
  end
end
