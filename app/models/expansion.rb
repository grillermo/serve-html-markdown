class Expansion < ApplicationRecord
  STATUSES = %w[pending processing completed failed].freeze

  serialize :timings, coder: JSON, default: {}

  belongs_to :user

  validates :file_name, :selected_text, :question, presence: true
  validates :occurrence, numericality: { only_integer: true, greater_than_or_equal_to: 0 }
  validates :status, inclusion: { in: STATUSES }

  def self.now_ms
    (Time.now.to_f * 1000).round
  end

  def claim!
    self.class.where(id: id, status: "pending")
      .update_all(status: "processing", updated_at: Time.current) == 1
  end

  def complete!(generated_url)
    update!(status: "completed", url: generated_url, error_detail: nil)
    stamp!(:completed)
  end

  def fail!(detail)
    update!(status: "failed", url: nil, error_detail: detail)
  end

  def stamp!(stage, epoch_ms = self.class.now_ms)
    with_lock do
      self.timings = timings.merge(stage.to_s => epoch_ms)
      save!(validate: false)
    end
  rescue StandardError => error
    Rails.logger.error("[Expansion] stamp! failed for stage=#{stage}: #{error.class}")
  end
end
