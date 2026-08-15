#!/usr/bin/env bash
# Interactive / one-shot remote command: ./deploy/ssh.sh 'df -h'
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck disable=SC1091
source ./lib.sh
load_secrets
remote "$@"
