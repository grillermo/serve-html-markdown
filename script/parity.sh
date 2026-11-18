#!/usr/bin/env bash
# Parity check of the Go server against the running Rails one (Phase 10 of
# docs/superpowers/plans/2026-11-17-go-migration.md).
#
#   Rails (production) on RAILS_URL, Go (shadow, --no-jobs, FILES_DIR on a copy)
#   on GO_URL. Both read the same database.
#
#   ADMIN_EMAIL and ADMIN_PASSWORD must be in the environment (inject them with
#   your secret tool; the script never reads a dotenv file). Every name in
#   served_files (psql, with DATABASE_NAME set) or, without psql, in FILES_DIR
#   is fetched from both servers and diffed, then /index, /last and the last
#   five video summaries.
#
#   script/parity.sh              # all of it
#   script/parity.sh a.md b.html  # only these names (plus /index, /last, videos)
#
# Exit status: 0 when everything matched, 1 on any difference, 2 on bad setup.
set -euo pipefail

cd "$(dirname "$0")/.."

RAILS_URL=${RAILS_URL:-http://localhost:8009}
GO_URL=${GO_URL:-http://localhost:8019}
FILES_DIR=${FILES_DIR:-/tmp/shm-shadow-files}
: "${ADMIN_EMAIL:?ADMIN_EMAIL is not set}"
: "${ADMIN_PASSWORD:?ADMIN_PASSWORD is not set}"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
rails_jar=$work/rails.jar
go_jar=$work/go.jar

# What legitimately differs between the two servers, removed from both sides:
# - the CSRF meta tags (Go has http.CrossOriginProtection instead of a token),
#   and the authenticity_token value of forms;
# - the digest of the assets;
# - the self-closing slash of <link> tags (Rails writes " />");
# - blank lines (the layouts differ in whitespace only).
normalize() {
  sed -E \
    -e 's#<meta name="csrf-(token|param)" content="[^"]*" ?/?>##g' \
    -e 's|(name="authenticity_token" value=")[^"]*|\1X|' \
    -e 's|(/assets/[A-Za-z0-9_./-]*)-[0-9a-f]{8,64}\.([a-z0-9]+)|\1-DIGEST.\2|g' \
    -e 's|(<link [^>]*[^ />]) ?/>|\1>|g' |
    grep -v '^[[:space:]]*$' || true
}

# Syntect (Rails) and chroma (Go) tokenise code differently, so highlighted
# code blocks differ in their <span> markup. strip_spans leaves the text.
strip_spans() { sed -E 's|</?span[^>]*>||g'; }

urlencode() { python3 -I -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"; }

psql_query() {
  psql -At -h "${DATABASE_HOST:-localhost}" -p "${DATABASE_PORT:-5432}" "$DATABASE_NAME" -c "$1"
}

have_psql() { command -v psql >/dev/null && [ -n "${DATABASE_NAME:-}" ]; }

sign_in_rails() {
  local token
  token=$(curl -fsS -c "$rails_jar" "$RAILS_URL/users/sign_in" |
    grep -o 'name="authenticity_token" value="[^"]*"' | head -1 | sed -E 's/.*value="([^"]*)"/\1/')
  [ -n "$token" ] || { echo "no authenticity token on $RAILS_URL/users/sign_in" >&2; exit 2; }
  curl -sS -o /dev/null -b "$rails_jar" -c "$rails_jar" \
    --data-urlencode "authenticity_token=$token" \
    --data-urlencode "user[email]=$ADMIN_EMAIL" \
    --data-urlencode "user[password]=$ADMIN_PASSWORD" \
    --data-urlencode "user[remember_me]=0" \
    "$RAILS_URL/users/sign_in"
}

sign_in_go() {
  curl -sS -o /dev/null -c "$go_jar" \
    --data-urlencode "user[email]=$ADMIN_EMAIL" \
    --data-urlencode "user[password]=$ADMIN_PASSWORD" \
    --data-urlencode "user[remember_me]=0" \
    "$GO_URL/users/sign_in"
}

# signed_in <jar> <url>: a signed-in client reaches /index instead of being
# sent to the sign-in page.
signed_in() {
  local status
  status=$(curl -sS -b "$1" -o /dev/null -w '%{http_code}' "$2/index")
  [ "$status" = 200 ]
}

# fetch <jar> <url> <out>: the normalized body, then the status and Location.
fetch() {
  curl -sS -b "$1" -o "$work/body" -D "$work/headers" "$2" || true
  {
    normalize <"$work/body"
    echo "--- status: $(head -1 "$work/headers" | tr -d '\r' | awk '{print $2}')"
    echo "--- location: $(grep -i '^location:' "$work/headers" | tr -d '\r' | sed -E 's/^[Ll]ocation: *//; s|^https?://[^/]+||' || true)"
  } >"$3"
}

failed=0
highlight_only=()
compare() { # <label> <path>
  fetch "$rails_jar" "$RAILS_URL$2" "$work/rails.out"
  fetch "$go_jar" "$GO_URL$2" "$work/go.out"
  if cmp -s "$work/rails.out" "$work/go.out"; then
    echo "OK $1"
    return
  fi
  strip_spans <"$work/rails.out" >"$work/rails.text"
  strip_spans <"$work/go.out" >"$work/go.text"
  if cmp -s "$work/rails.text" "$work/go.text"; then
    echo "OK~ $1 (only the highlighting markup differs)"
    highlight_only+=("$1")
    return
  fi
  failed=1
  echo "DIFF $1"
  if command -v difft >/dev/null; then
    difft --color never "$work/rails.text" "$work/go.text" || true
  else
    diff -u "$work/rails.text" "$work/go.text" || true
  fi
}

sign_in_rails
sign_in_go
signed_in "$rails_jar" "$RAILS_URL" || { echo "sign-in to $RAILS_URL failed" >&2; exit 2; }
signed_in "$go_jar" "$GO_URL" || { echo "sign-in to $GO_URL failed" >&2; exit 2; }

names=()
if [ "$#" -gt 0 ]; then
  names=("$@")
elif have_psql; then
  while IFS= read -r line; do names+=("$line"); done < <(psql_query 'SELECT name FROM served_files ORDER BY id')
else
  echo "psql or DATABASE_NAME missing: using the names in $FILES_DIR" >&2
  while IFS= read -r line; do names+=("$line"); done < <(cd "$FILES_DIR" && find . -maxdepth 1 -type f \( -name '*.md' -o -name '*.markdown' -o -name '*.html' -o -name '*.htm' \) -print | sed 's|^\./||' | sort)
fi

for name in ${names[@]+"${names[@]}"}; do
  compare "$name" "/$(urlencode "$name")"
done

compare /index /index
compare /last /last

ids=()
if have_psql; then
  while IFS= read -r line; do ids+=("$line"); done < <(psql_query 'SELECT id FROM twitter_videos ORDER BY id DESC LIMIT 5')
fi
for id in ${ids[@]+"${ids[@]}"}; do
  compare "/summarize-video/$id" "/summarize-video/$id"
done

if [ "$failed" -eq 0 ]; then
  echo "All ${#names[@]} files and the pages matched; ${#highlight_only[@]} differ only in highlighting markup."
else
  echo "Differences found." >&2
fi
exit "$failed"
