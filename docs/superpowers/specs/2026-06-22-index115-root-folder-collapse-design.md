# Index115 Root-Folder Collapse Design

## Problem

Every 115 share today is a single root folder, and 115 auto-sets the share
title to that folder's name. So the consumer renders a redundant nesting
level: the share node (labeled `ShareTitle`) and the single root folder under
`parent_id="0"` have the same name, producing paths like
`/115分享索引/2025年电影/2025年电影/亡命之徒(2025)`.

## Root cause

The duplication appears in two consumer code paths, both because the share's
effective root is treated as `parent_id="0"` (which holds one folder named the
same as `ShareTitle`):

1. **Tree browse** — `service.Browse` wraps each share as `Path="/"+ShareTitle`
   (`ListShares`), then `ListChildren(parent_id="0")` returns the same-named
   root folder. A client concatenating node names gets the duplicate segment.
2. **Full/play path** — `store.resolveFullPath` walks the parent chain up to
   `parent_id="0"`, returning `/2025年电影/亡命之徒(2025)`; prepending
   `ShareTitle` duplicates the segment.

`search.go` is **not** affected: it returns the `path` column, which holds only
the immediate name (no ancestor chain), so no duplicate appears in search
results.

## Decision

Collapse the single root folder for any share that has exactly one root entry
(`parent_id="0"`) **and** that entry is a directory. Store that folder's
`file_id` as `share.root_folder_id`; the consumer treats it as the share's
effective root. `""` (default) means "no collapse" and preserves current
behavior for multi-root / single-file-root / zero-root shares.

Two design choices, locked:

- **Collapse rule**: any single root folder, regardless of whether its name
  equals `ShareTitle`. (In the common case name == title they coincide; in the
  rare renamed-share case the root folder name is dropped from the path —
  accepted for simpler, one-less-click navigation.)
- **Compute location**: indexer populates `root_folder_id` into the `share`
  table; it ships with the published snapshot. The consumer stays dumb (reads,
  does not recompute).

## Indexer changes (`five`)

### Schema

Add column to `share`:

```sql
root_folder_id TEXT NOT NULL DEFAULT ''
```

Via the existing `ensureColumns(ctx, "share", ...)` migration path
(`internal/store/sqlite.go`).

### Computation

A **one-shot backfill into the working `share` table** (not inline in export).
`export-db` then copies the populated value as part of its normal snapshot —
export stays a pure copy.

This mirrors the existing `backfill-share-meta` CLI mode in shape, but with a
crucial difference: `backfill-share-meta` hits the 115 share/snap API
(rate-limited, needs cookie + proxy, per-share delay). `root_folder_id` is
derived purely from the local `file` table, so the backfill is a single local
SQL `UPDATE` — instant, idempotent, re-runnable, no network.

**Performance:** the backfill does NOT scan the full directory set. It reads
only each share's root-level rows (`parent_id='0'`), served by the existing
`idx_file_share_parent(share_code, parent_id)` index. 94 shares ≈ 94 index
lookups — milliseconds. The total `is_dir=1` count (119305) is irrelevant
because every subquery filters `parent_id='0'`.

**Logic** — a single `UPDATE` over all shares:

```sql
UPDATE share
SET root_folder_id = COALESCE(
  (SELECT f.file_id
   FROM file f
   WHERE f.share_code = share.share_code
     AND f.parent_id = '0'
     AND f.is_dir = 1
     AND (SELECT COUNT(*) FROM file g
          WHERE g.share_code = share.share_code
            AND g.parent_id = '0') = 1
   LIMIT 1),
  '');
```

- Exactly one root-level row **and** it is a dir → `root_folder_id = its file_id`.
- Otherwise (zero, multiple, or a single file) → `root_folder_id = ''`.

**Wiring:**

- New store method `BackfillRootFolderIDs(ctx) (updated int, error)` runs the
  `UPDATE` on the working DB; `updated` = rows changed.
- New CLI mode `backfill-root-folder` calls it (parity with
  `backfill-share-meta` for explicit/ad-hoc re-runs).
- `export-db` calls `BackfillRootFolderIDs` on the working DB **before** the
  `VACUUM INTO` copy, as an idempotent safety net so a shipped zip is always
  populated even if the operator skipped the manual mode. (Harmless to run
  twice.)

## Consumer changes (`PowerList`, `internal/index115/`)

### Read it

- `shareMeta` struct gains `RootFolderID string`.
- `RefreshShares` SELECT adds `COALESCE(root_folder_id, '')`; scan it.

### Backward compatibility (old index without the column)

Old published zips lack the column; a bare `SELECT root_folder_id` would throw
"no such column" and break the whole index115 module. Mitigation:

- At `OpenStoreRuntime`, run `PRAGMA table_info(share)` once and cache a
  `hasRootFolderID bool` on the `Store`.
- `RefreshShares` includes `root_folder_id` in the SELECT only when
  `hasRootFolderID`; otherwise `RootFolderID` stays `""` (current behavior, no
  collapse). Old indexes degrade gracefully with zero breakage.

This mirrors the indexer's own `ensureColumns` column-detection pattern, on the
read side.

### Use it — Browse collapse (`service.go`)

After normalizing `parentID` (`""` / `"/"` → `"0"`): if `parentID == "0"` and
the share's `RootFolderID != ""`, set `parentID = RootFolderID` before
`ListChildren`. Drilling into children (parentID = some child id) is
unaffected.

### Use it — path-walk collapse (`store.go resolveFullPath`)

Add the root folder as a walk terminator: the loop condition gains
`&& parentID != rootFolderID` (looked up from `s.shares[item.ShareCode]`). The
returned path is relative to the root folder, so prepending `ShareTitle` no
longer duplicates the segment.

## Explicitly unchanged

- `search.go` — returns immediate-name `path` column; no duplicate; no change.
- `ListShares` — share node still `Path="/"+ShareTitle`; collapse happens at
  drill-in; no change.
- Link resolution — uses `file_id` (cid), path-independent; no change.
- `file` table and bleve index — untouched. `root_folder_id` is purely a
  `share`-table annotation.

## Edge cases / invariants

| Scenario | root_folder_id | Behavior |
|---|---|---|
| Multi-root share | `""` | falls back to `"0"`, identical to today |
| Single root, but a file (not dir) | `""` | no collapse (no folder to skip) |
| Single root dir, name ≠ ShareTitle | that file_id | collapses (root name drops from path) |
| Stale root_folder_id (deleted folder) | n/a | consumer reads a frozen snapshot; cannot happen |

## API surface

`root_folder_id` is **not** exposed in `/index115` API responses. The collapse
is internal to `Browse`/`Detail`; clients (incl. alist-tvbox via the API)
simply receive the already-collapsed children. No client change required.

## Testing

- **Indexer**: `BackfillRootFolderIDs` sets `root_folder_id` for a single-root-
  dir share; `""` for multi-root, single-root-file, and zero-root; idempotent on
  re-run; export-db populates the column in the shipped DB.
- **Consumer**:
  - Single-root share `Browse(parent_id="0")` → returns the root folder's
    children directly (not the folder itself).
  - Multi-root share `Browse` → unchanged (regression: existing tests pass).
  - Single-root share file `resolveFullPath` → path lacks the root-folder-name
    prefix.
  - Old index (column absent) → no collapse, `Browse`/`resolveFullPath` take
    the legacy path, no error.

## Cross-repo

Touches both `five` (export change + re-export zip) and `PowerList` (consumer
change). Sequence: indexer + re-export first, then consumer. Spec lives in
`five/docs/superpowers/` per the two-project layout.
