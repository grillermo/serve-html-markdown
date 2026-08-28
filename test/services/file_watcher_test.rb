require "test_helper"

class FileWatcherTest < ActiveSupport::TestCase
  test "handle records added files and removes deleted ones" do
    watcher = FileWatcher.new

    watcher.handle(
      [],
      ["/served/files/new.html", "/served/files/skip.txt"],
      []
    )

    assert_equal %w[new.html], ServedFile.pluck(:name)

    watcher.handle([], [], ["/served/files/new.html"])

    assert_equal 0, ServedFile.count
  end

  test "handle ignores modified paths" do
    watcher = FileWatcher.new

    watcher.handle(["/served/files/notes.md"], [], [])

    assert_equal 0, ServedFile.count
  end
end
