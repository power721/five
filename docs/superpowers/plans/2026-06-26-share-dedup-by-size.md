# Share dedup by file size — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop crawling/indexing duplicate 115 shares (same total `file_size` ≥ 1GiB), clean existing duplicates and orphan files, and harden the delete/crawl race.

**Architecture:** Detect duplicates on the first crawl page (where `file_size` first becomes known) and abort before indexing; mark losers `DUPLICATE`. Add a one-time `dedupe-shares-by-size` mode for already-indexed duplicates, a `cleanup-orphans` mode for files whose share row is gone, and a per-crawl `PurgeIfOrphan` guard so a delete that races with an in-flight crawl can no longer resurrect orphan rows.

**Tech Stack:** Go 1.21, SQLite (modernc.org/sqlite), net/http, table-driven tests, TDD.

**Spec:** `docs/superpowers/specs/2026-06-26-share-dedup-by-size-design.md`

## Global Constraints

- Dedup threshold default `1 << 30` (1GiB), exposed as flag `-dedupe-min-size`.
- Duplicate signal: equal `file_size`, both `> 0` and `>= minSize`. Below threshold → never deduped.
- New status `'DUPLICATE'`; whitelisted scheduling (`ListSharesForCrawl` returns only `ACTIVE/STALE/QUARANTINE`) already excludes it.
- Canonical = oldest share by `(COALESCE(last_crawled_at,0) ASC, id ASC)` — used by both `FindDuplicateShare` and `DedupeSharesBySize`.
- `DUPLICATE` shares keep their `share` row (with `duplicate_of`), carry no files, and are pruned from `export-db` like `DEAD`.
- DRY, YAGNI, TDD. Commit after every green test cycle. Conventional-commit messages scoped to the package.

## File Structure

- `internal/model/model.go` — add `Share.DuplicateOf`.
- `internal/store/sqlite.go` — migration (`ensureColumns`), `ExportTrimmed` prune `DUPLICATE`.
- `internal/store/share_status.go` — `MarkShareDuplicate`; `ReactivateShare` clears `duplicate_of`.
- `internal/store/dedup.go` (new) — `FindDuplicateShare`, `DedupeSharesBySize`, `DedupeAction`.
- `internal/store/orphans.go` (new) — `OrphanShares`, `OrphanShare`, `DeleteOrphans`.
- `internal/store/share_race.go` (new) — `PurgeIfOrphan`.
- `internal/crawler/crawler.go` — `Config.DedupeMinFileSize`, `DuplicateShareError`, `Store.FindDuplicateShare`, first-page check.
- `internal/scheduler/scheduler.go` — `Registry.MarkShareDuplicate`/`PurgeIfOrphan`, `RunOnce` handling.
- `cmd/115-indexer/main.go` — `-dedupe-min-size` flag, new modes, daemon wiring.
- `README.md` — document new modes/flag.

---

### Task 1: Schema — `duplicate_of` column + model field

**Files:**
- Modify: `internal/model/model.go` (`Share` struct)
- Modify: `internal/store/sqlite.go:251-257` (`ensureColumns` call in `migrate`)
- Test: `internal/store/sqlite_test.go` (append)

**Interfaces:**
- Produces: `model.Share.DuplicateOf string`; column `share.duplicate_of TEXT NOT NULL DEFAULT ''`.

- [ ] **Step 1: Add the field to the model**

`internal/model/model.go`, add to the `Share` struct (after `FileSize`):

```go
	DuplicateOf string `json:"duplicate_of"`
```

- [ ] **Step 2: Write the failing test**

Append to `internal/store/sqlite_test.go`:

```go
func TestMigrateAddsDuplicateOfColumn(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	var typ, dflt string
	err = s.db.QueryRowContext(ctx, "SELECT type, dflt_value FROM pragma_table_info('share') WHERE name='duplicate_of'").Scan(&typ, &dflt)
	if err != nil {
		t.Fatalf("duplicate_of column missing: %v", err)
	}
	if typ != "TEXT" {
		t.Fatalf("duplicate_of type = %q, want TEXT", typ)
	}
	// Re-open is idempotent (ensureColumns must not re-add).
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestMigrateAddsDuplicateOfColumn -v`
Expected: FAIL (column missing).

- [ ] **Step 4: Add the column via the existing migration**

`internal/store/sqlite.go`, in `migrate`, extend the `ensureColumns("share", ...)` call:

```go
	if err := s.ensureColumns(ctx, "share", []columnDef{
		{name: "share_title", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "file_size", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "group_id", ddl: "INTEGER"},
		{name: "duplicate_of", ddl: "TEXT NOT NULL DEFAULT ''"},
	}); err != nil {
		return fmt.Errorf("migrate share columns: %w", err)
	}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestMigrateAddsDuplicateOfColumn -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/model/model.go internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat(store): add share.duplicate_of column"
```

---

### Task 2: Store dedup primitives — `MarkShareDuplicate`, `FindDuplicateShare`, `ReactivateShare` clears `duplicate_of`

**Files:**
- Modify: `internal/store/share_status.go` (`ReactivateShare`, add `MarkShareDuplicate`)
- Create: `internal/store/dedup.go`
- Create: `internal/store/dedup_test.go`

**Interfaces:**
- Produces:
  - `MarkShareDuplicate(ctx context.Context, shareCode, canonical string) error`
  - `FindDuplicateShare(ctx context.Context, shareCode string, fileSize, minSize int64) (canonical string, ok bool, err error)`
