# Search Content Dedup Design

## Goal

Collapse byte-identical files in search results. Today the same content shared
by multiple users (or repeated inside one share) is indexed once per copy, so a
search returns N identical hits. After this change, Bleve returns **one hit per
unique content**, with pagination that reflects unique contents, not copies.

## Context / Data (measured on the live `data/index.db`)

- 1,027,809 file rows (`is_dir=0`); **0 empty `sha1`** — every file has a hash.
- 639,424 distinct `sha1` ⇒ **~388k rows (38%) are content-duplicates**.
- Worst group: one file copied **330×**; m2ts clips at 202/199/171×. Duplicates
  are both cross-share and heavily intra-share (same trailer/nfo in every movie
  folder).
- Browsing is a per-share `parent_id` tree walk (`idx_file_share_parent`), so
  **file rows cannot be dropped** — every copy must stay. Dedup is purely a
  search-result concern.
- **Alias diversity is high**: of 161,691 multi-copy content groups, **45% have
  more than one distinct filename** (e.g. the same episode named
  `金粉世家.The.Story…S01E09…mp4` in one share and `金粉世家 - S01E09 - 第9集.mp4`
  in another). So dedup MUST index all distinct names, or ~half of duplicate
  groups lose recall.

### Why the key is `(sha1, size)`, not `sha1` alone

`sha1` is a content fingerprint: identical content ⇒ identical size, so for
*correct* hashes `size` is redundant and splits nothing. Across 639,424 sha1
groups, `(sha1, size)` splits **exactly one**:

```
DA39A3EE5E6B4B0D3255BFEF95601890AFD80709   sizes = {0, 2212128132}   31 rows
```

`DA39A3…` is the SHA-1 of the empty string. 30 rows are genuine size-0 empty
files; 1 row is a 2.2 GB file that 115 returned a **placeholder/corrupt hash**
for. `sha1`-alone would merge that real file with empty files (it vanishes from
search); `size` isolates it. (Known collision attacks like SHattered produce
same-size pairs, so `size` does not defend against deliberate collisions — only
against 115's occasional bad hash. Cost is zero, so include it.)

## Design (entirely inside `five`; zero PowerList change)

### Core idea

Index **one representative row per content group**, keeping that row's existing
composite doc id and the `name` field. Skip the other rows of each group. The
file table is untouched (browse integrity); only the search index shrinks.

This is consumer-neutral on purpose: PowerList's `search.go` resolves every hit
by `hit.ID` → `FilesBySearchIDs` → `parseCompositeFileID` (splits on first `-`).
Keeping the doc id as `shareCode-fileId` means that path works **unchanged**. (A
content-keyed id like `c:<sha1>:<size>` would have no `-`, fall into the legacy
bare-cid branch, match nothing, and silently blank all file results — rejected
for exactly this reason.)

### Dedup key & representative

- `key = (sha1, size)` for files with a real hash (`is_dir=0 AND sha1 <> ''`).
  Exclude the empty-string hash sentinel group from dedup (it only merges junk);
  those rows are indexed individually like unhashed files.
- For each key group, the **representative** = the row with the lexicographically
  smallest `(share_code, file_id)` pair. Deterministic and crawl-order
  independent, so re-indexing is stable.
- Directories (`is_dir=1`, no hash) and unhashed files: indexed one-doc-per-row
  as today (no dedup). Dir dedup by name is unsafe and out of scope.

### Bleve `Rebuild` change (`internal/searchindex/indexer.go`)

Currently `Rebuild` emits one doc per `(share_code, file_id)`. New flow:

1. First pass over `AllFiles`: bucket real-hash files by `(sha1, size)`,
  recording (a) the representative row and (b) the set of distinct names. Track
  every bucketed row's id in a `seen` set. Dirs and unhashed rows pass through
  untouched.
2. Second pass emits:
   - **one doc per bucket** — id = `docID(rep.ShareCode, rep.FileID)`
     (composite, unchanged format), `Name` = sorted distinct names of the group.
   - **one doc per dir / unhashed / sentinel row** — id = composite, `Name` =
     `[name]`.
3. Batch/flush as today (`rebuildBatchSize`).

Memory: the bucket map holds ~640k entries of small string sets — bounded; the
existing batch flush still caps Bleve-side memory.

### Bleve doc schema

```go
type searchDoc struct {
    Name []string `json:"name"` // all distinct names for this content; indexed for matching
}
```

- Field name stays `name` (was a single string, now a slice) so `NewNameQuery`
  (`SetField("name")`) and the consumer's field-agnostic `NewMatchQuery` both
  keep working with no query changes. bleve indexes string slices as multiple
  values of the field.
- `Name []string` preserves recall across the 45% of groups with alias names.
- The consumer never reads doc fields — it resolves `hit.ID` to a row and
  displays that row's name. So a hit matched via an alias shows the
  representative's name (cosmetic; same file). Accepted for v1.

### What does NOT change

- PowerList `search.go` / `store.go` / linking / browsing — **zero changes**.
  No coordinated release required; old consumer + new index work immediately.
- The `file` table schema and row count (browse integrity).
- The bleve doc-id format and the `name` field name.
- Export/publish pipeline shape (`index.db` + `bleve/` + `version.txt`).

### Observable behavior change

`SearchRequest` `total` now counts **unique contents**, not copies (e.g. a term
matching a 330-copy group returns `total` reflecting 1 content). This is the
desired dedup; clients that display result counts will show smaller numbers.

## Open questions

1. **Representative survival across the rebuild/trim drift.** Bleve is rebuilt
   in `rebuild-index` at T1; `index.db` is trimmed (DEAD shares pruned) at T2 in
   `export-db`. If the representative's share goes DEAD between T1 and T2, the
   doc's row is gone and the content disappears from search until the next
   rebuild (today each copy had its own doc, so other copies survived).
   Mitigation options: (a) rebuild bleve from the **trimmed** db at export so
   the representative always exists; (b) exclude `status='DEAD'` shares when
   picking the representative. Recommend (a) as a follow-up hardening — not a
   blocker, since DEAD is rare and a rebuild follows.
2. **`idx_file_sha1` in the shipped db** — ship it (cheap; enables a future
   "N sources" / dead-share fallback query in the consumer) or skip until
   needed? Lean: skip for v1 (consumer doesn't use it yet).
3. **Share-scoped search is already dead** — `buildSearchQuery` filters on a
   bleve `share_code` field that `five` never indexes, so `?share_code=` search
   returns empty today. Out of scope here; flagged for a separate fix if wanted
   (and cross-share dedup would need thought if ever revived).

## Testing (`internal/searchindex/indexer_test.go`)

- Two files, same `(sha1,size)`, different shares ⇒ **one** doc, id is the
  MIN-`(share,file)` composite, both names present in `Name`.
- Same content, two different names ⇒ both names indexed (alias recall).
- Representative stable across two rebuilds with reordered input.
- Directory rows ⇒ one doc each, composite id, not merged.
- Empty-string-hash sentinel group ⇒ NOT merged into one; one doc per row.
- Doc count = distinct real-hash `(sha1,size)` + dir rows + unhashed rows +
  sentinel rows.

## Out of scope

- Filtering junk/empty files from search.
- Directory dedup.
- Storage reduction by dropping file rows (impossible — breaks browse).
- Fixing the dead share-scoped search path.
- Any PowerList code change.
