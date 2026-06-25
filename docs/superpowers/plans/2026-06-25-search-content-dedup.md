# Search Content Dedup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse duplicate files in the Bleve search index — movies by content (merging differently-named copies, all names still searchable), episodes by name+content (each filename stays its own entry) — with zero PowerList change.

**Architecture:** A pure helper `planDocs(files) []indexedDoc` groups rows into the exact Bleve docs to emit (composite doc id + distinct names). `Rebuild` iterates its output instead of every row. Doc id stays `shareCode-fileId`; `searchDoc.Name` becomes `[]string` so merged movies can carry multiple names.

**Tech Stack:** Go 1.26, module `five`, bleve v2 (`github.com/blevesearch/bleve/v2`).

## Global Constraints

- `file` table PK `(share_code, file_id)` must not change; rows are never dropped (browse integrity).
- Bleve doc id MUST stay `shareCode-fileId` — PowerList resolves hits via `hit.ID` → `FilesBySearchIDs` → `parseCompositeFileID`.
- Bleve field name MUST stay `name` (consumer's field-agnostic `MatchQuery` and five's `NewNameQuery(SetField("name"))` rely on it).
- No PowerList changes; no coordinated release.
- `manifest.FileCount` stays `len(files)` (raw crawled rows; consumer result totals come from bleve `res.Total`).
- All existing tests in `internal/searchindex/` must keep passing (they use empty-`sha1` rows → passthrough → unaffected).

---

### Task 1: `planDocs` grouping helper (pure, TDD)

**Files:**
- Create: `internal/searchindex/dedupe.go`
- Test: `internal/searchindex/dedupe_test.go`

**Interfaces:**
- Consumes: `model.File` (fields `FileID, ShareCode, Name, Size, IsDir, SHA1`), and `docID(shareCode, fileID string) string` already defined in `indexer.go` (same package).
- Produces: `const emptyStringHash`, `const movieSizeThreshold`, `var episodeMarker`, `type indexedDoc`, `func isEpisodeFile(model.File) bool`, `func planDocs([]model.File) []indexedDoc`. Task 2 consumes `planDocs` and `indexedDoc{docID, names}`.

- [ ] **Step 1: Write the failing tests**

Create `internal/searchindex/dedupe_test.go`:

