class FileWatcher
  FILES_DIR = ResolvesServedFiles::FILES_DIR

  def self.start
    new.start
  end

  def initialize(dir = FILES_DIR)
    @dir = dir
  end

  def start
    listener = Listen.to(@dir.to_s) do |modified, added, removed|
      handle(modified, added, removed)
    end
    listener.start
    listener
  end

  def handle(_modified, added, removed)
    added.each { |path| ServedFile.record(File.basename(path)) }
    removed.each { |path| ServedFile.remove(File.basename(path)) }
  end
end
