# Inline-Element Selection Linking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `SelectionLinker` handle selections that span inline markup (HTML tags, markdown inline syntax) by matching in a rendered-text projection and splicing links back at exact source offsets.

**Architecture:** `SelectionLinker.link` keeps its public API and error classes. Internals become a pipeline: per-format offset-preserving `Map` (HtmlMap / MarkdownMap) → whitespace-tolerant `SelectionLocator` → `Planner` (cross-block reject, snap-to-boundary, segment around existing links, trim) → splice writer. Spec: `docs/superpowers/specs/2026-07-21-inline-element-selection-linking-design.md`.

**Tech Stack:** Ruby / Rails 8, Minitest (`bin/rails test`), Zeitwerk autoloading (`SelectionLinker::HtmlMap` lives in `app/services/selection_linker/html_map.rb`), stdlib `CGI` for entity decoding. No new gems.

**Key conventions:**
- All offsets are Ruby *character* offsets into the raw source string (`String#match(re, pos)`, `String#[]=` with ranges). Never `StringScanner` (byte positions).
- Ranges are exclusive (`start...end`).
- The final spliced output must be byte-identical to the input outside the inserted link markup.

---

### Task 1: `Map` struct and `SelectionLocator`

The `Map` is the shared data structure every format tokenizer produces. The `SelectionLocator` finds the nth occurrence of the (whitespace-normalized) selection in the map's plain-text projection.

**Files:**
- Create: `app/services/selection_linker/map.rb`
- Create: `app/services/selection_linker/selection_locator.rb`
- Test: `test/services/selection_linker/selection_locator_test.rb`

- [ ] **Step 1: Write the failing tests**

Create `test/services/selection_linker/selection_locator_test.rb`:

```ruby
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/services/selection_linker/selection_locator_test.rb`
Expected: FAIL/ERROR with `NameError: uninitialized constant SelectionLinker::Map` (or `SelectionLocator`).

- [ ] **Step 3: Implement `Map` and `SelectionLocator`**

Create `app/services/selection_linker/map.rb`:

```ruby
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
```

Create `app/services/selection_linker/selection_locator.rb`:

```ruby
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
```

Note: `SelectionLinker::NotFound` already exists in `app/services/selection_linker.rb` — do not redefine it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `bin/rails test test/services/selection_linker/selection_locator_test.rb`
Expected: 7 runs, 0 failures, 0 errors.

- [ ] **Step 5: Commit**

```bash
git add app/services/selection_linker/map.rb app/services/selection_linker/selection_locator.rb test/services/selection_linker/selection_locator_test.rb
git commit -m "feat: add selection map struct and whitespace-tolerant locator"
```

---

### Task 2: `HtmlMap`

Offset-preserving HTML tokenizer: emits text chars, decodes entities, drops tags, skips script/style, tracks block ids and inline/link element ranges.

**Files:**
- Create: `app/services/selection_linker/html_map.rb`
- Test: `test/services/selection_linker/html_map_test.rb`

- [ ] **Step 1: Write the failing tests**

Create `test/services/selection_linker/html_map_test.rb`:

```ruby
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/services/selection_linker/html_map_test.rb`
Expected: ERROR with `NameError: uninitialized constant SelectionLinker::HtmlMap`.

- [ ] **Step 3: Implement `HtmlMap`**

Create `app/services/selection_linker/html_map.rb`:

