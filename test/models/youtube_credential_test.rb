require "test_helper"

class YoutubeCredentialTest < ActiveSupport::TestCase
  test "prefers the stored row over the env var" do
    YoutubeCredential.create!(refresh_token: "from-db", obtained_at: Time.current)

    with_env("YOUTUBE_REFRESH_TOKEN" => "from-env") do
      assert_equal "from-db", YoutubeCredential.refresh_token
    end
  end

  test "falls back to the env var when nothing is stored" do
    with_env("YOUTUBE_REFRESH_TOKEN" => "from-env") do
      assert_equal "from-env", YoutubeCredential.refresh_token
    end
  end

  test "returns nil when neither a row nor the env var exists" do
    with_env("YOUTUBE_REFRESH_TOKEN" => nil) do
      assert_nil YoutubeCredential.refresh_token
    end
  end

  test "store! keeps a single row and overwrites the token" do
    YoutubeCredential.store!("first")
    YoutubeCredential.store!("second")

    assert_equal 1, YoutubeCredential.count
    assert_equal "second", YoutubeCredential.current.refresh_token
    assert_not_nil YoutubeCredential.current.obtained_at
  end

  private
    # dotenv-rails loads the real .env in the test env, so restore whatever was there.
    def with_env(values)
      previous = values.keys.index_with { |key| ENV[key] }
      values.each { |key, value| value.nil? ? ENV.delete(key) : ENV[key] = value }
      yield
    ensure
      previous.each { |key, value| value.nil? ? ENV.delete(key) : ENV[key] = value }
    end
end
