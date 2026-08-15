#!/usr/bin/env bash
# Push local music library to remote MUSIK_REMOTE_LIBRARY (merge, no delete).
# Usage: ./deploy/push-music.sh [/path/to/local/music]
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./lib.sh
load_secrets

SRC="${1:-}"
if [[ -z "$SRC" ]]; then
  if [[ -n "${MUSIK_LIBRARY:-}" && -d "${MUSIK_LIBRARY}" ]]; then
    SRC="$MUSIK_LIBRARY"
  elif [[ -d "$REPO_ROOT/data/music" ]] && [[ -n "$(ls -A "$REPO_ROOT/data/music" 2>/dev/null || true)" ]]; then
    SRC="$REPO_ROOT/data/music"
  else
    echo "Usage: $0 /path/to/local/music" >&2
    echo "Or set MUSIK_LIBRARY in deploy/secrets.env / environment." >&2
    exit 1
  fi
fi
[[ -d "$SRC" ]] || { echo "Not a directory: $SRC" >&2; exit 1; }

echo "==> music $SRC → ${MUSIK_SSH_HOST}:${MUSIK_REMOTE_LIBRARY} (merge, no delete)"
sync_dir_to_remote "$SRC" "${MUSIK_REMOTE_LIBRARY}"

echo "OK — music synced."
remote "du -sh ${MUSIK_REMOTE_LIBRARY}; find ${MUSIK_REMOTE_LIBRARY} -type f | wc -l"
