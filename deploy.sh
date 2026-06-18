#!/usr/bin/env bash
set -euo pipefail

HOST=${HOST:-ubuntu@YOUR_SERVER_IP}
BIN_NAME=five

go build -o "${BIN_NAME}" ./cmd/115-indexer/
scp "${BIN_NAME}" install.sh "${HOST}:~"

if [ -f .env ]; then
  scp .env "${HOST}:~/.env"
fi

ssh "${HOST}" "chmod +x ~/install.sh && ~/install.sh"
