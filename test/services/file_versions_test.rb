require "test_helper"
require "tmpdir"

class FileVersionsTest < ActiveSupport::TestCase
  test "parses a bare filename as version 1" do
    parsed = FileVersions.parse("foo.md")

    assert_equal "foo", parsed.stem
    assert_equal 1, parsed.version
    assert_equal ".md", parsed.extension
    assert_equal "foo.md", parsed.base_name
  end

  test "parses a versioned filename" do
    parsed = FileVersions.parse("foo--v3.html")

    assert_equal "foo", parsed.stem
    assert_equal 3, parsed.version
    assert_equal ".html", parsed.extension
    assert_equal "foo.html", parsed.base_name
  end

  test "treats --v1 and padded versions as part of the stem" do
    assert_equal 1, FileVersions.parse("foo--v1.md").version
    assert_equal "foo--v1", FileVersions.parse("foo--v1.md").stem
    assert_equal "foo--v02", FileVersions.parse("foo--v02.md").stem
  end

  test "builds a name for a given version" do
    assert_equal "foo--v4.md", FileVersions.parse("foo--v2.md").name_for(4)
    assert_equal "foo.md", FileVersions.parse("foo--v2.md").name_for(1)
  end

  test "lists the family in version order from ServedFile" do
    %w[foo.md foo--v3.md foo--v2.md unrelated.md].each { |name| ServedFile.create!(name: name) }

    assert_equal %w[foo.md foo--v2.md foo--v3.md], FileVersions.parse("foo--v2.md").family_names
  end

  test "excludes family members with a different extension" do
    %w[foo.md foo--v2.html foo--v2.md].each { |name| ServedFile.create!(name: name) }

    assert_equal %w[foo.md foo--v2.md], FileVersions.parse("foo.md").family_names
  end

  test "escapes LIKE wildcards in the stem" do
    %w[a_b.md axb--v2.md a_b--v2.md].each { |name| ServedFile.create!(name: name) }

    assert_equal %w[a_b.md a_b--v2.md], FileVersions.parse("a_b.md").family_names
  end

  test "returns the first free version path starting at 2" do
    Dir.mktmpdir do |dir|
      files_dir = Pathname.new(dir)
      files_dir.join("foo.md").write("v1")
      files_dir.join("foo--v2.md").write("v2")

      assert_equal files_dir.join("foo--v3.md"), FileVersions.parse("foo.md").next_path(files_dir)
    end
  end

  test "next_path from a versioned file stays in the same family" do
    Dir.mktmpdir do |dir|
      files_dir = Pathname.new(dir)
      files_dir.join("foo--v2.md").write("v2")

      assert_equal files_dir.join("foo--v3.md"), FileVersions.parse("foo--v2.md").next_path(files_dir)
    end
  end

  test "RESERVED_SUFFIX matches any --v<digits> stem" do
    assert_match FileVersions::RESERVED_SUFFIX, "foo--v2"
    assert_match FileVersions::RESERVED_SUFFIX, "foo--v1"
    assert_match FileVersions::RESERVED_SUFFIX, "foo--v007"
    assert_no_match FileVersions::RESERVED_SUFFIX, "foo--version"
    assert_no_match FileVersions::RESERVED_SUFFIX, "foo-v2"
  end
end
