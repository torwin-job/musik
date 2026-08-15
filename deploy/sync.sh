#!/usr/bin/env bash
# Sync project code to LXC (no .venv, no local music, no secrets.env).
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./lib.sh
load_secrets

TMP_ENV="$(mktemp)"
TMP_TAR="$(mktemp)"
trap 'rm -f "$TMP_ENV" "$TMP_TAR"' EXIT
write_remote_env "$TMP_ENV"

echo "==> sync code → ${MUSIK_SSH_USER}@${MUSIK_SSH_HOST}:${MUSIK_REMOTE_ROOT}"
remote "mkdir -p ${MUSIK_REMOTE_ROOT}"

if command -v rsync >/dev/null 2>&1; then
  export SSHPASS="${MUSIK_SSH_PASS:-}"
  RSH="$(rsync_ssh)"
  rsync -az --delete \
    -e "$RSH" \
    --exclude '.git/' \
    --exclude '.venv/' \
    --exclude '.venv-rocm/' \
    --exclude '.rocm-extra/' \
    --exclude 'data/db/' \
    --exclude 'data/cache/' \
    --exclude 'data/music/' \
    --exclude 'mobile/flutter/build/' \
    --exclude 'mobile/flutter/.dart_tool/' \
    --exclude 'player/bin/' \
    --exclude '.env' \
    --exclude 'deploy/secrets.env' \
    --exclude 'deploy/.ssh/' \
    --exclude 'deploy/.vendor/' \
    --exclude '*.apk' \
    --exclude '__pycache__/' \
    --exclude '.pytest_cache/' \
    "$REPO_ROOT/" \
    "${MUSIK_SSH_USER}@${MUSIK_SSH_HOST}:${MUSIK_REMOTE_ROOT}/"
  rsync -az -e "$RSH" "$TMP_ENV" \
    "${MUSIK_SSH_USER}@${MUSIK_SSH_HOST}:${MUSIK_REMOTE_ROOT}/.env"
else
  echo "rsync missing — using tar|ssh"
  tar -C "$REPO_ROOT" --ignore-failed-read -cf "$TMP_TAR" \
    --exclude='.git' \
    --exclude='.venv' \
    --exclude='.venv-rocm' \
    --exclude='.rocm-extra' \
    --exclude='data/db' \
    --exclude='data/cache' \
    --exclude='data/music' \
    --exclude='data/navidrome' \
    --exclude='data/postgres' \
    --exclude='mobile/flutter/build' \
    --exclude='mobile/flutter/.dart_tool' \
    --exclude='player/bin' \
    --exclude='.env' \
    --exclude='deploy/secrets.env' \
    --exclude='deploy/.ssh' \
    --exclude='deploy/.vendor' \
    --exclude='*.apk' \
    --exclude='__pycache__' \
    --exclude='.pytest_cache' \
    . || true
  KEY="$DEPLOY_DIR/.ssh/id_ed25519"
  # wipe code dir contents carefully but keep named volume data elsewhere
  remote "mkdir -p ${MUSIK_REMOTE_ROOT} && find ${MUSIK_REMOTE_ROOT} -mindepth 1 -maxdepth 1 ! -name '.env' -exec rm -rf {} +"
  cat "$TMP_TAR" | ssh -i "$KEY" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new \
    "${MUSIK_SSH_USER}@${MUSIK_SSH_HOST}" "tar -C ${MUSIK_REMOTE_ROOT} -xf -"
  cat "$TMP_ENV" | ssh -i "$KEY" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new \
    "${MUSIK_SSH_USER}@${MUSIK_SSH_HOST}" "cat > ${MUSIK_REMOTE_ROOT}/.env"
fi

remote "mkdir -p ${MUSIK_REMOTE_LIBRARY}"
echo "OK — code synced. Next: ./deploy/push-db.sh && ./deploy/remote-up.sh"
