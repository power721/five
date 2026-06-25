# Search Series Folder Rollup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Roll episode files up into their containing folder's bleve doc at index time so a series search returns the folder (not dozens of episodes), while episode codes still hit via merged names; movies stay as files.

**Architecture:** All logic lives in `internal/searchindex/dedupe.go`'s `planDocs`, which already groups files into bleve docs. Add three pure helpers — `stem` (strip file extension), `isContainer` (classify a folder as an episode container), `folderNames` (capped name set for a rolled-up folder) — then restructure `planDocs` to emit one doc per container folder (absorbing + suppressing its direct child files) and apply `stem` to every indexed name. `indexer.go`/`Rebuild` is unchanged (it already calls `planDocs`). Zero PowerList change.

**Tech Stack:** Go 1.x, bleve v2, existing `five/internal/model.File` and `five/internal/searchindex` package. Tests are plain `testing` (no testify).

## Global Constraints

- **All logic in `five` at index time; zero PowerList/SPA change.** No coordinated release.
- `searchDoc` schema (`Name []string`), doc id format `shareCode-fileId`, and the bleve field name `name` are **unchanged**.
- The `file` table schema and row count are **unchanged** (browse/playlist integrity).
- Reuse existing constants in `dedupe.go`: `episodeMarker`, `movieSizeThreshold` (10 GiB), `emptyStringHash`. Do not redefine.
- Container threshold: a folder is an episode container iff its direct child files have **≥5 marker children**, **or** **≥5 files** that are **≥60% small (≤10 GiB, size>0)** with **<2 large**.
- Indexed names use the **file stem** (extension stripped via the `Ext` field); directory names are indexed verbatim.
- Folder doc name cap: **256** total; the folder's own name is always kept.
- Reuse existing `isEpisodeFile` (unchanged) for the movie/episode content-dedup key; content-dedup **keys** stay on the full row `(sha1,size)` / `(name,sha1,size)` — only the indexed text is stemmed.
- Commits follow the repo's Conventional Commits style (`feat(searchindex):`, `test(searchindex):`).

---

## File Structure

- **Modify** `internal/searchindex/dedupe.go` — add `strings` import, `maxFolderNames` const, `stem`/`isContainer`/`folderNames` helpers, and restructure `planDocs`. `isEpisodeFile` and the constants are unchanged.
- **Modify** `internal/searchindex/dedupe_test.go` — add `TestStem`, `TestIsContainer`, `TestFolderNames`, and the rollup `TestPlanDocs*` cases.
- **Modify** `internal/searchindex/indexer_test.go` — add two `Rebuild` integration tests.
- `internal/searchindex/indexer.go` — **no changes** (`Rebuild` already iterates `planDocs`).

---

### Task 1: `stem` helper

**Files:**
- Modify: `internal/searchindex/dedupe.go` (add `"strings"` import; add `stem` func)
- Test: `internal/searchindex/dedupe_test.go` (add `TestStem`)

**Interfaces:**
- Consumes: `model.File` (`IsDir`, `Ext`, `Name` fields).
- Produces: `func stem(f model.File) string` — the name to index (filename minus extension for files; verbatim for dirs).

- [ ] **Step 1: Write the failing test**

Append to `internal/searchindex/dedupe_test.go`:

```go
func TestStem(t *testing.T) {
	cases := []struct {
		label string
		f     model.File
		want  string
	}{
		{"file strips ext", model.File{Name: "S01E18.mkv", Ext: "mkv"}, "S01E18"},
		{"file keeps dotpack", model.File{Name: "Show.S01E01.1080p.mkv", Ext: "mkv"}, "Show.S01E01.1080p"},
		{"file no ext unchanged", model.File{Name: "README"}, "README"},
		{"file empty ext unchanged", model.File{Name: "movie.mkv", Ext: ""}, "movie.mkv"},
		{"dir verbatim", model.File{Name: "2024合集", IsDir: true}, "2024合集"},
		{"dir with dot verbatim", model.File{Name: "v2.0", IsDir: true, Ext: "0"}, "v2.0"},
	}
	for _, c := range cases {
		if got := stem(c.f); got != c.want {
			t.Errorf("%s: stem=%q, want %q", c.label, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/searchindex/ -run TestStem -v`
