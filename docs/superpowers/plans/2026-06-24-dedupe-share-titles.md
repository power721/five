# Dedupe Share Titles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename duplicate `share_title`s to be globally unique (`原盘精选`, `原盘精选1`, …) via a CLI command and automatically in the scheduler, durable across re-crawl.

**Architecture:** A reusable store op `DedupeShareTitles` (pure planner + apply) is shared by a new `-mode dedupe-share-titles` CLI command and a post-crawl hook in the scheduler. A crawler gate makes any set title survive re-crawl.

**Tech Stack:** Go 1.x, sqlite (modernc via `five/internal/store`), table-driven tests, `go test`.

Spec: `docs/superpowers/specs/2026-06-24-dedupe-share-titles-design.md`

---

## File map

- **Create** `internal/store/dedupe.go` — `planShareRenames`, `RenameShareTitle`, `DedupeShareTitles`.
- **Create** `internal/store/dedupe_test.go` — planner + store tests.
- **Modify** `internal/model/model.go` — add `ShareRename` DTO.
- **Modify** `internal/crawler/crawler.go:135` — gate meta write on empty title.
- **Modify** `internal/crawler/crawler_test.go` — regression test for the gate.
- **Modify** `internal/scheduler/scheduler.go` — `Registry.DedupeShareTitles` + post-crawl hook.
- **Modify** `internal/scheduler/scheduler_test.go` — update both fakes + new test.
- **Create** `cmd/115-indexer/dedupe.go` — `runDedupeShareTitles` + `dedupeStore` interface.
- **Create** `cmd/115-indexer/dedupe_test.go` — CLI handler test.
- **Modify** `cmd/115-indexer/main.go` — `-apply` flag + `dedupe-share-titles` mode.

---

### Task 1: Pure planner `planShareRenames` + `model.ShareRename`

**Files:**
- Modify: `internal/model/model.go` (append `ShareRename`)
- Create: `internal/store/dedupe.go`
- Create: `internal/store/dedupe_test.go`

- [ ] **Step 1: Add the DTO**

Append to `internal/model/model.go` (after `ShareGroup`):

```go
// ShareRename is one planned/applied title rename produced by store
// DedupeShareTitles (internal/store dedupe.go).
type ShareRename struct {
	ShareCode string
	From      string
	To        string
}
```

- [ ] **Step 2: Write the failing planner test**

Create `internal/store/dedupe_test.go`:

```go
package store

import (
	"testing"

	"five/internal/model"
)

func TestPlanShareRenames(t *testing.T) {
	t.Run("no duplicates yields no renames", func(t *testing.T) {
		got := planShareRenames([]model.Share{
			{ShareCode: "a", ShareTitle: "X"},
			{ShareCode: "b", ShareTitle: "Y"},
		})
		if len(got) != 0 {
			t.Fatalf("got %v, want no renames", got)
		}
	})

	t.Run("lowest id keeps bare title, rest get numeric suffix", func(t *testing.T) {
		// Input order is id-ASC (as ListShares returns).
		got := planShareRenames([]model.Share{
			{ShareCode: "id1", ShareTitle: "原盘精选"},
			{ShareCode: "id2", ShareTitle: "原盘精选"},
			{ShareCode: "id3", ShareTitle: "原盘精选"},
		})
		want := []model.ShareRename{
			{ShareCode: "id2", From: "原盘精选", To: "原盘精选1"},
			{ShareCode: "id3", From: "原盘精选", To: "原盘精选2"},
		}
		assertRenames(t, got, want)
	})

	t.Run("suffix skips titles already used by other shares (global uniqueness)", func(t *testing.T) {
		// "原盘精选1" is already a real title on a different share, so id2
		// cannot reuse it and must take "原盘精选2".
		got := planShareRenames([]model.Share{
			{ShareCode: "id1", ShareTitle: "原盘精选"},
			{ShareCode: "id2", ShareTitle: "原盘精选"},
			{ShareCode: "id3", ShareTitle: "原盘精选1"},
		})
		want := []model.ShareRename{
			{ShareCode: "id2", From: "原盘精选", To: "原盘精选2"},
		}
		assertRenames(t, got, want)
	})

	t.Run("whitespace trimmed before grouping", func(t *testing.T) {
		got := planShareRenames([]model.Share{
			{ShareCode: "id1", ShareTitle: "  X "},
			{ShareCode: "id2", ShareTitle: "X"},
		})
		want := []model.ShareRename{
			{ShareCode: "id2", From: "X", To: "X1"},
		}
		assertRenames(t, got, want)
	})

	t.Run("empty titles are skipped", func(t *testing.T) {
		got := planShareRenames([]model.Share{
			{ShareCode: "id1", ShareTitle: ""},
			{ShareCode: "id2", ShareTitle: "   "},
		})
		if len(got) != 0 {
			t.Fatalf("got %v, want no renames for empty titles", got)
		}
	})
}

func assertRenames(t *testing.T, got, want []model.ShareRename) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("renames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("renames[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/store/ -run TestPlanShareRenames -v`
Expected: FAIL / build error — `planShareRenames` undefined.

