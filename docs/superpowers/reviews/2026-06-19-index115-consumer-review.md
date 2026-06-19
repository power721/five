# index115 Consumer — Design & Implementation Review

**Date:** 2026-06-19 — *revised after reconciling against the implementation branch.*

**What this document is now:** it began as a review of the design spec + implementation *plan*, then was **reconciled against the actual code** on branch `feat/index115-consumer` (worktree `.worktrees/powerlist-index115/`, OpenList v4 fork). Every finding below is tagged **OPEN** (still present in that branch) or **RESOLVED** (handled there). Do not read RESOLVED items as current blockers — they describe the original plan, not the current code.

**Citations:** `five/…` = producer, this repo (`main`). `PL:…` = consumer at `feat/index115-consumer`, repo-relative.

**Current verdict:** the link flow, search query, search pagination, WebDAV mount, and handler param parsing flagged against the original *plan* are **resolved** in `feat/index115-consumer`. **Five genuine issues remain** in that branch — **B4, C2, G1, G5, G6** — plus a closed-in-practice note on **B1**. The producer-side items **N1 / B6 / B1** have landed on `five` `main`.

---

## OPEN — still present in `feat/index115-consumer`

### B4 — manifest Bleve path is joined naïvely; an absolute stored path wins
`PL: internal/index115/runtime.go:46-59` → `return filepath.Join(bleveBaseDir, relPath)`. `five` stores `index_path` as a complete build path (`filepath.Join(*blevePath, "index_NNNNNN")`, `five/internal/searchindex/indexer.go:58`). If that is absolute, `filepath.Join(base, "/abs/idx")` returns `/abs/idx`, ignoring `bleveBaseDir`; after extraction into the container the build-host path doesn't exist. **Fix:** resolve relative to the bleve root (discover `index_NNNNNN` under it), or have `five` emit a relative `index_path` in the manifest.

### C2 — cleanup deletes by sha1 (too broad)
`PL: internal/index115/linker_115.go:51` schedules `scheduleCleanup(cookie, file.FileID, sha1)`; the adapter's `DeleteReceivedBySHA1` (`PL: internal/index115/linker_115_adapter.go:51-83`) deletes the first received-dir file whose `Sha1` matches. A file the user already had, or a concurrent transfer of the same content, can be deleted; it can also race `five`'s own delayed cleanup. **Fix:** target the specific transferred file_id; make idempotent.

