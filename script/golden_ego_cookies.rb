# Captures golden input/output pairs from the real Ruby EgoCookies (its OpenSSL
# decryption and Netscape writer), so the Go port can be verified byte-for-byte.
#
#   bin/rails runner script/golden_ego_cookies.rb
#
# Writes testdata/golden/video/ego_cookies.json: a list of
# {name, password, version, rows, output} where rows are the lines (already split on
# tabs) the sqlite3 query would print and output is the cookies file Ruby wrote
# (or error: the EgoCookies::Error message).
require "json"
require "tmpdir"

rng = Random.new(20261117)

def encrypt(value, password, strip_prefix: true, raw: false)
  cipher = OpenSSL::Cipher.new("aes-128-cbc").encrypt
  cipher.key = OpenSSL::PKCS5.pbkdf2_hmac_sha1(password, "saltysalt", 1003, 16)
  cipher.iv = " " * 16
  plain = (strip_prefix ? "h" * 32 : "") + value
  "v10" + cipher.update(plain) + cipher.final
end

def hex(blob) = blob.unpack1("H*").upcase

def run(name, password:, version:, rows:)
  Dir.mktmpdir do |dir|
    db = File.join(dir, "Cookies").tap { |p| File.write(p, "") }
    runner = lambda do |argv, _timeout|
      next [password + "\n", true] if argv.first.end_with?("security")
      next ["#{version}\n", true] if argv.last.include?("FROM meta")
      [rows.map { |r| r.join("\t") }.join("\n") + "\n", true]
    end
    out = File.join(dir, "cookies.txt")
    begin
      EgoCookies.new(db_path: db, runner: runner).export(out)
      { name: name, password: password, version: version, rows: rows, output: File.binread(out).unpack1("H*") }
    rescue EgoCookies::Error => error
      { name: name, password: password, version: version, rows: rows, error: error.message }
    end
  end
end

PW = "Zm9vYmFyYmF6cXV4"
values = ["abc", "", "f6=1", "ñandú=ü", "a" * 15, "a" * 16, "a" * 17, "x" * 100, "k=v;j=w", "😀 emoji", "=", " lead and trail ", "é" * 20]
hosts = %w[.youtube.com youtube.com .google.com google.com]

cases = []
rows = values.each_with_index.map do |v, i|
  [hosts[i % 4], "N#{i}", ["/", "/watch", "/a b"][i % 3], (i % 2).to_s, ["0", "13400000000000000", "13300000000000001", "11644473600000000", "11644473599999999", "1"][i % 6],
   ["", "https://youtube.com"][i % 2], hex(encrypt(v, PW))]
end
cases << run("version 24 strips the host hash", password: PW, version: 24, rows: rows)
cases << run("version 25 strips the host hash too", password: PW, version: 25, rows: rows)
cases << run("version 23 keeps it", password: PW, version: 23,
             rows: values.each_with_index.map { |v, i| ["youtube.com", "N#{i}", "/", "1", "13400000000000000", "", hex(encrypt(v, PW, strip_prefix: false))] })
cases << run("version 0 / blank", password: PW, version: "", rows: [["youtube.com", "A", "/", "1", "0", "", hex(encrypt("v", PW, strip_prefix: false))]])
cases << run("duplicates keep the first row", password: PW, version: 24, rows: [
  [".youtube.com", "SID", "/", "1", "0", "", hex(encrypt("first", PW))],
  [".youtube.com", "SID", "/", "1", "0", "https://youtube.com", hex(encrypt("second", PW))],
  [".youtube.com", "SID", "/x", "1", "0", "", hex(encrypt("other path", PW))],
  ["youtube.com", "SID", "/", "1", "0", "", hex(encrypt("other host", PW))],
  [".youtube.com", "sid", "/", "1", "0", "", hex(encrypt("other name", PW))]
])
cases << run("blobs that are not v10, empty or malformed are skipped", password: PW, version: 24, rows: [
  [".youtube.com", "V11", "/", "1", "0", "", hex("v11" + "x" * 16)],
  [".youtube.com", "EMPTY", "/", "1", "0", "", ""],
  [".youtube.com", "SHORT", "/", "1", "0", "", hex("v10")],
  [".youtube.com", "ODD", "/", "1", "0", "", hex("v10" + "x" * 17)],
  [".youtube.com", "OK", "/", "1", "0", "", hex(encrypt("fine", PW))]
])
cases << run("nothing decrypts", password: PW, version: 24, rows: [[".youtube.com", "V11", "/", "1", "0", "", hex("v11" + "x" * 16)]])
cases << run("no rows", password: PW, version: 24, rows: [])
wrong = 40.times.map do |i|
  [".youtube.com", "W#{i}", "/", "1", "0", "", hex(encrypt("v#{i}", "another password", strip_prefix: true))]
end
cases << run("wrong key: bad padding is skipped", password: PW, version: 24, rows: wrong + [[".youtube.com", "OK", "/", "1", "0", "", hex(encrypt("fine", PW))]])
cases << run("secure flag is only the string 1", password: PW, version: 24, rows: ["1", "0", "", "true", "01", "2"].each_with_index.map do |s, i|
  [".youtube.com", "S#{i}", "/", s, "0", "", hex(encrypt("v", PW))]
end)
cases << run("expiry forms", password: PW, version: 24, rows: ["0", "00", "", "abc", "13400000000000000abc", "-5", "999999", "1000000", "11644473600000000", "17000000000000000", "1_0000000000000000", " 13400000000000000"].each_with_index.map do |e, i|
  [".youtube.com", "E#{i}", "/", "1", e, "", hex(encrypt("v", PW))]
end)
cases << run("password with odd bytes", password: "pässwörd with spaces\t", version: 24, rows: [[".youtube.com", "A", "/", "1", "0", "", hex(encrypt("x", "pässwörd with spaces\t"))]])

# random bulk
bulk = Array.new(300) do |i|
  v = Array.new(rng.rand(0..60)) { ((rng.rand(32..126)).chr) }.join
  v = v.tr("\t\n\r\0", "    ")
  [hosts[rng.rand(4)], "R#{rng.rand(40)}", ["/", "/a", "/b"][rng.rand(3)], rng.rand(2).to_s, rng.rand(5).zero? ? "0" : (13_000_000_000_000_000 + rng.rand(10**15)).to_s,
   ["", "https://youtube.com"][rng.rand(2)], hex(encrypt(v, PW))]
end
cases << run("random bulk with duplicates", password: PW, version: 24, rows: bulk)

FileUtils.mkdir_p(Rails.root.join("testdata/golden/video"))
File.write(Rails.root.join("testdata/golden/video/ego_cookies.json"), JSON.pretty_generate(cases) + "\n")
puts "wrote #{cases.size} cases"
