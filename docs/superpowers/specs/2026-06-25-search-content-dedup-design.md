# Search Content Dedup Design

## Goal

Collapse duplicate files in search results with two regimes:
- **Movies / single-play units**: dedup by content alone — differently-named
  copies of the same movie merge into one result, carrying every name so any of
  them still matches.
- **TV episodes**: dedup by name AND content — differently-named copies stay
  separate, because users play series as folder playlists where the filename IS
  the episode identity and merging names would mix conventions.

A file is an **episode** if its name matches an episode pattern (`S02E18`,
`EP09`, `第18集`, bare `E09`) OR it is small (`size ≤ 10 GiB`). Otherwise
(large, no episode marker) it is a **movie**. The marker overrides size so a
big 4K episode is never merged as a movie. Same-name same-content copies (the
egregious 330× cases) collapse under both regimes.

## Context / Data (measured on the live `data/index.db`)

- 1,028,371 real-hash file rows (`is_dir=0`, excluding the empty-string-hash
  sentinel); **0 empty `sha1`**.
- Browsing is a per-share `parent_id` tree walk (`idx_file_share_parent`), so
  **file rows cannot be dropped** — every copy must stay. Dedup is purely a
  search-result concern; folder/playlist playback is untouched regardless.
- **Alias groups** (same content, >1 distinct name) split bimodally by size:
  >30 GB 26.7%, 15–30 GB 14.5%, 8–15 GB 1.8%, 4–8 GB 3.4%, **≤4 GB 53.6%**.
  So episodes dominate the small end, concerts/movies the large end.
- **Only 1.1% of >15 GB files look like episodes** (2,667 / 243,852); the
  episode-marker override reclassifies exactly these, so a size-only rule would
  mismerge almost none once the marker is honored.

### Why `size` stays in the content key

`sha1` is a content fingerprint, so for *correct* hashes `size` is redundant.
`size` is kept purely as a **corrupt-hash guard** — 115 occasionally returns a
placeholder hash (the empty-string sha1 `DA39A3…`, seen on a 2.2 GB file mixed
with genuine size-0 empties). The empty-string-hash sentinel group is excluded
from dedup entirely (indexed one-doc-per-row) so junk never merges.

## Design (entirely inside `five`; zero PowerList change)

### Two-regime dedup key

```
isEpisode = episodeMarker.MatchString(name) || size <= movieSizeThreshold
if !isEpisode:  key = (sha1, size)             // movie:  merge across names
else:           key = (name, sha1, size)        // episode: keep names separate
```

- `movieSizeThreshold = 10 GiB` (named constant, tunable).
- `episodeMarker` is a package-level regexp matching the common episode-naming
  patterns; a match means episode regardless of size:
  `(?i)(S\d{1,2}E\d{1,3})|(EP\d{1,3})|(第\d{1,3}[集话話])|(\bE\d{2,3}\b)`.
  The bare-`E` arm uses a word boundary and 2–3 digits to avoid matching years
  like `E2015`. Tunable as more naming variants surface.
- Movies merge all copies regardless of name into one doc; episodes keep each
  distinct name as its own doc. Directories, unhashed files (`sha1 == ""`),
  and the empty-string-hash sentinel are passthrough — one doc each, never
  merged.

### Movie recall — multi-name doc

A merged movie doc indexes **all distinct names** of its content, so a search by
any of them still hits. Episode docs index exactly their one name. Therefore the
bleve `name` field becomes multi-valued (`searchDoc.Name []string`); the field
NAME stays `name`, so the consumer's field-agnostic `MatchQuery` and five's
`NewNameQuery(SetField("name"))` keep working unchanged.

### Representative

Within each group, the representative = the row with the lexicographically
smallest `(share_code, file_id)` pair. Deterministic and crawl-order
independent. Its composite id is the bleve doc id; for movies its name is just
one of the indexed names (display comes from the resolved row anyway).

### Bleve `Rebuild` change (`internal/searchindex/indexer.go`)

A new pure helper `planDocs(files) []indexedDoc` (in
`internal/searchindex/dedupe.go`) does the grouping and returns exactly the docs
to emit — each a composite doc id + the distinct names to index. `Rebuild` then
iterates `planDocs(files)` instead of every row. `manifest.FileCount` stays
`len(files)` (raw crawled rows; the consumer's result total comes from bleve's
`res.Total`, which now reflects deduped docs).

### Why the doc id stays composite

PowerList's `search.go` resolves every hit by `hit.ID` → `FilesBySearchIDs` →
`parseCompositeFileID` (splits on first `-`). Keeping the doc id as
`shareCode-fileId` means that path works **unchanged**. (A content-keyed id
would have no `-`, fall into the legacy bare-cid branch, match nothing, and
silently blank all file results — rejected.)

### Bleve doc schema

```go
type searchDoc struct {
    Name []string `json:"name"` // distinct names to match on (1 for episodes/dirs; many for merged movies)
}
```

### What does NOT change

- PowerList `search.go` / `store.go` / linking / browsing — **zero changes**.
  No coordinated release required.
- The `file` table schema and row count (browse / playlist integrity).
- The bleve doc-id format and the `name` field name.
- Export/publish pipeline shape.

### Observable behavior change

`SearchRequest` `total` counts deduped entries, not raw copies; a merged movie is
one result findable by any of its names.

## Open questions

1. **Representative survival across rebuild/trim drift** — bleve is rebuilt at
   T1, `index.db` trimmed (DEAD shares pruned) at T2. If the rep's share dies
   in between, that entry vanishes from search until next rebuild. Recommend
   rebuilding bleve from the trimmed db at export as follow-up hardening.
2. **`idx_file_sha1` in the shipped db** — ship (enables future "N sources" /
   fallback) or skip until needed? Lean: skip v1.
3. **Share-scoped search is already dead** — `buildSearchQuery` filters on a
   bleve `share_code` field `five` never indexes. Out of scope; flagged.
4. **`episodeMarker` coverage** — initial set covers `S02E18` / `EP09` / `第18集`
   / bare `E09`. Extend if real episodes slip through (treat as movie) — these
   stay separate anyway if small, only large no-marker files risk a wrong merge.

## Testing (`internal/searchindex/dedupe_test.go` + `indexer_test.go`)

`planDocs` unit tests (no bleve):
- Movie (size>threshold, no marker), two different names same `(sha1,size)` ⇒
  **one** doc, `names` = both (sorted), doc id = MIN-`(share,file)`.
- Episode by size (size≤threshold), two different names same `(sha1,size)` ⇒
  **two** docs, one name each.
- Episode by marker (size>threshold but name has `S02E18`), two different names
  ⇒ **two** docs (marker overrides size).
- Episode, same name across shares ⇒ one doc.
- Dir / empty-sha1 / sentinel rows ⇒ passthrough, one doc each.
- Deterministic output across input reorderings.

Rebuild integration tests (bleve):
- Movie with two different names ⇒ 1 doc, and a search for *either* name hits.
- Episode with two different names ⇒ 2 docs.
- Existing tests (`TestRebuildCreatesSearchableIndexAndManifest`,
  `TestRebuildFlushesBoundedBatchesAcrossRemainder`,
  `TestRebuildKeysDocsByShareCodeAndFileID`) still pass — they use empty-`sha1`
  rows (passthrough), unaffected.

## Out of scope

- Filtering junk/empty files from search.
- Directory dedup.
- Storage reduction by dropping file rows (impossible — breaks browse).
- Fixing the dead share-scoped search path.
- Any PowerList code change.
