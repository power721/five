# Search Series Folder Rollup Design

## Goal

Searching a TV series should return the **series folder**, not dozens of episode
files. Movies still return as files. Episode-specific searches (e.g. `S02E18`)
must keep working — they resolve to the containing folder, which the user then
browses.

This is a search-result rollup. It is purely a search concern: browsing and
playlist playback walk the `file` tree by `parent_id` and are untouched.

## Context / Decisions (from brainstorming, 2026-06-25)

- **Rollup target: folder/leaf level.** Each "episode container" folder absorbs
  its **direct** child file names into its own bleve doc and suppresses those
  files. No tree walk, no series-root detection. Searching the series name hits
  the series root folder (a branch folder, indexed as its own doc); searching an
  episode code hits the season/leaf folder that absorbed it.
  - Trade-off accepted: for a 2-level `series/season/episodes` layout, a
    series-name search returns the root **plus** each season whose absorbed
    episode names contain the term (a few folders, not dozens of episodes).
- **Container threshold: ≥5 episode-like children.** A folder rolls up only with
  ≥5 marker children, or ≥5 files that are ≥60% small (≤10 GiB) with <2 large.
  Small 2–4 episode folders don't flood search, so they stay as individual files.
  vs a ≥2/≥3 floor this drops only ~800 marker containers and ~4,000 marker-less —
  all small, non-flooding.
- **Movie folder dedup: out of scope for v1.** A single-movie folder
  (`阿凡达3/` + `阿凡达3.mkv`) still returns both folder and file. Deferred.
- **All logic in `five` at index time, zero PowerList change** — same shape and
  constraint as the 2026-06-25 content-dedup design. Reuses its `searchDoc`,
  `docID(shareCode, fileID)`, `episodeMarker`, and `movieSizeThreshold`.

## Data (measured on live `data/index.db`, ~100k folders-referenced-as-parent)

| Folder type | Count | Rollup? |
|---|---|---|
| single movie (1 file, no marker) | 33,016 | no → file |
| episode container — by markers (≥5 marker children) | 14,349 | yes |
| episode container — marker-less (≥5 files, ≥60% small, <2 large) | ~14,400 | yes |
| movie collection (≥2 large, not container) | 5,486 | no → files |
| other / mixed | ~14,989 | no |

(~28,800 containers total roll up.)

Key findings driving the rules:

1. **Episode markers cover only ~half of containers.** ~14,300 containers
   qualify by ≥5 `S01E01`/`EP09`/`第N集` markers; ~14,400 by the marker-less rule
   (`01.mkv`..`20.mkv`). A count-only heuristic either misses the marker-less half
   or confuses them with movie collections (also multi-file). The classifier must
   combine **marker + size + count**.
2. **The ≥5 floor is cheap.** vs a ≥2/≥3 floor it drops only ~800 marker
   containers (2–4 episodes) and ~4,000 marker-less (3–4 files) — all small,
   non-flooding; their files stay visible.
3. **~94% of marker-series are 2-level** (`series/season/episodes`; ~5,900 series
   roots). This is what makes leaf-level rollup acceptable: the series root
   surfaces for series-name searches, seasons surface for episode-code searches.

## Design (entirely inside `five`; zero PowerList change)

### Episode-container classification (per folder, local, no tree walk)

For a folder `F` with its **direct** child files, `F` is an **episode container**
(roll up) iff:

```
markerChildren    = count of direct child files matching episodeMarker
fileChildren      = count of direct child files
smallChildren     = count of direct child files with 0 < size <= movieSizeThreshold
largeChildren     = count of direct child files with size >  movieSizeThreshold

isContainer = markerChildren >= 5
          OR (fileChildren >= 5 AND smallChildren >= 0.6*fileChildren AND largeChildren < 2)
```

- `episodeMarker` and `movieSizeThreshold` (10 GiB) are the existing constants
  from `dedupe.go`.
- The marker-less arm catches `01.mkv`..`20.mkv`-style series; the `largeChildren
  < 2` guard stops movie collections from rolling up.
- The **≥5 floor** (both arms) avoids rolling up small 2–4 episode folders — they
  don't flood search, so their files stay visible individually.
- Folders with no direct files (branch folders, e.g. a series root holding
  seasons) are never containers — they stay passthrough.

### Doc emission

For each folder, depending on classification:

- **Episode container `F`**: emit **one** doc, id = `docID(F.ShareCode,
  F.FileID)`, `Name` = `{F.Name}` ∪ {distinct child file **stems**}, **capped at
  256 names** (always keep `F.Name`, then up to 255 child stems by sort).
  **Suppress** every direct child file of `F` (they emit no doc of their own).
  `F`'s subfolder children are unaffected — each is classified independently.
