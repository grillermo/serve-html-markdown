# Captures golden input/output pairs from the real Ruby TwitterUrl, YoutubeUrl and
# VttText, so the Go port (internal/video) can be verified against them.
#
#   bin/rails runner script/golden_video.rb [--seed N] [--urls N] [--vtts N] [--out PATH]
#
# Defaults write testdata/golden/video/cases.json (the committed corpus). A bigger,
# different corpus for a one-off differential run:
#
#   bin/rails runner script/golden_video.rb --seed 99 --urls 20000 --vtts 5000 --out /tmp/big.json
#   VIDEO_GOLDEN_FILE=/tmp/big.json go test ./internal/video -run Golden
#
# Entries: {"kind":"url","input":…,"twitter":<normalized|null>,"youtube":<id|null>} (or
# "ruby_raises" where a URI::InvalidComponentError escapes Ruby's services) and
# {"kind":"vtt","input":…,"output":…} or, where Ruby raises (a numeric reference to a
# surrogate decodes to invalid UTF-8), {"kind":"vtt","input":…,"error":…}.
require "json"
require "optparse"

opts = { seed: 20261117, urls: 1500, vtts: 700, out: Rails.root.join("testdata/golden/video/cases.json").to_s }
OptionParser.new do |o|
  o.on("--seed N", Integer) { |v| opts[:seed] = v }
  o.on("--urls N", Integer) { |v| opts[:urls] = v }
  o.on("--vtts N", Integer) { |v| opts[:vtts] = v }
  o.on("--out PATH") { |v| opts[:out] = v }
end.parse!(ARGV)

rng = Random.new(opts[:seed])
pick = ->(list) { list[rng.rand(list.size)] }
ID = "KpILHX2D-AA"

SCHEMES = ["https://", "http://", "HTTPS://", "", "", "ftp://", "FTP://", "ldap://", "mailto://", "file://", "ws://", "wss://", "x-y+z.1://", "//", "https:/", "https:", "1a://"]
HOSTS = %w[
  x.com twitter.com mobile.x.com mobile.twitter.com fxtwitter.com vxtwitter.com fixupx.com fixvx.com
  twittpr.com xcancel.com nitter.net X.COM Twitter.Com www.x.com www.twitter.com www.www.x.com
  youtube.com m.youtube.com music.youtube.com gaming.youtube.com youtube-nocookie.com youtu.be
  www.youtube.com www.youtu.be WWW.YOUTUBE.COM YouTu.be studio.youtube.com notx.com x.com.evil.com
  evil.com youtube.com.evil.com xx.com example.org localhost 127.0.0.1 [::1] x.com:443 x.com:abc x.com:
  www.youtube.com:443 user@x.com user:pw@youtube.com x.com@evil.com
]
TWEET_PATHS = [
  "/foo/status/123", "/i/status/123", "/i/web/status/123", "/foo/statuses/123", "/foo/status/123/video/1",
  "/foo/status/123/photo/2", "/foo/status/abc", "/foo/status/", "/foo/status", "/foo", "/", "",
  "/status/123", "/a/b/status/123", "/foo/status/0012", "/foo/status/123abc", "/i/web/status/9/x",
  "/web/status/5", "/i/web/statuses/5", "/foo/STATUS/5", "/%66oo/status/1", "/foo/status/%31",
  "/foo//status/1", "//foo/status/1", "/foo/status/١٢٣", "/fóo/status/1", "/f o/status/1",
  "/foo/status/12345678901234567890123", "/%2Ffoo/status/1", "//%2Ffoo/status/1", "/foo/status/1;type=a", "/a;type=a/foo/status/1"
]
YT_PATHS = [
  "/watch", "/watch/", "/shorts/", "/live/", "/embed/", "/v/", "/e/", "/watch/", "/", "", "/@channel",
  "/playlist", "/attribution_link", "/channel/", "/user/", "/#{ID}", "/shorts//", "/watch//"
]
QUERY_BITS = [
  "v=#{ID}", "v=short", "v=", "v", "t=42", "si=abc", "feature=share", "list=PL123", "u=%2Fwatch%3Fv%3D#{ID}",
  "u=/shorts/#{ID}", "u=%2Fattribution_link", "u=#{ID}", "a=xyz", "s=20", "v=%4BpILHX2D-AA", "v=KpILHX2D+AA",
  "v=#{ID}x", "v=%zz", "t=a%", "t=a%4", "t=a%zz", "t=a|b", "t=<b>", "t={x}", "t=é", "x=%25", "&", "=",
  "v=#{ID}&v=short", "v=short&v=#{ID}", "u=%2Fwatch%3Fv%3Dshort", "u=%2Fshorts%2F#{ID}%3Ffeature%3Dx"
]
FRAGS = ["", "", "", "#t=1", "#", "#a#b", "#a b", "#é", "#a%", "#a%41", "#a|b"]
SPECIAL = [" ", "\t", "\n", "\r", "\0", "\v", "\f", "%", "%zz", "%41", "%4", "|", "<", ">", "\\", "^", "`", "{", "}", "[", "]", '"',
           "é", "\u00a0", "\u0085", "\u2003", "#", "?", "&", "=", "+", ";", ":", "@", ".", "-", "_", "/", "'", "!", "*", "(", ")", "~", ",", "$", "😀"]

