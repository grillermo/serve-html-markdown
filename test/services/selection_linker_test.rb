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

  test "rejects an html match inside a tag" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: %(<p class="beta">x</p>),
        extension: ".html",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects an html match inside a script block" do
    assert_raises SelectionLinker::UnsafeMatch do
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

  test "rejects an html selection containing an existing anchor" do
    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: %(<p>before <a href="/old.html">beta</a> after</p>),
        extension: ".html",
        selected_text: %(<a href="/old.html">beta</a>),
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "rejects an html selection crossing an existing anchor" do
    assert_raises SelectionLinker::UnsafeMatch do
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
end