- [ ] **Step 4: Implement the planner**

Create `internal/store/dedupe.go`:

```go
package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"five/internal/model"
)

// planShareRenames assigns globally-unique titles to duplicate share-title
// groups. shares MUST be in id-ASC order (ListShares returns this order). The
// lowest-id share in each duplicate group keeps its bare title; the rest get
// <title><n> for n=1,2,3…, skipping any candidate already used by any share so
// the result never creates a new collision. Empty/whitespace titles are skipped.
func planShareRenames(shares []model.Share) []model.ShareRename {
	used := make(map[string]struct{})
	for _, sh := range shares {
		used[strings.TrimSpace(sh.ShareTitle)] = struct{}{}
	}

	type group struct {
		base    string
		members []model.Share
	}
	groups := map[string]*group{}
	var order []string
	for _, sh := range shares {
		t := strings.TrimSpace(sh.ShareTitle)
		if t == "" {
			continue
		}
		g, ok := groups[t]
		if !ok {
			g = &group{base: t}
			groups[t] = g
			order = append(order, t)
		}
		g.members = append(g.members, sh)
	}

	var renames []model.ShareRename
	for _, base := range order {
		g := groups[base]
		if len(g.members) <= 1 {
			continue
		}
		// members[0] (lowest id) keeps the bare title; rename the rest.
		for _, sh := range g.members[1:] {
			n := 1
			for {
				candidate := base + strconv.Itoa(n)
				if _, taken := used[candidate]; !taken {
					used[candidate] = struct{}{}
					renames = append(renames, model.ShareRename{
						ShareCode: sh.ShareCode,
						From:      sh.ShareTitle,
						To:        candidate,
					})
					break
				}
				n++
			}
		}
	}
	return renames
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/store/ -run TestPlanShareRenames -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/model/model.go internal/store/dedupe.go internal/store/dedupe_test.go
git commit -m "feat(store): add planShareRenames for deduping share titles"
```

---

### Task 2: `RenameShareTitle` store method

**Files:**
- Modify: `internal/store/dedupe.go` (append method)
- Modify: `internal/store/dedupe_test.go` (append test)

- [ ] **Step 1: Write the failing test**

Append to `internal/store/dedupe_test.go`:

```go
func TestRenameShareTitle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpdateShareMeta(ctx, "sw1", "rc", "Original", 1234); err != nil {
		t.Fatalf("seed share: %v", err)
	}

	if err := s.RenameShareTitle(ctx, "sw1", "Renamed"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, ok, err := s.GetShare(ctx, "sw1")
	if err != nil || !ok {
		t.Fatalf("get share: ok=%v err=%v", ok, err)
	}
	if got.ShareTitle != "Renamed" {
		t.Fatalf("share_title = %q, want Renamed", got.ShareTitle)
	}
	if got.FileSize != 1234 || got.Status != "ACTIVE" || got.Version != 0 {
		t.Fatalf("rename touched other columns: %#v", got)
	}

	// Renaming a share that does not exist is a no-op, not an error.
	if err := s.RenameShareTitle(ctx, "nope", "Whatever"); err != nil {
		t.Fatalf("rename missing share: %v", err)
	}
}
```

