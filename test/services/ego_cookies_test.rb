require "test_helper"
require "tmpdir"

class EgoCookiesTest < ActiveSupport::TestCase
  PASSWORD = "pw".freeze

  test "decrypts cookies into a netscape file, dropping the host hash" do
    rows = [[".youtube.com", "SID", "/", "1", "13400000000000000", "", hex(encrypt("abc"))],
            ["youtube.com", "PREF", "/", "0", "0", "", hex(encrypt("f6=1"))]]
    Dir.mktmpdir do |dir|
      db = File.join(dir, "Cookies").tap { |p| File.write(p, "") }
      out = EgoCookies.new(db_path: db, runner: runner(rows)).export(File.join(dir, "cookies.txt"))

      lines = File.readlines(out, chomp: true)
      assert_equal "# Netscape HTTP Cookie File", lines.first
      assert_equal [".youtube.com", "TRUE", "/", "TRUE", (13400000000000000 / 1_000_000 - 11_644_473_600).to_s, "SID", "abc"],
        lines[1].split("\t")
      assert_equal ["youtube.com", "FALSE", "/", "FALSE", "0", "PREF", "f6=1"], lines[2].split("\t")
    end
  end

  test "raises when the keychain item can't be read" do
    Dir.mktmpdir do |dir|
      db = File.join(dir, "Cookies").tap { |p| File.write(p, "") }
      failing = ->(argv, _timeout) { argv.first.end_with?("security") ? ["", false] : ["24\n", true] }
      assert_raises(EgoCookies::Error) do
        EgoCookies.new(db_path: db, runner: failing).export(File.join(dir, "cookies.txt"))
      end
    end
  end

  test "raises when the database is missing" do
    assert_raises(EgoCookies::Error) { EgoCookies.new(db_path: "/nope/Cookies").export("/tmp/x") }
  end

  private
    def runner(rows)
      lambda do |argv, _timeout|
        next ["#{PASSWORD}\n", true] if argv.first.end_with?("security")
        next ["24\n", true] if argv.last.include?("FROM meta")
        [rows.map { |r| r.join("\t") }.join("\n") + "\n", true]
      end
    end

    def encrypt(value)
      cipher = OpenSSL::Cipher.new("aes-128-cbc").encrypt
      cipher.key = OpenSSL::PKCS5.pbkdf2_hmac_sha1(PASSWORD, "saltysalt", 1003, 16)
      cipher.iv = " " * 16
      "v10" + cipher.update(("h" * 32) + value) + cipher.final
    end

    def hex(blob) = blob.unpack1("H*")
end
