# Share crawl terminal COMPLETED status — design

Date: 2026-06-26
Status: approved (pending implementation)

## Problem

A 115 share is an immutable snapshot: once crawled it does not change. Yet the
indexer never records "this share is done." `scheduler.RunOnce` lists every
share with `status IN ('ACTIVE','STALE','QUARANTINE') AND retry_after_unix <=
now`, and on a successful crawl `MarkShareCrawled` sets `status='ACTIVE'`
again. So a fully-crawled share stays `ACTIVE` and is re-listed and re-"crawled"
**every scheduler cycle**.

The re-crawl is cheap (the checkpoint has root visited, so the BFS loop
`continue`s with zero `ListPage` calls), but it is pointless: it rewrites the
checkpoint and bumps `last_crawled_at`/`version` forever, and the scheduler
keeps touching shares that are conceptually finished.

## Goal

After a share's BFS queue fully drains (a complete snapshot crawl), mark the
share terminal so the scheduler never re-queues it.

- One full successful crawl ⇒ share reaches a terminal `COMPLETED` status.
- `COMPLETED` shares are excluded from scheduling.
- `COMPLETED` shares (and their files) are still exported — only `DEAD` and
  `DUPLICATE` are pruned at export.
- An operator can still force a re-crawl via the existing `POST
  /shares/<code>/reactivate` endpoint.

## Non-goals

- Periodic re-crawl / freshness cadence. The premise is that snapshots are
  immutable; completed shares are not re-crawled on a schedule.
- A new "force re-crawl" endpoint. Reactivate already serves this.
- Migration script for existing shares. They self-migrate (see Migration).

## Background: why success == full crawl

`crawler.CrawlShare` runs the entire resumable BFS in one call. Its main loop
is `for activeCID != "" || len(queue) > 0`; it returns `nil` only when both the
active CID and the queue are drained. Any interruption (context cancel, pause
gate, page error) returns a non-nil error. Therefore the scheduler's success
branch (`err == nil` after `CrawlShare`) is reached exactly when the snapshot
has been completely crawled. A share whose root returns zero nodes also drains
immediately and returns `nil` — a complete (empty) snapshot — and correctly
becomes `COMPLETED`.

## Status machine (after change)

```
ACTIVE ──crawl success──▶ COMPLETED              (new: terminal, unscheduled)
ACTIVE ──failure──▶ STALE / QUARANTINE           (unchanged; backoff retry)
QUARANTINE ──persistent empty-data──▶ shelved    (unchanged; far-future retry)
… ──dead──▶ DEAD  /  ─duplicate──▶ DUPLICATE     (unchanged)
COMPLETED ──POST /reactivate──▶ ACTIVE ─▶ re-crawl ─▶ COMPLETED
```

New shares still enter as `ACTIVE` (`POST /shares`, `register-share`,
`import-shares` — all unchanged). `UpdateShareMeta` sets `status='ACTIVE'`
only on its `INSERT` (new-row) path; on conflict it updates just
`share_title`/`file_size` and never overwrites `status`, so it cannot clobber a
`COMPLETED` share. `backfill-share-meta` uses only `UpdateShareMeta`, so it is
safe on completed shares.

## Changes

1. **`store.MarkShareCrawled`** (`internal/store/sqlite.go`): change
   `SET status='ACTIVE'` to `SET status='COMPLETED'`. Add/expand the doc
   comment to state it marks a *complete* crawl. **Keep the method name**
   (`MarkShareCrawled`): it still means "mark this share's crawl as done," and
   renaming would ripple through the `scheduler.Registry` interface, its mock,
   and tests for no real gain. The other columns it sets (`last_crawled_at`,
   clearing `last_error`/`failure_count`/`retry_after_unix`, `version+1`) are
   unchanged and still correct for a terminal write.

2. **`store.ListSharesForCrawl`**: **no change.** Its `WHERE status IN
   ('ACTIVE','STALE','QUARANTINE')` already excludes `COMPLETED`.

3. **Export prune** (`ExportTrimmed`): **no change.** It deletes only
   `status IN ('DEAD','DUPLICATE')`; `COMPLETED` shares ship with their files.

4. **`store.ReactivateShare`** (`internal/store/share_status.go`): **no
   change.** It unconditionally `SET status='ACTIVE'` (clearing
   `last_error`/`duplicate_of`/`failure_count`/`retry_after_unix`), so it
   already resets `COMPLETED → ACTIVE`. Only update its doc comment to mention
   `COMPLETED`.

5. **`-mode crawl`** (`cmd/115-indexer/main.go`): on success, mark the share
   `COMPLETED`. This mode currently builds a bare `model.Share{ShareCode,
   ReceiveCode}` and never persists a share row, so `MarkShareCrawled`'s
   `UPDATE` would match zero rows. Change it to:
   1. `UpsertShare` with `Status:"ACTIVE"` (idempotent; if the share already
      exists as `COMPLETED`, this flips it back to `ACTIVE` immediately before
      the forced re-crawl — desired).
   2. `CrawlShare` (unchanged).
   3. `MarkShareCrawled` → `COMPLETED`.

   This makes `crawl` mode leave a properly registered, completed share, where
   today it leaves files/checkpoint with no share row. That is a deliberate,
   minor improvement, not a regression: previously such a share was invisible
   to the scheduler and admin API.

6. **`run-scheduler-once` / daemon**: **no change.** They go through
   `scheduler.RunOnce`, which calls `MarkShareCrawled` → `COMPLETED`
   automatically.

## What does not change

- `POST /shares` (add share) → `ACTIVE`.
- `UpdateShareMeta` (re-insert path) → `ACTIVE`.
- Failure/dead/duplicate/shelved transitions.
- `backfill-share-meta`, `validate-share-counts` (independent of status).
- Export format and the `DEAD`/`DUPLICATE` prune set.

## Tests

- `internal/store`: `MarkShareCrawled` writes `status='COMPLETED'` (and still
  clears failure bookkeeping / bumps version). Existing test asserting
  `ACTIVE` after crawl updates to `COMPLETED`.
- `internal/store`: `ListSharesForCrawl` does **not** return a `COMPLETED`
  share (new case).
- `internal/store`: `ReactivateShare` on a `COMPLETED` share resets it to
  `ACTIVE` with `retry_after_unix=0` (new case).
- `internal/scheduler`: success-path assertion changes from `ACTIVE` to
  `COMPLETED`; a re-run of `RunOnce` does not re-list the completed share.
- `cmd/115-indexer` (`crawl` mode): after a successful crawl the share row
  exists with `status='COMPLETED'`.

## Migration

No script. Existing shares sitting in `ACTIVE` that were already fully crawled
self-migrate on the first scheduler sweep after deploy: `CrawlShare` loads the
final checkpoint (root visited), the BFS loop `continue`s with zero `ListPage`
calls, it writes the final checkpoint and returns `nil`, and `MarkShareCrawled`
flips them to `COMPLETED`. Zero 115 network traffic, one checkpoint rewrite per
share. Shares still mid-crawl (interrupted) finish normally first, then
complete.

## Documentation

Update `README.md` near the modes / duplicate-shares section: a successfully
crawled share enters terminal `COMPLETED` and is not re-crawled; `POST
/shares/<code>/reactivate` forces a re-crawl.
