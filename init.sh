#!/usr/bin/env bash
set -euo pipefail

ls -lh
rm -rf data/
mkdir data/

echo "crawl"

go run ./cmd/115-indexer \
  -mode crawl \
  -db data/index.db \
  -share-code swz8h1h33xj \
  -receive-code 0000

go run ./cmd/115-indexer \
  -mode crawl \
  -db data/index.db \
  -share-code sw6e6i13flt \
  -receive-code f794

sqlite3 data/index.db "select * from share;"
sqlite3 data/index.db "select count(*) from file;"

echo "rebuild-index"
./five -mode rebuild-index -db data/index.db -bleve data/bleve

echo "export-db"
./five -mode export-db -db data/index.db -bleve data/bleve -out 115.index.zip

echo "done"
ls -lh 115.index.zip

cp 115.index.zip /home/user/workspace/alist-tvbox/data/115.index.zip
