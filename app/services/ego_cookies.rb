require "fileutils"
require "open3"
require "openssl"
require "tmpdir"

# Exports the YouTube/Google cookies of the ego lite browser (a Chromium clone) into a
# Netscape cookies.txt for yt-dlp. yt-dlp can't read ego's cookies itself: on macOS it
# only knows the Keychain entries of the browsers it names ("Chrome Safe Storage", ...).
#
# Signed-in cookies keep YouTube from answering caption downloads with HTTP 429.
# Reading the key needs the "ego safe storage" Keychain item to allow /usr/bin/security
# ("Always Allow" on the first prompt).
class EgoCookies
  Error = Class.new(StandardError)

  DB_PATH = File.expand_path("~/Library/Application Support/Citro Labs/ego lite/Default/Cookies")
  KEYCHAIN_SERVICE = "ego safe storage".freeze
  HOSTS = %w[.youtube.com youtube.com .google.com google.com].freeze
  # Partitioned (CHIPS) copies are keyed by the embedding site; only youtube.com's own count.
  TOP_FRAME_SITES = ["", "https://youtube.com"].freeze
  CHROME_EPOCH_OFFSET = 11_644_473_600
  KEY_TIMEOUT = 10

  DEFAULT_RUNNER = lambda do |argv, timeout|
    Open3.popen3(*argv) do |stdin, stdout, _stderr, wait_thr|
      stdin.close
      unless wait_thr.join(timeout)
        Process.kill("TERM", wait_thr.pid)
        raise Error, "#{argv.first} timed out (Keychain prompt waiting for approval?)"
      end
      [stdout.read, wait_thr.value.success?]
    end
  end

  def initialize(db_path: DB_PATH, runner: DEFAULT_RUNNER)
    @db_path = db_path
    @runner = runner
  end

  # Writes the cookies file to dest_path; returns it, or raises Error.
  def export(dest_path)
    raise Error, "ego lite cookie database not found" unless File.exist?(@db_path)

    Dir.mktmpdir do |dir|
      # The browser keeps the database locked while it runs, so read a copy.
      db = File.join(dir, "Cookies")
      FileUtils.cp(@db_path, db)
      key = derive_key(keychain_password)
      strip_host_hash = query(db, "SELECT value FROM meta WHERE key = 'version'").strip.to_i >= 24
      lines = rows(db).filter_map { |row| netscape_line(row, key, strip_host_hash) }
      raise Error, "ego lite has no YouTube cookies" if lines.empty?

      File.write(dest_path, "# Netscape HTTP Cookie File\n" + lines.join("\n") + "\n")
    end
    dest_path
  end

  private
    def keychain_password
      stdout, ok = @runner.call(["/usr/bin/security", "find-generic-password", "-w", "-s", KEYCHAIN_SERVICE], KEY_TIMEOUT)
      raise Error, "could not read the #{KEYCHAIN_SERVICE.inspect} Keychain item" unless ok

      stdout.chomp
    end

    def derive_key(password) = OpenSSL::PKCS5.pbkdf2_hmac_sha1(password, "saltysalt", 1003, 16)

    def rows(db)
      hosts = HOSTS.map { |h| "'#{h}'" }.join(",")
      sites = TOP_FRAME_SITES.map { |s| "'#{s}'" }.join(",")
      sql = "SELECT host_key, name, path, is_secure, expires_utc, top_frame_site_key, hex(encrypted_value) " \
            "FROM cookies WHERE host_key IN (#{hosts}) AND top_frame_site_key IN (#{sites}) " \
            "ORDER BY top_frame_site_key"
      query(db, sql).each_line.map { |line| line.chomp.split("\t", -1) }
                    .uniq { |host, name, path| [host, name, path] }
    end

    def query(db, sql)
      stdout, ok = @runner.call(["/usr/bin/sqlite3", "-readonly", "-separator", "\t", db, sql], KEY_TIMEOUT)
      raise Error, "could not read the ego lite cookie database" unless ok

      stdout
    end

    def netscape_line((host, name, path, secure, expires_utc, _site, hex), key, strip_host_hash)
      value = decrypt([hex].pack("H*"), key, strip_host_hash)
      return nil if value.nil?

      expires = expires_utc.to_i.zero? ? 0 : (expires_utc.to_i / 1_000_000) - CHROME_EPOCH_OFFSET
      flag = ->(bool) { bool ? "TRUE" : "FALSE" }
      [host, flag.(host.start_with?(".")), path, flag.(secure == "1"), expires, name, value].join("\t")
    end

    def decrypt(blob, key, strip_host_hash)
      return nil unless blob.start_with?("v10")

      cipher = OpenSSL::Cipher.new("aes-128-cbc").decrypt
      cipher.key = key
      cipher.iv = " " * 16
      plain = cipher.update(blob.byteslice(3..)) + cipher.final
      plain = plain.byteslice(32..) if strip_host_hash
      plain.force_encoding("UTF-8")
    rescue OpenSSL::Cipher::CipherError
      nil
    end
end
