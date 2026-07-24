require "test_helper"
require "tmpdir"

class ServedHtmlWriterTest < ActiveSupport::TestCase
  test "writes slugified html and records it" do
    Dir.mktmpdir do |dir|
      dir = Pathname.new(dir)
      name = ServedHtmlWriter.write(title: "The Big Idea!", summary_html: "<p>Hi</p>", files_dir: dir)

      assert_equal "the-big-idea.html", name
      body = dir.join(name).read
      assert_includes body, "<title>The Big Idea!</title>"
      assert_includes body, "<p>Hi</p>"
      assert ServedFile.exists?(name: name)
    end
  end

  test "avoids collisions with a numeric suffix" do
    Dir.mktmpdir do |dir|
      dir = Pathname.new(dir)
      first = ServedHtmlWriter.write(title: "Dup", summary_html: "<p>1</p>", files_dir: dir)
      second = ServedHtmlWriter.write(title: "Dup", summary_html: "<p>2</p>", files_dir: dir)

      assert_equal "dup.html", first
      assert_equal "dup-1.html", second
    end
  end

  test "falls back to a default slug when the title has no word characters" do
    Dir.mktmpdir do |dir|
      dir = Pathname.new(dir)
      name = ServedHtmlWriter.write(title: "!!!", summary_html: "<p>x</p>", files_dir: dir)
      assert_equal "summary.html", name
    end
  end
end
