require "test_helper"
require "tmpdir"

class ExpansionProcessorTest < ActiveSupport::TestCase
  HTML = "<!DOCTYPE html><html><body>answer</body></html>"

  setup do
    @files_dir = Pathname.new(Dir.mktmpdir("expansion-processor"))
    @user = User.create!(email: "processor@example.com", password: "s3cretpass")
    @expansion = @user.expansions.create!(file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "Why?")
    swap_constant(ExpansionProcessor, :FILES_DIR, @files_dir)
  end

  teardown do
    FileUtils.remove_entry(@files_dir)
    restore_constants
  end

  test "generates an expansion and links the latest source while holding its lock" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")

    with_expander(->(**) { HTML }) do
      assert_equal "/notes--expand-1.html", ExpansionProcessor.process(@expansion)
    end

    assert_equal HTML, @files_dir.join("notes--expand-1.html").read
    assert_equal "Alpha [beta](/notes--expand-1.html) gamma.", @files_dir.join("notes.md").read
  end

  test "records the new expand file and bumps the source file's updated_at" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    source_row = ServedFile.create!(name: "notes.md", created_at: 2.days.ago, updated_at: 2.days.ago)
    original_updated = source_row.updated_at

    with_expander(->(**) { HTML }) do
      ExpansionProcessor.process(@expansion)
    end

    assert ServedFile.exists?(name: "notes--expand-1.html"),
      "expected the new expand file to be recorded"
    assert_operator ServedFile.find_by!(name: "notes.md").updated_at, :>, original_updated
  end

  test "does not write either file when the expander fails" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")

    with_expander(->(**) { raise ClaudeExpandService::Error, "cli unavailable" }) do
      assert_raises(ClaudeExpandService::Error) { ExpansionProcessor.process(@expansion) }
    end

    assert_equal "Alpha beta gamma.", @files_dir.join("notes.md").read
    assert_not @files_dir.join("notes--expand-1.html").exist?
  end

  test "uses the next suffix and leaves source unchanged when latest source cannot be linked" do
    @files_dir.join("notes.md").write("Alpha gamma.")
    @files_dir.join("notes--expand-1.html").write("taken")

    with_expander(->(**) { HTML }) do
      assert_raises(SelectionLinker::NotFound) { ExpansionProcessor.process(@expansion) }
    end

    assert_equal "Alpha gamma.", @files_dir.join("notes.md").read
    assert_equal "taken", @files_dir.join("notes--expand-1.html").read
    assert_not @files_dir.join("notes--expand-2.html").exist?
  end

  test "stamps source_read, lock_acquired, link_rewritten, and files_written" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")

    with_expander(->(**) { HTML }) do
      ExpansionProcessor.process(@expansion)
    end

    timings = @expansion.reload.timings
    %w[source_read lock_acquired link_rewritten files_written].each do |stage|
      assert_kind_of Integer, timings[stage], "expected #{stage} to be stamped"
    end
    assert_operator timings["source_read"], :<=, timings["lock_acquired"]
    assert_operator timings["lock_acquired"], :<=, timings["link_rewritten"]
    assert_operator timings["link_rewritten"], :<=, timings["files_written"]
  end

  test "passes the expansion to the expander" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    received = nil

    with_expander(->(expansion:, **) { received = expansion; HTML }) do
      ExpansionProcessor.process(@expansion)
    end

    assert_equal @expansion, received
  end

  test "writes the rewrite to the next version and leaves the source untouched" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @expansion.update!(mode: "edit_in_place")
    rewritten = "Alpha ⟦EXPANSION_ANCHOR⟧beta, at length, gamma. Plus much more text here."

    with_rewriter(->(**) { rewritten }) do
      assert_equal "/notes--v2.md#expansion-anchor", ExpansionProcessor.process(@expansion)
    end

    assert_equal "Alpha beta gamma.", @files_dir.join("notes.md").read
    assert_equal(
      %(Alpha <a id="expansion-anchor"></a>beta, at length, gamma. Plus much more text here.),
      @files_dir.join("notes--v2.md").read
    )
    assert ServedFile.exists?(name: "notes--v2.md")
  end

  test "includes the fallback anchor in the completed url" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @expansion.update!(mode: "edit_in_place", fallback_anchor: "section two")

    with_rewriter(->(**) { "Alpha ⟦EXPANSION_ANCHOR⟧beta expanded gamma, and then some more." }) do
      assert_equal "/notes--v2.md?fallback=section%20two#expansion-anchor", ExpansionProcessor.process(@expansion)
    end
  end

  test "rewrites a versioned file into the next version of the same family" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @files_dir.join("notes--v2.md").write("Alpha beta gamma, expanded once already.")
    @expansion.update!(mode: "edit_in_place", file_name: "notes--v2.md")

    with_rewriter(->(**) { "Alpha ⟦EXPANSION_ANCHOR⟧beta expanded twice now, with more words." }) do
      assert_equal "/notes--v3.md#expansion-anchor", ExpansionProcessor.process(@expansion)
    end

    assert @files_dir.join("notes--v3.md").exist?
  end

  test "keeps only the first anchor sentinel" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @expansion.update!(mode: "edit_in_place")

    with_rewriter(->(**) { "A ⟦EXPANSION_ANCHOR⟧B ⟦EXPANSION_ANCHOR⟧C with plenty of extra text." }) do
      ExpansionProcessor.process(@expansion)
    end

    assert_equal(
      %(A <a id="expansion-anchor"></a>B C with plenty of extra text.),
      @files_dir.join("notes--v2.md").read
    )
  end

  test "still writes a version when the model omits the sentinel" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")
    @expansion.update!(mode: "edit_in_place")

    with_rewriter(->(**) { "Alpha beta gamma, expanded without any marker at all." }) do
      assert_equal "/notes--v2.md#expansion-anchor", ExpansionProcessor.process(@expansion)
    end

    assert_equal "Alpha beta gamma, expanded without any marker at all.", @files_dir.join("notes--v2.md").read
  end

  test "refuses a rewrite shorter than half the source" do
    @files_dir.join("notes.md").write("Alpha beta gamma, a reasonably long document body.")
    @expansion.update!(mode: "edit_in_place")

    with_rewriter(->(**) { "Alpha." }) do
      error = assert_raises(ExpansionProcessor::TruncatedRewrite) { ExpansionProcessor.process(@expansion) }
      assert_equal "Rewrite looked truncated.", error.message
    end

    assert_not @files_dir.join("notes--v2.md").exist?
    assert_equal "Alpha beta gamma, a reasonably long document body.", @files_dir.join("notes.md").read
  end

  private

  def with_rewriter(callable)
    fake = Object.new
    fake.define_singleton_method(:rewrite, &callable)
    fake.define_singleton_method(:expand) { |**| raise "expand must not be called in edit_in_place mode" }
    swap_constant(ExpansionProcessor, :EXPANDER, fake)
    yield
  end

  def with_expander(callable)
    swap_constant(ExpansionProcessor, :EXPANDER, Object.new.tap { |fake| fake.define_singleton_method(:expand, &callable) })
    yield
  end

  def swap_constant(owner, name, value)
    @constants ||= {}
    @constants[[owner, name]] ||= owner.const_get(name) if owner.const_defined?(name, false)
    owner.send(:remove_const, name) if owner.const_defined?(name, false)
    owner.const_set(name, value)
  end

  def restore_constants
    @constants&.each do |(owner, name), value|
      owner.send(:remove_const, name) if owner.const_defined?(name, false)
      owner.const_set(name, value)
    end
  end
end
