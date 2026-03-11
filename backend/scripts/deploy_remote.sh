#!/usr/bin/env bash
set -euo pipefail

REMOTE_HOST="178.140.207.217"
REMOTE_PORT="2222"
REMOTE_USER="mgg"
REMOTE_DIR="/home/mgg/qgramm-backend"

if [[ "${1:-}" != "" ]]; then
  REMOTE_DIR="$1"
fi

echo "Deploying backend to ${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_DIR}"

ssh -p "${REMOTE_PORT}" "${REMOTE_USER}@${REMOTE_HOST}" "mkdir -p ${REMOTE_DIR}"

rsync -avz --delete \
  -e "ssh -p ${REMOTE_PORT}" \
  --exclude '.git' \
  --exclude 'data' \
  ./ "${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_DIR}/"

ssh -p "${REMOTE_PORT}" "${REMOTE_USER}@${REMOTE_HOST}" <<EOSSH
set -euo pipefail
cd ${REMOTE_DIR}
mkdir -p data/postgres data/attachments data/avatars data/tmp
docker compose pull || true
docker compose build --no-cache
docker compose up -d
EOSSH

echo "Deployment complete"
