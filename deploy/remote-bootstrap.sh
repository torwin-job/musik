#!/usr/bin/env bash
# Bootstrap LXC: dirs, docker, optional caddy. Safe to re-run.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./lib.sh
load_secrets

remote_bash <<EOF
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

mkdir -p ${MUSIK_REMOTE_ROOT} ${MUSIK_REMOTE_LIBRARY} /var/lib/musik-data
chmod 755 ${MUSIK_REMOTE_ROOT} ${MUSIK_REMOTE_LIBRARY}

if ! command -v docker >/dev/null 2>&1; then
  echo "==> installing docker…"
  if command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm docker docker-compose rsync ffmpeg
    systemctl enable --now docker || true
  elif command -v apt-get >/dev/null 2>&1; then
    apt-get update -y
    apt-get install -y ca-certificates curl gnupg rsync ffmpeg
    apt-get install -y docker.io docker-compose-v2 || apt-get install -y docker.io docker-compose || true
    systemctl enable --now docker || service docker start || true
  else
    echo "Unknown package manager — install docker manually" >&2
    exit 1
  fi
fi

docker version
docker compose version 2>/dev/null || docker-compose version 2>/dev/null || true

if ! command -v caddy >/dev/null 2>&1; then
  echo "==> caddy optional…"
  if command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm caddy || echo "caddy skipped"
  elif command -v apt-get >/dev/null 2>&1; then
    apt-get install -y caddy || echo "caddy skipped — use nginx or LAN :8787"
  fi
fi

echo "==> bootstrap done"
uname -a
command -v free >/dev/null && free -h | head -2 || true
df -h / ${MUSIK_REMOTE_LIBRARY} 2>/dev/null || df -h /
EOF

echo "OK — remote bootstrap finished."
