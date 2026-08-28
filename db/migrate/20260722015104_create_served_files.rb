class CreateServedFiles < ActiveRecord::Migration[8.1]
  def change
    create_table :served_files do |t|
      t.string :name, null: false
      t.timestamps
    end
    add_index :served_files, :name, unique: true
  end
end
