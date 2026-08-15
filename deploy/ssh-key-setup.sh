#!/usr/bin/env bash
# Create deploy key and install on LXC (uses paramiko; no sshpass/sudo needed).
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./lib.sh
load_secrets
need_cmd python3
need_cmd ssh-keygen

export MUSIK_SSH_HOST MUSIK_SSH_USER MUSIK_SSH_PASS
python3 ./py_ssh.py install-key

echo "Test key auth…"
# Prefer OpenSSH with the new key
ssh -i .ssh/id_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new \
  -o ConnectTimeout=10 \
  "${MUSIK_SSH_USER}@${MUSIK_SSH_HOST}" 'echo OK; uname -a; free -h | head -2; df -h / | tail -1'

echo "Done. Next: ./deploy/remote-bootstrap.sh"
