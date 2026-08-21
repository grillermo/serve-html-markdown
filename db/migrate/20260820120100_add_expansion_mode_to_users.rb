class AddExpansionModeToUsers < ActiveRecord::Migration[8.1]
  def change
    add_column :users, :expansion_mode, :string, null: false, default: "create_new"
  end
end
