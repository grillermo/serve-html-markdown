require "test_helper"

class SelectionLinker
  class SelectionLocatorTest < ActiveSupport::TestCase
    test "finds a literal match" do
      result = SelectionLocator.locate(map_for("alpha beta gamma"), "beta", 0)

      assert_equal 6...10, result
    end

    test "matches across collapsed whitespace" do
      result = SelectionLocator.locate(map_for("some  \n code"), "some code", 0)

      assert_equal 0...12, result
    end

    test "picks the requested occurrence" do
      result = SelectionLocator.locate(map_for("cat dog cat"), "cat", 1)

      assert_equal 8...11, result
    end

    test "falls back to the first occurrence when out of range" do
      result = SelectionLocator.locate(map_for("cat dog cat"), "cat", 9)

      assert_equal 0...3, result
    end

    test "raises NotFound when absent" do
      assert_raises SelectionLinker::NotFound do
        SelectionLocator.locate(map_for("alpha"), "missing", 0)
      end
    end

    test "raises NotFound for a blank selection" do
      assert_raises SelectionLinker::NotFound do
        SelectionLocator.locate(map_for("alpha"), "   ", 0)
      end
    end

    test "escapes regex metacharacters in the selection" do
      result = SelectionLocator.locate(map_for("cost is $5 (usd)"), "$5 (usd)", 0)

      assert_equal 8...16, result
    end

    private
      def map_for(plain)
        starts = (0...plain.length).to_a
        SelectionLinker::Map.new(
          plain: plain,
          starts: starts,
          ends: starts.map { |i| i + 1 },
          block_ids: Array.new(plain.length, 0),
          inline_ranges: [],
          link_ranges: [],
          unlinkable_ranges: []
        )
      end
  end
end
