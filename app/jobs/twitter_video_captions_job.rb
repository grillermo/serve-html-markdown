# Placeholder — full implementation lands in Task 10 (poll -> summarize -> save -> push).
class TwitterVideoCaptionsJob < ApplicationJob
  queue_as :default

  def perform(twitter_video_id)
  end
end
