# 115 Indexer Service Design

## Goal

Build only the indexing service for 115 share data. Browse and search APIs are out of scope for this repository and will be integrated by other applications as read-only consumers of the generated SQLite database and Bleve index.

## Scope

This project owns:

- Share registry and share lifecycle state.
- 115 `share/snap` API crawling.
- Proxy-aware retry policy hooks.
- Resumable BFS checkpointing.
- Idempotent SQLite file metadata writes.
- Bleve index creation, incremental updates, and full rebuild.
- Stable read-only data contracts for external browse and search applications.

This project does not own:

- Browse REST API.
- Search REST API.
- WebDAV.
- AList or TVBox integration.
- TMDB, scraping, media recognition, or episode grouping.

## Service Boundary

`115-indexer` is the only writer. External applications may read the SQLite database and Bleve index but must not mutate them.

```text
115-indexer
  -> writes SQLite file metadata
  -> writes Bleve index versions
  -> writes index manifest

external browse app
  -> reads SQLite

external search app
  -> reads Bleve
  -> optionally reads SQLite for file details
```

SQLite is the source of truth. Bleve is a derived artifact and must be rebuildable from SQLite.

## 115 Snap API Contract

The crawler uses:

```text
GET https://115cdn.com/webapi/share/snap
```

Required query parameters:

- `share_code`: 115 share code, for example `swf01d43zby`.
- `receive_code`: extraction code, for example `echo`.
- `cid`: folder id to list.
- `offset`: page offset.
- `limit`: page size.
- `asc`: sorting direction.
- `o`: sorting field, default `file_name`.
- `format`: `json`.

Important response fields:

- `state`: API success flag.
- `errno` and `error`: error classification.
- `data.count`: number of items in the current folder.
- `data.list[].fid`: file id for files.
- `data.list[].cid`: node id. For directories this is the folder id to crawl next.
- `data.list[].n`: display name.
- `data.list[].s`: size.
- `data.list[].d`: directory flag, `1` means directory.
- `data.list[].ico`: extension-like type.
- `data.list[].sha`: sha1 for files.
- `data.shareinfo.share_state`: share validity.
- `data.shareinfo.receive_code`: confirmed receive code.

The crawler maps a directory node id from `cid` and a file identity from `fid`. If `fid` is empty, it falls back to `cid` so every node still has a stable id.

## Data Model

### share

```sql
CREATE TABLE IF NOT EXISTS share (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    share_code TEXT NOT NULL,
    receive_code TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    last_crawled_at INTEGER,
    last_error TEXT,
    version INTEGER NOT NULL DEFAULT 0,
    UNIQUE(share_code, receive_code)
);
```

### file

```sql
CREATE TABLE IF NOT EXISTS file (
    file_id TEXT PRIMARY KEY,
    share_code TEXT NOT NULL,
    parent_id TEXT NOT NULL,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    ext TEXT NOT NULL DEFAULT '',
    size INTEGER NOT NULL DEFAULT 0,
    is_dir INTEGER NOT NULL DEFAULT 0,
    depth INTEGER NOT NULL DEFAULT 0,
    sha1 TEXT NOT NULL DEFAULT '',
    updated_at INTEGER,
    crawled_at INTEGER NOT NULL
);
```

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_file_share_parent ON file(share_code, parent_id);
CREATE INDEX IF NOT EXISTS idx_file_share_path ON file(share_code, path);
CREATE INDEX IF NOT EXISTS idx_file_ext ON file(ext);
CREATE INDEX IF NOT EXISTS idx_file_depth ON file(depth);
CREATE INDEX IF NOT EXISTS idx_file_size ON file(size);
```

### crawl_checkpoint

```sql
CREATE TABLE IF NOT EXISTS crawl_checkpoint (
    share_code TEXT PRIMARY KEY,
    cid TEXT NOT NULL,
    queue_json TEXT NOT NULL,
    visited_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
```

### index_event

```sql
CREATE TABLE IF NOT EXISTS index_event (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id TEXT NOT NULL,
    op TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    processed_at INTEGER
);
```

### index_manifest

```sql
CREATE TABLE IF NOT EXISTS index_manifest (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL,
    index_path TEXT NOT NULL,
    status TEXT NOT NULL,
    built_at INTEGER NOT NULL,
    file_count INTEGER NOT NULL
);
```

## Crawler Flow

```text
1. Load active shares.
2. For each share, restore checkpoint or start from fixed root `cid=0`.
3. Pop one folder task from BFS queue.
4. Page through 115 snap API using offset and limit.
5. Upsert every returned node into SQLite.
6. Append directories to BFS queue.
7. Save checkpoint after each folder page.
8. Emit index_event for each upserted node.
9. Continue until queue is empty.
```

The crawler is resumable. A crash may repeat the last page or folder, but `file_id` idempotence makes repeats safe.

## Indexing Flow

Incremental indexing:

```text
1. Read unprocessed index_event rows.
2. Load each file from SQLite.
3. Upsert a Bleve document keyed by file_id.
4. Mark the event processed only after Bleve succeeds.
```

Full rebuild:

```text
1. Build into a new directory named index_<version>_building.
2. Scan all SQLite file rows.
3. Write Bleve documents.
4. Close index.
5. Rename directory to index_<version>.
6. Update index_manifest to READY.
```

External search applications should only open the manifest path whose status is `READY`.

## Error Handling

- Timeout: retry with exponential backoff.
- HTTP 403: rotate proxy and retry.
- Empty data with successful share state: retry with backoff.
- Invalid receive code or invalid share state: mark share `DEAD`.
- Repeated transient failure: mark share `STALE` or `QUARANTINE`.

The initial implementation exposes retry hooks and stable error classification. Production proxy pool policy can be expanded without changing the storage contract.

## Verification Requirements

- Unit tests cover 115 snap response parsing.
- Unit tests cover SQLite migration, file upsert, checkpoint persistence, and index events.
- Unit tests cover BFS crawler behavior using a fake 115 client.
- Unit tests cover Bleve full rebuild from SQLite.
- `go test ./...` must pass before claiming completion.