- Consumes: `duplicate_of` column (Task 1).

- [ ] **Step 1: Write failing tests**

`internal/store/dedup_test.go`:

```go
package store

import (
	"context"
	"path/filepath"
	"testing"

	"five/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMarkShareDuplicateSetsStatusAndCanonical(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "dup", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkShareDuplicate(ctx, "dup", "canon"); err != nil {
		t.Fatal(err)
	}
	sh, ok, err := s.GetShare(ctx, "dup")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if sh.Status != "DUPLICATE" || sh.DuplicateOf != "canon" {
		t.Fatalf("share = %+v, want DUPLICATE/canon", sh)
	}
}

func TestReactivateShareClearsDuplicateOf(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "dup", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkShareDuplicate(ctx, "dup", "canon"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReactivateShare(ctx, "dup"); err != nil {
		t.Fatal(err)
	}
	sh, _, _ := s.GetShare(ctx, "dup")
	if sh.Status != "ACTIVE" || sh.DuplicateOf != "" {
		t.Fatalf("after reactivate = %+v, want ACTIVE/empty duplicate_of", sh)
	}
}

func TestFindDuplicateShare(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	// canon: crawled first (earlier last_crawled_at), size 2GiB.
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "canon", ReceiveCode: "p", Status: "ACTIVE", FileSize: 2 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE share SET last_crawled_at=100 WHERE share_code='canon'`); err != nil {
		t.Fatal(err)
	}
	// same size, crawled later -> the duplicate.
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "late", ReceiveCode: "p", Status: "ACTIVE", FileSize: 2 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE share SET last_crawled_at=200 WHERE share_code='late'`); err != nil {
		t.Fatal(err)
	}
	// different size -> not a dup.
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "other", ReceiveCode: "p", Status: "ACTIVE", FileSize: 3 << 30}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.FindDuplicateShare(ctx, "late", 2<<30, 1<<30)
	if err != nil || !ok || got != "canon" {
		t.Fatalf("FindDuplicateShare(late) = %q %v %v, want canon true nil", got, ok, err)
	}
	// Excludes self.
	if got, ok, err := s.FindDuplicateShare(ctx, "canon", 2<<30, 1<<30); ok || err != nil {
		t.Fatalf("FindDuplicateShare(canon) = %q %v %v, want not found", got, ok, err)
	}
	// Below threshold -> not a dup.
	if _, ok, err := s.FindDuplicateShare(ctx, "late", 2<<30, 5<<30); ok || err != nil {
		t.Fatalf("above-threshold minSize should not match: ok=%v err=%v", ok, err)
	}
	// Zero size -> not a dup.
	if _, ok, err := s.FindDuplicateShare(ctx, "late", 0, 1<<30); ok || err != nil {
		t.Fatalf("zero size should not match: ok=%v err=%v", ok, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run 'MarkShareDuplicate|ReactivateShareClearsDuplicateOf|FindDuplicateShare' -v`
