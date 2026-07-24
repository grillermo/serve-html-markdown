class AddVideoPathToTwitterVideos < ActiveRecord::Migration[8.1]
  def change
    add_column :twitter_videos, :video_path, :string
    add_index :twitter_videos, :source_url
  end
end
