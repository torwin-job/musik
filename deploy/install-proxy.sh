#!/usr/bin/env bash
# Install Caddy reverse-proxy for MUSIK_DOMAIN → 127.0.0.1:8787
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./lib.sh
load_secrets

if [[ "${MUSIK_DOMAIN}" == "music.example.com" ]]; then
  echo "Set a real MUSIK_DOMAIN in deploy/secrets.env first" >&2
  exit 1
fi

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
sed "s/__MUSIK_DOMAIN__/${MUSIK_DOMAIN}/g" "$DEPLOY_DIR/Caddyfile.example" >"$TMP"

export SSHPASS="${MUSIK_SSH_PASS:-}"
RSH="$(rsync_ssh)"
rsync -az -e "$RSH" "$TMP" \
  "${MUSIK_SSH_USER}@${MUSIK_SSH_HOST}:/etc/caddy/Caddyfile"

remote_bash <<EOF
set -euo pipefail
if ! command -v caddy >/dev/null 2>&1; then
  echo "caddy not installed — run ./deploy/remote-bootstrap.sh" >&2
  exit 1
fi
caddy validate --config /etc/caddy/Caddyfile
systemctl enable --now caddy
systemctl reload caddy || systemctl restart caddy
systemctl --no-pager status caddy | head -20 || true
echo "Proxy: https://${MUSIK_DOMAIN} → 127.0.0.1:8787"
EOF