Expected: FAIL (`MarkShareDuplicate`/`FindDuplicateShare` undefined; ReactivateShare doesn't clear `duplicate_of`).

- [ ] **Step 3: Implement `MarkShareDuplicate` + update `ReactivateShare`**

`internal/store/share_status.go` — add:

```go
// MarkShareDuplicate records that shareCode is a duplicate of canonical (same
// file_size, above the dedup threshold) and parks it: DUPLICATE status is
// excluded from scheduling and export. Clears failure bookkeeping.
func (s *Store) MarkShareDuplicate(ctx context.Context, shareCode, canonical string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share
		SET status='DUPLICATE',
			duplicate_of=?,
			retry_after_unix=0,
			failure_count=0
		WHERE share_code = ?`, canonical, shareCode)
	return err
}
```

In `ReactivateShare`, extend the UPDATE so it also clears `duplicate_of`:

```go
func (s *Store) ReactivateShare(ctx context.Context, shareCode string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE share
		SET status='ACTIVE',
			last_error='',
			duplicate_of='',
			failure_count=0,
			retry_after_unix=0
		WHERE share_code = ?`, shareCode)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
```

- [ ] **Step 4: Implement `FindDuplicateShare`**

`internal/store/dedup.go`:

```go
package store

import (
	"context"
	"fmt"
)

// FindDuplicateShare returns the canonical (oldest) other share whose file_size
// equals fileSize and is >= minSize, or ok=false if none. A share's size is only
// known after its first crawl page, so this is called once the size is learned.
func (s *Store) FindDuplicateShare(ctx context.Context, shareCode string, fileSize, minSize int64) (string, bool, error) {
	if fileSize <= 0 || fileSize < minSize {
		return "", false, nil
	}
	var canonical string
	err := s.db.QueryRowContext(ctx, `SELECT share_code FROM share
		WHERE file_size = ? AND file_size >= ? AND file_size > 0
		  AND share_code <> ? AND status IN ('ACTIVE','STALE','QUARANTINE')
		ORDER BY COALESCE(last_crawled_at,0) ASC, id ASC
		LIMIT 1`, fileSize, minSize, shareCode).Scan(&canonical)
	if err != nil {
		if isNoRows(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("find duplicate share: %w", err)
	}
	return canonical, true, nil
}
```

Use the project's existing no-rows check. Check `internal/store/sqlite.go` for a helper like `isNoRows` (search `sql.ErrNoRows`); if none exists, use:

```go
import "errors"
// ...
if errors.Is(err, sql.ErrNoRows) { return "", false, nil }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/store/ -run 'MarkShareDuplicate|ReactivateShareClearsDuplicateOf|FindDuplicateShare' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/share_status.go internal/store/dedup.go internal/store/dedup_test.go
git commit -m "feat(store): add MarkShareDuplicate and FindDuplicateShare"
```

---

### Task 3: Crawler first-page dedup

**Files:**
- Modify: `internal/crawler/crawler.go` (`Config`, `Store` interface, `CrawlShare`, add error type)
- Modify: `internal/crawler/crawler_test.go` (`memoryStore` fake gains `FindDuplicateShare`)

**Interfaces:**
- Consumes: `store.FindDuplicateShare` signature (Task 2).
- Produces:
  - `crawler.Config.DedupeMinFileSize int64`
  - `crawler.DuplicateShareError{Canonical string}` + sentinel `crawler.ErrDuplicateShare`
  - `crawler.Store` interface gains `FindDuplicateShare(ctx context.Context, shareCode string, fileSize, minSize int64) (string, bool, error)`

- [ ] **Step 1: Add `FindDuplicateShare` to the `memoryStore` test fake**

`internal/crawler/crawler_test.go`, add a field + method so the fake can simulate a duplicate:

```go
type memoryStore struct {
	files            []model.File
	checkpoint       model.Checkpoint
	checkpointSeen   bool
	history          []model.Checkpoint
	upsertBatches    [][]string
	metaUpdates      []shareMetaUpdate
	duplicateOf      string // if set, FindDuplicateShare returns this (with ok=true)
	duplicateMinSize int64
}

func (m *memoryStore) FindDuplicateShare(_ context.Context, _ string, fileSize, minSize int64) (string, bool, error) {
	if m.duplicateOf != "" && fileSize >= m.duplicateMinSize && fileSize >= minSize {
		return m.duplicateOf, true, nil
	}
	return "", false, nil
}
```

- [ ] **Step 2: Write the failing test**

Append to `internal/crawler/crawler_test.go`:

```go
func TestCrawlerAbortsOnDuplicateShareBeforeIndexing(t *testing.T) {
	lister := &fakeLister{
		pages: map[string][]Page{
			"0": {{
				ShareTitle: "Pack",
				FileSize:   2 << 30,
				Nodes: []model.File{
					{FileID: "f1", ShareCode: "dup", ParentID: "0", Name: "A.mkv", Ext: "mkv"},
				},
				HasMore: false,
			}},
		},
	}
	store := &memoryStore{duplicateOf: "canon", duplicateMinSize: 1 << 30}
	c := New(lister, store, Config{PageSize: 100, DedupeMinFileSize: 1 << 30})

	err := c.CrawlShare(context.Background(), model.Share{ShareCode: "dup", ReceiveCode: "p"}, 100)
	var dup *DuplicateShareError
	if !errors.As(err, &dup) || dup.Canonical != "canon" {
		t.Fatalf("err = %v, want DuplicateShareError{canon}", err)
	}
	if len(store.files) != 0 {
		t.Fatalf("duplicate must not be indexed; files = %#v", store.files)
	}
	if len(store.metaUpdates) != 0 {
		t.Fatalf("duplicate must not persist meta; metaUpdates = %#v", store.metaUpdates)
	}
}

func TestCrawlerCrawlsBelowThresholdOrNoDuplicate(t *testing.T) {
	// Below threshold: no dedup, crawl normally.
	lister := &fakeLister{
		pages: map[string][]Page{
			"0": {{Nodes: []model.File{{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "A.mkv", Ext: "mkv"}}, HasMore: false}},
		},
	}
	store := &memoryStore{duplicateOf: "canon", duplicateMinSize: 1 << 30}
	c := New(lister, store, Config{PageSize: 100, DedupeMinFileSize: 1 << 30})
	if err := c.CrawlShare(context.Background(), model.Share{ShareCode: "sw1"}, 100); err != nil {
		t.Fatalf("below-threshold crawl: %v", err)
	}
	if len(store.files) != 1 {
		t.Fatalf("small share should be indexed; files = %#v", store.files)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/crawler/ -run 'AbortsOnDuplicate|CrawlsBelowThreshold' -v`
Expected: FAIL (`DedupeMinFileSize`, `DuplicateShareError` undefined; `memoryStore` doesn't satisfy `Store`).

- [ ] **Step 4: Implement**

`internal/crawler/crawler.go`:

Add to `Config`:
```go
type Config struct {
	PageSize   int
	RetryCount int
	PauseChecker func() bool
	// DedupeMinFileSize: if a share's first-page file_size is >= this and another
	// share already has the same size, the share is a duplicate and is not indexed.
	DedupeMinFileSize int64
}
```

Add to the `Store` interface:
```go
type Store interface {
	UpsertFiles(ctx context.Context, files []model.File) error
	SaveCheckpoint(ctx context.Context, cp model.Checkpoint) error
	LoadCheckpoint(ctx context.Context, shareCode string) (model.Checkpoint, bool, error)
	UpdateShareMeta(ctx context.Context, shareCode, receiveCode, title string, fileSize int64) error
	FindDuplicateShare(ctx context.Context, shareCode string, fileSize, minSize int64) (string, bool, error)
}
```

Add the error type (near `ErrPaused`):
```go
// DuplicateShareError is returned by CrawlShare when the share's file_size matches
// an already-indexed share (above the dedup threshold). Canonical is the keeper.
type DuplicateShareError struct {
	Canonical string
}

func (e *DuplicateShareError) Error() string { return "duplicate of " + e.Canonical }

var ErrDuplicateShare = errors.New("duplicate share")

func (e *DuplicateShareError) Unwrap() error { return ErrDuplicateShare }
```

In `CrawlShare`, insert the dedup gate immediately after the retry `for` loop that resolves `page`, right before the existing `if !metaPersisted && page.ShareTitle != "" ...` meta block. Gate it on `!metaPersisted` (which is false after the share's first page) and do NOT set `metaPersisted` here, so the existing meta write still runs for non-duplicates:

```go
			if !metaPersisted && c.cfg.DedupeMinFileSize > 0 && page.FileSize >= c.cfg.DedupeMinFileSize {
				if canonical, ok, err := c.store.FindDuplicateShare(ctx, share.ShareCode, page.FileSize, c.cfg.DedupeMinFileSize); err != nil {
					return err
				} else if ok {
					log.Printf("event=crawl_share_duplicate share=%s canonical=%s file_size=%d", share.ShareCode, canonical, page.FileSize)
					return &DuplicateShareError{Canonical: canonical}
				}
			}
```

This runs once per share (first page only), returns before any `UpsertFiles`/`UpdateShareMeta` if a duplicate is found, and is invisible to non-duplicate crawls.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/crawler/ -v`
Expected: PASS (all crawler tests, including the two new ones).

- [ ] **Step 6: Commit**

```bash
git add internal/crawler/crawler.go internal/crawler/crawler_test.go
git commit -m "feat(crawler): abort duplicate shares on first page by file_size"
```

---

### Task 4: Scheduler — mark duplicates + post-crawl `PurgeIfOrphan`

**Files:**
- Modify: `internal/scheduler/scheduler.go` (`Registry` interface, `RunOnce`)
- Create: `internal/store/share_race.go`
- Modify: `internal/scheduler/scheduler_test.go` (fakes gain the two methods)

**Interfaces:**
- Consumes: `crawler.DuplicateShareError` (Task 3).
- Produces:
  - `scheduler.Registry` gains `MarkShareDuplicate(ctx, shareCode, canonical string) error` and `PurgeIfOrphan(ctx, shareCode string) (bool, error)`
  - `store.PurgeIfOrphan(ctx, shareCode string) (bool, error)`

- [ ] **Step 1: Write the failing store test for `PurgeIfOrphan`**

`internal/store/share_race_test.go`:

```go
package store

import (
	"context"
	"testing"

	"five/internal/model"
)

func TestPurgeIfOrphan(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "alive", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fa','alive','0','a.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fx','ghost','0','g.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO crawl_checkpoint(share_code, cid, next_offset, active_depth, queue_json, visited_json, updated_at) VALUES('ghost','0',0,0,'[]','{}',1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// 'ghost' has no share row -> purged.
	purged, err := s.PurgeIfOrphan(ctx, "ghost")
	if err != nil || !purged {
		t.Fatalf("PurgeIfOrphan(ghost) = %v %v, want true nil", purged, err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code='ghost'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ghost files = %d, want 0", n)
	}

	// 'alive' has a share row -> not purged.
	purged, err = s.PurgeIfOrphan(ctx, "alive")
	if err != nil || purged {
		t.Fatalf("PurgeIfOrphan(alive) = %v %v, want false nil", purged, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code='alive'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("alive files = %d, want 1", n)
	}
}
```

- [ ] **Step 2: Run store test to verify it fails**

Run: `go test ./internal/store/ -run TestPurgeIfOrphan -v`
Expected: FAIL (`PurgeIfOrphan` undefined).

- [ ] **Step 3: Implement `PurgeIfOrphan`**

`internal/store/share_race.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// PurgeIfOrphan removes the file and crawl_checkpoint rows for shareCode IF no
// share row exists. It is the cleanup for a delete that raced with an in-flight
// crawl: the crawler finished and re-wrote rows after DeleteShare committed.
// Returns true if it purged (share was gone), false if the share still exists.
func (s *Store) PurgeIfOrphan(ctx context.Context, shareCode string) (bool, error) {
	var tmp int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM share WHERE share_code = ?`, shareCode).Scan(&tmp)
	if err == nil {
		return false, nil // share still exists
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("purge orphan check: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM file WHERE share_code = ?;`,
		`DELETE FROM crawl_checkpoint WHERE share_code = ?;`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, shareCode); err != nil {
			return false, fmt.Errorf("purge orphan %q: %w", shareCode, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
```

(If the project already uses `errors.Is(err, sql.ErrNoRows)`, prefer that; match the style used elsewhere in the package.)

- [ ] **Step 4: Run store test to verify it passes**

Run: `go test ./internal/store/ -run TestPurgeIfOrphan -v`
Expected: PASS.

- [ ] **Step 5: Extend the scheduler `Registry` interface + test fakes**

`internal/scheduler/scheduler.go`, add to `Registry`:

```go
type Registry interface {
	ListSharesForCrawl(ctx context.Context, now int64) ([]model.Share, error)
	MarkShareCrawled(ctx context.Context, shareCode string, crawledAt int64) error
	RecordShareFailure(ctx context.Context, shareCode, errText string) error
	MarkShareDead(ctx context.Context, shareCode, errText string) error
	MarkShareShelved(ctx context.Context, shareCode, errText string) error
	MarkShareDuplicate(ctx context.Context, shareCode, canonical string) error
	PurgeIfOrphan(ctx context.Context, shareCode string) (bool, error)
	DedupeShareTitles(ctx context.Context, dryRun bool) ([]model.ShareRename, error)
}
```

`internal/scheduler/scheduler_test.go` — add no-op/stub implementations to both fakes:

```go
// registryStore:
func (r *registryStore) MarkShareDuplicate(_ context.Context, shareCode, canonical string) error {
	r.markedDuplicate = append(r.markedDuplicate, shareCode+":"+canonical)
	return nil
}
func (r *registryStore) PurgeIfOrphan(context.Context, string) (bool, error) { return false, nil }

// emptyRegistry:
func (emptyRegistry) MarkShareDuplicate(context.Context, string, string) error { return nil }
func (emptyRegistry) PurgeIfOrphan(context.Context, string) (bool, error)       { return false, nil }
```

Add a `markedDuplicate []string` field to `registryStore`.

- [ ] **Step 6: Write the failing scheduler test**

Append to `internal/scheduler/scheduler_test.go`:

```go
type duplicateRunner struct{ canonical string }

func (r *duplicateRunner) CrawlShare(context.Context, model.Share, int64) error {
	return &crawler.DuplicateShareError{Canonical: r.canonical}
}

func TestSchedulerMarksDuplicateWithoutFailure(t *testing.T) {
	store := &registryStore{
		shares: []model.Share{{ShareCode: "dup", ReceiveCode: "a"}},
	}
	s := New(store, &duplicateRunner{canonical: "canon"}, nil)

	_, err := s.RunOnce(context.Background(), 1)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(store.markedDuplicate) != 1 || store.markedDuplicate[0] != "dup:canon" {
		t.Fatalf("markedDuplicate = %#v, want [dup:canon]", store.markedDuplicate)
	}
	if len(store.markedFailed) != 0 {
		t.Fatalf("duplicate must not be recorded as failure; markedFailed=%#v", store.markedFailed)
	}
}
```

Add `"five/internal/crawler"` to the test file imports.

- [ ] **Step 7: Run scheduler test to verify it fails**

Run: `go test ./internal/scheduler/ -run TestSchedulerMarksDuplicateWithoutFailure -v`
Expected: FAIL (RunOnce doesn't handle `DuplicateShareError`).

- [ ] **Step 8: Handle `DuplicateShareError` + call `PurgeIfOrphan` in `RunOnce`**

`internal/scheduler/scheduler.go`, add `"five/internal/crawler"` to imports. In `RunOnce`, after `err := s.runner.CrawlShare(ctx, share, now)` and before the `switch`, add duplicate handling and a post-crawl purge. The final loop body becomes:

```go
	for _, share := range shares {
		if s.paused() {
			return false, errPaused
		}
		s.logger.Printf("event=share_crawl_started share=%s", share.ShareCode)
		err := s.runner.CrawlShare(ctx, share, now)
		var dup *crawler.DuplicateShareError
		if errors.As(err, &dup) {
			if e := s.registry.MarkShareDuplicate(ctx, share.ShareCode, dup.Canonical); e != nil {
				return false, e
			}
			s.logger.Printf("event=share_crawl_finished share=%s result=duplicate canonical=%s", share.ShareCode, dup.Canonical)
			continue
		}
		// Purge any rows a deleted share's in-flight crawl resurrected.
		// (A still-present share is a no-op here.)
		if _, e := s.registry.PurgeIfOrphan(ctx, share.ShareCode); e != nil {
			return false, e
		}
		switch {
		case err == nil:
			// ... unchanged success / dead / failure branches ...
```

The `PurgeIfOrphan` call sits before the `switch` so it covers every non-duplicate outcome (success, dead, failure). Duplicates `continue` before it; their `share` row still exists so it would be a no-op anyway.

- [ ] **Step 9: Run scheduler tests to verify they pass**

Run: `go test ./internal/scheduler/ -v`
Expected: PASS (all scheduler tests).

- [ ] **Step 10: Commit**

```bash
git add internal/store/share_race.go internal/store/share_race_test.go internal/scheduler/scheduler.go internal/scheduler/scheduler_test.go
git commit -m "feat(scheduler): mark duplicate shares and purge post-crawl orphans"
```

---

### Task 5: `dedupe-shares-by-size` mode (clean already-indexed duplicates)

**Files:**
- Modify: `internal/store/dedup.go` (add `DedupeSharesBySize`, `DedupeAction`)
- Modify: `internal/store/dedup_test.go`
- Modify: `cmd/115-indexer/main.go` (new mode + `-dedupe-min-size` flag)

**Interfaces:**
- Consumes: `MarkShareDuplicate` (Task 2).
- Produces:
  - `store.DedupeSharesBySize(ctx, minSize int64, apply bool) ([]DedupeAction, error)`
  - `store.DedupeAction{Loser, Canonical string; FileCount int64}`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/dedup_test.go`:

```go
func TestDedupeSharesBySize(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	mk := func(code string, size int64, crawled int64) {
		t.Helper()
		if err := s.UpsertShare(ctx, model.Share{ShareCode: code, ReceiveCode: "p", Status: "ACTIVE", FileSize: size}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE share SET last_crawled_at=? WHERE share_code=?`, crawled, code); err != nil {
			t.Fatal(err)
		}
	}
	mk("keep", 2<<30, 100)   // oldest -> canonical
	mk("lose", 2<<30, 200)   // same size, later -> loser
	mk("solo", 3<<30, 150)   // unique -> untouched
	// file rows for the loser
	if _, err := s.db.ExecContext(ctx, `INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f1','lose','0','a.mkv','mkv',1,0,1,'',1)`); err != nil {
		t.Fatal(err)
	}

	// dry-run: reports the action, changes nothing
	actions, err := s.DedupeSharesBySize(ctx, 1<<30, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Loser != "lose" || actions[0].Canonical != "keep" || actions[0].FileCount != 1 {
		t.Fatalf("dry-run actions = %#v", actions)
	}
	loser, _, _ := s.GetShare(ctx, "lose")
	if loser.Status != "ACTIVE" {
		t.Fatalf("dry-run must not mutate; status=%s", loser.Status)
	}

	// apply: marks loser DUPLICATE + deletes its files
	if _, err := s.DedupeSharesBySize(ctx, 1<<30, true); err != nil {
		t.Fatal(err)
	}
	loser, _, _ = s.GetShare(ctx, "lose")
	if loser.Status != "DUPLICATE" || loser.DuplicateOf != "keep" {
		t.Fatalf("after apply loser = %+v, want DUPLICATE/keep", loser)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code='lose'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("loser files = %d, want 0", n)
	}
	// canonical untouched
	keep, _, _ := s.GetShare(ctx, "keep")
	if keep.Status != "ACTIVE" {
		t.Fatalf("canonical mutated: %+v", keep)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestDedupeSharesBySize -v`
Expected: FAIL (`DedupeSharesBySize` undefined).

- [ ] **Step 3: Implement `DedupeSharesBySize`**

`internal/store/dedup.go`, add:

```go
// DedupeAction records one share marked as a duplicate by DedupeSharesBySize.
type DedupeAction struct {
	Loser     string
	Canonical string
	FileCount int64
}

// DedupeSharesBySize scans crawlable shares whose file_size >= minSize, groups by
// file_size, and for each group with >1 share marks all but the oldest as
// DUPLICATE of the oldest and deletes their files. With apply=false it returns
// the actions without mutating.
func (s *Store) DedupeSharesBySize(ctx context.Context, minSize int64, apply bool) ([]DedupeAction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT share_code, file_size, COALESCE(last_crawled_at,0), id
		FROM share
		WHERE file_size >= ? AND file_size > 0 AND status IN ('ACTIVE','STALE','QUARANTINE')
		ORDER BY file_size, COALESCE(last_crawled_at,0) ASC, id ASC`, minSize)
	if err != nil {
		return nil, fmt.Errorf("dedupe scan: %w", err)
	}
	defer rows.Close()

	type entry struct {
		code string
		id   int64
	}
	groups := map[int64][]entry{}
	for rows.Next() {
		var (
			code string
			size int64
			_    int64
			id   int64
		)
		if err := rows.Scan(&code, &size, &_, &id); err != nil {
			return nil, err
		}
		groups[size] = append(groups[size], entry{code, id})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var actions []DedupeAction
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		// members sorted oldest-first by the ORDER BY (last_crawled_at, id).
		canonical := members[0].code
		for _, m := range members[1:] {
			var fc int64
			_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code = ?`, m.code).Scan(&fc)
			actions = append(actions, DedupeAction{Loser: m.code, Canonical: canonical, FileCount: fc})
		}
	}
	if !apply {
		return actions, nil
	}
	// Execute: mark each loser DUPLICATE + delete its files.
	for _, a := range actions {
		if err := s.MarkShareDuplicate(ctx, a.Loser, a.Canonical); err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM file WHERE share_code = ?`, a.Loser); err != nil {
			return nil, err
		}
	}
	return actions, nil
}
```

(Counts are resolved once in the action-building loop so both dry-run and apply report them; the test checks `FileCount == 1` on the dry-run action.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestDedupeSharesBySize -v`
Expected: PASS.

- [ ] **Step 5: Wire the CLI mode + flag**

`cmd/115-indexer/main.go`:

Add a flag (with the other flags near `-apply`):

```go
	dedupeMinSize = flag.Int64("dedupe-min-size", 1<<30, "minimum file_size in bytes for share dedup by size (default 1GiB)")
```

Add a case in the `switch *mode`:

```go
	case "dedupe-shares-by-size":
		actions, err := s.DedupeSharesBySize(ctx, *dedupeMinSize, *apply)
		if err != nil {
			log.Fatalf("dedupe shares by size: %v", err)
		}
		for _, a := range actions {
			fmt.Fprintf(os.Stdout, "duplicate share=%s canonical=%s files=%d\n", a.Loser, a.Canonical, a.FileCount)
		}
		if !*apply {
			fmt.Fprintf(os.Stdout, "%d duplicate(s) (dry-run; pass -apply to mark DUPLICATE and delete files)\n", len(actions))
		} else {
			fmt.Fprintf(os.Stdout, "marked %d duplicate share(s)\n", len(actions))
		}
```

- [ ] **Step 6: Build + run mode help**

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 7: Commit**

```bash
git add internal/store/dedup.go internal/store/dedup_test.go cmd/115-indexer/main.go
git commit -m "feat(store): add dedupe-shares-by-size mode"
```

---

### Task 6: `cleanup-orphans` mode (clean orphan files)

**Files:**
- Create: `internal/store/orphans.go`
- Create: `internal/store/orphans_test.go`
- Modify: `cmd/115-indexer/main.go` (new mode)

**Interfaces:**
- Produces:
  - `store.OrphanShares(ctx) ([]OrphanShare, error)`, `store.OrphanShare{ShareCode string; FileCount int64}`
  - `store.DeleteOrphans(ctx) (int64, error)`

- [ ] **Step 1: Write the failing test**

`internal/store/orphans_test.go`:

```go
package store

import (
	"context"
	"testing"

	"five/internal/model"
)

func seedFile(t *testing.T, s *Store, q string) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestOrphans(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "alive", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	seedFile(t, s, `INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fa','alive','0','a.mkv','mkv',1,0,1,'',1)`)
	seedFile(t, s, `INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f1','ghost','0','g.mkv','mkv',1,0,1,'',1)`)
	seedFile(t, s, `INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f2','ghost','0','h.mkv','mkv',1,0,1,'',1)`)
	seedFile(t, s, `INSERT INTO crawl_checkpoint(share_code, cid, next_offset, active_depth, queue_json, visited_json, updated_at) VALUES('ghost','0',0,0,'[]','{}',1)`)

	// list
	orphans, err := s.OrphanShares(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].ShareCode != "ghost" || orphans[0].FileCount != 2 {
		t.Fatalf("orphans = %#v", orphans)
	}

	// delete
	n, err := s.DeleteOrphans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 { // 2 files + 1 checkpoint
		t.Fatalf("deleted = %d, want 3", n)
	}
	orphans, _ = s.OrphanShares(ctx)
	if len(orphans) != 0 {
		t.Fatalf("orphans after delete = %#v", orphans)
	}
	var alive int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code='alive'`).Scan(&alive); err != nil {
		t.Fatal(err)
	}
	if alive != 1 {
		t.Fatalf("alive files = %d, want 1", alive)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestOrphans -v`
Expected: FAIL (`OrphanShares`/`DeleteOrphans` undefined).

- [ ] **Step 3: Implement**

`internal/store/orphans.go`:

```go
package store

import (
	"context"
	"fmt"
)

// OrphanShare is a file row whose share_code no longer has a share row.
type OrphanShare struct {
	ShareCode string
	FileCount int64
}

// OrphanShares lists orphan file groups (share_code + file count) for dry-run
// review before cleanup.
func (s *Store) OrphanShares(ctx context.Context) ([]OrphanShare, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT share_code, COUNT(*)
		FROM file
		WHERE share_code NOT IN (SELECT share_code FROM share)
		GROUP BY share_code`)
	if err != nil {
		return nil, fmt.Errorf("list orphans: %w", err)
	}
	defer rows.Close()
	var out []OrphanShare
	for rows.Next() {
		var o OrphanShare
		if err := rows.Scan(&o.ShareCode, &o.FileCount); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteOrphans removes file and crawl_checkpoint rows whose share_code is not in
// the share table. Returns the total number of rows deleted.
func (s *Store) DeleteOrphans(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM file WHERE share_code NOT IN (SELECT share_code FROM share);`)
	if err != nil {
		return 0, fmt.Errorf("delete orphan files: %w", err)
	}
	files, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	res, err = tx.ExecContext(ctx, `DELETE FROM crawl_checkpoint WHERE share_code NOT IN (SELECT share_code FROM share);`)
	if err != nil {
		return 0, fmt.Errorf("delete orphan checkpoints: %w", err)
	}
	cps, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return files + cps, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestOrphans -v`
Expected: PASS.

- [ ] **Step 5: Wire the CLI mode**

`cmd/115-indexer/main.go`, add a case:

```go
	case "cleanup-orphans":
		orphans, err := s.OrphanShares(ctx)
		if err != nil {
			log.Fatalf("list orphans: %v", err)
		}
		var total int64
		for _, o := range orphans {
			fmt.Fprintf(os.Stdout, "orphan share=%s files=%d\n", o.ShareCode, o.FileCount)
			total += o.FileCount
		}
		if !*apply {
			fmt.Fprintf(os.Stdout, "%d orphan share(s), %d orphan files (dry-run; pass -apply to delete)\n", len(orphans), total)
			return
		}
		n, err := s.DeleteOrphans(ctx)
		if err != nil {
			log.Fatalf("delete orphans: %v", err)
		}
		fmt.Fprintf(os.Stdout, "deleted %d orphan rows (files + checkpoints)\n", n)
```

- [ ] **Step 6: Build**

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 7: Commit**

```bash
git add internal/store/orphans.go internal/store/orphans_test.go cmd/115-indexer/main.go
git commit -m "feat(store): add cleanup-orphans mode"
```

---

### Task 7: Export excludes `DUPLICATE`

**Files:**
- Modify: `internal/store/sqlite.go:120-123` (`ExportTrimmed` prune clause)
- Modify: `internal/store/sqlite_test.go` (add test) — or a dedicated export test if one exists; check first.

**Interfaces:**
- Consumes: `DUPLICATE` status (Task 2).

- [ ] **Step 1: Write the failing test**

Append to `internal/store/sqlite_test.go` (or the existing export test file). Seed an `ACTIVE` share with a file, a `DEAD` share, and a `DUPLICATE` share with a file; call `ExportTrimmed`; open the trimmed DB and assert `DUPLICATE` and `DEAD` shares (and their files) are absent, the `ACTIVE` share + file remain.

```go
func TestExportTrimmedPrunesDuplicateAndDead(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, sh := range []model.Share{
		{ShareCode: "alive", ReceiveCode: "p", Status: "ACTIVE"},
		{ShareCode: "dead", ReceiveCode: "p", Status: "DEAD"},
		{ShareCode: "dup", ReceiveCode: "p", Status: "DUPLICATE"},
	} {
		if err := s.UpsertShare(ctx, sh); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fa','alive','0','a.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fd','dead','0','d.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fx','dup','0','x.mkv','mkv',1,0,1,'',1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "trimmed.db")
	if err := s.ExportTrimmed(ctx, dest); err != nil {
		t.Fatalf("export: %v", err)
	}
	d, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, code := range []string{"dead", "dup"} {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM share WHERE share_code=?`, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("share %s survived export, want pruned", code)
		}
	}
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM share WHERE share_code='alive'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("alive share = %d, want 1", n)
	}
}
```

Add imports `"database/sql"` if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestExportTrimmedPrunesDuplicateAndDead -v`
Expected: FAIL (`dup` survives — currently only `DEAD` is pruned).

- [ ] **Step 3: Extend the prune clause**

`internal/store/sqlite.go`, in `ExportTrimmed`, change the two prune statements from `status = 'DEAD'` to `status IN ('DEAD','DUPLICATE')`:

```go
	for _, stmt := range []string{
		`DELETE FROM file WHERE share_code IN (SELECT share_code FROM share WHERE status IN ('DEAD','DUPLICATE'));`,
		`DELETE FROM share WHERE status IN ('DEAD','DUPLICATE');`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("prune dead/duplicate shares %q: %w", stmt, err)
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestExportTrimmedPrunesDuplicateAndDead -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat(store): prune DUPLICATE shares from export"
```

---

### Task 8: Daemon wiring + README + full verification

**Files:**
- Modify: `cmd/115-indexer/main.go` (daemon crawler `Config.DedupeMinFileSize`)
- Modify: `README.md` (new modes + flag, `DUPLICATE` behavior)

**Interfaces:**
- Consumes: `crawler.Config.DedupeMinFileSize` (Task 3), `-dedupe-min-size` flag (Task 5).

- [ ] **Step 1: Wire the daemon crawler**

`cmd/115-indexer/main.go`, in the `daemon` case, pass the threshold into the crawler config:

```go
		c := crawler.New(lister, s, crawler.Config{PageSize: 100, PauseChecker: gate.Paused, DedupeMinFileSize: *dedupeMinSize})
```

Also apply the same in the `run-scheduler-once` case (so one-shot runs dedup too):

```go
		c := crawler.New(lister, s, crawler.Config{PageSize: 100, DedupeMinFileSize: *dedupeMinSize})
```

- [ ] **Step 2: Update README**

In the Modes table add rows:

```markdown
| `dedupe-shares-by-size` | Mark same-`file_size` (≥ `-dedupe-min-size`) duplicates `DUPLICATE` and delete their files (dry-run unless `-apply`). |
| `cleanup-orphans` | List/delete `file` rows whose share was removed (dry-run unless `-apply`). |
```

Add a short subsection after the crawler pause example explaining duplicate handling:

```markdown
Duplicate shares (identical total `file_size`, by default ≥ 1GiB via
`-dedupe-min-size`) are detected on the first crawl page and marked `DUPLICATE`
without indexing; they are excluded from scheduling and `export-db`.
`-mode dedupe-shares-by-size [-apply]` cleans already-indexed duplicates;
`-mode cleanup-orphans [-apply]` removes files whose share row is gone.
```

- [ ] **Step 3: Full build + tests + race**

Run: `go build ./... && go vet ./... && go test ./... && go test -race ./internal/store/ ./internal/crawler/ ./internal/scheduler/`
Expected: all PASS, clean vet, clean race.

- [ ] **Step 4: Commit**

```bash
git add cmd/115-indexer/main.go README.md
git commit -m "feat: wire dedupe threshold into daemon and document modes"
```

---

## Self-Review (completed)

- **Spec coverage:** §1 first-page dedup → T3+T4; `FindDuplicateShare`/`MarkShareDuplicate` → T2; `duplicate_of`/`DUPLICATE` → T1+T2. §2 `dedupe-shares-by-size` → T5. §3 `cleanup-orphans` → T6. §4 race hardening `PurgeIfOrphan` → T4. Export exclusion → T7. Reactivate clears `duplicate_of` → T2. Daemon wiring + docs → T8.
- **Type consistency:** `FindDuplicateShare(ctx, shareCode, fileSize, minSize) (string,bool,error)` used identically in crawler.Store (T3) and store (T2). `MarkShareDuplicate(ctx, shareCode, canonical) error` identical in Registry (T4) and store (T2). `DedupeAction{Loser,Canonical,FileCount}`, `OrphanShare{ShareCode,FileCount}` consistent.
- **Checked:** no placeholder/TBD steps; all code blocks are valid Go.
