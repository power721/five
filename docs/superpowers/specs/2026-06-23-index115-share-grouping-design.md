# index115 Share Grouping (Virtual Directories) — Design

Date: 2026-06-23
Repos: `five` (indexer) + `PowerList` (consumer). See [[index115-two-project-layout]].

## Goal

The consumer homepage lists every indexed share flat. With hundreds of shares this is unusable. Group shares (e.g. 600 Korean dramas) under a **virtual directory**; the homepage shows the group dir, entering it lists its member shares.

## Source of truth

`115_groups.txt` (user-authored, renamed from `full.txt`) is a **grouping overlay only** — independent of `115_shares.txt`, which remains the share source (crawl list) **unchanged**. `115_groups.txt` maps share codes to groups; it neither adds nor alters shares. Format:

```
# 欧美剧
美剧【冰与火之歌…】	https://115.com/s/swXXXX?password=8888&#
美剧【神探夏洛克…】	https://115.com/s/swYYYY?password=8888#

# 纪录片
...
```

- `# <name>` lines = group headers.
- Each share line carries one share identifier in the *current* group. A leading title column is **optional**. The identifier may be any of:
  - bare code: `sw68wz93ncb`
  - token: `swnsdrk3h2m?password=p783`
  - URL: `https://115.com/s/swXXX?password=YYY` or `https://115cdn.com/s/swXXX?password=YYY`
  When a title is present (`美剧【…】\t<id>`), the identifier is the **last whitespace-separated field**; otherwise the whole line is the identifier.
- Only the share code is used; title and receive_code are ignored. `shares.ParseURL` is host-agnostic (115.com or 115cdn.com both yield `/s/<code>`); trailing `&#`/`#` fall into the URL fragment and do not affect parsing.

## Locked decisions

1. Grouping defined **indexer-side** in `115_groups.txt`; baked into `index.db`; consumer only renders.
2. `115_groups.txt` is a **separate grouping overlay**; `115_shares.txt` (share source) is **unchanged**.
3. Homepage is **mixed**: group virtual-dirs **plus** any loose (ungrouped) shares.
4. WebDAV group-drilling fix is **in scope**.
5. Title from `115_groups.txt` **ignored**.
6. A `share_code` under two groups → **last wins + log warn**.

## Navigation contract (the crux)

All three consumers thread `share_code` straight through to `index115.Service.Browse`:

| Consumer | How it picks `share_code` on drill-in |
|---|---|
| Storage driver `drivers/115_index/driver.go` `List` | splits obj id `share:recv:fid:sha1` (`utils.go:64`), parts[0] |
| JSON `/index115/browse` | SPA sends `share_code` query |
| WebDAV `server/index115_webdav.go` | matches root item by `Name`, then re-sends `current.ShareCode` |

Therefore a **group = a synthetic root `FileItem` with a reserved sentinel `share_code`**. No SPA, driver, or JSON-handler change.

Sentinel constraints:
- Real share codes start with `sw`. Sentinels use prefix `grp` + numeric group id, e.g. `grp1`.
- Obj id is colon-delimited, so the sentinel must be colon-free → `grp<N>` is safe.
- The group **name is display-only** (goes into `Name`/FileName, never parsed back) → Chinese/colons in names are fine.
- `Browse` resolves `grp<N>` → group via the `share_group` table.

## Data model

### Indexer (`five`) — `internal/store/sqlite.go` migrate

New table:

```sql
CREATE TABLE IF NOT EXISTS share_group (
    group_id   INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    sort_order INTEGER NOT NULL
);
```

New column on `share` (via `ensureColumns`):

```sql
group_id INTEGER  -- NULL = loose/ungrouped
```

Both survive `ExportTrimmed` (it drops only `crawl_checkpoint`, `index_event`, `index_manifest`, `kv`).

### Consumer (`PowerList`) — `internal/index115`

- `shareMeta` gains `GroupID int64` (0 = none).
- `ShareSummary` gains `GroupID`.
- In-memory `shareGroup map[int64]string` (group_id → name), loaded in `RefreshShares` from `share_group`.

## Indexer changes

### Parser — new `internal/shares/groups.go`; `shares.Parse` untouched

`115_shares.txt` parsing (`shares.Parse`) is unchanged. A separate parser reads the overlay:

```go
type Group struct { Name string; ShareCodes []string }
func ParseGroups(r io.Reader) ([]Group, error)
```

- `# <name>` line → start a new group (header order = output/sort order).
- Share line → take the **last whitespace-separated field** (or the whole line if there is only one) and extract the share code via a new `parseShareCode` helper that handles bare code, `code?password=…` token, and `http(s)://host/s/<code>` URL (any host). Append to the current group.
- A `share_code` appearing in two groups → last wins (removed from the earlier group); returned in `duplicates` for the caller to warn.

