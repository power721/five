# 115 Indexer

This repository implements only the indexing service.

It crawls 115 share snapshots, stores normalized file metadata in SQLite, and builds a Bleve index for external browse and search applications.

## Scope

- Crawl 115 `share/snap`
- Resumable BFS checkpoints
- Idempotent SQLite metadata writes
- Bleve full rebuild

Out of scope:

- Browse API
- Search API
- WebDAV
- AList or TVBox integration

## Run

```bash
go run ./cmd/115-indexer \
  -mode crawl \
  -db data/index.db \
  -bleve data/bleve \
  -share-code swf01d43zby \
  -receive-code echo \
  -cookie 'acw_tc=...'
```

Import shares from `115_shares.txt`:

```bash
go run ./cmd/115-indexer -mode import-shares -shares-file 115_shares.txt -db data/index.db
```

Register a single share from URL:

```bash
go run ./cmd/115-indexer -mode register-share -share-url 'https://115.com/s/swf01d43zby?password=echo' -db data/index.db
```

Run as a long-lived daemon with status and admin endpoints:

```bash
go run ./cmd/115-indexer \
  -mode daemon \
  -db data/index.db \
  -bleve data/bleve \
  -admin-addr :8080 \
  -metrics-addr :9090
```

Useful endpoints while running:

- `GET /status` on `admin-addr`: current share count, indexed file count, pending index events
- `POST /shares` on `admin-addr`: add a new share task while the service is running

Example:

```bash
curl -X POST http://127.0.0.1:8080/shares \
  -H 'content-type: application/json' \
  -d '{"share_url":"https://115.com/s/swf01d43zby?password=echo"}'
```

Rebuild the Bleve index from SQLite:

```bash
go run ./cmd/115-indexer -mode rebuild-index -db data/index.db -bleve data/bleve
```
