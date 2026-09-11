class CreateYoutubeCredentials < ActiveRecord::Migration[8.1]
  def change
    create_table :youtube_credentials do |t|
      t.text :refresh_token, null: false
      t.datetime :obtained_at, null: false
      t.timestamps
    end
  end
end
