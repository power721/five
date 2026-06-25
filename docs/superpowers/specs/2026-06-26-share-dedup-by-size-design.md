# Share dedup by file size — design

Date: 2026-06-26
Status: approved (pending implementation)

## Problem

115 shares are frequently re-uploaded as duplicates. Two duplicate shares have
identical total `file_size`. Today both get fully crawled and indexed. When the
operator later deletes the duplicate via the admin API, a race leaves its files
behind as orphans: `scheduler.RunOnce` snapshots a batch of shares, the API
`DeleteShare` removes `file`/`crawl_checkpoint`/`share` rows mid-batch, and the
in-flight crawl finishes and writes the duplicate's `file` rows back after the
`share` row is gone (e.g. `sw62hpo3hhq`: 19,720 orphan file rows, no share row).

Two things are broken:
1. Duplicate shares are crawled at all — wasted work and the root cause of the
   orphan-producing deletes.
2. A delete that races with an in-flight crawl resurrects orphan rows.

## Goal

- Do not crawl shares that duplicate an already-indexed share (by total
  `file_size`).
- Clean already-indexed duplicates and existing orphan files.
- Stop the delete/crawl race from producing orphans for any share.

## Non-goals

- Content-hash dedup (byte-level). `file_size` equality above a threshold is the
  duplicate signal; good enough for large shares.
- Changing how consumers resolve a share link. The canonical share stays
  browsable; duplicates are hidden from the published index.

## Data model

- Add column `share.duplicate_of TEXT NOT NULL DEFAULT ''` via an idempotent
  `ALTER TABLE` migration (follow the existing migration pattern in
  `internal/store/sqlite.go`).
- New status `'DUPLICATE'`. `ListSharesForCrawl` already whitelists
  `ACTIVE/STALE/QUARANTINE`, so `DUPLICATE` shares are never scheduled.
- `ReactivateShare` also clears `duplicate_of` (so a misjudged duplicate can be
  recovered).

## 1. First-page dedup (prevent crawling duplicates)

A share's total `file_size` is only known on its first snap page
(`page.FileSize` from `ShareInfo`). Dedup happens there, before any file is
indexed — one page of wasted work per duplicate.

- Config: `crawler.Config.DedupeMinFileSize int64`. main.go flag
  `-dedupe-min-size` (default `1 << 30` = 1GiB) feeds the daemon crawler and the
  `dedupe-shares-by-size` mode.
- Store method `FindDuplicateShare(ctx, shareCode string, fileSize, minSize int64) (canonical string, ok bool, err)`:
  ```sql
  SELECT share_code FROM share
  WHERE file_size = ? AND file_size >= ? AND file_size > 0
    AND share_code <> ? AND status IN ('ACTIVE','STALE','QUARANTINE')
  ORDER BY COALESCE(last_crawled_at,0) ASC, id ASC LIMIT 1
  ```
  Returns the oldest other share with the same size, or `ok=false`.
- Crawler: after the first page is fetched and `page.FileSize` is known, before
  `UpsertFiles`/`UpdateShareMeta`, if `fileSize >= minSize` call
  `FindDuplicateShare`. On hit return `DuplicateShareError{Canonical}` and write
  nothing (no files, no meta). The share stays size-0 in the table; that is fine
  because the scheduler immediately marks it `DUPLICATE`.
- `DuplicateShareError` lives in package `crawler` (sentinel `ErrDuplicateShare`
  + `Canonical` field). The scheduler already imports `api115` for error
  sentinels; importing `crawler` for this type follows the same pattern and adds
  no import cycle.
- Scheduler `RunOnce`: after `CrawlShare`, if `errors.As(err, &dup)` →
  `MarkShareDuplicate(ctx, shareCode, canonical)` (sets `status='DUPLICATE'`,
  `duplicate_of=canonical`, `retry_after_unix=0`, `failure_count=0`). **No
  `RecordShareFailure`.** Log `result=duplicate`.

First-crawled wins as canonical: `RunOnce` crawls sequentially, so a duplicate
crawled later sees the earlier share's persisted size and aborts.

## 2. `dedupe-shares-by-size` mode (clean already-indexed duplicates)