Add imports to `dedupe_test.go`: `"context"`, `"path/filepath"`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/ -run TestRenameShareTitle -v`
Expected: FAIL — `s.RenameShareTitle undefined`.

- [ ] **Step 3: Implement `RenameShareTitle`**

Append to `internal/store/dedupe.go`:

```go
// RenameShareTitle sets share_title for every row with share_code, leaving
// file_size, status, and version untouched. Used by DedupeShareTitles.
func (s *Store) RenameShareTitle(ctx context.Context, shareCode, newTitle string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share SET share_title=? WHERE share_code=?`, newTitle, shareCode)
	return err
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/store/ -run TestRenameShareTitle -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/dedupe.go internal/store/dedupe_test.go
git commit -m "feat(store): add RenameShareTitle"
```

---

### Task 3: `DedupeShareTitles` store op

**Files:**
- Modify: `internal/store/dedupe.go` (append method)
- Modify: `internal/store/dedupe_test.go` (append test)

- [ ] **Step 1: Write the failing test**

Append to `internal/store/dedupe_test.go`:

```go
func TestDedupeShareTitles(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	for _, c := range []string{"id1", "id2", "id3"} {
		if err := s.UpdateShareMeta(ctx, c, "rc", "原盘精选", 0); err != nil {
			t.Fatalf("seed %s: %v", c, err)
		}
	}

	t.Run("dry run plans without writing", func(t *testing.T) {
		renames, err := s.DedupeShareTitles(ctx, true)
		if err != nil {
			t.Fatalf("dedupe dry-run: %v", err)
		}
		if len(renames) != 2 {
			t.Fatalf("dry-run renames = %v, want 2", renames)
		}
		got, _, _ := s.GetShare(ctx, "id2")
		if got.ShareTitle != "原盘精选" {
			t.Fatalf("dry-run mutated db: id2 title = %q", got.ShareTitle)
		}
	})

	t.Run("apply writes the planned renames", func(t *testing.T) {
		renames, err := s.DedupeShareTitles(ctx, false)
		if err != nil {
			t.Fatalf("dedupe apply: %v", err)
		}
		if len(renames) != 2 {
			t.Fatalf("apply renames = %v, want 2", renames)
		}
		titles := map[string]string{}
		for _, c := range []string{"id1", "id2", "id3"} {
			sh, _, _ := s.GetShare(ctx, c)
			titles[c] = sh.ShareTitle
		}
		want := map[string]string{"id1": "原盘精选", "id2": "原盘精选1", "id3": "原盘精选2"}
		if titles["id1"] != want["id1"] || titles["id2"] != want["id2"] || titles["id3"] != want["id3"] {
			t.Fatalf("titles after apply = %v, want %v", titles, want)
		}
	})

	t.Run("second apply is a no-op (idempotent)", func(t *testing.T) {
		renames, err := s.DedupeShareTitles(ctx, false)
		if err != nil {
			t.Fatalf("second dedupe: %v", err)
		}
		if len(renames) != 0 {
			t.Fatalf("second apply renames = %v, want none", renames)
		}
	})
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/store/ -run TestDedupeShareTitles -v`
Expected: FAIL — `s.DedupeShareTitles undefined`.

- [ ] **Step 3: Implement `DedupeShareTitles`**

Append to `internal/store/dedupe.go`:

```go
// DedupeShareTitles plans title renames over all shares and, unless dryRun,
// applies them. Returns the planned/applied renames. Idempotent: re-running
// re-plans on the current state, so a partial apply completes on the next run.
func (s *Store) DedupeShareTitles(ctx context.Context, dryRun bool) ([]model.ShareRename, error) {
	shares, err := s.ListShares(ctx)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	renames := planShareRenames(shares)
	if dryRun {
		return renames, nil
	}
	for _, r := range renames {
		if err := s.RenameShareTitle(ctx, r.ShareCode, r.To); err != nil {
			return nil, fmt.Errorf("rename share %s: %w", r.ShareCode, err)
		}
	}
	return renames, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/store/ -run TestDedupeShareTitles -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/dedupe.go internal/store/dedupe_test.go
git commit -m "feat(store): add DedupeShareTitles op"
```

---

### Task 4: Crawler gate — don't overwrite a set title

**Files:**
- Modify: `internal/crawler/crawler.go:135`
- Modify: `internal/crawler/crawler_test.go` (append test)

- [ ] **Step 1: Write the failing test**

Append to `internal/crawler/crawler_test.go`:

```go
func TestCrawlerDoesNotOverwriteExistingShareTitle(t *testing.T) {
	// A share whose title is already set (e.g. manually renamed, or backfilled)
	// must keep it: the scheduler re-crawls ACTIVE shares, and overwriting would
	// undo a dedupe. Only an empty title is filled from 115.
	c := New(&fakeLister{
		pages: map[string][]Page{
			"0": {
				{
					ShareTitle: "From115",
					FileSize:   999,
					Nodes: []model.File{
						{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "x.mkv", Ext: "mkv", Depth: 1},
					},
					HasMore: false,
				},
			},
		},
	}, &memoryStore{}, Config{PageSize: 100})

	store := c.store.(*memoryStore)
	share := model.Share{ShareCode: "sw1", ReceiveCode: "rc", ShareTitle: "ManuallyRenamed"}
	if err := c.CrawlShare(context.Background(), share, 100); err != nil {
		t.Fatalf("crawl share: %v", err)
	}
	if len(store.metaUpdates) != 0 {
		t.Fatalf("expected no meta update when title already set, got %#v", store.metaUpdates)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/crawler/ -run TestCrawlerDoesNotOverwriteExistingShareTitle -v`
Expected: FAIL — `metaUpdates` has 1 entry (crawler overwrote).

- [ ] **Step 3: Apply the gate**

In `internal/crawler/crawler.go`, change the guard at the meta-write block (currently line 135) from:

```go
			if !metaPersisted && page.ShareTitle != "" {
```

to:

```go
			// Only fill the title when it is not already set: the scheduler
			// re-crawls ACTIVE shares, and overwriting here would undo a manual
			// rename (e.g. dedupe-share-titles). Backfill remains the force path.
			if !metaPersisted && page.ShareTitle != "" && share.ShareTitle == "" {
```

- [ ] **Step 4: Run the new test AND the existing meta test**

Run: `go test ./internal/crawler/ -run 'TestCrawlerDoesNotOverwriteExistingShareTitle|TestCrawlerPersistsShareMetadataOncePerCrawl' -v`
Expected: both PASS (existing test passes because its share starts with an empty title).

- [ ] **Step 5: Commit**

```bash
git add internal/crawler/crawler.go internal/crawler/crawler_test.go
git commit -m "fix(crawler): keep an existing share_title on re-crawl"
```

---

### Task 5: Scheduler auto-dedup after each crawl run

**Files:**
- Modify: `internal/scheduler/scheduler.go` (interface + RunOnce hook)
- Modify: `internal/scheduler/scheduler_test.go` (both fakes + new test)

- [ ] **Step 1: Write the failing test**

Append to `internal/scheduler/scheduler_test.go`:

```go
func TestSchedulerDedupesShareTitlesAfterCrawl(t *testing.T) {
	var buf bytes.Buffer
	store := &registryStore{
		shares: []model.Share{
			{ShareCode: "ok", ReceiveCode: "a"},
		},
		dedupeReturns: []model.ShareRename{
			{ShareCode: "ok", From: "Dup", To: "Dup1"},
		},
	}
	s := New(store, crawlRunner{}, &buf)
	if _, err := s.RunOnce(context.Background(), 1); err != nil {
		t.Fatalf("run once: %v", err)
	}
	if store.dedupeCalls != 1 {
		t.Fatalf("dedupe calls = %d, want 1", store.dedupeCalls)
	}
	if store.dedupeDryRun != false {
		t.Fatalf("dedupe dryRun = %v, want false (scheduler applies)", store.dedupeDryRun)
	}
	if !bytes.Contains(buf.Bytes(), []byte("event=share_title_deduped share=ok")) ||
		!bytes.Contains(buf.Bytes(), []byte(`from="Dup"`)) ||
		!bytes.Contains(buf.Bytes(), []byte(`to="Dup1"`)) {
		t.Fatalf("missing dedupe log: %q", buf.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails (compile error)**

Run: `go test ./internal/scheduler/ -run TestSchedulerDedupesShareTitlesAfterCrawl -v`
Expected: FAIL / compile error — `registryStore` has no `dedupeReturns`/`dedupeCalls` fields and the `Registry` interface lacks `DedupeShareTitles`.

- [ ] **Step 3: Update the test fakes**

In `internal/scheduler/scheduler_test.go`, extend `registryStore`:

```go
type registryStore struct {
	shares        []model.Share
	markedCrawled []string
	markedFailed  []string
	markedDead    []string
	markedShelved []string
	dedupeCalls   int
	dedupeDryRun  bool
	dedupeReturns []model.ShareRename
}

func (r *registryStore) DedupeShareTitles(_ context.Context, dryRun bool) ([]model.ShareRename, error) {
	r.dedupeCalls++
	r.dedupeDryRun = dryRun
	return r.dedupeReturns, nil
}
```

And add the no-op to `emptyRegistry` (so it still satisfies the interface):

```go
func (emptyRegistry) DedupeShareTitles(context.Context, bool) ([]model.ShareRename, error) {
	return nil, nil
}
```

- [ ] **Step 4: Extend the `Registry` interface and add the hook**

In `internal/scheduler/scheduler.go`, add to the `Registry` interface (after `MarkShareShelved`):

```go
	DedupeShareTitles(ctx context.Context, dryRun bool) ([]model.ShareRename, error)
```

Then in `RunOnce`, replace the final `return proxyFailureOnly, nil` with:

```go
	renames, err := s.registry.DedupeShareTitles(ctx, false)
	if err != nil {
		return proxyFailureOnly, err
	}
	for _, r := range renames {
		s.logger.Printf("event=share_title_deduped share=%s from=%q to=%q", r.ShareCode, r.From, r.To)
	}
	return proxyFailureOnly, nil
```

- [ ] **Step 5: Run the scheduler tests**

Run: `go test ./internal/scheduler/ -v`
Expected: PASS (new test + all existing tests — fakes now satisfy the interface).

- [ ] **Step 6: Commit**

```bash
git add internal/scheduler/scheduler.go internal/scheduler/scheduler_test.go
git commit -m "feat(scheduler): auto-dedupe share titles after each crawl run"
```

---

### Task 6: CLI mode `dedupe-share-titles`

**Files:**
- Create: `cmd/115-indexer/dedupe.go`
- Create: `cmd/115-indexer/dedupe_test.go`
- Modify: `cmd/115-indexer/main.go` (flag + mode)

- [ ] **Step 1: Write the failing test**

Create `cmd/115-indexer/dedupe_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"testing"

	"five/internal/model"
)

