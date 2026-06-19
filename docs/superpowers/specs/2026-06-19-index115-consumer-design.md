# Index115 Consumer Design

## Goal

Build an `index115` consumer module inside `PowerList` that reads the SQLite database and Bleve index produced by `five`, exposes browse and search APIs under `/index115`, and generates temporary 115 playback links from user-supplied cookies.

## Scope

This design owns:

- A read-only consumer for `five` SQLite metadata.
- A read-only consumer for `five` Bleve search artifacts.
- REST APIs under `/index115` for browse, search, and link resolution.
- A read-only WebDAV view for indexed 115 shares.
- Temporary 115 share-to-playback link resolution using request cookies.
- Basic observability for browse, search, and link latency and outcomes.

This design does not own:

- Crawling 115 shares.
- Writing or mutating the `five` SQLite database.
- Rebuilding or updating the Bleve index.
- Persisting user 115 cookies.
- Media metadata enrichment beyond indexed file attributes.
- Distributed cache or multi-node coordination.

## Boundary

`five` remains the only index producer. `PowerList` consumes the generated artifacts without mutating them.

```text
five
  -> writes SQLite metadata
  -> writes Bleve index versions
  -> writes index manifest

PowerList /index115
  -> reads SQLite for browse and metadata lookup
  -> reads Bleve for global search
  -> calls 115 APIs with request cookie for temporary playback links
  -> exposes read-only WebDAV browse view
```

SQLite is the source of truth for browse and file metadata. Bleve is used only for global search and search result candidate selection.

Consistency model for the MVP:

- browse and WebDAV reflect the latest committed SQLite state visible to the consumer
- search reflects the latest `READY` Bleve index referenced by `index_manifest`
- search and browse are eventually consistent with each other, not strongly snapshot-consistent
- temporary divergence between search and browse is expected during crawl or index rebuild windows and is not treated as a correctness bug in the MVP

Scale assumptions for the MVP:

- indexed share count is small and can be treated as low-cardinality metadata
- all share metadata can be loaded fully into process memory
- the dominant engineering risks are query-path quality, page stability, and 115 link resolution behavior rather than horizontal scale

## Artifact Contracts

`five` currently stores file rows in `file` and share rows in `share`. The consumer depends on the existing fields and on one planned share-table addition:

- `share_title`: the title of the 115 share, which is effectively the share root directory name, stored on `share`.

The current schema does not store `receive_code` on `file`. The consumer must resolve `receive_code` from `share` by `share_code` and must treat it as optional even if current data usually has a value.

## External API

All REST endpoints are mounted under `/index115`.

### Browse

```text
GET /index115/browse
GET /index115/browse?share_code=...&receive_code=...&parent_id=...
```

Rules:

- No parameters means browse the virtual root and list all indexed shares.
- `share_code` is required for non-root browse.
- `receive_code` is optional for non-root browse and defaults to empty string.
- `parent_id` is optional. If omitted, browse the share root.

Root response item fields:

- `share_code`
- `receive_code`
- `share_title`
- `path`
- `is_dir`
- `file_count`
- `dir_count`
- `updated_at`

Child response item fields:

- `file_id`
- `share_code`
- `receive_code`
- `share_title`
- `parent_id`
- `name`
- `path`
- `size`
- `is_dir`
- `ext`
- `sha1`
- `updated_at`

Behavior:

- Root browse returns one synthetic directory entry per share.
- Child browse returns direct children only.
- Ordering is fixed: directories first, then files, then `name` ascending.

### Search

```text
GET /index115/search?q=...
GET /index115/search?q=...&page=...&per_page=...&scope=...&share_code=...
```

Rules:

- Search is global by default.
- `q` is required.
- `share_code` is optional and acts only as a filter.
- `scope` supports `all`, `dir`, and `file` semantics.
- `parent_id` is not part of the search contract.

Response fields:

- `query`
- `total`
- `items`

Each search item includes:

- `file_id`
- `share_code`
- `receive_code`
- `share_title`
- `parent_id`
- `name`
- `path`
- `size`
- `is_dir`
- `ext`
- `sha1`
- `updated_at`

Behavior:

- Bleve searches globally across indexed documents.
- SQLite is queried afterward to enrich the results with full file metadata and share metadata.
- Search results are usable as direct inputs for follow-up browse or link requests.

