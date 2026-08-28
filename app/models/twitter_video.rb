class TwitterVideo < ApplicationRecord
  STATUSES = %w[downloading uploading awaiting_captions summarizing publishing done failed].freeze
  # Minutes elapsed from upload_completed_at. The last two are 1 and 2 days out, for
  # videos whose auto-captions only show up long after upload.
  CHECKPOINTS = [5, 10, 20, 30, 40, 60, 120, 1440, 2880].freeze

  validates :source_url, presence: true
  validates :status, inclusion: { in: STATUSES }

  # Rows for the same tweet, ignoring any tracking query string.
  scope :for_source_url, ->(url) {
    canonical = url.to_s.split("?").first
    where("source_url = ? OR source_url LIKE ?", canonical, "#{canonical}?%")
  }

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