type fakeDedupeStore struct {
	renames []model.ShareRename
	dryRun  bool
}

func (f *fakeDedupeStore) DedupeShareTitles(_ context.Context, dryRun bool) ([]model.ShareRename, error) {
	f.dryRun = dryRun
	return f.renames, nil
}

func TestRunDedupeShareTitles(t *testing.T) {
	t.Run("dry run prints plan without applying", func(t *testing.T) {
		store := &fakeDedupeStore{renames: []model.ShareRename{
			{ShareCode: "sw1", From: "原盘精选", To: "原盘精选1"},
		}}
		var out bytes.Buffer
		if err := runDedupeShareTitles(context.Background(), store, false, &out); err != nil {
			t.Fatalf("run: %v", err)
		}
		if store.dryRun != true {
			t.Fatal("expected dryRun=true for non-apply run")
		}
		got := out.String()
		if !bytes.Contains([]byte(got), []byte(`share sw1: "原盘精选" -> "原盘精选1"`)) {
			t.Fatalf("missing rename line: %q", got)
		}
		if !bytes.Contains([]byte(got), []byte("would rename 1 shares; re-run with -apply to commit")) {
			t.Fatalf("missing dry-run summary: %q", got)
		}
	})

	t.Run("apply prints renamed summary", func(t *testing.T) {
		store := &fakeDedupeStore{renames: []model.ShareRename{
			{ShareCode: "sw1", From: "X", To: "X1"},
		}}
		var out bytes.Buffer
		if err := runDedupeShareTitles(context.Background(), store, true, &out); err != nil {
			t.Fatalf("run: %v", err)
		}
		if store.dryRun != false {
			t.Fatal("expected dryRun=false for apply run")
		}
		if !bytes.Contains([]byte(out.String()), []byte("renamed 1 shares")) {
			t.Fatalf("missing apply summary: %q", out.String())
		}
	})

	t.Run("no duplicates prints nothing-found", func(t *testing.T) {
		store := &fakeDedupeStore{}
		var out bytes.Buffer
		if err := runDedupeShareTitles(context.Background(), store, false, &out); err != nil {
			t.Fatalf("run: %v", err)
		}
		if !bytes.Contains([]byte(out.String()), []byte("no duplicate titles found")) {
			t.Fatalf("missing empty summary: %q", out.String())
		}
	})
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/115-indexer/ -run TestRunDedupeShareTitles -v`
Expected: FAIL / compile error — `runDedupeShareTitles` undefined.

- [ ] **Step 3: Implement the handler**

Create `cmd/115-indexer/dedupe.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"

	"five/internal/model"
)

