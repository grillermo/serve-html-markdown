class User < ApplicationRecord
  devise :database_authenticatable, :rememberable, :validatable

  has_many :scroll_positions, dependent: :destroy
end
