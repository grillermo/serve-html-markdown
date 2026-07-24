# test/controllers/twitter_videos_controller_test.rb
require "test_helper"

class TwitterVideosControllerTest < ActionDispatch::IntegrationTest
  setup { ENV["API_TOKEN"] = "secret-token" }

  test "rejects missing bearer token" do
    post "/twitter-video", params: { url: "https://x.com/a/status/1" }, as: :json
    assert_response :unauthorized
  end

  test "creates a row and enqueues ingest job" do
    assert_enqueued_with(job: TwitterVideoIngestJob) do
      post "/twitter-video",
        params: { url: "https://twitter.com/a/status/1" }, as: :json,
        headers: { "Authorization" => "Bearer secret-token" }
    end
    assert_response :accepted
    body = JSON.parse(response.body)
    video = TwitterVideo.find(body["id"])
    assert_equal "https://x.com/a/status/1", video.source_url
    assert_equal "downloading", body["status"]
  end

  test "rejects an invalid url" do
    post "/twitter-video",
      params: { url: "https://youtube.com/watch?v=x" }, as: :json,
      headers: { "Authorization" => "Bearer secret-token" }
    assert_response :bad_request
  end

  test "returns status for an existing row" do
    v = TwitterVideo.create!(source_url: "u", status: "awaiting_captions", youtube_id: "YT1")
    get "/twitter-video/#{v.id}"
    assert_response :success
    assert_equal "awaiting_captions", JSON.parse(response.body)["status"]
  end
end
