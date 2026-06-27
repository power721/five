#!/usr/bin/env bash
set -euo pipefail

# Deploy target is read from .env (FIVE_SERVER_IP), not hardcoded.
if [ -f .env ]; then . ./.env; fi
: "${FIVE_SERVER_IP:?set FIVE_SERVER_IP in .env, e.g. FIVE_SERVER_IP=203.0.113.10}"
HOST=${HOST:-${FIVE_SSH_USER:-ubuntu}@${FIVE_SERVER_IP}}
BIN_NAME=five

go build -o "${BIN_NAME}" ./cmd/115-indexer/
scp "${BIN_NAME}" install.sh "${HOST}:~"

if [ -f .env ]; then
  scp .env "${HOST}:~/.env"
fi

ssh "${HOST}" "chmod +x ~/install.sh && ~/install.sh"
