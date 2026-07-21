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

  test "does not write either file when the expander fails" do
    @files_dir.join("notes.md").write("Alpha beta gamma.")

    with_expander(->(**) { raise ClaudeExpandService::Error, "cli unavailable" }) do
      assert_raises(ClaudeExpandService::Error) { ExpansionProcessor.process(@expansion) }
    end

    assert_equal "Alpha beta gamma.", @files_dir.join("notes.md").read
    assert_not @files_dir.join("notes--expand-1.html").exist?
  end

  test "uses the next suffix and leaves source unchanged when latest source cannot be linked" do
    @files_dir.join("notes.md").write("Alpha **be**ta gamma.")
    @files_dir.join("notes--expand-1.html").write("taken")

    with_expander(->(**) { HTML }) do
      assert_raises(SelectionLinker::NotFound) { ExpansionProcessor.process(@expansion) }
    end

    assert_equal "Alpha **be**ta gamma.", @files_dir.join("notes.md").read
    assert_equal "taken", @files_dir.join("notes--expand-1.html").read
    assert_not @files_dir.join("notes--expand-2.html").exist?
  end

  private

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
