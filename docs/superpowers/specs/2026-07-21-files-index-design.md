# /index — Served Files Table

## Purpose

Add a `/index` page listing every served file with a link to view it and its `updated_at` timestamp, sourced from the `served_files` table (not the filesystem directly).

## Route

Add above the catch-all wildcard route in `config/routes.rb` (must precede `get "/:file_name"` or it will be swallowed by the wildcard match):

```ruby
get "/index", to: "files#index"
```

## Controller

`FilesController#index`:

```ruby
def index
  @served_files = ServedFile.order(updated_at: :desc)
end
```

No new authorization needed — `ApplicationController`'s `before_action :authenticate_user!` already covers this action (same as `show`/`last`).

## View

New `app/views/files/index.html.erb`, rendered under the default `application` layout (not `markdown`, since this is an app page rather than served file content). Simple HTML table:

- Column 1: file name, linked to `/<name>` (URL-encoded), reusing the same route `files#show` already serves.
- Column 2: `updated_at`, formatted human-readable (e.g. `l10n` default or `strftime`).

No pagination, filtering, or styling beyond a plain table — matches the minimal scope of the rest of the app's views.

## Testing

Add a controller test in `test/controllers/files_controller_test.rb`:
- `GET /index` as authenticated user returns 200 and lists file names/links from `ServedFile` records.
- Confirms table reflects DB state (e.g. a record with a distinct `updated_at`), not disk state, by seeding `ServedFile` directly without a matching file on disk (or vice versa) to prove no dependency on filesystem in this action.

## Out of scope

- Pagination/sorting controls
- Deleting/managing files from this page
- Any change to how `ServedFile` records get created/updated (already handled elsewhere)
