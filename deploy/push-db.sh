#!/usr/bin/env bash
# Checkpoint local DB, rewrite paths for server (/home/void/test → /music),
# upload DB + embeddings into docker volume on LXC. Does NOT touch /music files.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./lib.sh
load_secrets

LOCAL_DB="${MUSIK_DB_PATH:-$REPO_ROOT/data/db/musik.db}"
LOCAL_EMB="${REPO_ROOT}/data/cache/embeddings"
LOCAL_ART="${REPO_ROOT}/data/cache/artwork"
SRC_PREFIX="${MUSIK_PATH_PREFIX:-/home/void/test}"
DST_PREFIX="${MUSIK_REMOTE_LIBRARY}"
ART_SRC_PREFIX="${MUSIK_ART_SRC_PREFIX:-$REPO_ROOT/data/cache/artwork}"
ART_DST_PREFIX="${MUSIK_ART_DST_PREFIX:-/data/cache/artwork}"

need_cmd python3

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
mkdir -p "$STAGE/db" "$STAGE/cache/embeddings" "$STAGE/cache/artwork"

echo "==> checkpoint + path rewrite ${SRC_PREFIX} → ${DST_PREFIX}"
echo "==> artwork path rewrite ${ART_SRC_PREFIX} → ${ART_DST_PREFIX}"
python3 - <<PY
import sqlite3
from pathlib import Path

src = Path("${LOCAL_DB}")
dst = Path("${STAGE}/db/musik.db")
src_conn = sqlite3.connect(f"file:{src}?mode=ro", uri=True)
dst.unlink(missing_ok=True)
dst_conn = sqlite3.connect(dst)
src_conn.backup(dst_conn)
src_conn.close()
try:
    dst_conn.execute("PRAGMA wal_checkpoint(TRUNCATE)")
except Exception:
    pass
src_pref = "${SRC_PREFIX}".rstrip("/")
art_src = str(Path("${ART_SRC_PREFIX}").resolve())
art_src_alts = list({art_src, "${ART_SRC_PREFIX}".rstrip("/")})
art_dst = "${ART_DST_PREFIX}".rstrip("/")
n = n_art = 0
for tid, path, art in dst_conn.execute(
    "SELECT id, path, artwork_path FROM tracks"
):
    new_p, new_a = path, art
    if path and path.startswith(src_pref + "/"):
        new_p = "${DST_PREFIX}".rstrip("/") + "/" + path[len(src_pref) + 1 :]
        n += 1
    elif path and path == src_pref:
        new_p = "${DST_PREFIX}".rstrip("/")
        n += 1
    if art:
        for a in sorted(art_src_alts, key=len, reverse=True):
            if art.startswith(a + "/") or art.startswith(a):
                rel = art[len(a) :].lstrip("/")
                new_a = art_dst + ("/" + rel if rel else "")
                n_art += 1
                break
    if new_p != path or new_a != art:
        dst_conn.execute(
            "UPDATE tracks SET path=?, artwork_path=? WHERE id=?",
            (new_p, new_a, tid),
        )
dst_conn.commit()
dst_conn.execute("VACUUM")
dst_conn.close()
print(f"rewrote track paths={n} artwork={n_art} → {dst}")
sample = sqlite3.connect(dst).execute(
    "SELECT path, artwork_path FROM tracks WHERE artwork_path IS NOT NULL AND artwork_path!='' LIMIT 2"
).fetchall()
print("sample:", sample)
PY

if [[ -d "$LOCAL_EMB" ]]; then
  cp -a "$LOCAL_EMB"/. "$STAGE/cache/embeddings/" 2>/dev/null || true
fi
if [[ -d "$LOCAL_ART" ]]; then
  cp -a "$LOCAL_ART"/. "$STAGE/cache/artwork/" 2>/dev/null || true
  echo "artwork files staged: $(find "$STAGE/cache/artwork" -type f | wc -l)"
fi

echo "==> upload staged data to ${MUSIK_SSH_HOST}:/opt/musik-import"
remote "mkdir -p /opt/musik-import && rm -rf /opt/musik-import/db /opt/musik-import/cache && mkdir -p /opt/musik-import/db /opt/musik-import/cache/embeddings /opt/musik-import/cache/artwork"
KEY="$DEPLOY_DIR/.ssh/id_ed25519"
tar -C "$STAGE" -cf - db cache | ssh -i "$KEY" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new \
  "${MUSIK_SSH_USER}@${MUSIK_SSH_HOST}" "tar -C /opt/musik-import -xf -"

echo "==> inject into docker volume musik-data (compose project dir ${MUSIK_REMOTE_ROOT})"
remote_bash <<EOF
set -euo pipefail
cd ${MUSIK_REMOTE_ROOT}
# Ensure volume exists (compose may create it)
docker compose pull >/dev/null 2>&1 || true
VOL=\$(docker volume ls -q | grep -E 'musik.*musik-data|musik-data' | head -1 || true)
if [[ -z "\$VOL" ]]; then
  docker volume create musik_musik-data >/dev/null 2>&1 || docker volume create musik-data
  VOL=\$(docker volume ls -q | grep -E 'musik.*musik-data|musik-data' | head -1)
fi
echo "volume=\$VOL"
# stop player/worker so SQLite is free
docker compose stop player worker 2>/dev/null || true
docker run --rm \
  -v "\$VOL:/data" \
  -v /opt/musik-import:/import:ro \
  alpine:3.20 \
  sh -c 'mkdir -p /data/db /data/cache/embeddings /data/cache/artwork && \
         rm -f /data/db/musik.db /data/db/musik.db-wal /data/db/musik.db-shm && \
         cp -a /import/db/musik.db /data/db/musik.db && \
         cp -a /import/cache/embeddings/. /data/cache/embeddings/ 2>/dev/null || true && \
         cp -a /import/cache/artwork/. /data/cache/artwork/ 2>/dev/null || true && \
         ls -lh /data/db/musik.db && \
         echo embeddings=\$(find /data/cache/embeddings -type f | wc -l) && \
         echo artwork=\$(find /data/cache/artwork -type f | wc -l)'
# start again if compose project present
if [[ -f docker-compose.yml ]]; then
  docker compose up -d --no-deps player 2>/dev/null || docker compose up -d
  sleep 2
  curl -fsS http://127.0.0.1:8787/api/health || true
  echo
fi
echo "OK — DB + artwork injected"
EOF

echo "Done."
