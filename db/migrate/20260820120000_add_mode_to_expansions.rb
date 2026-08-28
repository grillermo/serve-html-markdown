class AddModeToExpansions < ActiveRecord::Migration[8.1]
  def change
    add_column :expansions, :mode, :string, null: false, default: "create_new"
    add_column :expansions, :fallback_anchor, :string
  end
end
