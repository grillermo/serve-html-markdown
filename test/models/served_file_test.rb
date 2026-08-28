require "test_helper"
require "tmpdir"

class ServedFileTest < ActiveSupport::TestCase
  setup do
    @files_dir = Pathname.new(Dir.mktmpdir("served-files-model"))
    swap_constant(ServedFile, :FILES_DIR, @files_dir)
  end

  teardown do
    FileUtils.remove_entry(@files_dir)
    restore_constants
  end

  test "sync! inserts allowed files and ignores unsupported ones" do
    write "a.html", "x"
    write "b.md", "x"
    write "c.markdown", "x"
    write "d.txt", "x"
    write ".gitkeep", ""

    ServedFile.sync!

    assert_equal %w[a.html b.md c.markdown].sort, ServedFile.pluck(:name).sort
  end

  test "sync! is idempotent and keeps created_at frozen" do
    write "a.html", "x"
    ServedFile.sync!
    original = ServedFile.find_by!(name: "a.html").created_at

    travel 1.hour do
      ServedFile.sync!
    end

    assert_equal 1, ServedFile.where(name: "a.html").count
    assert_equal original, ServedFile.find_by!(name: "a.html").created_at
  end

  test "sync! prunes rows for files no longer on disk" do
    write "a.html", "x"
    write "b.md", "x"
    ServedFile.sync!
    @files_dir.join("a.html").delete

    ServedFile.sync!

    assert_equal %w[b.md], ServedFile.pluck(:name)
  end

  test "sync! empties the table when no supported files remain" do
    write "a.html", "x"
    ServedFile.sync!
    @files_dir.join("a.html").delete

    ServedFile.sync!

    assert_equal 0, ServedFile.count
  end

  test "record inserts an allowed name and ignores unsupported extensions" do
    ServedFile.record("page.html")
    ServedFile.record("skip.txt")

    assert_equal %w[page.html], ServedFile.pluck(:name)
  end

  test "record leaves created_at frozen when the row already exists" do
    ServedFile.record("page.html")
    original = ServedFile.find_by!(name: "page.html").created_at

    travel 1.hour do
      ServedFile.record("page.html")
    end

    assert_equal 1, ServedFile.count
    assert_equal original, ServedFile.find_by!(name: "page.html").created_at
  end

  test "remove deletes the row" do
    ServedFile.record("page.html")

    ServedFile.remove("page.html")

    assert_equal 0, ServedFile.count
  end

  test "record_modification creates the row when absent" do
    ServedFile.record_modification("notes.md")

    assert_equal %w[notes.md], ServedFile.pluck(:name)
  end

  test "record_modification bumps updated_at without moving created_at" do
    ServedFile.record("notes.md")
    row = ServedFile.find_by!(name: "notes.md")
    created = row.created_at

    travel 1.hour do
      ServedFile.record_modification("notes.md")
    end
    row.reload

    assert_equal created, row.created_at
    assert_operator row.updated_at, :>, created
  end

  test "newest returns the most recently added row, ties broken by id" do
    a = ServedFile.create!(name: "a.html", created_at: 2.days.ago)
    _b = ServedFile.create!(name: "b.html", created_at: 1.day.ago)
    c = ServedFile.create!(name: "c.html", created_at: 1.day.ago)

    assert_equal c.name, ServedFile.newest.name
    assert_not_equal a.name, ServedFile.newest.name
  end

  test "newest is nil for an empty table" do
    assert_nil ServedFile.newest
  end

  private

  def write(name, content)
    @files_dir.join(name).write(content)
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
