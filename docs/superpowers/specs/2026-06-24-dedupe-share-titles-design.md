# index115 Dedupe Share Titles — Design

Date: 2026-06-24
Repo: `five` (indexer). See [[index115-two-project-layout]].

## Goal

Some indexed shares share an identical `share_title` (e.g. eight shares titled `原盘精选`). The consumer (PowerList SPA) browses the exported `share` table by title, so duplicates make those shares unopenable. Rename duplicates to be globally unique — `原盘精选`, `原盘精选1`, `原盘精选2`, … — both as a one-shot CLI command (for the existing backlog) **and** automatically in the running daemon (so newly added shares are covered). Make renamed titles survive re-crawl.

## Context / facts (verified)

- `share_title` lives only in the `share` table (sqlite). bleve does **not** index it; the consumer reads the exported trimmed `share` table directly. So fixing `share_title` is sufficient — no `rebuild-index`.
- All maintenance ops are CLI modes (`-mode import-groups`, `-mode backfill-share-meta`, …). New mode fits the pattern.
- **Durability hazard:** `CrawlShare` resets `metaPersisted=false` each run (crawler.go:56) and unconditionally overwrites `share_title` from 115 on the first page (crawler.go:135). The scheduler re-crawls ACTIVE shares over time (ListSharesForCrawl), so a manual rename is clobbered on the next re-crawl unless the crawler is gated. A new share's first crawl also creates its title here — the point where a new collision can be introduced.
- Scheduler passes title-populated shares into `CrawlShare`: `ListSharesForCrawl` (selects `share_title`) → `CrawlShare(ctx, share, now)` (scheduler.go:48,55). So a crawler gate on `share.ShareTitle == ""` reliably reflects DB state for scheduler crawls, while the `crawl` one-shot mode (empty title) still writes.
- The scheduler already loops `RunOnce` in both `daemon` and `run-scheduler-once`; its store view is the `Registry` interface (scheduler.go:17). A post-crawl dedup hook there catches every newly crawled share.

## Locked decisions

1. Durability via **crawler gate** (first-writer-wins on title), not a separate override column.
2. One reusable store op `DedupeShareTitles(ctx, dryRun) ([]model.ShareRename, error)` — pure planner + apply — shared by **both** the CLI command and the scheduler (DRY).
3. CLI surface = new `-mode dedupe-share-titles` + `-apply` flag (dry-run by default).
4. **Auto-trigger = daemon scheduler:** at the end of each `RunOnce`, the scheduler applies dedup (dryRun=false) and logs each rename. Catches newly added shares as they are crawled.
5. Ordering = insertion id ASC; lowest id in a duplicate group keeps the bare title, the rest get numeric suffixes.
6. Suffix = bare number appended (`原盘精选1`, no separator).
7. Operate on **all** duplicate groups at once (no per-title filter).
8. `share_title` only; `file_size`/`status`/`version` untouched by the rename.

## Indexer changes

### 1. Durability — crawler gate (crawler.go:135)

```go
// before
if !metaPersisted && page.ShareTitle != "" {
// after
if !metaPersisted && page.ShareTitle != "" && share.ShareTitle == "" {
```

Re-crawls preserve a set title; the `crawl` one-shot mode still writes (empty title). `file_size` still writes only on first page (constant per share). Store `UpdateShareMeta` is **unchanged** → backfill remains the force-refresh path; existing `TestSQLiteStoreUpdateShareMeta` stays green.

### 2. Model — new DTO (internal/model/model.go)

```go
// ShareRename is one planned/applied title rename from DedupeShareTitles.
type ShareRename struct {
    ShareCode string
    From      string
    To        string
}
```

### 3. Store — new `internal/store/dedupe.go`

Pure planner (unexported, in-package unit-tested) + two methods:

```go
// planShareRenames assigns globally-unique titles to duplicate groups.
// shares MUST be in id-ASC order (ListShares output is). Lowest id in each
// duplicate group keeps the bare title; the rest get <title><n> for n=1,2,3…,
// skipping any value already used by any share.
func planShareRenames(shares []model.Share) []model.ShareRename

// RenameShareTitle sets share_title for every row with share_code.
func (s *Store) RenameShareTitle(ctx context.Context, shareCode, newTitle string) error

// DedupeShareTitles plans title renames over all shares and, unless dryRun,
// applies them. Returns the planned/applied renames. Idempotent: re-running
// re-plans on current state, so a partial apply completes on the next run.
func (s *Store) DedupeShareTitles(ctx context.Context, dryRun bool) ([]model.ShareRename, error)
```

