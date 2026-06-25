# Search Content Dedup Design

## Goal

Collapse duplicate files in search results — but only when they are truly the
same entry: **same name AND same content**. The use case is TV-series playback:
users play a series as a folder playlist where the filename IS the episode
identity, so differently-named copies of the same content must stay separate
(merging them would mix naming conventions inside a playlist and confuse episode
order/identity). Same-name same-content copies (the egregious 330× cases) still
collapse to one.

## Context / Data (measured on the live `data/index.db`)

- 1,028,371 real-hash file rows (`is_dir=0`, excluding the empty-string-hash
  sentinel); **0 empty `sha1`** — every file has a hash.
- Browsing is a per-share `parent_id` tree walk (`idx_file_share_parent`), so
  **file rows cannot be dropped** — every copy must stay. Dedup is purely a
  search-result concern; folder/playlist playback is untouched regardless.
- **Alias diversity is high**: of 161,691 multi-copy content groups, **45% have
  more than one distinct filename** (e.g. the same episode named
  `金粉世家.The.Story…S01E09…mp4` in one share and `金粉世家 - S01E09 - 第9集.mp4`
  in another). These must NOT merge.

### Dedup-key comparison

| key | docs | reduction vs raw | rows collapsed |
|---|---|---|---|
| none (raw) | 1,028,371 | — | — |
| `(sha1, size)` | 639,423 | 37.8% | 388,948 |
| **`(name, sha1, size)`** | **754,701** | **26.6%** | **273,670** |

`(name, sha1, size)` retains ~70% of the collapse while preserving the 115,278
alias-named copies the playlist use case needs. The egregious same-name cases
(330× BBQDDQ trailer, 200× m2ts clips, 回到未来3 across shares) all collapse to
one because they share a name.

### Why `size` stays in the key

`sha1` is a content fingerprint, so for *correct* hashes `size` is redundant:
once `name` is in the key, `(name, sha1)` and `(name, sha1, size)` are identical
across all real-hash rows. `size` is kept purely as a **corrupt-hash guard** —
115 occasionally returns a placeholder hash (the empty-string sha1
`DA39A3…`, seen on a 2.2 GB file mixed with genuine size-0 empties). `size`
isolates those at zero cost. The empty-string-hash sentinel group is also
excluded from dedup entirely (indexed one-doc-per-row) so junk never merges.

## Design (entirely inside `five`; zero PowerList change, zero bleve-schema change)

### Core idea

Index **one representative row per `(name, sha1, size)` group**, keeping that
row's existing composite doc id and single `name` field. Skip the other rows of
each group. The file table is untouched (browse integrity); only the search
index shrinks.

Because every row in a group shares the same name by definition, each emitted
doc has exactly one name — so `searchDoc` stays a single `Name string`, alias
copies become separate docs (preserving recall naturally), and there is no
"searched alias B, displayed name A" mismatch.

This is consumer-neutral on purpose: PowerList's `search.go` resolves every hit
by `hit.ID` → `FilesBySearchIDs` → `parseCompositeFileID` (splits on first `-`).
Keeping the doc id as `shareCode-fileId` means that path works **unchanged**. (A
content-keyed id like `c:<sha1>:<size>` would have no `-`, fall into the legacy
bare-cid branch, match nothing, and silently blank all file results — rejected.)

### Representative

Within each `(name, sha1, size)` group, the representative = the row with the
lexicographically smallest `(share_code, file_id)` pair. Deterministic and
crawl-order independent, so re-indexing is stable. Its name is the group's name.

### Bleve `Rebuild` change (`internal/searchindex/indexer.go`)

Currently `Rebuild` emits one doc per `(share_code, file_id)`. New flow:

1. First pass over `AllFiles`: bucket real-hash, non-sentinel files by
   `(name, sha1, size)`. For each bucket keep only the representative row (the
   MIN `(share_code, file_id)`); drop the rest. Dirs, unhashed files, and
   empty-string-hash sentinel rows pass through untouched.
2. Second pass emits one doc per surviving row — id =
   `docID(rep.ShareCode, rep.FileID)` (composite, unchanged), `Name = row.Name`.
3. Batch/flush as today (`rebuildBatchSize`).

Memory: the bucket map holds ~750k entries — bounded; the existing batch flush
still caps Bleve-side memory.

### Bleve doc schema — UNCHANGED

```go
type searchDoc struct {
    Name string `json:"name"`
}
```

No field-name change, no multi-valuing. `NewNameQuery` (`SetField("name")`) and
the consumer's field-agnostic `NewMatchQuery` keep working with no query changes.

### What does NOT change

- PowerList `search.go` / `store.go` / linking / browsing — **zero changes**.
  No coordinated release required; old consumer + new index work immediately.
- The `file` table schema and row count (browse / playlist integrity).
- The bleve doc-id format and the `name` field.
- Export/publish pipeline shape (`index.db` + `bleve/` + `version.txt`).
- Folder-based playlist playback (uses the file table, never bleve).

### Observable behavior change

`SearchRequest` `total` now counts unique `(name, content)` entries, not raw
copies. Desired: clients displaying result counts show smaller, de-duplicated
numbers while every distinct filename remains reachable.

## Open questions

1. **Representative survival across the rebuild/trim drift.** Bleve is rebuilt
   in `rebuild-index` at T1; `index.db` is trimmed (DEAD shares pruned) at T2 in
   `export-db`. If the representative's share goes DEAD between T1 and T2, the
   doc's row is gone and that `(name, content)` entry disappears from search
   until the next rebuild. Recommend rebuilding bleve from the **trimmed** db at
   export as follow-up hardening — not a blocker (DEAD is rare, rebuild follows).
2. **`idx_file_sha1` in the shipped db** — ship it (cheap; enables a future
   "N sources" / dead-share fallback query) or skip until needed? Lean: skip v1.
3. **Share-scoped search is already dead** — `buildSearchQuery` filters on a
   bleve `share_code` field that `five` never indexes, so `?share_code=` search
   returns empty today. Out of scope; flagged separately.
4. **Drop `size` from the key?** It never splits anything `(name, sha1)` wouldn't
   in real data; it's purely the corrupt-hash guard. Keep (zero cost) or drop.

## Testing (`internal/searchindex/indexer_test.go`)

- Two files, same name + same `(sha1,size)`, different shares ⇒ **one** doc, id
  is the MIN-`(share,file)` composite.
- Same content, **different names** ⇒ **two** docs (not merged) — alias entries
  preserved for playlists.
- Representative stable across two rebuilds with reordered input.
- Directory rows ⇒ one doc each, composite id, not merged.
- Empty-string-hash sentinel rows ⇒ NOT merged; one doc per row.
- Doc count = distinct real-hash `(name, sha1, size)` + dir rows + unhashed +
  sentinel rows.

## Out of scope

- Filtering junk/empty files from search.
- Directory dedup.
- Storage reduction by dropping file rows (impossible — breaks browse).
- Fixing the dead share-scoped search path.
- Any PowerList code change.
