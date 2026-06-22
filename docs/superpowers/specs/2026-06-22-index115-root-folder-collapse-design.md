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
(`parent_id="0"`) **and** that entry is a directory. The consumer **derives**
that folder's `file_id` at runtime (in `RefreshShares`) and treats it as the
share's effective root. `""` (no effective root) preserves current behavior for
multi-root / single-file-root / zero-root shares.

Two design choices, locked:

- **Collapse rule**: any single root folder, regardless of whether its name
  equals `ShareTitle`. (In the common case name == title they coincide; in the
  rare renamed-share case the root folder name is dropped from the path —
  accepted for simpler, one-less-click navigation.)
- **Compute location: consumer derives** — no schema change, no shipped-column.
  See "Why not store in `share`" below.

## Why not store `root_folder_id` in the `share` table

An earlier draft of this design stored `root_folder_id` in the `share` table
(backfilled by the indexer, shipped in the export). That was rejected during
implementation: the `five` repo has a committed guard test from the `init`
commit — `TestSQLiteShareSchemaDoesNotKeepRootFolderOrMountPath` — that
explicitly forbids `root_folder_id` (and `mount_path`) in the `share` table.
The v4.3 stability draft proposed them; the implementation deliberately kept
them out because the root is already implicit (`file.parent_id='0'`) and
storing it is redundant denormalization. Consumer-derives respects that
invariant, needs no indexer change, no migration, no re-export, and works on
the **already-shipped** index immediately.

PowerList is the sole reader of the file table (alist-tvbox reaches it through
PowerList's `/index115` API), so the rule living in PowerList costs nothing in
DRY.

## Consumer changes (`PowerList`, `internal/index115/store.go`) — as built

### `shareMeta.RootFolderID`

New field on the (unexported) `shareMeta` struct. Computed, not persisted.

### `RefreshShares` derives the effective root

After loading share rows, one grouped query over root-level rows derives each
share's effective root. Index-served via `idx_file_share_parent`; reads only
`parent_id='0'` rows, never the full tree (~119k dirs are not scanned):

```sql
SELECT share_code,
       COUNT(*) AS n,
       SUM(is_dir) AS dirs,
       MAX(CASE WHEN is_dir = 1 THEN file_id END) AS dir_id
FROM file
WHERE parent_id = '0'
GROUP BY share_code
```

For each share: `n == 1 && dirs == 1` → `RootFolderID = dir_id`; else `""`.

### `ListChildren` collapses the share root

At the share root (`parentID == "0"`), if the share's `RootFolderID != ""`,
remap to it before querying — so browsing the share lists the root folder's
children directly, skipping the redundant folder. Drilling into children
(parentID = some child id) is unaffected.

### `resolveFullPath` terminates at the root folder

The parent-walk loop condition gains `&& parentID != rootFolderID`, so the
returned path is relative to the root folder and prepending `ShareTitle` no
longer duplicates the segment.

## Explicitly unchanged

- **`five` (indexer) — entirely.** No schema, no backfill, no export change,
  no re-export, no version bump. The guard test stays green.
- `search.go` — returns immediate-name `path` column; no duplicate; no change.
- `ListShares` — share node still `Path="/"+ShareTitle`; collapse happens at
  drill-in; no change.
- Link resolution — uses `file_id` (cid), path-independent; no change.
- `file` table and bleve index — untouched.
- `/index115` API responses — `root_folder_id` is not exposed; the collapse is
  internal to `Browse`/`Detail`. Clients (incl. alist-tvbox) simply receive
  already-collapsed children. No client change.

## Edge cases / invariants

| Scenario | RootFolderID | Behavior |
|---|---|---|
| Multi-root share | `""` | falls back to `"0"`, identical to today |
| Single root, but a file (not dir) | `""` | no collapse (no folder to skip) |
| Single root dir, name ≠ ShareTitle | that file_id | collapses (root name drops from path) |
| Empty file table (no rows) | `""` | derivation query returns nothing; no collapse |
| Stale root folder | n/a | derived fresh on every `RefreshShares` from the live file table |

## Testing (as built, all green)

- `TestRefreshSharesDerivesRootFolderID` — single-root dir → id; multi-root →
  `""`; single-root file → `""`.
- `TestListChildrenCollapsesSingleRootFolder` — `ListChildren("0")` returns the
  root folder's child, not the folder; `resolveFullPath` drops the root-folder
  prefix.
- `TestListChildrenNoCollapseWhenMultiRoot` — both root dirs returned.
- `TestStoreListChildrenUsesShareFallbackMetadata` — updated to exercise the
  collapsed path (still asserts share fallback metadata).
- All existing store/service/search/linker tests pass.

### Pre-existing, unrelated failures

`config_test.go`'s `TestNewRuntimeOpensConfiguredManifestIndex` and
`TestNewRuntimeFallsBackToBleveDirBaseForAbsoluteManifestIndexPath` fail
because `runtime.go`'s `NewSearcher` has its manifest-path logic commented out
(`loadReadyIndexPath`). Unrelated to this feature; previously masked by a
compile break in `service_test.go` (`stubStore` missing `FileWithFullPath`),
which this work fixed.