### G1 — `scope` accepted but ignored
`SearchRequest.Scope` is parsed (`PL: server/handles/index115.go`) but never applied — `buildSearchQuery` (`PL: internal/index115/search.go:77`) and `service.go` ignore it. **Agreed resolution: remove the parameter**, not implement it. (Decided: server-side dir/file filtering isn't worth the cost for MVP — every result already carries `is_dir` for client-side filtering, and `five` will *not* add an explicit `is_dir` Bleve mapping. Doing it server-side would also break offset-pagination correctness.) So the action is to drop `scope` from the request/handler.

### G5 — a Bleve open failure takes down browse + link too
`PL: internal/index115/runtime.go:34-38` returns an error when `bleve.Open` fails; `PL: internal/bootstrap/index115.go` then registers no service, so every `/index115` route returns 503. Browse and link only need SQLite. **Fix:** initialize the store and the searcher independently; degrade search only.

### G6 — error→HTTP status mapping is coarse
`PL: server/handles/index115.go` maps Browse and Link errors to 400 (Search likewise, aside from service-not-initialised → 503). **Fix:** map error types — 404 unknown file / mismatched share, 503 SQLite/Bleve unavailable, 401/502 cookie invalid/expired.

### B1 — share_title compatibility — *closed in practice* (residual note, not a blocker)
The consumer reads `COALESCE(share_title,'')` (`PL: internal/index115/store.go:34`). Note: COALESCE covers NULL/empty only — it is **not** a column-presence guard, so it would not save a DB that lacks the column. The crash scenario is nonetheless closed, because **`five` now always persists `share_title`** (schema + idempotent migration on `main`); no shipped artifact can lack the column. Residual: the consumer isn't defensively gated against a literally column-less DB, but the pipeline can't produce one. No action required unless you want belt-and-suspenders gating (`PRAGMA table_info`).

---

## RESOLVED in `feat/index115-consumer` (originally raised against the plan)

- **B5** — `LinkResolver.Resolve` implemented: receive_code resolution → client call → cleanup scheduling → returns link (`PL: internal/index115/linker_115.go:38-54`).
- **C3** — per-request 115 client from a raw cookie + real download shape: `Credential.FromCookie` → `driver115.New(...).ImportCredential` → `CookieCheck` → `DownloadByShareCode` mapped to `ResolvedLink{URL.URL, 4h}` (`PL: internal/index115/linker_115_adapter.go:87-101`).
- **C4** — `common.DefaultInt` gone; local `parseInt(query, default)` helper (`PL: server/handles/index115.go:51-52,93`).
- **G2** — `bleve.NewMatchQuery` (+ Boolean/Term for the optional share filter), not `QueryStringQuery` on raw input (`PL: internal/index115/search.go:77-88`).
- **N5** — pagination hydrates Bleve hits via `store.FilesByIDs`, skips unresolved ids, and returns `resolvedTotal` (count of resolvable rows), not the raw Bleve total (`PL: internal/index115/search.go:16-74`).
- **G4** — standalone read-only WebDAV on `golang.org/x/net/webdav` with a custom `index115WebDAVFS` implementing the `FileSystem` interface (`PL: server/index115_webdav.go:35-62`), not the fs-coupled core handler.
- **B2** — (already noted) the snapshot model means no live writer on the consumer host, so no cross-process Bleve lock.

## NOT a defect — design choice
- **B3** — the implementation uses **two** explicit config values, `Index115.DBFile` + `Index115.BleveDir`, and checks both (`PL: internal/conf/config.go:109-112`, `internal/bootstrap/index115.go:28-31`). That's a valid, clear choice. The earlier "collapse into one `index_root`" was a *suggestion* (only worthwhile if you'd rather ship a single extraction dir), not a bug. No change required.

## Producer-side (`five`) — landed on `main`
- **N1** — directories no longer get a bogus `ext` (`five/internal/api115/snap.go` `ToFile`; e.g. a folder `…26.52T]` no longer yields `ext "52T]"`).
- **B6** — `export-db` mode + `Store.ExportSnapshot` (`VACUUM INTO`) ships a self-contained, WAL-folded `index.db` (no `-wal`/`-shm` sidecar needed).
- **B1 (producer half)** — `share` table now carries `share_title`/`file_size` (+ idempotent migration), auto-populated during crawl and via the `backfill-share-meta` command.

## Low-priority notes
- **N2 / N3 / N4** (contracts, producer): `path` is a full path with a leading slash; browse is a media-filtered view (a directory of only non-media files looks empty); root sentinel is `parent_id="0"`. Document these.
- **N6** (consumer): cleanup runs in an in-process `go func(){ time.Sleep }`; pending deletions are lost on restart → residue in users' 115 drives. Make recoverable.
- **N7** (consumer): the adapter runs `CookieCheck()` per request (`PL: linker_115_adapter.go`); consider caching clients by cookie hash, and never log the cookie.

## Open questions (current)
1. **B4:** resolve the manifest path relative on the consumer side, or have `five` write a relative `index_path`?
2. **G1:** confirm `scope` is **removed** (the agreed direction), not implemented.
3. **C2:** cleanup keyed on the transferred file_id instead of sha1 — acceptable?
4. **G5:** degrade to browse+link when Bleve is unavailable (recommended)?

## Verification (remaining items)
- Manifest `index_path` absolute → confirm B4 reproduces, then that the fix opens the right dir.
- Concurrent link requests for the same sha1 → confirm C2 doesn't cross-delete.
- Missing/corrupt Bleve dir → confirm browse + link still serve (G5).
- Unknown file_id / bad cookie → confirm 404 / 401|502 (G6).