TWITTER_HOSTS = HOSTS[0, 15] + %w[user@x.com x.com:443 www.x.com]
YOUTUBE_HOSTS = HOSTS[15, 9] + %w[user:pw@youtube.com www.youtube.com:443]

def_url = lambda do
  family = rng.rand
  host = if family < 0.4 then pick.(TWITTER_HOSTS)
         elsif family < 0.8 then pick.(YOUTUBE_HOSTS)
         else pick.(HOSTS)
         end
  tweet_host = TWITTER_HOSTS.include?(host)
  path = if tweet_host && rng.rand < 0.85 then pick.(TWEET_PATHS)
         elsif !tweet_host && rng.rand < 0.85 then pick.(YT_PATHS)
         else rng.rand < 0.5 ? pick.(TWEET_PATHS) : "/" + ID
         end
  path += ID if path.end_with?("/") && rng.rand < 0.7 && path.length > 1
  path = "/" + ID if host.include?("youtu.be") && rng.rand < 0.7
  query = rng.rand < 0.6 ? "?" + Array.new(rng.rand(1..3)) { pick.(QUERY_BITS) }.join("&") : ""
  url = (rng.rand < 0.6 ? "https://" : pick.(SCHEMES)) + host + path + query + (rng.rand < 0.7 ? "" : pick.(FRAGS))
  url = pick.([" ", "\n", "\t", "\0"]) + url + pick.([" ", "\n", "\t\n", "\0"]) if rng.rand < 0.2
  # mutate: insert 1..2 special characters anywhere
  rng.rand(1..2).times { url = url.dup.insert(rng.rand(url.length + 1), pick.(SPECIAL)) } if rng.rand < 0.25
  url
end

TAGS = ["<c>", "</c>", "<00:00:01.000>", "<c.colorE5E5E5>", "<b>", "</b>", "<", ">", "<>", "<a", "a>", "<c class=\"x\">", "<!-- x -->"]
WORDS = %w[How the do Q&amp;A time &lt;b&gt; &amp;amp; &nbsp; &#233; &#x1F600; &#39; &apos; &quot; &#0; &#xFFFFFFFF; &#; &#x; &AMP; hello world
           caf\u00e9 \u00a0 42 \u0661\u0662 NOTE\u0301 NOTE\u2160 NOTE\u24b6 NOTE_ NOTE\u203f NOTE\u00a0x NOTE-x NOTE\u0661 NOTE\u4e2d 3.5 -1 NOTE NOTES NOTE: NOTEé WEBVTT WEBVTTX Kind: Language: Kind Language kind: webvtt note --> -> a-->b 00:00:00.240 \u00e9\u00e9 😀]
SEPS = [" ", " ", " ", "  ", "   ", "\t", "\t\t", "\u00a0", "\0", "\v", "\f", "\r", ""]
LINE_ENDS = ["\n", "\n", "\n", "\n", "\r\n", "\n\n", "\r", ""]

def_vtt = lambda do
  lines = Array.new(rng.rand(0..14)) do
    case rng.rand(10)
    when 0 then pick.(["WEBVTT", "Kind: captions", "Language: en", "NOTE a note", "NOTE", "WEBVTT - x"])
    when 1 then "00:00:0#{rng.rand(10)}.240 --> 00:00:0#{rng.rand(10)}.150 align:start position:0%"
    when 2 then rng.rand(0..99).to_s
    when 3 then ""
    else
      Array.new(rng.rand(1..6)) { (rng.rand < 0.3 ? pick.(TAGS) : "") + (rng.rand < 0.01 ? "&#xD800;" : pick.(WORDS)) + (rng.rand < 0.3 ? pick.(TAGS) : "") }.join(pick.(SEPS))
    end
  end
  # rolling captions: repeat some lines
  lines = lines.flat_map { |l| rng.rand < 0.25 ? [l, l] : [l] }
  lines = lines.map { |l| pick.(SEPS) + l + pick.(SEPS) } if rng.rand < 0.3
  lines.map { |l| l + pick.(LINE_ENDS) }.join
end

