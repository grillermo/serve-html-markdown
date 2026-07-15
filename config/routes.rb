Rails.application.routes.draw do
  devise_for :users
  match "/health", to: proc { [200, {}, [""]] }, via: :head
  post "/file/new", to: "files#create"
  root "files#last"
  get "/last", to: "files#last"
  get "/:file_name", to: "files#show", constraints: { file_name: /[^\/]+/ }
end
