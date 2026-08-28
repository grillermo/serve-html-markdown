require "test_helper"

class SelectionLinker
  class PlannerTest < ActiveSupport::TestCase
    test "returns the matched source range for a plain match" do
      source = "<p>alpha beta gamma</p>"
      segments = plan(source, "beta")

      assert_equal [source.index("beta")...(source.index("beta") + 4)], segments
    end

    test "snaps to cover a partially overlapped inline element" do
      source = "<p>x <em>alpha beta</em> gamma</p>"
      segments = plan(source, "beta gamma")

      expected = source.index("<em>")...(source.index(" gamma") + " gamma".length)
      assert_equal [expected], segments
    end

    test "does not snap when the match sits fully inside an inline element" do
      source = "<p><span>alpha beta gamma</span></p>"
      segments = plan(source, "beta")

      assert_equal [source.index("beta")...(source.index("beta") + 4)], segments
    end

    test "rejects a match crossing block elements" do
      error = assert_raises SelectionLinker::UnsafeMatch do
        plan("<p>one</p><p>two</p>", "one two")
      end

      assert_equal "Selection spans multiple paragraphs — select within one.", error.message
    end

    test "segments around an existing anchor" do
      source = %(<p>before <a href="/old.html">beta</a> after</p>)
      segments = plan(source, "before beta after")

      before = source.index("before")...(source.index("before") + "before".length)
      after = source.index("after")...(source.index("after") + "after".length)
      assert_equal [before, after], segments
    end

    test "rejects a match entirely inside an existing anchor" do
      error = assert_raises SelectionLinker::UnsafeMatch do
        plan(%(<p><a href="/old.html">alpha beta</a></p>), "beta")
      end

      assert_equal "Selection overlaps an existing link.", error.message
    end

    test "rejects a match touching an unlinkable range" do
      source = "code here"
      map = HtmlMap.build(source)
      map.unlinkable_ranges << (0...source.length)
      plain_range = SelectionLocator.locate(map, "code", 0)

      error = assert_raises SelectionLinker::UnsafeMatch do
        Planner.plan(map, plain_range, source: source)
      end

      assert_equal "Selection is inside a code block.", error.message
    end

    test "trims whitespace from segment edges" do
      source = %(<p>one <a href="/old.html">beta</a> two</p>)
      segments = plan(source, "one beta two")

      assert_equal "one", source[segments.first]
      assert_equal "two", source[segments.last]
    end

    private
      def plan(source, selected_text, occurrence: 0)
        map = HtmlMap.build(source)
        plain_range = SelectionLocator.locate(map, selected_text, occurrence)
        Planner.plan(map, plain_range, source: source)
      end
  end
end
