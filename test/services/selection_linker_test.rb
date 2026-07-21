require "test_helper"

class SelectionLinkerTest < ActiveSupport::TestCase
  test "wraps a markdown selection in a link" do
    result = SelectionLinker.link(
      source: "Alpha beta gamma.",
      extension: ".md",
      selected_text: "beta",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal "Alpha [beta](/x.html) gamma.", result
  end

  test "wraps an html selection in an anchor" do
    result = SelectionLinker.link(
      source: "<p>Alpha beta gamma.</p>",
      extension: ".html",
      selected_text: "beta",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal %(<p>Alpha <a href="/x.html">beta</a> gamma.</p>), result
  end

  test "wraps an html selection containing ordinary greater-than text" do
    result = SelectionLinker.link(
      source: "<p>1 > 0</p>",
      extension: ".html",
      selected_text: ">",
      occurrence: 1,
      url: "/x.html"
    )

    assert_equal %(<p>1 <a href="/x.html">></a> 0</p>), result
  end

  test "picks the requested occurrence" do
    result = SelectionLinker.link(
      source: "cat dog cat bird cat",
      extension: ".md",
      selected_text: "cat",
      occurrence: 2,
      url: "/x.html"
    )

    assert_equal "cat dog cat bird [cat](/x.html)", result
  end

  test "falls back to the first occurrence when index is out of range" do
    result = SelectionLinker.link(
      source: "cat dog cat",
      extension: ".md",
      selected_text: "cat",
      occurrence: 9,
      url: "/x.html"
    )

    assert_equal "[cat](/x.html) dog cat", result
  end

  test "raises NotFound when the text is absent" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: "Alpha beta.",
        extension: ".md",
        selected_text: "missing",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "escapes closing brackets in the markdown label" do
    result = SelectionLinker.link(
      source: "a b] c",
      extension: ".md",
      selected_text: "b]",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal "a [b\\]](/x.html) c", result
  end

  test "rejects a match inside an existing markdown link label" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "see [beta gamma](/old.html) end",
        extension: ".md",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects a match inside an existing markdown link url" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "see [label](/beta.html) end",
        extension: ".md",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "does not find text that only appears inside a tag" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: %(<p class="beta">x</p>),
        extension: ".html",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "does not find text that only appears inside a script block" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: "<script>var beta = 1;</script><p>x</p>",
        extension: ".html",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects an html match inside an existing anchor" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: %(<a href="/old.html">beta</a>),
        extension: ".html",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "does not find a selection containing raw anchor markup" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: %(<p>before <a href="/old.html">beta</a> after</p>),
        extension: ".html",
        selected_text: %(<a href="/old.html">beta</a>),
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "does not find a selection containing raw closing-anchor markup" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: %(<p>before <a href="/old.html">beta</a> after</p>),
        extension: ".html",
        selected_text: "beta</a> after",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects a markdown selection containing an existing link" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "see [beta](/old.html) end",
        extension: ".md",
        selected_text: "[beta](/old.html)",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects a markdown selection containing a link with an escaped closing bracket" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "see [b\\]](/old.html) end",
        extension: ".md",
        selected_text: "[b\\]](/old.html)",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects a markdown selection crossing an existing link" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "see [beta](/old.html) and more",
        extension: ".md",
        selected_text: "[beta](/old.html) and",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "skips unsafe occurrences is not attempted; requested occurrence is judged as-is" do
    # occurrence 0 is inside an anchor -> unsafe, even though occurrence 1 is fine
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: %(<a href="/old.html">beta</a> and beta again),
        extension: ".html",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "wraps a selection spanning an inline span" do
    result = SelectionLinker.link(
      source: "this is <span> some </span> code",
      extension: ".html",
      selected_text: "some code",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal %(this is <a href="/x.html"><span> some </span> code</a>), result
  end

  test "matches across differing whitespace" do
    result = SelectionLinker.link(
      source: "<p>some\ncode here</p>",
      extension: ".html",
      selected_text: "some code",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal %(<p><a href="/x.html">some\ncode</a> here</p>), result
  end

  test "matches text containing entities and preserves them" do
    result = SelectionLinker.link(
      source: "<p>A &amp; B here</p>",
      extension: ".html",
      selected_text: "A & B",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal %(<p><a href="/x.html">A &amp; B</a> here</p>), result
  end

  test "snaps to cover a partially selected emphasis element" do
    result = SelectionLinker.link(
      source: "<p>x <em>alpha beta</em> gamma</p>",
      extension: ".html",
      selected_text: "beta gamma",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal %(<p>x <a href="/x.html"><em>alpha beta</em> gamma</a></p>), result
  end

  test "segments around an existing anchor" do
    result = SelectionLinker.link(
      source: %(<p>before <a href="/old.html">beta</a> after</p>),
      extension: ".html",
      selected_text: "before beta after",
      occurrence: 0,
      url: "/x.html"
    )

    expected = %(<p><a href="/x.html">before</a> <a href="/old.html">beta</a> <a href="/x.html">after</a></p>)
    assert_equal expected, result
  end

  test "rejects a selection spanning two paragraphs" do
    error = assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "<p>one</p><p>two</p>",
        extension: ".html",
        selected_text: "one two",
        occurrence: 0,
        url: "/x.html"
      )
    end

    assert_equal "Selection spans multiple paragraphs — select within one.", error.message
  end

  test "counts occurrences in rendered text, not raw source" do
    result = SelectionLinker.link(
      source: %(<p class="beta">beta one beta two</p>),
      extension: ".html",
      selected_text: "beta",
      occurrence: 1,
      url: "/x.html"
    )

    assert_equal %(<p class="beta">beta one <a href="/x.html">beta</a> two</p>), result
  end
end
