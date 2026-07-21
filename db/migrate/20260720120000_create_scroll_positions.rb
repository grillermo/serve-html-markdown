class CreateScrollPositions < ActiveRecord::Migration[8.1]
  def change
    create_table :scroll_positions do |t|
      t.references :user, null: false, foreign_key: true
      t.string :file_name, null: false
      t.string :anchor, null: false
      t.timestamps
    end

    add_index :scroll_positions, [:user_id, :file_name], unique: true
  end
end
