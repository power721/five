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

Proxy is required for all modes that access 115 directly: `crawl`, `run-scheduler-once`, `daemon`, and `validate-share-counts`. `backfill-share-meta` routes through the proxy when credentials are available and falls back to a direct request otherwise.

The indexer uses a single active proxy at a time. When the active proxy fails repeatedly, it is discarded and a new proxy is fetched on demand. If several consecutive proxy replacements fail, the service stops instead of continuing to burn through invalid proxies.

Credentials are resolved in this order:

1. `-proxy-key` / `-proxy-password`
2. environment variables `FIVE_PROXY_KEY` / `FIVE_PROXY_PASSWORD`
3. `.env` file in the current working directory, or a custom path via `-env-file`

Example `.env`:

```bash
FIVE_PROXY_KEY=your-proxy-key
FIVE_PROXY_PASSWORD=your-proxy-password
```

### Modes

| Mode | Description |
|---|---|
| `crawl` | Crawl one share (`-share-code` + `-receive-code`). |
| `daemon` | Long-lived: scheduler loop plus admin/metrics HTTP servers. |
| `run-scheduler-once` | Run a single scheduler crawl cycle, then exit. |
| `import-shares` | Register shares in bulk from `-shares-file`. |
| `import-groups` | Apply the virtual-dir group overlay from `-groups-file`. |
| `register-share` | Register one share from `-share-url` (or `-share-code`). |
| `backfill-share-meta` | Refresh share title/size from 115 for shares in `-shares-file`. |
| `validate-share-counts` | Compare indexed file counts against live 115 counts. |
| `dedupe-share-titles` | Collapse duplicate share titles (dry-run unless `-apply`). |
| `dedupe-shares-by-size` | Mark same-`file_size` (≥ `-dedupe-min-size`) shares `DUPLICATE` and delete their files (dry-run unless `-apply`). |
| `cleanup-orphans` | List/delete `file` rows whose share was removed (dry-run unless `-apply`). |
| `rebuild-index` | Rebuild the Bleve index from SQLite. |
| `export-db` | Package a self-contained index zip for consumers. |

Duplicate shares (identical total `file_size`, by default ≥ 1GiB via
`-dedupe-min-size`) are detected on the first crawl page and marked `DUPLICATE`
without indexing; `DUPLICATE` shares are excluded from scheduling and `export-db`.
`-mode dedupe-shares-by-size [-apply]` cleans already-indexed duplicates;
`-mode cleanup-orphans [-apply]` removes files whose share row is gone (e.g. a
share deleted mid-crawl).

A 115 share is an immutable snapshot, so once a share's crawl fully drains it is
marked `COMPLETED` and never re-queued by the scheduler (it is still included in
`export-db` with its files). To force a fresh re-crawl — e.g. after
`validate-share-counts` reports a mismatch — `POST /shares/<share_code>/reactivate`
resets it to `ACTIVE`.

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
- `GET /shares` on `admin-addr`: all registered shares with current status and failure state
- `GET /shares/<share_code>` on `admin-addr`: one share's detailed progress, including checkpoint queue size, visited count, and next page offset
- `POST /shares` on `admin-addr`: add a new share task while the service is running
- `DELETE /shares/<share_code>` on `admin-addr`: remove a share and all of its data — files, BFS checkpoint (queue/visited/offset), and the share row. Refused with `409` if the share still has indexed files unless `?force=true` is passed.
- `POST /shares/<share_code>/reactivate` on `admin-addr`: reset a shelved/quarantined/dead share back to `ACTIVE` (clears failure count, last error, and retry-after) so the scheduler picks it up again
- `POST /crawler/pause` on `admin-addr`: stop scheduling crawls. The in-flight share finishes its current page and writes its checkpoint, then the loop parks until resumed; nothing is recorded as a failure. Idempotent.
- `POST /crawler/resume` on `admin-addr`: resume crawling from the last checkpoint
- `GET /crawler/state` on `admin-addr`: current crawler state — `{"state":"running"}` or `{"state":"paused"}`

Example:

```bash
curl -X POST http://127.0.0.1:8080/shares \
  -H 'content-type: application/json' \
  -d '{"share_url":"https://115.com/s/swf01d43zby?password=echo"}'
```

Batch import from `115_shares.txt`:

```bash
curl -X POST http://127.0.0.1:8080/shares \
  -F 'file=@115_shares.txt'
```

Pause and resume the crawler without restarting the daemon (state is in-memory
and resets to `running` on restart):

```bash
curl -X POST http://127.0.0.1:8080/crawler/pause   # -> {"state":"paused"}
curl http://127.0.0.1:8080/crawler/state            # -> {"state":"paused"}
curl -X POST http://127.0.0.1:8080/crawler/resume   # -> {"state":"running"}
```

Rebuild the Bleve index from SQLite:

```bash
go run ./cmd/115-indexer -mode rebuild-index -db data/index.db -bleve data/bleve

```

```bash
curl http://127.0.0.1:8080/status

journalctl -u five -f

sqlite3 /opt/five/data/index.db

```

## Distribute the index

Package a self-contained index for downstream consumers (alist-tvbox / PowerList):

```bash
go run ./cmd/115-indexer -mode export-db -db data/index.db -bleve data/bleve -out 115.index.zip

```

`115.index.zip` contains a trimmed `index.db` (only `file` and `share` tables)
and the READY bleve index under `bleve/`. It extracts to `index.db` + `bleve/`.
By default the exported `file` table keeps `crawled_at`; pass
`-strip-file-crawled-at` only for consumers that require that column removed.

Manual publish to 115:

1. Upload `115.index.zip` to the publishing 115 account and create a share.
2. Overwrite the version pointer (`https://d.example.com/115.version.txt`) with one
   line `shareCode:receiveCode` (e.g. `swf01d43zby:6666`).