Expected: FAIL / compile error (`stem undefined`).

- [ ] **Step 3: Write minimal implementation**

In `internal/searchindex/dedupe.go`, change the import block to add `"strings"`:

```go
import (
	"regexp"
	"sort"
	"strings"

	"five/internal/model"
)
```

Add below the `episodeMarker` var (before `indexedDoc`):

```go
// stem returns the name to index for a row: the filename minus its extension
// (S01E18.mkv -> S01E18), so episode codes match cleanly and a search for an
// extension does not surface every file of that format. Directory names are
// returned verbatim (a folder like "2024合集" has no meaningful extension).
// Ext is extracted by the crawler and stored on the row; when absent the full
// name is indexed unchanged.
func stem(f model.File) string {
	if !f.IsDir && f.Ext != "" {
		return strings.TrimSuffix(f.Name, "."+f.Ext)
	}
	return f.Name
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/searchindex/ -run TestStem -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/searchindex/dedupe.go internal/searchindex/dedupe_test.go
git commit -m "feat(searchindex): add stem helper to strip file extensions from indexed names"
```

---

### Task 2: `isContainer` classifier

**Files:**
- Modify: `internal/searchindex/dedupe.go` (add `isContainer` func)
- Test: `internal/searchindex/dedupe_test.go` (add `TestIsContainer`)