```ruby
require "cgi"

class SelectionLinker
  class HtmlMap
    INLINE_TAGS = %w[
      a abbr b cite code del em i ins kbd mark q s samp small span strong sub sup time u var
    ].freeze
    VOID_TAGS = %w[area base br col embed hr img input link meta param source track wbr].freeze
    RAW_TEXT_TAGS = %w[script style].freeze
    TOKEN = %r{
      <!--.*?--> |
      </?[A-Za-z][^>]*> |
      <![^>]*> |
      &(?:[A-Za-z][A-Za-z0-9]*|\#[0-9]+|\#x[0-9A-Fa-f]+);
    }xm

    def self.build(source)
      new(source).build
    end

    def initialize(source)
      @source = source
      @map = Map.new(plain: +"", starts: [], ends: [], block_ids: [],
                     inline_ranges: [], link_ranges: [], unlinkable_ranges: [])
      @block_id = 0
      @stack = []
    end

    def build
      position = 0
      while (token = @source.match(TOKEN, position))
        emit_text(position, token.begin(0))
        position = handle_token(token)
      end
      emit_text(position, @source.length)
      @map
    end

    private
      def emit_text(from, upto)
        (from...upto).each { |i| emit_char(@source[i], i, i + 1) }
      end

      def emit_char(char, source_start, source_end)
        @map.plain << char
        @map.starts << source_start
        @map.ends << source_end
        @map.block_ids << @block_id
      end

      def handle_token(token)
        text = token[0]
        range = token.begin(0)...token.end(0)
        return handle_entity(text, range) if text.start_with?("&")
        return range.end unless text.match?(%r{\A</?[A-Za-z]})

        name = text[%r{\A</?([A-Za-z][A-Za-z0-9-]*)}, 1].downcase
        text.start_with?("</") ? close_tag(name, range) : open_tag(name, text, range)
      end

      def handle_entity(text, range)
        CGI.unescapeHTML(text).each_char { |char| emit_char(char, range.begin, range.end) }
        range.end
      end

      def open_tag(name, text, range)
        if RAW_TEXT_TAGS.include?(name)
          close = @source.match(%r{</#{name}\s*>}i, range.end)
          return close ? close.end(0) : @source.length
        end
        if name == "br" || name == "hr"
          emit_char("\n", range.end, range.end)
          return range.end
        end
        return range.end if VOID_TAGS.include?(name)

        if text.end_with?("/>")
          unless INLINE_TAGS.include?(name)
            emit_block_separator(range.begin)
            @block_id += 1
          end
        elsif INLINE_TAGS.include?(name)
          @stack << [name, range.begin]
        else
          emit_block_separator(range.begin)
          @block_id += 1
          @stack << [name, range.begin]
        end
        range.end
      end

      def close_tag(name, range)
        index = @stack.rindex { |entry| entry.first == name }
        if index
          _, open_start = @stack.delete_at(index)
          if INLINE_TAGS.include?(name)
            element = open_start...range.end
            @map.inline_ranges << element
            @map.link_ranges << element if name == "a"
          else
            emit_block_separator(range.begin)
            @block_id += 1
          end
        end
        range.end
      end

      # Rendered text has line breaks between block elements even when the
      # source has no whitespace there (<p>one</p><p>two</p> renders as
      # "one\ntwo"). Emit a zero-width newline so selections spanning blocks
      # still match — and then get rejected as cross-block, not NotFound.
      def emit_block_separator(position)
        return if @map.plain.empty? || @map.plain.end_with?("\n")

        emit_char("\n", position, position)
      end
  end
end
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `bin/rails test test/services/selection_linker/html_map_test.rb`
Expected: 10 runs, 0 failures, 0 errors.

- [ ] **Step 5: Commit**

```bash
git add app/services/selection_linker/html_map.rb test/services/selection_linker/html_map_test.rb
git commit -m "feat: add offset-preserving html source map"
```

---

### Task 3: `Planner`

Applies the safety rules to a located match: cross-block reject, unlinkable reject, snap-to-boundary, segment around existing links, drop/trim empty segments.

**Files:**
- Create: `app/services/selection_linker/planner.rb`
- Test: `test/services/selection_linker/planner_test.rb`

- [ ] **Step 1: Write the failing tests**

Tests use `HtmlMap` + `SelectionLocator` (already tested) to build realistic inputs. Create `test/services/selection_linker/planner_test.rb`:

```ruby
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/services/selection_linker/planner_test.rb`
Expected: ERROR with `NameError: uninitialized constant SelectionLinker::Planner`.

- [ ] **Step 3: Implement `Planner`**

Create `app/services/selection_linker/planner.rb`:

```ruby
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `bin/rails test test/services/selection_linker/planner_test.rb`
Expected: 8 runs, 0 failures, 0 errors.

- [ ] **Step 5: Commit**

```bash
git add app/services/selection_linker/planner.rb test/services/selection_linker/planner_test.rb
git commit -m "feat: add selection planner with snap and link segmentation"
```

---

### Task 4: Rewire `SelectionLinker` for HTML

Replace the literal-match internals with the pipeline. Markdown temporarily routes through `HtmlMap` — it fully works only after Task 6; markdown-specific tests are updated in Task 7. This task updates the HTML-facing tests whose expected error class changes, adds the new HTML behaviors, and keeps all old HTML expectations that still hold.

