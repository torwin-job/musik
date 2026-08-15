#!/usr/bin/env bash
# Micro-bench: queue BuildOpts on current DB (small library OK).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export MUSIK_ROOT="$ROOT"
export MUSIK_DB_PATH="${MUSIK_DB_PATH:-$ROOT/data/db/musik.db}"
export MUSIK_PASSWORD="${MUSIK_PASSWORD:-bench}"
export MUSIK_API_TOKEN="${MUSIK_API_TOKEN:-bench-token}"
export MUSIK_AUTH_DISABLED=0
export MUSIK_WORKER_AUTOSTART=0
# Force shortlist path even on small libs to validate pool code:
export MUSIK_CANDIDATE_POOL_AT="${MUSIK_CANDIDATE_POOL_AT:-50}"

cd "$ROOT"
go -C player test ./internal/queue/ ./internal/index/ -count=1
go -C player build -o /tmp/musik-bench-player ./cmd/musik-player

# Time radio start × 20 via API if player already up; else print hint
if curl -sf -H "Authorization: Bearer $MUSIK_API_TOKEN" http://127.0.0.1:8787/api/health >/dev/null 2>&1; then
  echo "bench radio/start × 20 (pool_at=$MUSIK_CANDIDATE_POOL_AT)"
  START=$(date +%s%3N)
  for i in $(seq 1 20); do
    curl -sf -X POST -H "Authorization: Bearer $MUSIK_API_TOKEN" \
      -H 'Content-Type: application/json' -d '{}' \
      http://127.0.0.1:8787/api/radio/start >/dev/null
  done
  END=$(date +%s%3N)
  echo "ok 20 radio/start in $((END-START)) ms (avg $(( (END-START)/20 )) ms)"
else
  echo "player not up — unit tests only. Start player then re-run for radio latency."
fi
