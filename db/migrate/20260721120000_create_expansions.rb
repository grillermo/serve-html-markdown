class CreateExpansions < ActiveRecord::Migration[8.1]
  def change
    create_table :expansions do |t|
      t.references :user, null: false, foreign_key: true
      t.string :file_name, null: false
      t.text :selected_text, null: false
      t.integer :occurrence, null: false, default: 0
      t.text :question, null: false
      t.boolean :use_openai, null: false, default: false
      t.string :status, null: false, default: "pending"
      t.string :url
      t.string :error_detail
      t.timestamps
    end

    add_index :expansions, [:user_id, :status]
  end
end
