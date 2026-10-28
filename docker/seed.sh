#!/bin/sh
# Seeds the docker-compose.yaml showcase once: a "demo" site admin with a
# random password (kept on the data volume and printed on every run), and a
# public repository with an issue and a wiki page. Runs in the oGit image,
# next to the server, with the same configuration.
set -eu

URL=http://ogit:8080/api/v1
EMAIL=demo@example.com
PASSWORD_FILE=/data/demo-password

say() { echo "seed: $*"; }

# post PATH JSON [TOKEN]: prints the answer; fails on HTTP errors.
post() {
  if [ -n "${3:-}" ]; then
    wget -q -O- --header 'Content-Type: application/json' --header "Authorization: Bearer $3" --post-data "$2" "$URL$1"
  else
    wget -q -O- --header 'Content-Type: application/json' --post-data "$2" "$URL$1"
  fi
}

say "waiting for oGit"
until wget -q -O /dev/null http://ogit:8080/api/health; do sleep 2; done

if [ ! -s "$PASSWORD_FILE" ]; then
  # The password policy wants upper and lower case, a digit and a symbol.
  echo "Demo-$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 16)-7x" > "$PASSWORD_FILE"
  chmod 600 "$PASSWORD_FILE"
fi
PASSWORD=$(cat "$PASSWORD_FILE")
login() { post /auth/login "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p'; }

TOKEN=$(login 2>/dev/null || true)
if [ -z "$TOKEN" ]; then
  say "creating the demo user"
  post /auth/register "{\"email\":\"$EMAIL\",\"username\":\"demo\",\"password\":\"$PASSWORD\"}" >/dev/null
  /app admin promote -c /config/config.yaml --username demo
  TOKEN=$(login)

  say "creating demo/showcase"
  post /repository '{"name":"Showcase","description":"A repository to look around: code, an issue and a wiki.","is_private":false}' "$TOKEN" >/dev/null
  post /repository/demo%2Fshowcase/issues '{"title":"Try opening a pull request","body":"Clone the repository, push a branch, and open a pull request against `main`.\n\nIssues and pull requests share their numbers: this is #1."}' "$TOKEN" >/dev/null
  post /repository/demo%2Fshowcase/wiki/pages '{"name":"Home","content":"# Showcase wiki\n\nThis wiki lives in its own git repository, `demo/showcase.wiki.git`.\n\n- [[Getting Started]] (a page that does not exist yet: click it to create it)\n- Edit this page, then look at its history."}' "$TOKEN" >/dev/null
fi

cat <<EOF

  oGit is ready: http://localhost:8080
  Sign in as  demo@example.com
  Password    $PASSWORD
  (kept in the ogit-data volume; "docker compose down -v" forgets it)

  demo is a site admin; the admin page asks for two-factor authentication first.
  Clone:  git clone http://localhost:8080/demo/showcase.git
          git clone ssh://git@localhost:2222/demo/showcase.git   (add an SSH key in your settings)
  Storage: MinIO console at http://localhost:9001 (ogit-showcase / ogit-showcase-secret)

EOF
