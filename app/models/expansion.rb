class Expansion < ApplicationRecord
  STATUSES = %w[pending processing completed failed].freeze

  belongs_to :user

  validates :file_name, :selected_text, :question, presence: true
  validates :occurrence, numericality: { only_integer: true, greater_than_or_equal_to: 0 }
  validates :status, inclusion: { in: STATUSES }

  def claim!
    self.class.where(id: id, status: "pending")
      .update_all(status: "processing", updated_at: Time.current) == 1
  end

  def complete!(generated_url)
    update!(status: "completed", url: generated_url, error_detail: nil)
  end

  def fail!(detail)
    update!(status: "failed", url: nil, error_detail: detail)
  end
end
