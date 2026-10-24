require "cgi"

# Plain spoken text out of a WebVTT caption file. YouTube's auto-captions tag every word
# with a timestamp and repeat each line across consecutive cues, so a 40-minute talk is
# ~400 KB of VTT for ~40 KB of words.
module VttText
  HEADER = /\A(WEBVTT|Kind:|Language:|NOTE\b)/
  TIMING = /-->/

  def self.call(vtt)
    lines = vtt.to_s.each_line.map(&:strip).reject do |line|
      line.empty? || line.match?(HEADER) || line.match?(TIMING) || line.match?(/\A\d+\z/)
    end
    lines = lines.map { |line| CGI.unescapeHTML(line.gsub(/<[^>]*>/, "")).squeeze(" ").strip }
    lines.reject(&:empty?).chunk_while { |a, b| a == b }.map(&:first).join("\n")
  end
end
