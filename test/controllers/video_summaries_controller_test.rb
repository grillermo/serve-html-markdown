# test/controllers/video_summaries_controller_test.rb
require "test_helper"

class VideoSummariesControllerTest < ActionDispatch::IntegrationTest
  setup { ENV["API_TOKEN"] = "secret-token" }

  test "rejects missing bearer token" do
    post "/summarize-video", params: { url: "https://x.com/a/status/1" }, as: :json
    assert_response :unauthorized
  end

  test "creates a row and enqueues ingest job" do
    assert_enqueued_with(job: TwitterVideoIngestJob) do
      post "/summarize-video",
        params: { url: "https://twitter.com/a/status/1" }, as: :json,
        headers: { "Authorization" => "Bearer secret-token" }
    end
    assert_response :accepted
    body = JSON.parse(response.body)
    video = TwitterVideo.find(body["id"])
    assert_equal "https://x.com/a/status/1", video.source_url
    assert_equal "downloading", body["status"]
  end

  test "reuses an existing row for the same tweet and re-enqueues ingest" do
    existing = TwitterVideo.create!(source_url: "https://x.com/a/status/7?s=20", status: "failed")

    assert_no_difference -> { TwitterVideo.count } do
      assert_enqueued_with(job: TwitterVideoIngestJob, args: [existing.id]) do
        post "/summarize-video",
          params: { url: "https://twitter.com/a/status/7" }, as: :json,
          headers: { "Authorization" => "Bearer secret-token" }
      end
    end

    assert_response :accepted
    assert_equal existing.id, JSON.parse(response.body)["id"]
  end

  test "sends a youtube link straight to the captions job" do
    assert_enqueued_with(job: TwitterVideoCaptionsJob) do
      post "/summarize-video",
        params: { url: "https://youtu.be/KpILHX2D-AA?si=3zdm0UjTBIYF-k0c" }, as: :json,
        headers: { "Authorization" => "Bearer secret-token" }
    end
    assert_response :accepted
    video = TwitterVideo.find(JSON.parse(response.body)["id"])
    assert_equal "https://www.youtube.com/watch?v=KpILHX2D-AA", video.source_url
    assert_equal "KpILHX2D-AA", video.youtube_id
    assert_equal "awaiting_captions", video.status
    assert_not_nil video.upload_completed_at
  end

  test "retries a failed youtube row instead of adding another" do
    existing = TwitterVideo.create!(source_url: "https://www.youtube.com/watch?v=KpILHX2D-AA",
                                    status: "failed", caption_attempts: 9, error_detail: "boom")

    assert_no_difference -> { TwitterVideo.count } do
      post "/summarize-video",
        params: { url: "https://www.youtube.com/watch?v=KpILHX2D-AA" }, as: :json,
        headers: { "Authorization" => "Bearer secret-token" }
    end
    existing.reload
    assert_equal ["awaiting_captions", 0, nil], [existing.status, existing.caption_attempts, existing.error_detail]
  end

  test "rejects an invalid url" do
    post "/summarize-video",
      params: { url: "https://youtube.com/@channel" }, as: :json,
      headers: { "Authorization" => "Bearer secret-token" }
    assert_response :bad_request
  end

  test "returns status for an existing row" do
    v = TwitterVideo.create!(source_url: "u", status: "awaiting_captions", youtube_id: "YT1")
    get "/summarize-video/#{v.id}"
    assert_response :success
    assert_equal "awaiting_captions", JSON.parse(response.body)["status"]
  end
  test "accepts the plain watch link that was reported as unsupported" do
    assert_enqueued_with(job: TwitterVideoCaptionsJob) do
      post "/summarize-video",
        params: { url: "https://www.youtube.com/watch?v=hWe-j78vNpc" }, as: :json,
        headers: { "Authorization" => "Bearer secret-token" }
    end
    assert_response :accepted
    assert_equal "hWe-j78vNpc", TwitterVideo.find(JSON.parse(response.body)["id"]).youtube_id
  end

  test "pulls the link out of shared text" do
    post "/summarize-video",
      params: { url: "  Look at this https://x.com/someone/status/42?s=46 wow\n" }, as: :json,
      headers: { "Authorization" => "Bearer secret-token" }
    assert_response :accepted
    assert_equal "https://x.com/someone/status/42",
      TwitterVideo.find(JSON.parse(response.body)["id"]).source_url
  end

  test "says the url is missing when it is blank" do
    post "/summarize-video", params: { url: "" }, as: :json,
      headers: { "Authorization" => "Bearer secret-token" }
    assert_response :bad_request
    assert_equal "Missing url", JSON.parse(response.body)["detail"]
  end

  test "keeps the old twitter-video path working" do
    post "/twitter-video",
      params: { url: "https://youtu.be/hWe-j78vNpc" }, as: :json,
      headers: { "Authorization" => "Bearer secret-token" }
    assert_response :accepted
    get "/twitter-video/#{JSON.parse(response.body)["id"]}"
    assert_response :success
  end
end