// dedupeStore is the subset of *store.Store used by dedupe-share-titles.
type dedupeStore interface {
	DedupeShareTitles(ctx context.Context, dryRun bool) ([]model.ShareRename, error)
}

// runDedupeShareTitles plans (and, when apply is set, commits) renames for
// duplicate share titles, printing each rename and a one-line summary.
func runDedupeShareTitles(ctx context.Context, store dedupeStore, apply bool, out io.Writer) error {
	renames, err := store.DedupeShareTitles(ctx, !apply)
	if err != nil {
		return err
	}
	for _, r := range renames {
		fmt.Fprintf(out, "share %s: %q -> %q\n", r.ShareCode, r.From, r.To)
	}
	switch {
	case apply:
		fmt.Fprintf(out, "renamed %d shares\n", len(renames))
	case len(renames) > 0:
		fmt.Fprintf(out, "would rename %d shares; re-run with -apply to commit\n", len(renames))
	default:
		fmt.Fprintln(out, "no duplicate titles found")
	}
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/115-indexer/ -run TestRunDedupeShareTitles -v`
Expected: PASS.

- [ ] **Step 5: Wire the mode + flag into main.go**

In `cmd/115-indexer/main.go`:

1. Add the flag to the `var (...)` block (e.g. after `outPath`):

```go
		apply            = flag.Bool("apply", false, "apply changes for dry-run modes (currently dedupe-share-titles)")
```

2. Add a case to the `switch *mode { ... }` (e.g. before `default:`):

```go
	case "dedupe-share-titles":
		if err := runDedupeShareTitles(ctx, s, *apply, os.Stdout); err != nil {
			log.Fatalf("dedupe share titles: %v", err)
		}
```

(`os` and `log` are already imported in main.go.)

- [ ] **Step 6: Build + run the whole suite**

Run: `go build ./... && go test ./...`
Expected: build OK, all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/115-indexer/dedupe.go cmd/115-indexer/dedupe_test.go cmd/115-indexer/main.go
git commit -m "feat(indexer): add dedupe-share-titles mode"
```

---

## Self-review notes

- Spec coverage: crawler gate (Task 4), store op + planner + RenameShareTitle (Tasks 1–3), CLI (Task 6), scheduler auto-trigger (Task 5), model DTO (Task 1) — all spec sections mapped.
- Type consistency: `model.ShareRename{ShareCode,From,To}` used identically in store, scheduler, CLI. `DedupeShareTitles(ctx, dryRun) ([]model.ShareRename, error)` signature identical across store method, scheduler `Registry`, and CLI `dedupeStore` interface.
- The existing `TestCrawlerPersistsShareMetadataOncePerCrawl` shares start with an empty title, so the gate preserves it; verified in Task 4 Step 4.
