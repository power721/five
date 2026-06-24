#!/usr/bin/env bash
set -euo pipefail

echo "building..."
go build -o five ./cmd/115-indexer/

echo "downloading..."
scp ubuntu@YOUR_SERVER_IP:~/115.index.zip 115-index.db.zip

ls -lh *.zip
rm -rf data/
mkdir data/

echo "unzip"
unzip 115-index.db.zip -d data/

echo "rebuild-index"
./five -mode rebuild-index -db data/index.db -bleve data/bleve

echo "export-db"
./five -mode export-db -db data/index.db -bleve data/bleve -out 115.index.zip

echo "done"
ls -lh 115.index.zip
