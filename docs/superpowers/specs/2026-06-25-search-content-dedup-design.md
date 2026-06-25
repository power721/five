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

### Why the key is `(sha1, size)`, not `sha1` alone

`sha1` is a content fingerprint: identical content ⇒ identical size, so for
*correct* hashes `size` is redundant and splits nothing. Across 639,424 sha1
groups, `(sha1, size)` splits **exactly one**:

```
DA39A3EE5E6B4B0D3255BFEF95601890AFD80709   sizes = {0, 2212128132}   31 rows
```

`DA39A3...` is the SHA-1 of the empty string. 30 rows are genuine size-0 empty
files; 1 row is a 2.2 GB file that 115 returned a **placeholder/corrupt hash**
for. `sha1`-alone would merge that real file with empty files (it vanishes from
search); `size` isolates it. (Known collision attacks like SHAttered produce
same-size pairs, so `size` does not defend against deliberate collisions — only
against 115's occasional bad hash. Cost is zero, so include it.)

## Design

### Dedup key

- `key = (sha1, size)` for files with a real hash (`is_dir=0 AND sha1 <> ''`).
- Directories (`is_dir=1`, no hash) and any unhashed file: **not deduped** — one
  Bleve doc per row, keyed by the existing composite id. Dir dedup by name is
  unsafe (different folders can share a name) and out of scope.

### Bleve `Rebuild` change (`internal/searchindex/indexer.go`)

Currently `Rebuild` emits one doc per `(share_code, file_id)`. New flow:

1. Bucket real-hash files into `map[key]{names, primarySrc}`. For each row in a
   bucket: add its `name` to the distinct-names set; update `primarySrc` per the
   primary-source rule below.
2. Emit:
   - **one doc per bucket** — id is a deterministic encoding of `(sha1, size)`
     (see invariants), fields `Names` + `Src`.
   - **one doc per dir / unhashed row** — id = composite `shareCode-fileId`
     (unchanged), fields `Names=[name]`, `Src = composite`.
3. Batch/flush as today (`rebuildBatchSize`).

Memory: the bucket map holds ~640k entries of small string sets — bounded; the
existing batch flush still caps Bleve-side memory.

### Bleve doc schema

```go
type searchDoc struct {
    Names []string `json:"names"` // distinct names for this content; indexed for matching
    Src   string   `json:"src"`   // primary source "shareCode-fileId"; stored, consumer parses it
}
```

- `Names []string` preserves recall: indexing **all distinct names** means a
  content reachable under an alias in one share still matches a query for that
  alias. Distinct-name count per content is almost always 1 (data confirms), so
  cost is negligible. Display name is read by the consumer from the resolved
  `index.db` row, not from the doc.
- `Src` replaces "parse the doc id": the consumer reads `Src` and runs
  `parseCompositeFileID(Src)` to open/download. Works for both file and dir
  docs.

### Doc-id invariants

- Content doc id = `c:<sha1>:<size>` (prefix `c:` guarantees no collision with
  composite dir ids, which start with a share code like `sw…`).
- The consumer no longer parses doc ids — it reads `Src`. The id is an opaque
  unique key for Bleve.

### Primary-source rule (deterministic, rebuild-stable)

Within a bucket, `primarySrc` is the composite id of the row with the
lexicographically smallest `(share_code, file_id)` pair (share_code compared
first, then file_id). Fully deterministic and independent of crawl order, so
re-indexing produces stable docs. All shipped shares are alive (DEAD pruned at export), so any choice
is reachable; the consumer can still surface alternates via `idx_file_sha1`.

### Shipped `index.db`: add `idx_file_sha1`

Add `CREATE INDEX IF NOT EXISTS idx_file_sha1 ON file(sha1);` in both the live
schema DDL and `ExportTrimmed`'s index-recreation block. Lets the consumer do
`SELECT … WHERE sha1=?` for "N sources" display and dead-share fallback without
a full scan. Cheap on ~1M rows.

### What does NOT change

- The `file` table schema and row count (browse integrity).
- Browsing / download paths (composite-keyed rows intact).
- Export/publish pipeline shape (`index.db` + `bleve/` + `version.txt`).

## Consumer change (PowerList — other repo, spec only)

- Bleve hit → read `Src` → `parseCompositeFileID(Src)` → resolve row (was: parse
  the doc id).
- Optional: `WHERE sha1=?` via `idx_file_sha1` to show "N sources" or fall back.
- Coordinate release: new index schema + updated PowerList ship together.

## Testing (`internal/searchindex/indexer_test.go`)

- Two files, same `(sha1,size)`, different shares ⇒ **one** doc, `Src` is the
  MIN pair, both names present in `Names`.
- Same content under two different names ⇒ both names indexed (recall).
- Primary source stable across two rebuilds with reordered input.
- Directory rows ⇒ one doc each, composite id, not merged.
- Unhashed/placeholder case (e.g. the empty-string hash `DA39A3…` across two
  sizes) ⇒ split by `size` into separate docs.
- Doc count = distinct real-hash `(sha1,size)` + dir rows + unhashed rows.

## Decisions on open questions

1. **Alias names** — index all distinct names (preserve recall). ✅
2. **Primary source** — deterministic `MIN(share_code, file_id)`. ✅
3. **Ship `idx_file_sha1`** — yes (cheap; enables fallback + "N sources"). ✅
4. **Filter empty/placeholder-hash files from the index** — no, out of scope;
   they dedup correctly under `(sha1,size)`. Left as a future option.
5. **Dedup directories** — no. ✅

## Out of scope

- Filtering junk/empty files from search.
- Directory dedup.
- Storage reduction by dropping file rows (impossible — breaks browse).
