# Captures a deterministic corpus of Markdown sources whose SelectionLinker map
# is NON-monotone (a raw inline tag inside a link label overshoots the label
# end, so later plain characters map to smaller source offsets than earlier
# ones), with EXHAUSTIVE selections, from the real Ruby implementation.
#
#   bin/rails runner script/golden_linker_nonmonotone.rb
#
# Writes testdata/golden/linker/nonmonotone.json (same shape as cases.json) so
# the Go port is covered without needing Ruby at test time.

ALPHABET = ["[", "]", "(", ")", "<br", ">", "*", "`", "&amp;"].freeze
rng = Random.new(20261117)

def non_monotone?(map)
  map.starts.each_cons(2).any? { |a, b| b < a } || map.ends.each_cons(2).any? { |a, b| b < a }
end

# The overshoot needs "[", "<br", "]", "(", ")" and ">" in this order (a raw
# tag opened inside a link label that closes after the label's "](...)"), so
# every source is that skeleton with 0-2 random symbols between the parts.
SKELETON = ["[", "<br", "]", "(", ")", ">"].freeze
def filler(rng) = Array.new(rng.rand(0..2)) { ALPHABET[rng.rand(ALPHABET.size)] }.join

sources = []
seen = {}
20_000.times do
  source = filler(rng) + SKELETON.map { |part| part + filler(rng) }.join + "\n"
  next if seen[source]
  seen[source] = true
  map = SelectionLinker::MarkdownMap.build(source)
  sources << source if non_monotone?(map)
end
abort "expected many non-monotone sources, got #{sources.size}" if sources.size < 60
sources = sources.shuffle(random: rng).first(60).sort

cases = []
sources.each_with_index do |source, index|
  plain = SelectionLinker::MarkdownMap.build(source).plain
  chars = plain.chars
  selections = []
  (0...chars.size).each do |from|
    (from...chars.size).each do |to|
      selection = chars[from..to].join
      selections << selection unless selection.strip.empty?
    end
  end
  selections.uniq.each do |selection|
    occurrences = plain.scan(Regexp.new(Regexp.escape(selection))).size > 1 ? [0, 1] : [0]
    occurrences.each do |occurrence|
      cases << { name: format("nonmonotone_%02d_%03d", index, cases.size), extension: ".md",
                 source: source, selected_text: selection, occurrence: occurrence }
    end
  end
end

out = cases.map do |c|
  result = SelectionLinker.link(
    source: c[:source], extension: c[:extension], selected_text: c[:selected_text],
    occurrence: c[:occurrence], url: "/x--expand-1.html"
  )
  c.merge(output: result)
rescue SelectionLinker::Error => e
  c.merge(error: "#{e.class.name.demodulize}: #{e.message}")
end

File.write(Rails.root.join("testdata/golden/linker/nonmonotone.json"), JSON.pretty_generate(out) + "\n")
puts "wrote #{out.size} cases from #{sources.size} sources (#{out.count { |x| x.key?(:output) }} output, #{out.count { |x| x.key?(:error) }} error)"
