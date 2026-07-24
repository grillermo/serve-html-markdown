class TwitterVideo < ApplicationRecord
  STATUSES = %w[downloading uploading awaiting_captions summarizing publishing done failed].freeze
  CHECKPOINTS = [5, 10, 20, 30, 40, 60, 120].freeze # minutes, elapsed from upload_completed_at

  validates :source_url, presence: true
  validates :status, inclusion: { in: STATUSES }

  def fail!(detail)
    update!(status: "failed", error_detail: detail)
  end

  # Seconds to wait before the next caption checkpoint, or nil when exhausted.
  def next_caption_wait(now: Time.current)
    minutes = CHECKPOINTS[caption_attempts]
    return nil if minutes.nil?

    target = upload_completed_at + minutes.minutes
    [(target - now).to_i, 0].max
  end
end