**Behavior changes to existing tests in `test/services/selection_linker_test.rb`** (projection drops tag/attribute/script bytes, so matches that used to be "unsafe" are now simply not found):

| Test | Old | New |
| --- | --- | --- |
| "rejects an html match inside a tag" | `UnsafeMatch` | `NotFound` |
| "rejects an html match inside a script block" | `UnsafeMatch` | `NotFound` |
| "rejects an html selection containing an existing anchor" (selection is raw markup) | `UnsafeMatch` | `NotFound` |
| "rejects an html selection crossing an existing anchor" (selection is raw markup) | `UnsafeMatch` | `NotFound` |

Unchanged: "rejects an html match inside an existing anchor" (`UnsafeMatch` — segments all inside link), "skips unsafe occurrences…" (`UnsafeMatch`), the three passing HTML tests, and all pure-markdown tests (left alone until Task 7).

**Files:**
- Modify: `app/services/selection_linker.rb` (full rewrite)
- Test: `test/services/selection_linker_test.rb`

- [ ] **Step 1: Update changed tests and add new failing HTML tests**

In `test/services/selection_linker_test.rb`, replace the four tests listed above with:

```ruby
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
```

Then add the new HTML behavior tests at the end of the class:

```ruby
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
```

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `bin/rails test test/services/selection_linker_test.rb`
Expected: FAIL — new tests fail against the old implementation (e.g. "wraps a selection spanning an inline span" raises `NotFound`); the four rewritten tests fail because the old code raises `UnsafeMatch` where `NotFound` is now expected.

- [ ] **Step 3: Rewrite `SelectionLinker`**

Replace the entire contents of `app/services/selection_linker.rb` with:

```ruby
class SelectionLinker
  Error = Class.new(StandardError)
  NotFound = Class.new(Error)
  UnsafeMatch = Class.new(Error)

  def self.link(source:, extension:, selected_text:, occurrence:, url:)
    new(source, extension, selected_text, occurrence, url).link
  end

  def initialize(source, extension, selected_text, occurrence, url)
    @source = source
    @extension = extension
    @selected_text = selected_text
    @occurrence = [occurrence.to_i, 0].max
    @url = url
  end

  def link
    map = build_map
    plain_range = SelectionLocator.locate(map, @selected_text, @occurrence)
    segments = Planner.plan(map, plain_range, source: @source)
    splice(segments)
  end

  private
    def markdown?
      @extension != ".html"
    end

    def build_map
      markdown? ? MarkdownMap.build(@source) : HtmlMap.build(@source)
    end

    def splice(segments)
      result = @source.dup
      segments.sort_by(&:begin).reverse_each do |segment|
        slice = result[segment]
        result[segment] = markdown? ? markdown_link(slice) : html_link(slice)
      end
      result
    end

    def html_link(slice)
      %(<a href="#{@url}">#{slice}</a>)
    end

    def markdown_link(slice)
      label = slice.gsub(/(?<!\\)\]/) { "\\]" }
      "[#{label}](#{@url})"
    end
end
```

Until Task 6 exists, `MarkdownMap` is missing. Add a temporary shim so markdown keeps working through `HtmlMap` (plain text passes through it unchanged; existing markdown tests are re-examined in Task 7). Create `app/services/selection_linker/markdown_map.rb`:

```ruby
class SelectionLinker
  # Temporary: markdown routes through the HTML tokenizer until the real
  # markdown map lands. Replaced in the markdown-map task.
  class MarkdownMap
    def self.build(source)
      HtmlMap.build(source)
    end
  end
end
```

- [ ] **Step 4: Run the selection linker tests**

Run: `bin/rails test test/services/selection_linker_test.rb test/services/selection_linker/`
Expected: All HTML tests pass. Some pre-existing *markdown* tests will now fail (e.g. "rejects a match inside an existing markdown link label" — the shim has no notion of markdown links). That is expected mid-migration; note the failures and continue. If any **HTML** test fails, stop and fix before committing.

Known-failing markdown tests at this point (all fixed by Tasks 6–7):
- "rejects a match inside an existing markdown link label"
- "rejects a match inside an existing markdown link url"
- "rejects a markdown selection containing an existing link"
- "rejects a markdown selection containing a link with an escaped closing bracket"
- "rejects a markdown selection crossing an existing link"

- [ ] **Step 5: Commit**

