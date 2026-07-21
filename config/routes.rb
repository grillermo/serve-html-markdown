Rails.application.routes.draw do
  devise_for :users
  match "/health", to: proc { [200, {}, [""]] }, via: :head
  post "/file/new", to: "files#create"
  post "/expansions", to: "expansions#create"
  get "/expansions/:id", to: "expansions#show", constraints: { id: /\d+/ }
  match "/scroll_position", to: "scroll_positions#update", via: [:patch, :post]
  get "/favicon.ico", to: proc { [204, {}, []] }
  root "files#last"
  get "/last", to: "files#last"
  get "/:file_name", to: "files#show", constraints: { file_name: /[^\/]+/ }, defaults: { format: :html }
end
