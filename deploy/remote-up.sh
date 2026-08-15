#!/usr/bin/env bash
# On remote: compose up, wait health, trigger rescan + mixes.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./lib.sh
load_secrets

INSTALL_PROXY="${INSTALL_PROXY:-1}"

remote_bash <<EOF
set -euo pipefail
cd ${MUSIK_REMOTE_ROOT}
test -f .env || { echo "missing .env — run ./deploy/sync.sh first"; exit 1; }
test -f docker-compose.yml || { echo "missing compose — sync code first"; exit 1; }

export MUSIK_LIBRARY=${MUSIK_REMOTE_LIBRARY}

echo "==> docker compose up"
if docker compose version >/dev/null 2>&1; then
  docker compose up -d --build
else
  docker-compose up -d --build
fi

echo "==> wait for player :8787"
for i in \$(seq 1 60); do
  if curl -fsS http://127.0.0.1:8787/api/health >/dev/null 2>&1; then
    echo "player up"
    break
  fi
  sleep 2
  if [[ \$i -eq 60 ]]; then
    echo "player did not become healthy" >&2
    docker compose logs --tail=80 player || true
    exit 1
  fi
done

set -a; source .env; set +a
TOKEN="\${MUSIK_API_TOKEN:?}"

echo "==> enqueue library rescan"
curl -sS -X POST -H "Authorization: Bearer \$TOKEN" \\
  -H "Content-Type: application/json" -d '{}' \\
  http://127.0.0.1:8787/api/library/rescan || true

echo "==> enqueue mix_pack (ok if embed not done yet)"
curl -sS -X POST -H "Authorization: Bearer \$TOKEN" \\
  -H "Content-Type: application/json" -d '{}' \\
  http://127.0.0.1:8787/api/jobs/mix_pack || true

curl -sS -H "Authorization: Bearer \$TOKEN" http://127.0.0.1:8787/api/health || true
echo
echo "Local LAN smoke: http://${MUSIK_SSH_HOST}:8787"
echo "Public (after DNS+proxy): $(public_base_url)"
EOF

if [[ "$INSTALL_PROXY" == "1" && "${MUSIK_DOMAIN}" != "music.example.com" ]]; then
  echo "==> install Caddyfile for ${MUSIK_DOMAIN}"
  ./install-proxy.sh || echo "proxy install skipped/failed — player still on :8787"
else
  echo "Proxy skipped (set real MUSIK_DOMAIN in secrets.env, or INSTALL_PROXY=1 after DNS)."
fi

echo "OK — stack starting. Embed can take hours on CPU — watch: ./deploy/ssh.sh 'cd /opt/musik && docker compose logs -f worker'"