```bash
git add app/services/selection_linker.rb app/services/selection_linker/markdown_map.rb test/services/selection_linker_test.rb
git commit -m "feat: link html selections across inline markup"
```

---

### Task 5: `MarkdownMap` block structure

Line pass: blank lines, headings, list items, blockquote prefixes, fenced and indented code blocks. Inline scanning arrives in Task 6 — this task emits line content verbatim via a placeholder `scan_inline` that Task 6 replaces.

**Files:**
- Modify: `app/services/selection_linker/markdown_map.rb` (replace shim)
- Test: `test/services/selection_linker/markdown_map_test.rb`

- [ ] **Step 1: Write the failing tests**

Create `test/services/selection_linker/markdown_map_test.rb`:

```ruby
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `bin/rails test test/services/selection_linker/markdown_map_test.rb`
Expected: FAIL — the shim delegates to `HtmlMap`, so heading/list/fence tests fail (markers not dropped, no unlinkable ranges).

- [ ] **Step 3: Implement the block pass**

Replace `app/services/selection_linker/markdown_map.rb` entirely with:

```ruby
require "cgi"

class SelectionLinker
  class MarkdownMap
    FENCE = /\A(`{3,}|~{3,})/
    HEADING = /\A\#{1,6}[ \t]+/
    LIST_ITEM = /\A[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+/
    BLOCKQUOTE = /\A[ \t]{0,3}>[ \t]?/
    INDENTED_CODE = /\A(?: {4}|\t)/

    def self.build(source)
      new(source).build
    end

    def initialize(source)
      @source = source
      @map = Map.new(plain: +"", starts: [], ends: [], block_ids: [],
                     inline_ranges: [], link_ranges: [], unlinkable_ranges: [])
      @block_id = 0
      @emphasis_stack = []
      @html_stack = []
    end

    def build
      offset = 0
      in_fence = false
      fence_char = nil
      previous_blank = true
      in_indented_code = false

      @source.each_line do |line|
        line_start = offset
        offset += line.length
        content = line.chomp
        content_end = line_start + content.length

        if in_fence
          if content.lstrip.match?(FENCE) && content.lstrip[0] == fence_char
            in_fence = false
            @block_id += 1
          else
            emit_verbatim(line_start, line_start + line.length)
            @map.unlinkable_ranges << (line_start...(line_start + line.length))
          end
          next
        end

        if (fence = content.lstrip[FENCE, 1])
          in_fence = true
          fence_char = fence[0]
          @block_id += 1
          previous_blank = false
          next
        end

        if content.strip.empty?
          @block_id += 1
          previous_blank = true
          in_indented_code = false
          next
        end

        if content.match?(INDENTED_CODE) && (previous_blank || in_indented_code)
          @block_id += 1 unless in_indented_code
          in_indented_code = true
          emit_verbatim(line_start, line_start + line.length)
          @map.unlinkable_ranges << (line_start...(line_start + line.length))
          previous_blank = false
          next
        end
        in_indented_code = false

        scan_start = line_start
        heading = false
        if (marker = content.match(HEADING))
          @block_id += 1
          heading = true
          scan_start = line_start + marker.end(0)
        elsif (marker = content.match(LIST_ITEM))
          @block_id += 1
          scan_start = line_start + marker.end(0)
        elsif (marker = content.match(BLOCKQUOTE))
          scan_start = line_start + marker.end(0)
        end

        scan_inline(scan_start, content_end)
        emit_char("\n", content_end, content_end + 1) if line.end_with?("\n")
        @block_id += 1 if heading
        previous_blank = false
      end
      @map
    end

    private
      # Replaced with a real inline scanner in the next task.
      def scan_inline(from, upto)
        emit_verbatim(from, upto)
      end

      def emit_verbatim(from, upto)
        (from...upto).each { |i| emit_char(@source[i], i, i + 1) }
      end

      def emit_char(char, source_start, source_end)
        @map.plain << char
        @map.starts << source_start
        @map.ends << source_end
        @map.block_ids << @block_id
      end
  end
end
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `bin/rails test test/services/selection_linker/markdown_map_test.rb`
Expected: 8 runs, 0 failures, 0 errors.

- [ ] **Step 5: Commit**

```bash
git add app/services/selection_linker/markdown_map.rb test/services/selection_linker/markdown_map_test.rb
git commit -m "feat: add markdown source map block structure"
```

---

### Task 6: `MarkdownMap` inline scanning

Escapes, code spans, links, images, raw inline HTML, entities, emphasis pairs. Replaces the placeholder `scan_inline`.

**Files:**
- Modify: `app/services/selection_linker/markdown_map.rb`
- Test: `test/services/selection_linker/markdown_map_test.rb`

- [ ] **Step 1: Add the failing tests**

Append inside `MarkdownMapTest`:

```ruby
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
```

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `bin/rails test test/services/selection_linker/markdown_map_test.rb`
Expected: FAIL — the placeholder `scan_inline` emits everything verbatim, so delimiter-dropping tests fail. The Task 5 tests must still pass.

- [ ] **Step 3: Implement the inline scanner**

In `app/services/selection_linker/markdown_map.rb`, add this constant next to the other regex constants:

```ruby
    INLINE_TOKEN = %r{
      \\[!-/:-@\[-`{-~] |
      `+ |
      !?\[ |
      </?[A-Za-z][^>\n]*> |
      &(?:[A-Za-z][A-Za-z0-9]*|\#[0-9]+|\#x[0-9A-Fa-f]+); |
      \*{1,2} | _{1,2}
    }x
```

Replace the placeholder `scan_inline` and add the handlers below it (all under `private`):

```ruby
      def scan_inline(from, upto)
        position = from
        while position < upto
          token = @source.match(INLINE_TOKEN, position)
          if token.nil? || token.begin(0) >= upto
            emit_verbatim(position, upto)
            return
          end
          emit_verbatim(position, token.begin(0))
          position = handle_inline_token(token, upto)
        end
      end

      def handle_inline_token(token, upto)
        text = token[0]
        start = token.begin(0)
        finish = token.end(0)
        case text
        when /\A\\/ then escape(text, start, finish)
        when /\A`/ then code_span(start, text.length, upto)
        when "![" then image(start, upto)
        when "[" then link(start, upto)
        when /\A</ then html_tag(text, start, finish)
        when /\A&/ then entity(text, start, finish)
        else emphasis(text, start, finish)
        end
      end

      def escape(text, start, finish)
        emit_char(text[1], start, finish)
        finish
      end

      def code_span(start, tick_count, upto)
        opener_end = start + tick_count
        closer = @source.match(/(?<!`)`{#{tick_count}}(?!`)/, opener_end)
        if closer.nil? || closer.end(0) > upto
          emit_verbatim(start, opener_end)
          return opener_end
        end

        emit_verbatim(opener_end, closer.begin(0))
        @map.inline_ranges << (start...closer.end(0))
        closer.end(0)
      end

      def link(start, upto)
        label_end = find_label_end(start + 1, upto)
        if label_end && @source[label_end + 1] == "("
          close = @source.index(")", label_end + 2)
          if close && close < upto
            scan_inline(start + 1, label_end)
            @map.link_ranges << (start...(close + 1))
            return close + 1
          end
        end
        emit_verbatim(start, start + 1)
        start + 1
      end

      def image(start, upto)
        label_end = find_label_end(start + 2, upto)
        if label_end && @source[label_end + 1] == "("
          close = @source.index(")", label_end + 2)
          if close && close < upto
            @map.link_ranges << (start...(close + 1))
            return close + 1
          end
        end
        emit_verbatim(start, start + 1)
        start + 1
      end

      def find_label_end(from, upto)
        position = from
        while position < upto
          char = @source[position]
          return position if char == "]"

          position += char == "\\" ? 2 : 1
        end
        nil
      end

      def html_tag(text, start, finish)
        name = text[%r{\A</?([A-Za-z][A-Za-z0-9-]*)}, 1].downcase
        if name == "br"
          emit_char("\n", finish, finish)
        elsif text.start_with?("</")
          index = @html_stack.rindex { |entry| entry.first == name }
          if index
            _, open_start = @html_stack.delete_at(index)
            element = open_start...finish
            @map.inline_ranges << element
            @map.link_ranges << element if name == "a"
          end
        elsif HtmlMap::INLINE_TAGS.include?(name) && !text.end_with?("/>")
          @html_stack << [name, start]
        end
        finish
      end

      def entity(text, start, finish)
        CGI.unescapeHTML(text).each_char { |char| emit_char(char, start, finish) }
        finish
      end

      # Simplified emphasis pairing: a delimiter run closes the stack top when
      # it matches exactly (same char, same length) and is preceded by
      # non-space; otherwise it opens when followed by non-space; otherwise it
      # stays literal. Openers are emitted provisionally and removed from the
      # projection when their closer arrives — an opener that never closes
      # stays literal, matching how it renders.
      def emphasis(text, start, finish)
        top = @emphasis_stack.last
        if closer?(start) && top && top[0] == text
          _, open_start, plain_index = @emphasis_stack.pop
          remove_plain(plain_index, text.length)
          @map.inline_ranges << (open_start...finish)
        elsif opener?(finish)
          @emphasis_stack << [text, start, @map.plain.length]
          emit_verbatim(start, finish)
        else
          emit_verbatim(start, finish)
        end
        finish
      end

      def opener?(finish)
        following = @source[finish]
        !following.nil? && !following.match?(/\s/)
      end

      def closer?(start)
        preceding = start.positive? ? @source[start - 1] : nil
        !preceding.nil? && !preceding.match?(/\s/)
      end

      def remove_plain(index, count)
        @map.plain.slice!(index, count)
        @map.starts.slice!(index, count)
        @map.ends.slice!(index, count)
        @map.block_ids.slice!(index, count)
      end
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `bin/rails test test/services/selection_linker/markdown_map_test.rb`
Expected: 16 runs, 0 failures, 0 errors.

- [ ] **Step 5: Commit**

```bash
git add app/services/selection_linker/markdown_map.rb test/services/selection_linker/markdown_map_test.rb
git commit -m "feat: add markdown inline scanning to source map"
```

---

### Task 7: Markdown end-to-end through `SelectionLinker`

Update the pre-existing markdown tests whose semantics change and add the new markdown behaviors. Changes to existing tests in `test/services/selection_linker_test.rb`:

| Test | Old | New | Why |
| --- | --- | --- | --- |
| "rejects a match inside an existing markdown link url" | `UnsafeMatch` | `NotFound` | URLs are dropped from the projection; the text isn't findable |
| "rejects a markdown selection containing an existing link" (selection is raw `[..](..)`) | `UnsafeMatch` | `NotFound` | raw syntax never appears in rendered text |
| "rejects a markdown selection containing a link with an escaped closing bracket" | `UnsafeMatch` | `NotFound` | same |
| "rejects a markdown selection crossing an existing link" (raw syntax) | `UnsafeMatch` | `NotFound` | same |

Unchanged: "rejects a match inside an existing markdown link label" stays `UnsafeMatch` (label text *is* findable; match sits entirely inside the link range), plus all passing markdown tests ("wraps a markdown selection…", occurrence tests, "escapes closing brackets…", "raises NotFound…").

**Files:**
- Test: `test/services/selection_linker_test.rb`

- [ ] **Step 1: Update changed tests and add new failing markdown tests**

Replace the four tests listed above with:

```ruby
  test "does not find text that only appears in a markdown link url" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: "see [label](/beta.html) end",
        extension: ".md",
        selected_text: "beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "does not find a selection containing raw markdown link syntax" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: "see [beta](/old.html) end",
        extension: ".md",
        selected_text: "[beta](/old.html)",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "does not find raw link syntax with an escaped closing bracket" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: "see [b\\]](/old.html) end",
        extension: ".md",
        selected_text: "[b\\]](/old.html)",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end

  test "does not find a selection containing raw syntax crossing a link" do
    assert_raises SelectionLinker::NotFound do
      SelectionLinker.link(
        source: "see [beta](/old.html) and more",
        extension: ".md",
        selected_text: "[beta](/old.html) and",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end
```

Add the new markdown behavior tests at the end of the class:

```ruby
  test "wraps a markdown selection spanning inline code" do
    result = SelectionLinker.link(
      source: "this is `some` code",
      extension: ".md",
      selected_text: "some code",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal "this is [`some` code](/x.html)", result
  end

  test "snaps to cover partially selected markdown emphasis" do
    result = SelectionLinker.link(
      source: "make *this bold* now ok",
      extension: ".md",
      selected_text: "bold now",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal "make [*this bold* now](/x.html) ok", result
  end

  test "segments around an existing markdown link" do
    result = SelectionLinker.link(
      source: "see [beta](/old.html) and more",
      extension: ".md",
      selected_text: "beta and",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal "see [beta](/old.html) [and](/x.html) more", result
  end

  test "segments around an image" do
    result = SelectionLinker.link(
      source: "pic ![alt](i.png) end",
      extension: ".md",
      selected_text: "pic end",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal "[pic](/x.html) ![alt](i.png) [end](/x.html)", result
  end

  test "rejects a markdown selection spanning two paragraphs" do
    error = assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "alpha\n\nbeta",
        extension: ".md",
        selected_text: "alpha beta",
        occurrence: 0,
        url: "/x.html"
      )
    end

    assert_equal "Selection spans multiple paragraphs — select within one.", error.message
  end

  test "rejects a selection inside a fenced code block" do
    error = assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "before\n\n```\ncode here\n```\n",
        extension: ".md",
        selected_text: "code here",
        occurrence: 0,
        url: "/x.html"
      )
    end

    assert_equal "Selection is inside a code block.", error.message
  end

  test "links heading text within the heading block" do
    result = SelectionLinker.link(
      source: "# Title\ntext",
      extension: ".md",
      selected_text: "Title",
      occurrence: 0,
      url: "/x.html"
    )

    assert_equal "# [Title](/x.html)\ntext", result
  end

  test "links one list item but rejects selections across items" do
    result = SelectionLinker.link(
      source: "- alpha\n- beta\n",
      extension: ".md",
      selected_text: "alpha",
      occurrence: 0,
      url: "/x.html"
    )
    assert_equal "- [alpha](/x.html)\n- beta\n", result

    assert_raises SelectionLinker::UnsafeMatch do
      SelectionLinker.link(
        source: "- alpha\n- beta\n",
        extension: ".md",
        selected_text: "alpha beta",
        occurrence: 0,
        url: "/x.html"
      )
    end
  end
```

- [ ] **Step 2: Run the full selection linker suite**

Run: `bin/rails test test/services/selection_linker_test.rb test/services/selection_linker/`
Expected: All pass — the real `MarkdownMap` from Tasks 5–6 is already wired in via `SelectionLinker#build_map`. If a markdown test fails, debug the map (inspect `MarkdownMap.build(source).plain` and the recorded ranges in a `bin/rails console`) before touching the planner — the planner is format-agnostic and already covered by Task 3.

- [ ] **Step 3: Commit**

```bash
git add test/services/selection_linker_test.rb
git commit -m "feat: link markdown selections across inline markup"
```

---

### Task 8: Full-suite regression

`ExpansionProcessor` and `GenerateExpansionJob` consume `SelectionLinker` — verify nothing else in the app depended on the old error semantics.

**Files:**
- Possibly modify: `test/jobs/generate_expansion_job_test.rb` (it references the "Selection not found in source" message — still raised, so likely untouched)

- [ ] **Step 1: Run the entire test suite**

Run: `bin/rails test`
Expected: 0 failures, 0 errors. If `generate_expansion_job_test.rb` or `expansion_processor_test.rb` fail, read the failing assertion: if it asserted an `UnsafeMatch` case that is now `NotFound` (per the tables in Tasks 4 and 7), update the expectation to match the new semantics; any other failure is a bug in Tasks 1–7 — fix the implementation, not the test.

- [ ] **Step 2: Commit (only if any test needed updating)**

```bash
git add test/
git commit -m "test: align expansion pipeline tests with new selection linker"
```

---

## Self-review notes (already applied)

- **Spec coverage:** snap (Task 3/4/7), segment-around-links incl. images (Tasks 3/4/6/7), cross-block reject (Tasks 3/4/7), code-block reject (Tasks 5/7), whitespace tolerance (Tasks 1/4), entity handling (Tasks 2/6), byte-identical splicing (Task 4 splice + entity-atomicity in maps), occurrence-in-rendered-space (Tasks 1/4), error messages table (Tasks 3/4/7). Requirement "no frontend changes" — no task touches `app/assets`.
- **Known accepted limitations** (from spec "Scope limits"): emphasis runs of length ≥3 stay literal; emphasis pairs spanning a link-label boundary may mispair; `<http://…>` autolinks are dropped from the projection (selecting one yields `NotFound`); lazy list-item continuation lines share the item's block id only when no blank line intervenes.
- **Type consistency:** `Map` fields (`plain/starts/ends/block_ids/inline_ranges/link_ranges/unlinkable_ranges`) used identically in both maps and the planner; `Planner.plan(map, plain_range, source:)` signature consistent across Tasks 3/4; `SelectionLocator.locate(map, selected_text, occurrence)` consistent across Tasks 1/3/4.
