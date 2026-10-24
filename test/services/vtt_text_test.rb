require "test_helper"

class VttTextTest < ActiveSupport::TestCase
  test "drops headers, timings and word tags, and collapses repeated rolling lines" do
    vtt = <<~VTT
      WEBVTT
      Kind: captions
      Language: en

      00:00:00.240 --> 00:00:02.150 align:start position:0%

      How<00:00:00.440><c> the</c><00:00:01.120><c> do</c>

      00:00:02.150 --> 00:00:02.160 align:start position:0%
      How the do

      00:00:02.160 --> 00:00:03.630 align:start position:0%
      How the do
      Q&amp;A<00:00:02.400><c> time</c>
    VTT

    assert_equal "How the do\nQ&A time", VttText.call(vtt)
  end

  test "passes plain text through" do
    assert_equal "full transcript", VttText.call("full transcript")
  end
end
