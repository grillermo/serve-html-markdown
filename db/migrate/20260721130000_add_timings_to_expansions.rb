class AddTimingsToExpansions < ActiveRecord::Migration[8.1]
  def change
    add_column :expansions, :timings, :text, null: false, default: "{}"
    add_column :expansions, :provider_used, :string
    add_column :expansions, :html_bytes, :integer
  end
end
