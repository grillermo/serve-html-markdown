class CreateTwitterVideos < ActiveRecord::Migration[8.1]
  def change
    create_table :twitter_videos do |t|
      t.string :source_url, null: false
      t.string :status, null: false, default: "downloading"
      t.string :youtube_id
      t.string :youtube_title
      t.integer :caption_attempts, null: false, default: 0
      t.datetime :upload_completed_at
      t.string :html_filename
      t.string :rulinky_link_id
      t.text :error_detail
      t.timestamps
    end
  end
end