**Interfaces:**
- Consumes: `episodeMarker`, `movieSizeThreshold` (existing in `dedupe.go`); `[]model.File` (the folder's direct child files).
- Produces: `func isContainer(kids []model.File) bool`.

- [ ] **Step 1: Write the failing test**

Append to `dedupe_test.go`:

```go
func TestIsContainer(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	mk := func(name string, size int64) model.File { return model.File{Name: name, Size: size} }
	// 5 marker episodes
	marker5 := []model.File{mk("S01E01.mkv", 2*gb), mk("S01E02.mkv", 2*gb), mk("S01E03.mkv", 2*gb), mk("S01E04.mkv", 2*gb), mk("S01E05.mkv", 2*gb)}
	// 5 small no-marker files (>=60% small)
	small5 := []model.File{mk("01.mkv", 1*gb), mk("02.mkv", 1*gb), mk("03.mkv", 1*gb), mk("04.mkv", 1*gb), mk("05.mkv", 1*gb)}
	// 4 markers (below floor)
	marker4 := marker5[:4]
	// 3 large movies (collection)
	large3 := []model.File{mk("A.2160p.mkv", 40*gb), mk("B.2160p.mkv", 40*gb), mk("C.2160p.mkv", 40*gb)}
	// mixed: 5 files but 3 large, 2 small (<60% small, 2 large)
	mixed := []model.File{mk("x.mkv", 1*gb), mk("y.mkv", 1*gb), mk("a.2160p.mkv", 40*gb), mk("b.2160p.mkv", 40*gb), mk("c.2160p.mkv", 40*gb)}
	cases := []struct {
		label string
		kids  []model.File
		want  bool
	}{
		{"5 markers", marker5, true},
		{"5 small no markers", small5, true},
		{"4 markers below floor", marker4, false},
		{"empty", nil, false},
		{"3 large collection", large3, false},
		{"5 files but 3 large", mixed, false},
	}
	for _, c := range cases {
		if got := isContainer(c.kids); got != c.want {
			t.Errorf("%s: isContainer=%v, want %v", c.label, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/searchindex/ -run TestIsContainer -v`
Expected: FAIL / compile error (`isContainer undefined`).

- [ ] **Step 3: Write minimal implementation**

Add to `dedupe.go` (after `isEpisodeFile`):

```go
// isContainer reports whether a folder should roll its direct child files up
// into its own search doc — i.e. it is an episode container. It needs ≥5
// episode-like children: ≥5 marker children (S01E01/EP09/第N集), or ≥5 files
// that are ≥60% small (0 < size ≤ movieSizeThreshold) with <2 large. Small 2–4
// episode folders don't flood search, so they stay as individual files. kids are
// the folder's DIRECT child files only; subfolders are classified independently.
// 0.6 is compared as small*5 >= files*3 to avoid floating point.
func isContainer(kids []model.File) bool {
	var markers, files, small, large int
	for _, k := range kids {
		files++
		if episodeMarker.MatchString(k.Name) {
			markers++
		}
		if k.Size > movieSizeThreshold {
			large++
		} else if k.Size > 0 {
			small++
		}
	}
	return markers >= 5 || (files >= 5 && small*5 >= files*3 && large < 2)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/searchindex/ -run TestIsContainer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/searchindex/dedupe.go internal/searchindex/dedupe_test.go
git commit -m "feat(searchindex): add isContainer classifier for episode-folder rollup"
```

---

### Task 3: `folderNames` builder (with cap)

**Files:**
- Modify: `internal/searchindex/dedupe.go` (add `maxFolderNames` const + `folderNames` func; uses `stem` from Task 1)
- Test: `internal/searchindex/dedupe_test.go` (add `TestFolderNames`)

**Interfaces:**
- Consumes: `stem` (Task 1); `model.File`.
- Produces: `const maxFolderNames = 256`; `func folderNames(d model.File, kids []model.File) []string`.

- [ ] **Step 1: Write the failing test**

Append to `dedupe_test.go`:

```go
func TestFolderNames(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	t.Run("folder_name_plus_distinct_child_stems_sorted", func(t *testing.T) {
		d := model.File{Name: "第一季", ShareCode: "sw1", FileID: "d1", IsDir: true}
		kids := []model.File{
			{FileID: "e3", ShareCode: "sw1", Name: "Show.S01E03.mkv", Ext: "mkv", Size: 2 * gb},
			{FileID: "e1", ShareCode: "sw1", Name: "Show.S01E01.mkv", Ext: "mkv", Size: 2 * gb},
			{FileID: "e1b", ShareCode: "sw1", Name: "Show.S01E01.avi", Ext: "avi", Size: 2 * gb}, // dup stem
		}
		got := folderNames(d, kids)
		want := []string{"Show.S01E01", "Show.S01E03", "第一季"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("folderNames=%v, want %v", got, want)
		}
	})
	t.Run("caps_at_maxFolderNames_keeps_folder_name", func(t *testing.T) {
		d := model.File{Name: "zzz_root", ShareCode: "sw1", FileID: "d1", IsDir: true}
		kids := make([]model.File, 300)
		for i := range kids {
			kids[i] = model.File{FileID: "e", ShareCode: "sw1", Name: "S01E01.mkv", Ext: "mkv"} // all same stem
		}
		// all identical -> 1 distinct child stem + folder name = 2 names
		got := folderNames(d, kids)
		if len(got) != 2 {
			t.Fatalf("deduped names = %d, want 2 (folder name + 1 distinct stem)", len(got))
		}
	})
	t.Run("many_distinct_stems_capped_folder_name_always_present", func(t *testing.T) {
		d := model.File{Name: "zfolder", ShareCode: "sw1", FileID: "d1", IsDir: true}
		kids := make([]model.File, 300)
		for i := range kids {
			kids[i] = model.File{ShareCode: "sw1", Name: "S01E01.mkv", Ext: "mkv"}
			kids[i].Name = "Show.S01E" + string(rune('A'+i%26)) + fmt.Sprintf("%02d", i) + ".mkv"
		}
		got := folderNames(d, kids)
		if len(got) > maxFolderNames {
			t.Fatalf("names = %d, want ≤ %d", len(got), maxFolderNames)
		}
		found := false
		for _, n := range got {
			if n == "zfolder" {
				found = true
			}
		}
		if !found {
			t.Errorf("folder name %q not retained after cap", d.Name)
		}
	})
}
```

Add `"fmt"` to the test file's imports (it is not currently imported in `dedupe_test.go`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/searchindex/ -run TestFolderNames -v`
Expected: FAIL / compile error (`folderNames undefined`, `maxFolderNames undefined`, `fmt` import needed).

- [ ] **Step 3: Write minimal implementation**

Add to `dedupe_test.go` imports:

```go
import (
	"fmt"
	"reflect"
	"testing"

	"five/internal/model"
)
```

Add to `dedupe.go` (near `movieSizeThreshold`):

```go
// maxFolderNames caps how many distinct names a rolled-up folder doc carries
// (the folder's own name plus child stems). Bounds giant folders — the corpus
// has episode containers up to ~18k files. The folder's own name is always kept.
const maxFolderNames = 256
```

Add to `dedupe.go` (after `isContainer`):

```go
// folderNames returns the distinct names to index for a rolled-up folder: the
// folder's own name (always kept, verbatim) plus its direct child stems, sorted
// and capped at maxFolderNames total. Child stems equal to the folder name are
// deduped; when capped, the smallest child stems by sort are kept alongside the
// folder name.
func folderNames(d model.File, kids []model.File) []string {
	seen := map[string]struct{}{d.Name: {}}
	var childStems []string
	for _, k := range kids {
		s := stem(k)
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			childStems = append(childStems, s)
		}
	}
	sort.Strings(childStems)
	names := make([]string, 0, len(childStems)+1)
	for _, s := range childStems {
		if len(names) >= maxFolderNames-1 {
			break
		}
		names = append(names, s)
	}
	names = append(names, d.Name)
	sort.Strings(names)
	return names
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/searchindex/ -run TestFolderNames -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/searchindex/dedupe.go internal/searchindex/dedupe_test.go
git commit -m "feat(searchindex): add folderNames builder with name cap for rolled-up folders"
```

---

### Task 4: Restructure `planDocs` for folder rollup + stem

**Files:**
- Modify: `internal/searchindex/dedupe.go` (rewrite `planDocs` body; uses `stem`, `isContainer`, `folderNames`)
- Test: `internal/searchindex/dedupe_test.go` (add rollup cases)

**Interfaces:**
- Consumes: `stem` (Task 1), `isContainer` (Task 2), `folderNames` (Task 3), `docID`, `isEpisodeFile`, `emptyStringHash` (existing).
- Produces: same `func planDocs(files []model.File) []indexedDoc` signature (callers — `indexer.go` `Rebuild` — unchanged).

**Backward compatibility:** existing rows that have no `Ext` are stemmed to their full name (no-op), and rows with no folder parent (no matching dir row) are never suppressed — so the existing `TestPlanDocs*` and `TestRebuild*` tests stay green unchanged. Do not edit them; they guard compatibility.

- [ ] **Step 1: Write the failing tests**

Append to `dedupe_test.go`:

```go
func TestPlanDocsRollsUpMarkerContainer(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	dir := model.File{FileID: "d2", ShareCode: "sw1", ParentID: "d1", Name: "第一季", IsDir: true}
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "剧", IsDir: true},
		dir,
		{FileID: "e1", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E01.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
		{FileID: "e2", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E02.mkv", Ext: "mkv", SHA1: "H2", Size: 2 * gb},
		{FileID: "e3", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E03.mkv", Ext: "mkv", SHA1: "H3", Size: 2 * gb},
		{FileID: "e4", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E04.mkv", Ext: "mkv", SHA1: "H4", Size: 2 * gb},
		{FileID: "e5", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E05.mkv", Ext: "mkv", SHA1: "H5", Size: 2 * gb},
	}
	got := planDocs(files)
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (root + season; episodes rolled into season)", len(got))
	}
	want := map[string][]string{
		"sw1-d1": {"剧"},
		"sw1-d2": {"Show.S01E01", "Show.S01E02", "Show.S01E03", "Show.S01E04", "Show.S01E05", "第一季"},
	}
	gotMap := map[string][]string{}
	for _, d := range got {
		gotMap[d.docID] = d.names
	}
	for id, names := range want {
		if !reflect.DeepEqual(gotMap[id], names) {
			t.Errorf("doc %s names = %v, want %v", id, gotMap[id], names)
		}
	}
}

func TestPlanDocsRollsUpMarkerLessContainer(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "剧", IsDir: true},
		{FileID: "c1", ShareCode: "sw1", ParentID: "d1", Name: "01.mkv", Ext: "mkv", SHA1: "H1", Size: 1 * gb},
		{FileID: "c2", ShareCode: "sw1", ParentID: "d1", Name: "02.mkv", Ext: "mkv", SHA1: "H2", Size: 1 * gb},
		{FileID: "c3", ShareCode: "sw1", ParentID: "d1", Name: "03.mkv", Ext: "mkv", SHA1: "H3", Size: 1 * gb},
		{FileID: "c4", ShareCode: "sw1", ParentID: "d1", Name: "04.mkv", Ext: "mkv", SHA1: "H4", Size: 1 * gb},
		{FileID: "c5", ShareCode: "sw1", ParentID: "d1", Name: "05.mkv", Ext: "mkv", SHA1: "H5", Size: 1 * gb},
	}
	got := planDocs(files)
	// d1 is a container (5 small files); c1..c5 suppressed. Root d1 is its own parent-less dir.
	if len(got) != 1 {
		t.Fatalf("got %d docs, want 1 (folder absorbed 5 marker-less episodes)", len(got))
	}
	if got[0].docID != "sw1-d1" {
		t.Errorf("docID = %q, want sw1-d1", got[0].docID)
	}
	want := []string{"01", "02", "03", "04", "05", "剧"}
	if !reflect.DeepEqual(got[0].names, want) {
		t.Errorf("names = %v, want %v", got[0].names, want)
	}
}

func TestPlanDocsDoesNotRollUpCollection(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "合集", IsDir: true},
		{FileID: "m1", ShareCode: "sw1", ParentID: "d1", Name: "A.2160p.mkv", Ext: "mkv", SHA1: "H1", Size: 40 * gb},
		{FileID: "m2", ShareCode: "sw1", ParentID: "d1", Name: "B.2160p.mkv", Ext: "mkv", SHA1: "H2", Size: 40 * gb},
		{FileID: "m3", ShareCode: "sw1", ParentID: "d1", Name: "C.2160p.mkv", Ext: "mkv", SHA1: "H3", Size: 40 * gb},
	}
	got := planDocs(files)
	// d1 not a container (3 large, <5 files) -> passthrough dir + 3 movie docs
	if len(got) != 4 {
		t.Fatalf("got %d docs, want 4 (folder passthrough + 3 movies, not rolled up)", len(got))
	}
}

func TestPlanDocsDoesNotRollUpSingleMovie(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "阿凡达3", IsDir: true},
		{FileID: "f1", ShareCode: "sw1", ParentID: "d1", Name: "阿凡达3.2025.mkv", Ext: "mkv", SHA1: "H1", Size: 40 * gb},
	}
	got := planDocs(files)
	// single movie: dir passthrough + 1 movie doc (NOT rolled up)
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (folder passthrough + movie file)", len(got))
	}
}

func TestPlanDocsSuppressesFilesExcludedFromDedup(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	// The same content (sha1 H1) appears as a container child (suppressed) and as
	// a loose root-level movie. The loose copy is still indexed; the suppressed
	// copy is NOT double-emitted.
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "剧", IsDir: true},
		{FileID: "e1", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E01.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
		{FileID: "e2", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E02.mkv", Ext: "mkv", SHA1: "H2", Size: 2 * gb},
		{FileID: "e3", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E03.mkv", Ext: "mkv", SHA1: "H3", Size: 2 * gb},
		{FileID: "e4", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E04.mkv", Ext: "mkv", SHA1: "H4", Size: 2 * gb},
		{FileID: "e5", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E05.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
		{FileID: "loose", ShareCode: "sw1", ParentID: "0", Name: "Show.S01E01.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
	}
	got := planDocs(files)
	// Expect exactly: 1 container folder doc (d1) + 1 loose episode doc.
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (container folder + loose copy; suppressed copy not double-emitted)", len(got))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/searchindex/ -run 'TestPlanDocsRollsUp|TestPlanDocsDoesNotRollUp|TestPlanDocsSuppressesFiles' -v`
Expected: FAIL (current `planDocs` emits one doc per row, so e.g. the marker container yields 7 docs, not 2).

- [ ] **Step 3: Rewrite `planDocs`**

Replace the entire body of `planDocs` in `dedupe.go` with:

```go
// planDocs groups files into the bleve documents Rebuild should index.
//
// Folder rollup: a folder classified as an episode container (isContainer) gets
// one doc carrying its own name plus its direct child file stems (folderNames),
// and its direct child files are suppressed — so a series search returns the
// folder, not dozens of episodes. Non-container folders are passthrough (name
// only). Remaining (non-suppressed) real-hash files are content-deduped as
// before: episodes (isEpisodeFile) key on (name, sha1, size) so each distinct
// filename is its own doc; movies key on (sha1, size) so differently-named copies
// merge into one doc carrying every name. Within a group the representative is
// the lexicographically smallest (share_code, file_id) row; its composite id is
// the doc id. Unhashed files and the empty-string-hash sentinel are passthrough.
// Every indexed name uses the file stem (stem). Output is deterministic for a
// given input.
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

	// Partition rows into dirs and files; index each file under its parent folder
	// (parent_id is share-scoped) so dirs can be classified as containers.
	var dirs []model.File
	childrenOf := map[[2]string][]model.File{} // (share_code, parent_id) -> direct child files
	for _, f := range files {
		if f.IsDir {
			dirs = append(dirs, f)
			continue
		}
		key := [2]string{f.ShareCode, f.ParentID}
		childrenOf[key] = append(childrenOf[key], f)
	}

	out := make([]indexedDoc, 0, len(files))

	// Folder docs first. Containers absorb their direct child file stems and
	// suppress those files; non-containers are passthrough (stem of a dir is its
	// verbatim name).
	suppressed := map[[2]string]bool{} // (share_code, file_id) rolled into a folder
	for _, d := range dirs {
		kids := childrenOf[[2]string{d.ShareCode, d.FileID}]
		if isContainer(kids) {
			out = append(out, indexedDoc{
				docID: docID(d.ShareCode, d.FileID),
				names: folderNames(d, kids),
			})
			for _, k := range kids {
				suppressed[[2]string{k.ShareCode, k.FileID}] = true
			}
		} else {
			out = append(out, indexedDoc{
				docID: docID(d.ShareCode, d.FileID),
				names: []string{stem(d)},
			})
		}
	}

	// File docs: existing content-dedup over non-suppressed real-hash files;
	// unhashed / sentinel rows are passthrough. Indexed names use the stem.
	groups := map[groupKey]*group{}
	var order []groupKey
	for _, f := range files {
		if f.IsDir {
			continue
		}
		if suppressed[[2]string{f.ShareCode, f.FileID}] {
			continue
		}
		if f.SHA1 == "" || f.SHA1 == emptyStringHash {
			out = append(out, indexedDoc{
				docID: docID(f.ShareCode, f.FileID),
				names: []string{stem(f)},
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
		g.names[stem(f)] = struct{}{}
		if f.ShareCode < g.rep.ShareCode || (f.ShareCode == g.rep.ShareCode && f.FileID < g.rep.FileID) {
			g.rep = f
		}
	}
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

- [ ] **Step 4: Run the whole package — new + existing tests must pass**

Run: `go test ./internal/searchindex/ -v`
Expected: PASS for all, including the pre-existing `TestIsEpisodeFile`, `TestPlanDocsMovieMergesAcrossNames`, `TestPlanDocsEpisodeBySizeKeepsNamesSeparate`, `TestPlanDocsEpisodeMarkerOverridesSize`, `TestPlanDocsSameNameAndContentCollapses`, `TestPlanDocsPassthroughNeverMerged`, `TestPlanDocsDeterministicAcrossInputOrder`, and the `TestRebuild*` tests in `indexer_test.go`. (Existing rows have no `Ext`, so `stem` is a no-op on them; rows with no folder parent are never suppressed.)

- [ ] **Step 5: Commit**

```bash
git add internal/searchindex/dedupe.go internal/searchindex/dedupe_test.go
git commit -m "feat(searchindex): roll episode files up into their folder doc in planDocs"
```

---

### Task 5: `Rebuild` integration tests

**Files:**
- Modify: `internal/searchindex/indexer_test.go` (add two tests using the existing `staticProvider`)

**Interfaces:**
- Consumes: `model.File`, `staticProvider` (already in `indexer_test.go`), `builder.Rebuild`, bleve search API.

- [ ] **Step 1: Write the failing tests**

Append to `internal/searchindex/indexer_test.go`:

```go
// TestRebuildRollsUpEpisodesIntoContainerFolder guards the folder rollup end to
// end: a season folder with >=5 marker episodes becomes ONE doc carrying the
// episode stems; the episodes are not separate docs, and an episode-code search
// hits the folder (not a file).
func TestRebuildRollsUpEpisodesIntoContainerFolder(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "剧", IsDir: true},
			{FileID: "d2", ShareCode: "sw1", ParentID: "d1", Name: "第一季", IsDir: true},
			{FileID: "e1", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E01.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
			{FileID: "e2", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E02.mkv", Ext: "mkv", SHA1: "H2", Size: 2 * gb},
			{FileID: "e3", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E03.mkv", Ext: "mkv", SHA1: "H3", Size: 2 * gb},
			{FileID: "e4", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E04.mkv", Ext: "mkv", SHA1: "H4", Size: 2 * gb},
			{FileID: "e5", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E05.mkv", Ext: "mkv", SHA1: "H5", Size: 2 * gb},
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
		t.Fatalf("doc count = %d, want 2 (root + season; 5 episodes rolled into season)", count)
	}

	// Episode code hits the season folder, not a file.
	req := bleve.NewSearchRequest(bleve.NewMatchQuery("S01E03"))
	res, err := index.Search(req)
	if err != nil {
		t.Fatalf("search S01E03: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("search S01E03 total = %d, want 1 (the season folder)", res.Total)
	}
	if res.Hits[0].ID != "sw1-d2" {
		t.Errorf("hit id = %q, want sw1-d2 (season folder)", res.Hits[0].ID)
	}
}

// TestRebuildDoesNotRollUpMovieCollection guards the converse: a folder of large
// movies (no markers, <5 files) is NOT a container, so the movies stay as
// separate docs and the folder is just a passthrough entry.
func TestRebuildDoesNotRollUpMovieCollection(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "合集", IsDir: true},
			{FileID: "m1", ShareCode: "sw1", ParentID: "d1", Name: "AvatarA.2160p.mkv", Ext: "mkv", SHA1: "H1", Size: 40 * gb},
			{FileID: "m2", ShareCode: "sw1", ParentID: "d1", Name: "AvatarB.2160p.mkv", Ext: "mkv", SHA1: "H2", Size: 40 * gb},
			{FileID: "m3", ShareCode: "sw1", ParentID: "d1", Name: "AvatarC.2160p.mkv", Ext: "mkv", SHA1: "H3", Size: 40 * gb},
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
	if count != 4 {
		t.Fatalf("doc count = %d, want 4 (folder passthrough + 3 movies)", count)
	}
	req := bleve.NewSearchRequest(bleve.NewMatchQuery("AvatarB"))
	res, err := index.Search(req)
	if err != nil {
		t.Fatalf("search AvatarB: %v", err)
	}
	if res.Total != 1 {
		t.Errorf("search AvatarB total = %d, want 1 (the movie file)", res.Total)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail (before planDocs exists they'd fail; after Task 4 they pass)**

Run: `go test ./internal/searchindex/ -run 'TestRebuildRollsUpEpisodesIntoContainerFolder|TestRebuildDoesNotRollUpMovieCollection' -v`
Expected: PASS (these exercise the `planDocs` from Task 4 end to end). If run before Task 4, the first test fails with doc count 7 vs 2.

- [ ] **Step 3: Run the full package once more**

Run: `go test ./internal/searchindex/ -v`
Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/searchindex/indexer_test.go
git commit -m "test(searchindex): add Rebuild integration tests for folder rollup"
```

---

## Verification (final)

- [ ] `go build ./...` compiles.
- [ ] `go test ./internal/searchindex/ -v` — all green.
- [ ] `git log --oneline -6` shows the five task commits.
- [ ] Confirm `internal/searchindex/indexer.go` was NOT modified (`git diff main -- internal/searchindex/indexer.go` is empty) — `Rebuild` already calls `planDocs`.
