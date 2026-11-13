Rails.application.routes.draw do
  devise_for :users
  match "/health", to: proc { [200, {}, [""]] }, via: :head
  post "/file/new", to: "files#create"
  post "/file/upload", to: "files#upload"
  post "/summarize-video", to: "video_summaries#create"
  get "/summarize-video/:id", to: "video_summaries#show", constraints: { id: /\d+/ }
  # Old name, kept so existing iOS Shortcuts keep working.
  post "/twitter-video", to: "video_summaries#create"
  get "/twitter-video/:id", to: "video_summaries#show", constraints: { id: /\d+/ }
  get "/youtube/reauth", to: "youtube_authorizations#new"
  get "/youtube/callback", to: "youtube_authorizations#create"
  get "/youtube/debug", to: "youtube_authorizations#debug"
  post "/expansions", to: "expansions#create"
  get "/expansions/:id", to: "expansions#show", constraints: { id: /\d+/ }
  match "/scroll_position", to: "scroll_positions#update", via: [:patch, :post]
  get "/favicon.ico", to: proc { [204, {}, []] }
  root "files#last"
  get "/last", to: "files#last"
  get "/index", to: "files#index"
  get "/:file_name", to: "files#show", constraints: { file_name: /[^\/]+/ }, defaults: { format: :html }
end