### Link

```text
POST /index115/link
```

Request body:

```json
{
  "cookie": "...",
  "share_code": "...",
  "receive_code": "...",
  "file_id": "..."
}
```

Rules:

- `cookie`, `share_code`, and `file_id` are required.
- `receive_code` is optional and defaults to empty string.
- The target file must exist in SQLite and must not be a directory.

Response body:

```json
{
  "url": "...",
  "expired_in": 14400
}
```

## WebDAV

`WebDAV` is provided only for read-only browse and is mounted as an `index115` virtual subtree.

Constraints:

- No search through WebDAV.
- No share-save or link-resolution side effects through WebDAV.
- No mutation methods.

The WebDAV layer reads SQLite and exposes:

- virtual root containing one entry per indexed share
- nested directory traversal under each share

## Data Model Mapping

The consumer should avoid inventing a new `share_id` abstraction because the index producer currently keys indexed file rows by `share_code`.

Consumer identity model:

- indexed share identity: `share_code`
- share access metadata: optional `receive_code`
- file identity: `file_id`
- browse context: `parent_id`

If `share_title` is absent in old datasets, the consumer falls back to:

1. indexed `share_title`
2. share root directory name if discoverable
3. `share_code`

## SQLite Read Model

The `PowerList` consumer opens SQLite in read-only mode and exposes these access patterns:

- list all indexed shares for browse root
- list direct children by `share_code` and `parent_id`
- fetch one file by `file_id`
- fetch many files by `file_id`
- fetch share metadata by `share_code`

Share metadata resolution is performed outside the root aggregation query to avoid duplicate root entries caused by joining `file` with multiple `share` rows for the same `share_code`.

Canonical share metadata selection rule:

- prefer `ACTIVE` rows over non-`ACTIVE`
- within the same status, prefer the highest `last_crawled_at`
- break remaining ties with the highest `id`

The consumer should implement this as a full in-memory `share_code -> share metadata` map refreshed as a whole. The MVP does not require partial invalidation or distributed cache coherence.

Expected browse queries:

Root browse:

```sql
SELECT
  share_code,
  MAX(updated_at) AS updated_at,
  SUM(CASE WHEN is_dir = 0 THEN 1 ELSE 0 END) AS file_count,
  SUM(CASE WHEN is_dir = 1 THEN 1 ELSE 0 END) AS dir_count
FROM file
GROUP BY share_code;
```

The share metadata map is then applied in memory to populate:

- `receive_code`
- `share_title`

This root browse aggregation intentionally remains in SQLite. Even with a small share count, keeping the file aggregation in SQL is simpler and avoids scanning the full file table into application memory for each request.

Child browse:

```sql
SELECT
  f.file_id,
  f.share_code,
  f.parent_id,
  f.name,
  f.path,
  f.size,
  f.is_dir,
  f.ext,
  f.sha1,
  f.updated_at
FROM file f
WHERE f.share_code = ?
  AND f.parent_id = ?
ORDER BY is_dir DESC, name ASC;
```

Current rollout requirement:

- `receive_code` is read from `share`, not `file`
- `share_title` is read from `share`
- `share_title` falls back to `share_code` until `five` persists it on `share`

Future optimization:

- the consumer should cache all `share` metadata in memory for lookup efficiency
- the consumer must not assume `receive_code` or `share_title` exist on `file`

## Bleve Read Model

The consumer opens the `READY` Bleve index path referenced by the current index manifest.

Search flow:

1. Open current index from manifest.
2. Execute a global query against `name` and `path`.
3. Optionally constrain to one `share_code`.
4. Apply pagination in Bleve.
5. Collect matching `file_id` values.
6. Fetch full rows from SQLite using `IN(file_id...)`.
7. Drop any Bleve hits whose `file_id` cannot be resolved from SQLite and emit a warning log and metric for the miss.
8. Reorder the fetched rows in memory to match the original Bleve hit order exactly.
9. Enrich the rows with share metadata resolved by `share_code`.

Bleve is not the source of metadata completeness. It is only the fast candidate selector.

The MVP intentionally keeps SQLite as the metadata source of truth for search results. Bleve documents are optimized for retrieval quality and candidate selection, not for acting as the sole browse and link metadata store.