fixed_urls = ["https://x.com/foo/status/123", "https://youtu.be/#{ID}", "x.com/a/status/9?s=1", "", " ", "\0", "https://", "https://www.youtube.com/watch?v=#{ID}"]
cases = fixed_urls.map { |u| u }.concat(Array.new(opts[:urls]) { def_url.() }).map do |input|
  input = input.dup.force_encoding(Encoding::UTF_8)
  entry = { kind: "url", input: input }
  begin
    entry[:twitter] = begin
      TwitterUrl.normalize(input)
    rescue TwitterUrl::InvalidError
      nil
    end
    entry[:youtube] = YoutubeUrl.video_id(input)
  rescue URI::Error => error
    # e.g. "mailto://x.com/..." without a query: URI::InvalidComponentError is not a
    # URI::InvalidURIError, so neither service rescues it and the request would 500.
    entry = { kind: "url", input: input, ruby_raises: error.class.name }
  end
  entry
end
vtts = ["", "full transcript", "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhi\n"].concat(Array.new(opts[:vtts]) { def_vtt.() })
cases.concat(vtts.map do |input|
  input = input.dup.force_encoding(Encoding::UTF_8)
  begin
    { kind: "vtt", input: input, output: VttText.call(input) }
  rescue ArgumentError => error
    # A numeric reference to a surrogate ("&#xD800;") decodes to invalid UTF-8, and squeeze raises.
    { kind: "vtt", input: input, error: error.message }
  end
end)

# Tab, CR and LF are deleted from a URI's query by uri 1.1.1, so they must not hide or
# break a video id. Appended last so the generated entries above never move.
NEWLINE_URLS = [
  "https://www.youtube.com/watch?v=KpILHX2D-A\nA", "https://www.youtube.com/watch?v=KpILHX2D-A\rA",
  "https://www.youtube.com/watch?v=KpILHX2D-A\tA", "https://www.youtube.com/watch?v=KpILHX2D-A\r\nA",
  "https://www.youtube.com/watch?v=\nKpILHX2D-AA", "https://www.youtube.com/watch?v=KpILHX2D-AA\n",
  "https://www.youtube.com/watch?v=KpILHX2D-AA\n&t=5", "https://www.youtube.com/watch?\nv=KpILHX2D-AA",
  "https://www.youtube.com/watch?v\n=KpILHX2D-AA", "https://www.youtube.com/watch?v=KpILHX2D-AA&\nt=1",
  "https://www.youtube.com/watch?v=KpILHX2D%4\n1A", "https://www.youtube.com/watch?v=KpILHX2D-A%4\n1",
  "https://www.youtube.com/watch?v=KpILHX2D-A%\n41", "https://www.youtube.com/watch?v=KpILHX2D-A%\n\nzz",
  "https://www.youtube.com/watch?v=KpILHX2D-A%\nzz", "https://www.youtube.com/watch?v=%\n\t%",
  "https://youtu.be/KpILHX2D-AA?si=a\nb", "https://youtu.be/KpILHX2D-A\nA", "https://youtu.be/KpILHX2D-AA?\n",
  "https://www.youtube.com/attribution_link?a=xyz&u=%2Fwatch%3Fv%3DKpILHX2D-A%0AA%26feature%3Dshare",
  "https://www.youtube.com/attribution_link?u=%2Fwatch%3Fv%3DKpILHX2D-AA\n",
  "https://www.youtube.com/attribution_link?u=%2Fwatch%3Fv%3DKpILHX2D-\nAA",
  "https://www.youtube.com/attribution_link?u=%2Fwatch%3Fv%3DKpILHX2D-A%0D%0AA",
  "https://www.youtube.com/attribution_link?u=/shorts/KpILHX2D\n-AA",
  "https://www.youtube.com/attribution_link?u\n=%2Fwatch%3Fv%3DKpILHX2D-AA",
  "https://x.com/foo/status/123?s=2\n0&t=abc", "https://x.com/foo/status/123?s=%4\n1", "https://x.com/foo/status/123?s=%4\nz",
  "https://x.com/foo/status/123?\r\n", "https://x.com/foo/status/123?a=%\n\n%", "x.com/foo/status/123?\t",
  "mailto://x.com/foo/status/123?\n", "ftp://x.com//foo/status/123?a\nb", "https://www.youtube.com/watch\n?v=KpILHX2D-AA",
  "https://www.youtube.com/watch?v=KpILHX2D-AA#\n", "https://www.youtube.com/watch?v=KpILHX2D-AA\n#frag"
].freeze
NEWLINE_URLS.each do |input|
  entry = { kind: "url", input: input }
  begin
    entry[:twitter] = begin
      TwitterUrl.normalize(input)
    rescue TwitterUrl::InvalidError
      nil
    end
    entry[:youtube] = YoutubeUrl.video_id(input)
  rescue URI::Error => error
    entry = { kind: "url", input: input, ruby_raises: error.class.name }
  end
  cases << entry
end

FileUtils.mkdir_p(File.dirname(opts[:out]))
File.write(opts[:out], JSON.generate(cases) + "\n")
puts "wrote #{cases.size} cases to #{opts[:out]}"