`DedupeShareTitles` = `ListShares` → `planShareRenames` → (if `!dryRun`) `RenameShareTitle` per entry. Planner algorithm:
- Group by **exact trimmed** title; skip empty/whitespace titles.
- For groups >1 (members in id-ASC order): index 0 keeps bare title (no entry); i≥1 get `<base><n>`, n from 1, skipping any candidate already in the global used-set (seeded with all current titles, updated as assignments are made) → global uniqueness.

### 4. CLI mode `dedupe-share-titles` (cmd/115-indexer/main.go switch)

New `-apply` flag (default false = dry-run). Handler:
```go
renames, err := s.DedupeShareTitles(ctx, !*apply)
// print "share <code>: "<from>" -> "<to>"" per rename,
// then "renamed N shares" (-apply) / "would rename N shares; re-run with -apply to commit" / "no duplicate titles found".
```
No new file needed — small handler inline in the switch, mirroring `validate-share-counts` / `import-groups`.

### 5. Auto-trigger — scheduler (internal/scheduler/scheduler.go)

Add to `Registry` interface:
```go
DedupeShareTitles(ctx context.Context, dryRun bool) ([]model.ShareRename, error)
```
At the end of `RunOnce` (after the crawl loop, before return), apply + log:
```go
renames, err := s.registry.DedupeShareTitles(ctx, false)
if err != nil {
    return proxyFailureOnly, err
}
for _, r := range renames {
    s.logger.Printf("event=share_title_deduped share=%s from=%q to=%q", r.ShareCode, r.From, r.To)
}
```
Runs every cycle (cheap in-memory group-by; writes only when collisions exist), so shares added via adminhttp/import and then crawled are deduped automatically. `daemon` and `run-scheduler-once` both inherit it.

## Edge cases & errors

- Exact-string grouping: `原盘精选` and `原盘精选1` are **distinct** groups (only exact matches collide). `原盘精选1` is only renumbered if it is itself duplicated.
- Suffix collision with an unrelated existing title (`<base>1` already taken by a different share) → auto-increment to `2`, `3`, … until free.
- Empty/whitespace titles → skipped from dedup (nothing meaningful to rename); not counted in the used-set meaningfully (never a suffix target).
- DEAD shares: included in planning (harmless; `ExportTrimmed` prunes them before shipping). Renaming them is a no-op for the consumer but kept for simplicity.
- A share whose computed target equals its current title (group's lowest id) → no rename entry.
- Scheduler dedup error propagates (a real DB problem), consistent with other Registry calls. Partial apply is safe — idempotent on re-run.

## Testing

- `planShareRenames` (store, in-package): single-member group → no entry; multi-member → lowest id bare, rest `1`,`2`,… in id order; global collision avoidance (`<base>1` pre-taken by an outsider → first rename becomes `2`); trimming before grouping; empty/whitespace titles skipped.
- Store `RenameShareTitle`: updates `share_title`, leaves `file_size`/`status`/`version`; no matching row → no error, 0 affected.
- Store `DedupeShareTitles`: dryRun plans without writing; apply writes exactly the planned renames and returns them; idempotent (second run returns empty).
- Crawler: a share with non-empty `ShareTitle` is **not** overwritten by the crawled page's title; a share with empty `ShareTitle` still writes once (regression — existing `TestCrawlerPersistsShareMetadataOncePerCrawl`).
- Scheduler: after a crawl run, colliding titles are deduped via the Registry and each rename is logged; the fake Registry implements `DedupeShareTitles`.
- CLI: dry-run prints the plan and writes nothing; `-apply` writes the planned renames (use a tmp sqlite via `store.Open`).

## Out of scope

- Per-title `-title` filter (whole-DB run only).
- Admin HTTP endpoint for live re-dedupe.
- Consumer (PowerList) changes — none.
- Undo / rename history.
- Transactional dedup apply (per-rename is idempotent; a tx adds little).