```go
package searchindex

import (
	"reflect"
	"testing"

	"five/internal/model"
)

func TestIsEpisodeFile(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	cases := []struct {
		label string
		f     model.File
		want  bool
	}{
		{"small no marker", model.File{Name: "movie.mkv", Size: 2 * gb}, true},
		{"small with marker", model.File{Name: "S01E09.mkv", Size: 2 * gb}, true},
		{"big no marker", model.File{Name: "Movie.2160p.mkv", Size: 40 * gb}, false},
		{"big S02E18", model.File{Name: "Series.S02E18.mkv", Size: 40 * gb}, true},
		{"big EP09", model.File{Name: "Show.EP09.mkv", Size: 40 * gb}, true},
		{"big 第18集", model.File{Name: "剧.第18集.mkv", Size: 40 * gb}, true},
		{"big bare E18", model.File{Name: "Show.E18.mkv", Size: 40 * gb}, true},
		{"big year 2018", model.File{Name: "Movie.2018.2160p.mkv", Size: 40 * gb}, false},
		{"big E2015 not episode", model.File{Name: "Something.E2015.mkv", Size: 40 * gb}, false},
	}
	for _, c := range cases {
		if got := isEpisodeFile(c.f); got != c.want {
			t.Errorf("%s: isEpisodeFile=%v, want %v", c.label, got, c.want)
		}
	}
}

func TestPlanDocsMovieMergesAcrossNames(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "9", ShareCode: "swz", Name: "Avatar.2009.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
		{FileID: "1", ShareCode: "swa", Name: "阿凡达.2009.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
	}
	got := planDocs(files)
	if len(got) != 1 {
		t.Fatalf("got %d docs, want 1 (movie merges across names)", len(got))
	}
	if got[0].docID != "swa-1" {
		t.Errorf("docID = %q, want swa-1 (lexicographically smallest)", got[0].docID)
	}
	want := []string{"Avatar.2009.2160p.mkv", "阿凡达.2009.2160p.mkv"}
	if !reflect.DeepEqual(got[0].names, want) {
		t.Errorf("names = %v, want %v (sorted)", got[0].names, want)
	}
}

func TestPlanDocsEpisodeBySizeKeepsNamesSeparate(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "1", ShareCode: "swa", Name: "Show - S01E09 - 第9集.mkv", SHA1: "AAA", Size: 2 * gb},
		{FileID: "2", ShareCode: "swb", Name: "Show.S01E09.mkv", SHA1: "AAA", Size: 2 * gb},
	}
	if got := planDocs(files); len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (episode keeps different names separate)", len(got))
	}
}

func TestPlanDocsEpisodeMarkerOverridesSize(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "1", ShareCode: "swa", Name: "Series.S02E18.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
		{FileID: "2", ShareCode: "swb", Name: "Series - S02E18.mkv", SHA1: "AAA", Size: 40 * gb},
	}
	if got := planDocs(files); len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (episode marker overrides size>threshold)", len(got))
	}
}

func TestPlanDocsSameNameAndContentCollapses(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "9", ShareCode: "swz", Name: "回到未来3.mkv", SHA1: "AAA", Size: 13 * gb},
		{FileID: "1", ShareCode: "swa", Name: "回到未来3.mkv", SHA1: "AAA", Size: 13 * gb},
	}
	got := planDocs(files)
	if len(got) != 1 {
		t.Fatalf("got %d docs, want 1 (same name+content collapses)", len(got))
	}
	if got[0].docID != "swa-1" {
		t.Errorf("docID = %q, want swa-1", got[0].docID)
	}
}

func TestPlanDocsPassthroughNeverMerged(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "1", ShareCode: "swa", Name: "folder", IsDir: true},
		{FileID: "2", ShareCode: "swb", Name: "folder", IsDir: true},
		{FileID: "3", ShareCode: "swc", Name: "a.bin", SHA1: "", Size: 5 * gb},
		{FileID: "4", ShareCode: "swd", Name: "a.bin", SHA1: "", Size: 5 * gb},
		{FileID: "5", ShareCode: "swe", Name: "empty", SHA1: emptyStringHash, Size: 0},
		{FileID: "6", ShareCode: "swf", Name: "empty", SHA1: emptyStringHash, Size: 0},
	}
	if got := planDocs(files); len(got) != 6 {
		t.Fatalf("got %d docs, want 6 (dirs/unhashed/sentinel never deduped)", len(got))
	}
}

func TestPlanDocsDeterministicAcrossInputOrder(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	mk := func(share, fid, name string) model.File {
		return model.File{FileID: fid, ShareCode: share, Name: name, SHA1: "AAA", Size: 40 * gb}
	}
	a := planDocs([]model.File{mk("swz", "9", "Avatar.mkv"), mk("swa", "1", "阿凡达.mkv")})
	b := planDocs([]model.File{mk("swa", "1", "阿凡达.mkv"), mk("swz", "9", "Avatar.mkv")})
	if len(a) != 1 || len(b) != 1 || a[0].docID != b[0].docID || !reflect.DeepEqual(a[0].names, b[0].names) {
		t.Fatalf("not deterministic across input order: a=%v b=%v", a, b)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/searchindex/ -run 'TestIsEpisodeFile|TestPlanDocs' -v`
Expected: FAIL — build error `undefined: isEpisodeFile` / `undefined: planDocs` / `undefined: emptyStringHash` (symbols not yet defined).

- [ ] **Step 3: Implement `dedupe.go`**

Create `internal/searchindex/dedupe.go`:

```go
package searchindex

import (
	"regexp"
	"sort"

	"five/internal/model"
)

// emptyStringHash is the SHA-1 of the empty string. 115 sometimes returns it as
// a placeholder for files it could not hash. Rows carrying it are never
// content-deduped — they only ever merge junk — so each is indexed on its own
// like an unhashed file.
const emptyStringHash = "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709"

// movieSizeThreshold is the size above which a file with no episode marker is
// treated as a movie / single-play unit (4K movies, concert ISOs): its
// differently-named copies merge into one search result. Files at or below this
// size are episodes (each filename kept separate) unless their name matches
// episodeMarker. The corpus is bimodal — ~54% of same-content groups are <=4GB
// (episodes), ~41% >15GB (concerts/movies) — so the value in the 4–15GB valley
// rarely matters; 10 GiB is a conservative split.
const movieSizeThreshold int64 = 10 * 1024 * 1024 * 1024

// episodeMarker matches filename patterns that unambiguously mark a TV episode.
// A match means the file is an episode regardless of size, so a large 4K episode
// is not merged as a movie. Bare "E" + digits uses a word boundary and 2–3
// digits to avoid matching years such as "E2015".
var episodeMarker = regexp.MustCompile(`(?i)(S\d{1,2}E\d{1,3})|(EP\d{1,3})|(第\d{1,3}[集话話])|(\bE\d{2,3}\b)`)

// indexedDoc is one bleve document Rebuild should emit.
type indexedDoc struct {
	docID string   // composite "shareCode-fileId" of the representative row
	names []string // distinct names to index for matching
}

// isEpisodeFile reports whether f should be deduped per-name (episode) rather
// than per-content (movie). Dirs and unhashed/sentinel rows are never deduped
// (handled by planDocs), so this only classifies real-hash files.
func isEpisodeFile(f model.File) bool {
	return episodeMarker.MatchString(f.Name) || f.Size <= movieSizeThreshold
}

// planDocs groups files into the bleve documents Rebuild should index.
//
// Real-hash files are grouped into episodes or movies. Episodes (isEpisodeFile)
// key on (name, sha1, size) so each distinct filename is its own doc. Movies key
// on (sha1, size) so differently-named copies merge into a single doc carrying
// every name (recall). Within a group the representative is the
// lexicographically smallest (share_code, file_id) row; its composite id is the
// doc id. Directories, unhashed files, and the empty-string-hash sentinel are
// passthrough — one doc each. Output is deterministic for a given input:
// passthrough rows in input order, then groups in first-seen order, names sorted.
func planDocs(files []model.File) []indexedDoc {
	type groupKey struct {
		name string // "" for movies (merge across names)
		sha1 string
		size int64
	}
	type group struct {
		rep   model.File
		names map[string]struct{}
	}
	groups := map[groupKey]*group{}
	var order []groupKey
	passthrough := make([]indexedDoc, 0)

	for _, f := range files {
		if f.IsDir || f.SHA1 == "" || f.SHA1 == emptyStringHash {
			passthrough = append(passthrough, indexedDoc{
				docID: docID(f.ShareCode, f.FileID),
				names: []string{f.Name},
			})
			continue
		}
		key := groupKey{sha1: f.SHA1, size: f.Size}
		if isEpisodeFile(f) {
			key.name = f.Name
		}
		g, ok := groups[key]
		if !ok {
			g = &group{rep: f, names: map[string]struct{}{}}
			groups[key] = g
			order = append(order, key)
		}
		g.names[f.Name] = struct{}{}
		if f.ShareCode < g.rep.ShareCode || (f.ShareCode == g.rep.ShareCode && f.FileID < g.rep.FileID) {
			g.rep = f
		}
	}

	out := make([]indexedDoc, 0, len(passthrough)+len(order))
	out = append(out, passthrough...)
	for _, key := range order {
		g := groups[key]
		names := make([]string, 0, len(g.names))
		for n := range g.names {
			names = append(names, n)
		}
		sort.Strings(names)
		out = append(out, indexedDoc{
			docID: docID(g.rep.ShareCode, g.rep.FileID),
			names: names,
		})
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/searchindex/ -run 'TestIsEpisodeFile|TestPlanDocs' -v`
Expected: PASS — all 7 tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/searchindex/dedupe.go internal/searchindex/dedupe_test.go
git commit -m "feat(searchindex): add planDocs content-dedup grouping helper"
```

---

### Task 2: Wire `planDocs` into `Rebuild` (integration, TDD)

**Files:**
- Modify: `internal/searchindex/indexer.go` (`searchDoc` struct + the `Rebuild` doc loop)
- Test: `internal/searchindex/indexer_test.go` (append two integration tests)

**Interfaces:**
- Consumes: `planDocs(files) []indexedDoc`, `indexedDoc{docID string; names []string}` from Task 1.
- Produces: `Rebuild` now indexes one doc per `planDocs` entry; `searchDoc.Name` is `[]string`.

- [ ] **Step 1: Write the failing integration tests**

Append to `internal/searchindex/indexer_test.go`:

```go
func TestRebuildMergesMovieAcrossNamesAndMatchesEither(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{FileID: "9", ShareCode: "swz", Name: "Avatar.2009.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
			{FileID: "1", ShareCode: "swa", Name: "阿凡达.2009.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
		},
	}
	manifest, err := builder.Rebuild(context.Background(), provider, 1, 1)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	index, err := bleve.Open(manifest.IndexPath)
	if err != nil {
		t.Fatalf("open bleve: %v", err)
	}
	defer index.Close()

	count, err := index.DocCount()
	if err != nil {
		t.Fatalf("doc count: %v", err)
	}
	if count != 1 {
		t.Fatalf("doc count = %d, want 1 (movie merged across names)", count)
	}
	for _, term := range []string{"Avatar", "阿凡达"} {
		req := bleve.NewSearchRequest(bleve.NewMatchQuery(term))
		res, err := index.Search(req)
		if err != nil {
			t.Fatalf("search %q: %v", term, err)
		}
		if res.Total != 1 {
			t.Errorf("search %q total = %d, want 1 (merged movie must match either name)", term, res.Total)
		}
	}
}