### Apply — new `import-groups` mode + `-groups-file` flag (`cmd/115-indexer/main.go`)

- New flag `-groups-file` (default `115_groups.txt`); new CLI mode `import-groups` (mirrors `import-shares`).
- Read + `ParseGroups`; upsert `share_group(group_id = 1..N by header order, name, sort_order)`; drop `share_group` rows no longer present.
- Assign membership by **share_code** only: `UPDATE share SET group_id = ? WHERE share_code = ?` (all rows with that code). Shares whose code is absent from the overlay get `group_id = NULL` (full re-apply reconciles). `share_title` and all other share columns untouched.
- `share_code` in the overlay but with no `share` row → dormant (nothing to update); log a warning.

Run order: `import-shares` (115_shares.txt) → crawl → `import-groups` (115_groups.txt) → `export-db`, so `group_id` ships in the trimmed index. No change to `model.Share`, `shares.Parse`, or the admin upload path.

## Consumer changes

### `store.go` — `RefreshShares`

- Load `share_group` into `s.shareGroup map[int64]string`.
- `share` SELECT adds `COALESCE(group_id, 0)` → `shareMeta.GroupID`.

### `service.go` — `Browse`

`ListShares()` already aggregates shares from the `file` table + `shareMeta`. Add `GroupID` to `ShareSummary` (from `shareMeta`). `Browse` then filters that one list — no new store method:

```
all, _ := s.store.ListShares(ctx)         // []ShareSummary, each now carries GroupID
switch {
case req.ShareCode == "":
    // homepage
    groups: for each share_group row (ORDER BY sort_order):
        append FileItem{ShareCode: "grp"+id, Name: name, IsDir:true, Path:"/"+name}
    loose: all where GroupID == 0 → existing share FileItems
    return groups ++ loose
case isGroupSentinel(req.ShareCode):   // ^grp[0-9]+$
    gid := atoi(trim "grp")
    return all where GroupID == gid → share FileItems (real sw codes, IsDir)
default:
    existing share-children behavior
}
```

`StoreReader` interface unchanged (still just `ListShares`).

### `server/index115_webdav.go` — `resolve` drilling fix

Today `current.ShareCode` is set once (from the root item matched by `Name`) and never updated, which is correct because every deeper part lives inside that one share. The group level breaks this: `/欧美剧/<memberShare>/...` crosses from the group sentinel into a *different* (member) share.

Fix: after a successful `parts[idx]` match, if `match.ShareCode != "" && match.ShareCode != current.ShareCode`, set `current.ShareCode = match.ShareCode` (and treat the member as a fresh share root: `parentID = "0"` for its first descent). This is the only webdav change; root listing already works because `Browse("")` now returns group nodes.

JSON handler (`server/handles/index115.go`) and driver: **no change** — they pass `share_code` through.

## Edge cases & errors

- Duplicate `share_code` across groups → last wins, warn. (decision 6)
- Empty group (header, no shares) → still rendered as an empty dir. (acceptable; cheap to skip later)
- Share in DB but not in `115_groups.txt` (e.g. legacy/admin-added) → `group_id NULL` → appears loose on homepage. `import-groups` does not delete shares.
- `share_code` in `115_groups.txt` but not in the `share` table → dormant; warned, no effect until that share is imported and `import-groups` re-runs.
- `share_code` not matching `grp\d+$` and not a real share → existing "not found" path.
- `grp<N>` with no matching `share_group` row → treat as empty / not-found.
- Re-ordering groups between imports changes `group_id`; sentinels are transient (recomputed each export), so no cross-version persistence needed.

## Testing

- `shares.ParseGroups`: header + share lines in all identifier forms (bare code, `code?password=` token, `115.com` and `115cdn.com` URLs, with and without title column), duplicate-in-two-groups (last wins), trailing `&#`/`#` URLs; `shares.Parse` regression (115_shares.txt still parses).
- Store migrate: `share_group` + `share.group_id` created; `ExportTrimmed` retains both (assert tables present in trimmed output).
- `import-groups` apply: upserts `share_group`, sets `share.group_id` by share_code, NULLs removed members, warns on absent codes.
- Consumer `Browse`: homepage returns groups (ordered) + loose; `grp<N>` returns only that group's members; real share unchanged; `RefreshShares` populates `shareGroup`.
- Webdav `resolve`: `/`, `/欧美剧`, `/欧美剧/<member>`, `/欧美剧/<member>/<file>` all resolve correctly (the last is the regression the fix addresses).

## Out of scope

- Nested groups (single level only).
- Search scoping by group (search stays global / by share_code as today).
- SPA UI changes.
- tvbox feed group rendering (consumes the same `Browse`; verify in plan, no change expected).