Pagination rules for the MVP:

- `page/per_page` is offset-based
- `per_page` has a strict upper bound
- very deep pagination is not optimized in the MVP
- SQLite misses may produce underfilled pages
- the MVP does not backfill missing rows from later Bleve offsets because that would create cross-page duplication and drift under offset pagination
- cursor-based pagination is a future upgrade if deep-page latency becomes material

## Link Resolution Flow

The playback link flow mirrors the current `drivers/115_share` logic, but it uses a per-request cookie instead of a configured storage account.

Flow:

1. Validate request fields.
2. Lookup `file_id` in SQLite.
3. Verify the file is not a directory and belongs to the requested `share_code`.
4. Resolve the indexed share metadata from `share` using `share_code`.
5. Resolve `receive_code` using this rule:
   - if `request.receive_code` is non-empty, use it
   - otherwise use `share.receive_code`
   - if neither has a value, use empty string
6. Build a temporary 115 client from the request cookie.
7. Call the share download flow with `share_code`, resolved `receive_code`, and `file_id`.
8. Return the resolved playback URL with the standard expiration.
9. Acquire or refresh a cleanup lease for the transferred file.
10. Schedule delayed cleanup of the transferred file from the user's receive directory only when the lease has expired and no newer lease exists.

Implementation notes:

- Reuse existing 115 client logic for cookie login.
- Reuse existing receive-directory cleanup logic.
- Do not mutate long-lived configured driver state.
- Encapsulate the cookie-based 115 flow behind an `index115`-local helper or service adapter.
- Use a lightweight in-process lease registry keyed by a stable hash of cookie identity and file identity for the MVP.
- Keep the lease mechanism internal so it can later be replaced by SQLite or Redis if multi-instance coordination is needed.

## PowerList Module Layout

Recommended module layout inside `PowerList`:

```text
internal/index115/
  model.go
  store.go
  search.go
  service.go
  linker_115.go
  webdavfs.go
```

Routing and handler integration:

```text
server/handles/index115.go
server/router.go
```

Responsibilities:

- `model.go`: API request and response structures.
- `store.go`: SQLite read access and share/file lookups.
- `search.go`: Bleve open and query logic.
- `service.go`: browse/search/link orchestration.
- `linker_115.go`: cookie-based 115 link resolution and delayed cleanup.
- `webdavfs.go`: read-only virtual filesystem adapter.

## Error Handling

REST behavior:

- empty search query returns `400`
- non-root browse without `share_code` returns `400`
- link request missing `cookie`, `share_code`, or `file_id` returns `400`
- unknown file or mismatched `share_code` returns `404`
- directory link request returns `400`
- SQLite unavailable returns `503`
- Bleve unavailable returns `503` for search
- 115 cookie invalid or expired returns `401` or `502` depending on the translated downstream error policy

WebDAV behavior:

- unsupported mutation methods return `405` or `403`
- unresolved shares or directories return `404`

## Compatibility

The design assumes `five` will be extended to persist on `share`:

- `share_title`
- optional `receive_code` semantics

Until then, `PowerList` must support fallback behavior:

- `share_title` falls back to `share_code`
- empty `receive_code` is valid
- `receive_code` is resolved from `share`
- browse and search results join `share` to enrich `file` rows

## Observability

The MVP should expose at least:

- browse request latency
- search total latency
- SQLite query latency
- Bleve query latency
- Bleve hit count vs resolved SQLite row count
- dropped search-hit count caused by unresolved `file_id`
- link generation success and failure counts
- cleanup lease creation and cleanup execution counts

## Verification Requirements

- Unit tests for SQLite root browse aggregation.
- Unit tests for SQLite child browse ordering and filtering.
- Unit tests for Bleve search plus SQLite enrichment.
- Unit tests for request validation on browse, search, and link.
- Unit tests for `receive_code` optional behavior.
- Unit tests for link service behavior using a mocked 115 client adapter.
- Unit tests for read-only WebDAV root and nested browse behavior.

## Out of Scope

- Session-based cookie storage.
- Authentication or authorization policy changes for `AList-TvBox`.
- Automatic sync or rebuild of the `five` index artifacts.
- Metadata recognition such as movies, seasons, or episodes.