func TestRebuildKeepsDifferentlyNamedEpisodesSeparate(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{FileID: "1", ShareCode: "swa", Name: "Show - S01E09 - 第9集.mkv", SHA1: "AAA", Size: 2 * gb},
			{FileID: "2", ShareCode: "swb", Name: "Show.S01E09.mkv", SHA1: "AAA", Size: 2 * gb},
		},
	}
	manifest, err := builder.Rebuild(context.Background(), provider, 1, 1)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	index, err := bleve.Open(manifest.IndexPath)
	if err != nil {
		t.Fatalf("open bleve: %v", err)
	}
	defer index.Close()
	count, err := index.DocCount()
	if err != nil {
		t.Fatalf("doc count: %v", err)
	}
	if count != 2 {
		t.Fatalf("doc count = %d, want 2 (episodes with different names stay separate)", count)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/searchindex/ -run 'TestRebuildMergesMovieAcrossNamesAndMatchesEither|TestRebuildKeepsDifferentlyNamedEpisodesSeparate' -v`
Expected: FAIL — `doc count = 2, want 1` (Rebuild still indexes every row; dedup not wired in yet).

- [ ] **Step 3: Make `searchDoc.Name` multi-valued**

In `internal/searchindex/indexer.go`, change:

```go
type searchDoc struct {
	Name string `json:"name"`
}
```

to:

```go
type searchDoc struct {
	Name []string `json:"name"` // distinct names to match on (1 for episodes/dirs; many for merged movies)
}
```

(Field name stays `name`; bleve indexes a string slice as multiple values, so the mapping and all field-agnostic queries are unaffected.)

- [ ] **Step 4: Index `planDocs` output instead of every row**

In `internal/searchindex/indexer.go`, in `Rebuild`, change the doc-emission loop from:

```go
	for _, f := range files {
		doc := searchDoc{
			Name: f.Name,
		}
		if err := batch.Index(docID(f.ShareCode, f.FileID), doc); err != nil {
			index.Close()
			return model.IndexManifest{}, err
		}
		pending++
		if pending >= rebuildBatchSize {
			if err := flush(); err != nil {
				index.Close()
				return model.IndexManifest{}, err
			}
		}
	}
```

to:

```go
	// Dedup before indexing: planDocs collapses same-content copies into one
	// doc per movie (carrying every name) and one doc per episode filename.
	// Doc id stays the representative's "shareCode-fileId" so the consumer
	// (FilesBySearchIDs) resolves hits unchanged.
	for _, d := range planDocs(files) {
		doc := searchDoc{
			Name: d.names,
		}
		if err := batch.Index(d.docID, doc); err != nil {
			index.Close()
			return model.IndexManifest{}, err
		}
		pending++
		if pending >= rebuildBatchSize {
			if err := flush(); err != nil {
				index.Close()
				return model.IndexManifest{}, err
			}
		}
	}
```

Leave `FileCount: int64(len(files))` in the returned manifest unchanged.

- [ ] **Step 5: Run the new integration tests to verify they pass**

Run: `go test ./internal/searchindex/ -run 'TestRebuildMergesMovieAcrossNamesAndMatchesEither|TestRebuildKeepsDifferentlyNamedEpisodesSeparate' -v`
Expected: PASS.

- [ ] **Step 6: Run the full package suite + vet to confirm no regressions**

Run: `go test ./internal/searchindex/ -v && go vet ./internal/searchindex/`
Expected: PASS — all tests green, including the three pre-existing tests (`TestRebuildCreatesSearchableIndexAndManifest`, `TestRebuildFlushesBoundedBatchesAcrossRemainder`, `TestRebuildKeysDocsByShareCodeAndFileID`), which use empty-`sha1` rows (passthrough, unaffected).

- [ ] **Step 7: Commit**

```bash
git add internal/searchindex/indexer.go internal/searchindex/indexer_test.go
git commit -m "feat(searchindex): dedup search docs via planDocs in Rebuild"
```

---

## Self-Review (completed during planning)

**Spec coverage:** Spec's two-regime key → Task 1 `planDocs`/`isEpisodeFile`. Multi-name movie recall → Task 1 `indexedDoc.names` + Task 2 `searchDoc.Name []string`. Composite doc id preserved → Task 2 step 4 keeps `d.docID`. Passthrough (dirs/unhashed/sentinel) → Task 1. Representative MIN(share,file) → Task 1. `manifest.FileCount` unchanged → Task 2 step 4 note. Open questions (drift, `idx_file_sha1`, dead share-scoped search) are explicitly out of scope / follow-ups — no task needed.

**Placeholder scan:** none — every step has complete code and exact commands.

**Type consistency:** `indexedDoc{docID string, names []string}` (Task 1) matches usage `d.docID` / `d.names` (Task 2 step 4) and `searchDoc{Name: d.names}` (Task 2 step 3/4). `planDocs([]model.File) []indexedDoc` signature consistent across both tasks. `isEpisodeFile(model.File) bool` consistent. `docID(shareCode, fileID string) string` reused from existing `indexer.go`.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-25-search-content-dedup.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