One-time scan for duplicates that were both fully indexed before dedup existed.

- Store `DedupeSharesBySize(ctx, minSize int64, apply bool) ([]DedupeAction, error)`:
  select crawlable shares with `file_size >= minSize AND file_size > 0`; group by
  `file_size` in-process; for groups with >1 share keep the oldest one
  (`COALESCE(last_crawled_at,0) ASC, id ASC` — same rule as `FindDuplicateShare`)
  and produce a `DedupeAction{Loser, Canonical, FileCount}` for each other share.
  `apply=true` executes each action: `UPDATE` loser to `DUPLICATE` +
  `duplicate_of=canonical`, then `DELETE FROM file WHERE share_code=loser`.
  `apply=false` only returns the actions.
- CLI `-mode dedupe-shares-by-size [-apply] [-dedupe-min-size]`. Dry-run prints
  actions + totals; `-apply` executes. Mirrors `dedupe-share-titles`'s `-apply`.

## 3. `cleanup-orphans` mode (clean orphan files)

For files whose `share` row is already gone (the race symptom, e.g.
`sw62hpo3hhq`).

- Store `OrphanShares(ctx) ([]OrphanShare{ShareCode, FileCount}, error)`:
  `SELECT share_code, COUNT(*) FROM file WHERE share_code NOT IN (SELECT
  share_code FROM share) GROUP BY share_code`.
- Store `DeleteOrphans(ctx) (int64, error)` (transactional): delete `file` and
  `crawl_checkpoint` rows whose `share_code` is not in `share`; return total
  rows deleted.
- CLI `-mode cleanup-orphans [-apply]`: dry-run lists orphan shares + counts;
  `-apply` calls `DeleteOrphans` and prints the count.

## 4. Race hardening (stop delete/crawl resurrection for any share)

Even with dedup, an operator can delete a non-duplicate share mid-crawl.

- Store `PurgeIfOrphan(ctx, shareCode) (bool, error)`: if no `share` row exists,
  delete that shareCode's `file` + `crawl_checkpoint` rows; return whether it
  purged.
- Scheduler `RunOnce`: after each `CrawlShare` returns (success, failure, or
  duplicate), call `PurgeIfOrphan(share.ShareCode)`. If the share was deleted
  during the crawl, this removes whatever the crawl just resurrected. O(shares)
  cheap existence checks per cycle; purges only when a delete actually happened.
- This makes the orphan invariant self-healing for the live DB without a full
  per-cycle table scan.

## Export

`export-db` / `ExportTrimmed` prunes `status='DEAD'` today. Also prune
`DUPLICATE` (delete their files — expected 0 — then the share rows) so consumers
never see duplicates. Canonical shares are unaffected.

## Edge cases / semantics

- `file_size = 0` (never crawled) is never a duplicate signal — excluded by
  `file_size > 0` and the `>= minSize` gate.
- Below the 1GiB threshold: no dedup (small shares can collide on size).
- A duplicate detected on the first page writes nothing; re-crawl is prevented by
  the `DUPLICATE` status, so no repeated one-page fetches.
- `PurgeIfOrphan` after a duplicate-abort is a no-op (share row still exists, no
  files written).
- `DUPLICATE` shares keep their `share` row (for traceability via
  `duplicate_of`) but carry no files and are excluded from scheduling + export.

## Testing (TDD per unit)

- store: `FindDuplicateShare`, `MarkDuplicate`/`ReactivateShare` clears
  `duplicate_of`, `DedupeSharesBySize` (dry-run + apply), `OrphanShares`,
  `DeleteOrphans`, `PurgeIfOrphan` (purges only when share gone).
- crawler: first page with a size-matching existing share → `DuplicateShareError`,
  no files/meta written; below threshold or no match → crawls normally.
- scheduler: `DuplicateShareError` → `MarkShareDuplicate`, no failure recorded;
  `PurgeIfOrphan` called after each crawl.
- cmd: new modes wire through (build + light tests); migration is idempotent.

## Open items

None — threshold (1GiB), race hardening (included), and export exclusion
(DUPLICATE pruned) are all confirmed.