- **Non-container folder**: passthrough — one doc with `Name = {F.Name}` only.
- Child files of non-container folders flow through the **existing content-dedup**
  (movies merge by `(sha1,size)`; episodes stay per-name).

### Indexed names: file stem, not full filename

Every indexed `name` uses the **stem** — the filename minus its extension — so
`S01E18.mkv` is indexed as `S01E18`. This applies **uniformly to all docs**
(container-absorbed child names, movie multi-name docs, episode docs, passthrough
files), not only the rollup. Directory names are indexed verbatim (a folder named
`2024合集` has no meaningful extension).

- `stem(f)`: if `!f.IsDir && f.Ext != ""` → `strings.TrimSuffix(f.Name, "."+f.Ext)`,
  else `f.Name`. `Ext` is already extracted by the crawler and stored on the row.
- The content-dedup **keys** are unchanged (still `(sha1,size)` / `(name,sha1,size)`
  on the full row) — only the text placed into the doc's `names` is stemmed.
- Effect: cleaner episode-code matches, and a search for an extension no longer
  returns every file of that format.

### Composition with existing content-dedup (`planDocs`)

`planDocs` is restructured to run in this order:

1. Partition rows into dirs and files. Build a per-share `parent_id → direct
   child files` map.
2. Classify every dir; collect the set of **suppressed** file rows = direct
   children of container dirs.
3. Emit one doc per **container dir** (dir id, capped names per above).
4. Emit one doc per **non-container dir** (passthrough, name only).
5. Run the existing movie/episode content-dedup over the **non-suppressed**
   real-hash files only; emit those docs.
   - Unhashed / empty-string-hash files that are not suppressed stay passthrough
     (one doc each), as today.

Cross-share: folders have no `sha1`, so container folders are not content-deduped
across shares (two shares of the same series = two folder docs — same as today,
not a regression). A file that is suppressed in one share (child of a container)
but a direct child of a non-container folder in another share is emitted in the
other share via content-dedup — two representations in two shares, acceptable.

### What does NOT change

- `searchDoc` schema (`Name []string`), doc id format (`shareCode-fileId`), the
  bleve `name` field.
- PowerList `search.go` / `store.go` / linking / browsing — **zero changes**; no
  coordinated release.
- The `file` table schema and row count (browse / playlist integrity).
- Export / publish pipeline.

### Observable behavior change

- Search a series name → the series root folder (1) [+ a few season folders whose
  absorbed names match]. Episodes no longer flood.
- Search an episode code → the leaf/season folder that absorbed it.
- Search a movie → the movie file (unchanged).
- `SearchRequest.total` counts rolled-up docs (fewer than raw rows).

## Open questions

1. **Name cap value (256).** Folder's own name is always kept; 255 child names by
   sort. Bounds the 18,054-file giant folder. Tunable.
2. **Mixed-bucket recall.** ~14,989 "other" folders may hide real series the
   conservative classifier misses; refine thresholds against real miss reports.
3. **Sentinel/junk in container classification.** The marker-less arm counts all
   direct child files by size; a folder of sentinel placeholder files could be
   misclassified. Low risk; revisit if seen.

## Testing (`internal/searchindex/dedupe_test.go` + `indexer_test.go`)

`planDocs` unit tests (no bleve):
- Marker container (≥5 marker children) with N episode files ⇒ **1** folder doc,
  `names` = {folder name} ∪ {episode **stems**} (sorted); child files suppressed.
- Marker-less container (≥5 small files, <2 large) ⇒ rolled up.
- Collection (≥2 large, no markers) ⇒ NOT rolled up; files emitted individually.
- Single-movie folder (1 file) ⇒ file emitted (existing dedup), folder passthrough.
- Non-container dir ⇒ passthrough, name only.
- **Stem**: `Show.S01E01.mkv` (Ext=`mkv`) is indexed as `Show.S01E01`; a dir row
  is indexed verbatim; applies to movie/episode/passthrough docs too.
- Name cap: folder with >255 child names ⇒ doc has ≤256 names, folder name kept.
- Suppressed files excluded from content-dedup (a movie whose only copy is a
  child of a container is not double-emitted).
- Existing content-dedup cases still pass (movies in non-container folders still
  merge by `(sha1,size)`).
- Deterministic output across input reorderings.

Rebuild integration tests (bleve):
- Series folder with episodes ⇒ search series name hits folder, not episodes.
- Search episode code ⇒ hits the folder that absorbed it.
- Existing `TestRebuild*` tests still pass (empty-`sha1` passthrough rows,
  unaffected).

## Out of scope

- Movie single-folder dedup (`阿凡达3/` + `阿凡达3.mkv` redundancy).
- Series-root / whole-subtree rollup (alternative "1 doc per series").
- Cross-share series dedup (folders have no content hash).
- Filtering junk/empty files from search.
- Any PowerList code change.
