# Captures Commonmarker's output for every corpus file into a sibling .html.
# Run with: bin/rails runner script/golden_markdown.rb
dir = Rails.root.join("testdata/golden/markdown")
puts "Commonmarker::Config::OPTIONS = #{Commonmarker::Config::OPTIONS.inspect}"
puts "Commonmarker::Config::PLUGINS = #{Commonmarker::Config::PLUGINS.inspect}"
puts "Effective (after process_options) = #{Commonmarker::Config.process_options(FilesController::MARKDOWN_OPTIONS).inspect}"
Dir[dir.join("*.md")].sort.each do |path|
  html = Commonmarker.to_html(File.read(path), options: FilesController::MARKDOWN_OPTIONS)
  File.write(path.sub(/\.md\z/, ".html"), html)
  puts "wrote #{File.basename(path, ".md")}.html"
end
