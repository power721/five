#!/usr/bin/env bash
set -euo pipefail

# Deploy target is read from .env (FIVE_SERVER_IP), not hardcoded.
if [ -f .env ]; then . ./.env; fi
: "${FIVE_SERVER_IP:?set FIVE_SERVER_IP in .env, e.g. FIVE_SERVER_IP=203.0.113.10}"
HOST=${HOST:-${FIVE_SSH_USER:-ubuntu}@${FIVE_SERVER_IP}}

echo "building..."
go build -o five ./cmd/115-indexer/

if [ $# -eq 0 ]; then
echo "downloading..."
scp "${HOST}:~/115.index.zip" 115-index.db.zip
fi

ls -lh *.zip
rm -rf data/
mkdir data/

echo "unzip"
unzip 115-index.db.zip -d data/

echo "rebuild-index"
./five -mode rebuild-index -db data/index.db -bleve data/bleve

echo "export-db"
./five -mode export-db -db data/index.db -bleve data/bleve -out 115.index.zip -strip-file-crawled-at

echo "done"
ls -lh 115.index.zip
