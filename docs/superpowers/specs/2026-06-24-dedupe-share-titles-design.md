# index115 Dedupe Share Titles — Design

Date: 2026-06-24
Repo: `five` (indexer). See [[index115-two-project-layout]].

## Goal

Some indexed shares share an identical `share_title` (e.g. eight shares titled `原盘精选`). The consumer (PowerList SPA) browses the exported `share` table by title, so duplicates make those shares unopenable. Add a command that renames duplicates to be globally unique — `原盘精选`, `原盘精选1`, `原盘精选2`, … — and make those renames survive re-crawl.

## Context / facts (verified)

- `share_title` lives only in the `share` table (sqlite). bleve does **not** index it; the consumer reads the exported trimmed `share` table directly. So fixing `share_title` is sufficient — no `rebuild-index`.
- All maintenance ops are CLI modes (`-mode import-groups`, `-mode backfill-share-meta`, …). New mode fits the pattern.
- **Durability hazard:** `CrawlShare` resets `metaPersisted=false` each run (crawler.go:56) and unconditionally overwrites `share_title` from 115 on the first page (crawler.go:135). The scheduler re-crawls ACTIVE shares over time (ListSharesForCrawl), so a manual rename is clobbered on the next re-crawl unless the crawler is gated.
- Scheduler passes title-populated shares into `CrawlShare`: `ListSharesForCrawl` (selects `share_title`) → `CrawlShare(ctx, share, now)` (scheduler.go:48,55). So a crawler gate on `share.ShareTitle == ""` reliably reflects DB state for scheduler crawls, while the `crawl` one-shot mode (empty title) still writes.

## Locked decisions

1. Durability via **crawler gate** (first-writer-wins on title), not a separate override column.
2. Surface = new CLI mode `-mode dedupe-share-titles` (not an adminhttp endpoint).
3. Ordering = insertion id ASC; lowest id in a duplicate group keeps the bare title, the rest get numeric suffixes.
4. Suffix = bare number appended (`原盘精选1`, no separator).
5. Dry-run by default; `-apply` to commit.
6. Operate on **all** duplicate groups at once (no per-title filter); printout is reviewable.
7. `share_title` only; `file_size`/`status`/`version` untouched by the rename.

## Indexer changes

### 1. Durability — crawler gate (crawler.go:135)

```go
// before
if !metaPersisted && page.ShareTitle != "" {
// after
if !metaPersisted && page.ShareTitle != "" && share.ShareTitle == "" {
```

Re-crawls preserve a set title; the `crawl` one-shot mode still writes (empty title). `file_size` still writes only on first page (constant per share). Store `UpdateShareMeta` is **unchanged** → backfill remains the force-refresh path for correcting a wrong title; existing `TestSQLiteStoreUpdateShareMeta` stays green.

### 2. Store — new `RenameShareTitle` (internal/store/sqlite.go)

```go
func (s *Store) RenameShareTitle(ctx context.Context, shareCode, newTitle string) error {
    _, err := s.db.ExecContext(ctx, `UPDATE share SET share_title=? WHERE share_code=?`, newTitle, shareCode)
    return err
}
```

(All rows with that `share_code`; `share_code` is unique per the crawl model.)

### 3. CLI mode `dedupe-share-titles` (new `cmd/115-indexer/dedupe.go`, wired in main.go switch)

Mirrors `backfill.go` — pure planning function + thin mode handler. New flag `-apply` (default false = dry-run).

**Pure planner** (no DB, fully unit-testable):

```go
type shareRename struct { ShareCode, From, To string }
func planShareRenames(shares []model.Share) []shareRename
```

Algorithm:
- Input is `ListShares` output, already `ORDER BY id ASC`.
- Group by **exact** trimmed title.
- For each group with >1 member:
  - lowest id keeps the bare title (no entry);
  - remaining members, in id ASC order, get `<title><n>` for n=1,2,3…
  - before assigning `<title><n>`, **skip any value already used by any share** (built from all titles across the whole table, updated as assignments are made) → guarantees global uniqueness, never creates a new collision.
- Emit one `shareRename` per renamed member.

**Mode handler** (main.go):
- `s.ListShares(ctx)` → `planShareRenames`.
- Dry-run: print `share <code>: "<from>" -> "<to>"` per entry, then `would rename N of M shares; re-run with -apply to commit`.
- `-apply`: call `s.RenameShareTitle` per entry, then print `renamed N of M shares`.

## Edge cases & errors

- Exact-string grouping: `原盘精选` and `原盘精选1` are **distinct** groups (only exact matches collide). `原盘精选1` is only renumbered if it is itself duplicated.
- Suffix collision with an unrelated existing title (`<title>1` already taken by a different share) → auto-increment to `2`, `3`, … until free.
- Whitespace-only / empty titles → their own group(s); empty-title shares are skipped from rename (nothing meaningful to dedupe; left as-is).
- DEAD shares: included in planning (harmless; `ExportTrimmed` prunes them before shipping). Renaming them is a no-op for the consumer but kept for simplicity.
- A share whose computed target equals its current title (e.g. it is the group's lowest id) → no rename entry emitted.

## Testing

- `planShareRenames`: a single-member group → no entry; a multi-member group → lowest id bare, rest `1`,`2`,… in id order; global collision avoidance (`<title>1` pre-taken by an outsider → first rename becomes `2`); trimming of titles before grouping; empty/whitespace titles skipped.
- Store `RenameShareTitle`: updates `share_title`, leaves `file_size`/`status`/`version` intact; no row → affects 0 (no error).
- Crawler: a share passed with a non-empty `ShareTitle` is **not** overwritten by the crawled page's title; a share with empty `ShareTitle` still writes (regression). Update `crawler_test.go` if any existing case relied on overwrite.
- Mode handler: dry-run prints plan and writes nothing; `-apply` writes exactly the planned renames (use an in-memory fake store / tmp sqlite).

## Out of scope

- Per-title `-title` filter (whole-DB run only).
- Admin HTTP endpoint / live re-dedupe.
- Consumer (PowerList) changes — none.
- Undo / rename history.
