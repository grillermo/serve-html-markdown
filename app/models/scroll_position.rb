class ScrollPosition < ApplicationRecord
  belongs_to :user

  validates :file_name, presence: true
  validates :anchor, presence: true, format: { with: /\A[\w\-:.]+\z/ }
  validates :file_name, uniqueness: { scope: :user_id }
end
