#!/usr/bin/env bash
# HTTP smoke tests for musik-player (cookie + Bearer).
set -euo pipefail

BASE="${MUSIK_BASE:-http://127.0.0.1:8787}"
PASS="${MUSIK_PASSWORD:-}"
TOKEN="${MUSIK_API_TOKEN:-}"
COOKIE_JAR="$(mktemp)"
trap 'rm -f "$COOKIE_JAR"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok  $*"; }

code() {
  local method=$1 path=$2; shift 2
  curl -sS -o /tmp/musik_smoke_body -w "%{http_code}" -X "$method" "$@" "${BASE}${path}"
}

echo "smoke against $BASE"

# health is public
c=$(code GET /api/health)
[[ "$c" == "200" ]] || fail "health got $c"
ok health

c=$(code GET /api/openapi.json)
[[ "$c" == "200" ]] || fail "openapi got $c"
ok openapi.json

me=$(curl -sS "${BASE}/api/auth/me")
echo "auth/me: $me"

if [[ -n "$PASS" ]]; then
  c=$(curl -sS -o /tmp/musik_smoke_body -w "%{http_code}" -c "$COOKIE_JAR" -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"password\":\"${PASS}\"}" \
    "${BASE}/api/auth/login")
  [[ "$c" == "200" ]] || fail "login got $c $(cat /tmp/musik_smoke_body)"
  ok login cookie

  c=$(curl -sS -o /tmp/musik_smoke_body -w "%{http_code}" -b "$COOKIE_JAR" "${BASE}/api/library")
  [[ "$c" == "200" ]] || fail "library cookie got $c"
  ok library via cookie

  c=$(curl -sS -o /tmp/musik_smoke_body -w "%{http_code}" -b "$COOKIE_JAR" "${BASE}/api/artists")
  [[ "$c" == "200" ]] || fail "artists got $c"
  ok artists

  c=$(curl -sS -o /tmp/musik_smoke_body -w "%{http_code}" -b "$COOKIE_JAR" "${BASE}/api/albums")
  [[ "$c" == "200" ]] || fail "albums got $c"
  ok albums
elif curl -sS "${BASE}/api/auth/me" | grep -q '"auth_enabled":true'; then
  fail "auth enabled but MUSIK_PASSWORD not set for smoke"
else
  ok auth disabled — cookie checks skipped
fi

if [[ -n "$TOKEN" ]]; then
  c=$(curl -sS -o /tmp/musik_smoke_body -w "%{http_code}" \
    -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/status")
  [[ "$c" == "200" ]] || fail "status bearer got $c"
  ok status via bearer

  c=$(curl -sS -o /tmp/musik_smoke_body -w "%{http_code}" \
    -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/favorites/status?type=track&track_id=1")
  [[ "$c" == "200" || "$c" == "400" ]] || fail "favorites/status got $c"
  ok favorites/status

  c=$(curl -sS -o /tmp/musik_smoke_body -w "%{http_code}" \
    -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/recommend/seed?type=track&track_id=1")
  # 200 or 404 empty seed is fine
  [[ "$c" == "200" || "$c" == "404" || "$c" == "400" ]] || fail "recommend/seed got $c"
  ok recommend/seed
elif curl -sS "${BASE}/api/auth/me" | grep -q '"auth_enabled":true'; then
  echo "WARN: auth on but MUSIK_API_TOKEN unset — bearer checks skipped"
else
  ok auth disabled — bearer checks skipped
fi

# unauthenticated protected route should 401 when auth on
if curl -sS "${BASE}/api/auth/me" | grep -q '"auth_enabled":true'; then
  c=$(curl -sS -o /tmp/musik_smoke_body -w "%{http_code}" "${BASE}/api/library")
  [[ "$c" == "401" ]] || fail "expected 401 library without creds, got $c"
  ok unauth library → 401
fi

echo "ALL SMOKE PASSED"
