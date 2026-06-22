# Index115 Root-Folder Collapse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the duplicated path segment (`/115分享索引/2025年电影/2025年电影/...`) by collapsing the single root folder of single-root shares, via a `share.root_folder_id` column the indexer backfills and the consumer honors.

**Architecture:** Indexer (`five`) adds a `root_folder_id` column to `share`, backfills it in one local SQL pass (root-level rows only, index-served), and ships it in the export. Consumer (`PowerList`) reads it, collapses `Browse(parent_id="0")` to the root folder's children, and terminates `resolveFullPath` at the root folder. `""` default preserves current behavior for multi-root / single-file-root / zero-root shares and for old indexes lacking the column.

**Tech Stack:** Go, SQLite (`github.com/glebarez/go-sqlite`), table-driven tests. Two repos: `five` (indexer, cwd) and `PowerList` (consumer, `/home/user/GolandProjects/PowerList`).

---

## File Structure

### Indexer (`five`, cwd `/home/user/workspace/five`)

- `internal/store/sqlite.go` — schema (`CREATE TABLE share`, `ensureColumns`), new `BackfillRootFolderIDs` method.
- `cmd/115-indexer/main.go` — new `backfill-root-folder` CLI mode; call backfill in `export-db` before copy.
- `internal/store/sqlite_test.go` — tests for column default, backfill correctness/idempotency, export carries column.

### Consumer (`PowerList`, `/home/user/GolandProjects/PowerList`)

- `internal/index115/store.go` — `shareMeta.RootFolderID`; `Store` schema-detection fields; `ensureShareSchemaDetected`; conditional `RefreshShares` SELECT; `ListChildren` collapse; `resolveFullPath` terminator.
- `internal/index115/store_test.go` — tests for reading the column, legacy (no-column) path, collapse, no-collapse multi-root, path termination. New helper `openTestStoreWithRootFolderID`.

`service.go`, `search.go`, `model.go`, `runtime.go` are **unchanged** — collapse happens inside `ListChildren`, detection piggybacks on `RefreshShares` (called from `OpenStoreRuntime`).

---

## Part A — Indexer (`five`)

Implement and verify Part A first; it produces the shipped artifact Part B depends on.

> **Heads-up — pre-existing uncommitted changes.** At plan time, `internal/store/sqlite.go` (and `internal/searchindex/*`) already carry uncommitted modifications unrelated to this feature. The line numbers above are current-tree-accurate. When committing, stage only the hunks this plan introduces (e.g. `git add -p internal/store/sqlite.go`) so unrelated WIP isn't swept into these commits.

### Task 1: Add `root_folder_id` column to `share`

**Files:**
- Modify: `internal/store/sqlite.go` (CREATE TABLE share ~line 140; ensureColumns ~line 206)
- Test: `internal/store/sqlite_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/sqlite_test.go`:

```go
func TestSQLiteStoreShareRootFolderIDDefaultsEmpty(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "rc1"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}

	var rfid string
	err = s.db.QueryRowContext(ctx, `SELECT root_folder_id FROM share WHERE share_code = 'sw1'`).Scan(&rfid)
	if err != nil {
		t.Fatalf("select root_folder_id (column should exist after migrate): %v", err)
	}
	if rfid != "" {
		t.Fatalf("expected default '', got %q", rfid)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/workspace/five && go test ./internal/store/ -run TestSQLiteStoreShareRootFolderIDDefaultsEmpty -v`
Expected: FAIL — `no such column: root_folder_id`.

- [ ] **Step 3: Add the column to CREATE TABLE**

In `internal/store/sqlite.go`, in the `CREATE TABLE IF NOT EXISTS share (...)` statement (the `migrate` slice, ~line 136), add the column right after `share_title`:

```go
		`CREATE TABLE IF NOT EXISTS share (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			share_code TEXT NOT NULL,
			receive_code TEXT NOT NULL,
			share_title TEXT NOT NULL DEFAULT '',
			root_folder_id TEXT NOT NULL DEFAULT '',
			file_size INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'ACTIVE',
			last_crawled_at INTEGER,
			last_error TEXT,
			failure_count INTEGER NOT NULL DEFAULT 0,
			retry_after_unix INTEGER NOT NULL DEFAULT 0,
			version INTEGER NOT NULL DEFAULT 0,
			UNIQUE(share_code, receive_code)
		);`,
```

- [ ] **Step 4: Add the column to ensureColumns (already-deployed DBs)**

In the same file, the `ensureColumns(ctx, "share", ...)` call (~line 206), append the new column to the slice:

```go
	if err := s.ensureColumns(ctx, "share", []columnDef{
		{name: "share_title", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "file_size", ddl: "INTEGER NOT NULL DEFAULT 0"},
		{name: "root_folder_id", ddl: "TEXT NOT NULL DEFAULT ''"},
	}); err != nil {
		return fmt.Errorf("migrate share columns: %w", err)
	}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /home/user/workspace/five && go test ./internal/store/ -run TestSQLiteStoreShareRootFolderIDDefaultsEmpty -v`
Expected: PASS.

- [ ] **Step 6: Run the full store test suite (regression)**

Run: `cd /home/user/workspace/five && go test ./internal/store/ -v`
Expected: PASS (all existing tests still green).

- [ ] **Step 7: Commit**

```bash
cd /home/user/workspace/five
git add internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat(store): add share.root_folder_id column"
```

---

### Task 2: `BackfillRootFolderIDs` store method

**Files:**
- Modify: `internal/store/sqlite.go` (new method, place near `ExportTrimmed` ~line 79)
- Test: `internal/store/sqlite_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/sqlite_test.go`:

```go
func TestBackfillRootFolderIDs(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().Unix()

	// sw1: single root dir -> root_folder_id = "d1"
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "rc1"}); err != nil {
		t.Fatalf("upsert sw1: %v", err)
	}
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "Movies", IsDir: true, Depth: 1, CrawledAt: now},
		{FileID: "f1", ShareCode: "sw1", ParentID: "d1", Name: "a.mkv", Depth: 2, CrawledAt: now},
	}); err != nil {
		t.Fatalf("upsert sw1 files: %v", err)
	}

	// sw2: two root dirs -> "" (no collapse)
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw2", ReceiveCode: "rc2"}); err != nil {
		t.Fatalf("upsert sw2: %v", err)
	}
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "d2a", ShareCode: "sw2", ParentID: "0", Name: "A", IsDir: true, Depth: 1, CrawledAt: now},
		{FileID: "d2b", ShareCode: "sw2", ParentID: "0", Name: "B", IsDir: true, Depth: 1, CrawledAt: now},
	}); err != nil {
		t.Fatalf("upsert sw2 files: %v", err)
	}

	// sw3: single root that is a FILE -> "" (no folder to collapse)
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw3", ReceiveCode: "rc3"}); err != nil {
		t.Fatalf("upsert sw3: %v", err)
	}
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "f3", ShareCode: "sw3", ParentID: "0", Name: "lone.mkv", IsDir: false, Depth: 1, CrawledAt: now},
	}); err != nil {
		t.Fatalf("upsert sw3 files: %v", err)
	}

	if _, err := s.BackfillRootFolderIDs(ctx); err != nil {
		t.Fatalf("BackfillRootFolderIDs: %v", err)
	}

	assertRFID := func(code, want string) {
		t.Helper()
		var got string
		if err := s.db.QueryRowContext(ctx, `SELECT root_folder_id FROM share WHERE share_code = ?`, code).Scan(&got); err != nil {
			t.Fatalf("select %s root_folder_id: %v", code, err)
		}
		if got != want {
			t.Fatalf("%s: want root_folder_id %q, got %q", code, want, got)
		}
	}
	assertRFID("sw1", "d1")
	assertRFID("sw2", "")
	assertRFID("sw3", "")

	// Idempotent: re-running does not change values.
	if _, err := s.BackfillRootFolderIDs(ctx); err != nil {
		t.Fatalf("BackfillRootFolderIDs (2nd): %v", err)
	}
	assertRFID("sw1", "d1")
	assertRFID("sw2", "")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/workspace/five && go test ./internal/store/ -run TestBackfillRootFolderIDs -v`
Expected: FAIL — `s.BackfillRootFolderIDs undefined`.

- [ ] **Step 3: Implement `BackfillRootFolderIDs`**

In `internal/store/sqlite.go`, add this method immediately after `ExportTrimmed` (after its closing `}` ~line 118):

```go
// BackfillRootFolderIDs sets share.root_folder_id for every share from the
// current file table: the single root folder's file_id when a share has
// exactly one parent_id='0' row and it is a directory, else ''. It is a pure
// local computation (no API), idempotent, and index-served — it reads only
// each share's root-level rows via idx_file_share_parent, not the full tree.
func (s *Store) BackfillRootFolderIDs(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE share
		SET root_folder_id = COALESCE(
			(SELECT f.file_id
			 FROM file f
			 WHERE f.share_code = share.share_code
			   AND f.parent_id = '0'
			   AND f.is_dir = 1
			   AND (SELECT COUNT(*) FROM file g
			        WHERE g.share_code = share.share_code
			          AND g.parent_id = '0') = 1
			 LIMIT 1),
			'');`)
	if err != nil {
		return 0, fmt.Errorf("backfill root_folder_id: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("backfill root_folder_id rows affected: %w", err)
	}
	return int(n), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/user/workspace/five && go test ./internal/store/ -run TestBackfillRootFolderIDs -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/workspace/five
git add internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat(store): BackfillRootFolderIDs derives single-root share roots"
```

---

### Task 3: Wire backfill into `export-db` + add `backfill-root-folder` CLI mode

**Files:**
- Modify: `cmd/115-indexer/main.go` (export-db case ~line 321; new case near backfill-share-meta ~line 130)
- Test: `internal/store/sqlite_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/sqlite_test.go`:

```go
func TestExportTrimmedCarriesRootFolderID(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().Unix()
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "rc1"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "Movies", IsDir: true, Depth: 1, CrawledAt: now},
		{FileID: "f1", ShareCode: "sw1", ParentID: "d1", Name: "a.mkv", Depth: 2, CrawledAt: now},
	}); err != nil {
		t.Fatalf("upsert files: %v", err)
	}

	if _, err := s.BackfillRootFolderIDs(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	trimmed := filepath.Join(t.TempDir(), "trimmed.db")
	if err := s.ExportTrimmed(ctx, trimmed); err != nil {
		t.Fatalf("export trimmed: %v", err)
	}

	db, err := sql.Open("sqlite", trimmed)
	if err != nil {
		t.Fatalf("open trimmed: %v", err)
	}
	defer db.Close()

	var rfid string
	if err := db.QueryRowContext(ctx, `SELECT root_folder_id FROM share WHERE share_code = 'sw1'`).Scan(&rfid); err != nil {
		t.Fatalf("select from trimmed: %v", err)
	}
	if rfid != "d1" {
		t.Fatalf("trimmed DB root_folder_id: want %q, got %q", "d1", rfid)
	}
}
```

- [ ] **Step 2: Run test to verify it passes already**

Run: `cd /home/user/workspace/five && go test ./internal/store/ -run TestExportTrimmedCarriesRootFolderID -v`
Expected: PASS — `BackfillRootFolderIDs` + `ExportTrimmed` (VACUUM INTO copy) already carry the column; this test locks in that contract.

- [ ] **Step 3: Call backfill in `export-db` before the copy**

In `cmd/115-indexer/main.go`, in the `case "export-db":` block (~line 321), add the backfill call immediately before `trimmedDB := filepath.Join(tmp, "index.db")` (i.e. before `s.ExportTrimmed`). Find the lines:

```go
		tmp, err := os.MkdirTemp("", "five-export-")
		if err != nil {
			log.Fatalf("mkdir temp: %v", err)
		}
		trimmedDB := filepath.Join(tmp, "index.db")
		if err := s.ExportTrimmed(ctx, trimmedDB); err != nil {
			log.Fatalf("export trimmed: %v", err)
		}
```

Change to:

```go
		tmp, err := os.MkdirTemp("", "five-export-")
		if err != nil {
			log.Fatalf("mkdir temp: %v", err)
		}
		if n, err := s.BackfillRootFolderIDs(ctx); err != nil {
			log.Fatalf("backfill root_folder_id: %v", err)
		} else {
			log.Printf("backfilled root_folder_id for %d shares", n)
		}
		trimmedDB := filepath.Join(tmp, "index.db")
		if err := s.ExportTrimmed(ctx, trimmedDB); err != nil {
			log.Fatalf("export trimmed: %v", err)
		}
```

- [ ] **Step 4: Add the `backfill-root-folder` CLI mode**

In `cmd/115-indexer/main.go`, in the `switch mode` (immediately after the `case "backfill-share-meta":` block ends, ~line 161), add a new case:

```go
	case "backfill-root-folder":
		// Pure local derivation (no API): sets share.root_folder_id from the
		// current file table. Re-runnable / idempotent. Run after a crawl, or
		// before export-db (which also runs it automatically).
		n, err := s.BackfillRootFolderIDs(ctx)
		if err != nil {
			log.Fatalf("backfill root folder: %v", err)
		}
		fmt.Fprintf(os.Stdout, "backfilled root_folder_id for %d shares\n", n)
```

- [ ] **Step 5: Build and vet**

Run: `cd /home/user/workspace/five && go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 6: Run full indexer test suite**

Run: `cd /home/user/workspace/five && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /home/user/workspace/five
git add cmd/115-indexer/main.go internal/store/sqlite_test.go
git commit -m "feat(cli): backfill-root-folder mode + run it in export-db"
```

- [ ] **Step 8: Re-export and ship (manual, distribution pipeline)**

Run the indexer export to produce a new `115.index.zip` whose `index.db` carries `root_folder_id`:

```bash
cd /home/user/workspace/five
go run ./cmd/115-indexer export-db -out dist/115.index.zip   # adjust -out to your real path
```

Then upload the zip to 115 and bump `d.example.com/115.version.txt` per the distribution runbook so alist-tvbox fetches it. (Part B degrades gracefully on an old zip, so this can happen before or after Part B.)

---

## Part B — Consumer (`PowerList`)

All paths below are under `/home/user/GolandProjects/PowerList`. `cd` there for every command.

### Task 4: Read `root_folder_id` with legacy-DB detection

**Files:**
- Modify: `internal/index115/store.go` (`shareMeta` ~line 11; `Store` ~line 20; `RefreshShares` ~line 32)
- Test: `internal/index115/store_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/index115/store_test.go`. First add a small `mustExec` helper near the bottom (after `insertTestFile`):

```go
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// openTestStoreWithRootFolderID mirrors openTestStore but creates the share
// table WITH the root_folder_id column, to exercise the collapse path.
func openTestStoreWithRootFolderID(t *testing.T, dbPath string) *Store {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stmts := []string{
		`CREATE TABLE share (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			share_code TEXT NOT NULL,
			receive_code TEXT NOT NULL DEFAULT '',
			share_title TEXT NOT NULL DEFAULT '',
			root_folder_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'ACTIVE',
			last_crawled_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE file (
			file_id TEXT PRIMARY KEY,
			share_code TEXT NOT NULL,
			parent_id TEXT NOT NULL,
			name TEXT NOT NULL,
			path TEXT NOT NULL,
			ext TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0,
			is_dir INTEGER NOT NULL DEFAULT 0,
			depth INTEGER NOT NULL DEFAULT 0,
			sha1 TEXT NOT NULL DEFAULT '',
			updated_at INTEGER,
			crawled_at INTEGER NOT NULL DEFAULT 0
		);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("db.Exec(%q) error = %v", stmt, err)
		}
	}

	return &Store{db: db}
}
```

Then add the two tests:

```go
func TestRefreshSharesReadsRootFolderID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := openTestStoreWithRootFolderID(t, dbPath)

	insertTestShare(t, store.db, testShareRow{
		ShareCode:     "sw1",
		ReceiveCode:   "rc1",
		ShareTitle:    "Movies",
		Status:        "ACTIVE",
		LastCrawledAt: 1,
	})
	mustExec(t, store.db, `UPDATE share SET root_folder_id = 'd1' WHERE share_code = 'sw1'`)

	if err := store.RefreshShares(context.Background()); err != nil {
		t.Fatalf("RefreshShares() error = %v", err)
	}
	if got := store.shares["sw1"].RootFolderID; got != "d1" {
		t.Fatalf("RootFolderID = %q, want %q", got, "d1")
	}
}

func TestRefreshSharesHandlesMissingRootFolderIDColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := openTestStore(t, dbPath) // legacy schema: no root_folder_id column

	insertTestShare(t, store.db, testShareRow{
		ShareCode:     "sw1",
		ReceiveCode:   "rc1",
		ShareTitle:    "Movies",
		Status:        "ACTIVE",
		LastCrawledAt: 1,
	})

	if err := store.RefreshShares(context.Background()); err != nil {
		t.Fatalf("RefreshShares() on legacy DB should not error: %v", err)
	}
	if got := store.shares["sw1"].RootFolderID; got != "" {
		t.Fatalf("RootFolderID on legacy DB = %q, want %q", got, "")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -run 'TestRefreshSharesReadsRootFolderID|TestRefreshSharesHandlesMissingRootFolderIDColumn' -v`
Expected: FAIL — `shareMeta.RootFolderID undefined` (compile error) / detection not present.

- [ ] **Step 3: Add `RootFolderID` to `shareMeta` and detection fields to `Store`**

In `internal/index115/store.go`, edit the two structs:

```go
type shareMeta struct {
	ShareCode     string
	ReceiveCode   string
	ShareTitle    string
	RootFolderID  string
	Status        string
	LastCrawledAt int64
	ID            int64
}

type Store struct {
	db              *sql.DB
	shares          map[string]shareMeta
	hasRootFolderID bool
	schemaDetected  bool
}
```

- [ ] **Step 4: Add `ensureShareSchemaDetected`**

In `internal/index115/store.go`, add this method immediately after `OpenStore`:

```go
// ensureShareSchemaDetected probes the share table once for the root_folder_id
// column so RefreshShares can build a query that works on both new indexes
// (column present) and old published indexes (column absent -> no collapse).
func (s *Store) ensureShareSchemaDetected(ctx context.Context) error {
	if s.schemaDetected {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(share)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notNull int
			dflt    any
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "root_folder_id" {
			s.hasRootFolderID = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.schemaDetected = true
	return nil
}
```

- [ ] **Step 5: Make `RefreshShares` detect + conditionally select the column**

In `internal/index115/store.go`, replace the `RefreshShares` function header and query/scan with:

```go
func (s *Store) RefreshShares(ctx context.Context) error {
	if err := s.ensureShareSchemaDetected(ctx); err != nil {
		return err
	}
	// Table/column names are compile-time constants, so the formatted SQL is safe.
	rootCol := "''"
	if s.hasRootFolderID {
		rootCol = "COALESCE(root_folder_id, '')"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, share_code, COALESCE(receive_code, ''), COALESCE(share_title, ''), status, COALESCE(last_crawled_at, 0), %s
		FROM share`, rootCol))
	if err != nil {
		return err
	}
	defer rows.Close()

	shares := map[string]shareMeta{}
	for rows.Next() {
		var meta shareMeta
		if err := rows.Scan(&meta.ID, &meta.ShareCode, &meta.ReceiveCode, &meta.ShareTitle, &meta.Status, &meta.LastCrawledAt, &meta.RootFolderID); err != nil {
			return err
		}
		current, ok := shares[meta.ShareCode]
		if !ok || preferShareMeta(meta, current) {
			shares[meta.ShareCode] = meta
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.shares = shares
	return nil
}
```

(`fmt` is already imported in `store.go`.)

- [ ] **Step 6: Run the two tests to verify they pass**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -run 'TestRefreshSharesReadsRootFolderID|TestRefreshSharesHandlesMissingRootFolderIDColumn' -v`
Expected: PASS.

- [ ] **Step 7: Run the existing consumer test suite (regression)**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -v`
Expected: PASS — legacy `openTestStore` DBs (no column) are detected as such and behave exactly as before.

- [ ] **Step 8: Commit**

```bash
cd /home/user/GolandProjects/PowerList
git add internal/index115/store.go internal/index115/store_test.go
git commit -m "feat(index115): read share.root_folder_id with legacy-DB detection"
```

---

### Task 5: Collapse in `ListChildren` + terminate `resolveFullPath`

**Files:**
- Modify: `internal/index115/store.go` (`ListChildren` ~line 110; `resolveFullPath` ~line 168)
- Test: `internal/index115/store_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/index115/store_test.go`:

```go
func TestListChildrenCollapsesSingleRootFolder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := openTestStoreWithRootFolderID(t, dbPath)

	insertTestShare(t, store.db, testShareRow{
		ShareCode: "sw1", ReceiveCode: "rc1", ShareTitle: "Movies",
		Status: "ACTIVE", LastCrawledAt: 1,
	})
	mustExec(t, store.db, `UPDATE share SET root_folder_id = 'd1' WHERE share_code = 'sw1'`)
	insertTestFile(t, store.db, testFileRow{
		FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "Movies", Path: "/Movies", IsDir: true, UpdatedAt: 10,
	})
	insertTestFile(t, store.db, testFileRow{
		FileID: "f1", ShareCode: "sw1", ParentID: "d1", Name: "a.mkv", Path: "/a.mkv", Ext: ".mkv", Size: 1024, UpdatedAt: 20,
	})

	if err := store.RefreshShares(context.Background()); err != nil {
		t.Fatalf("RefreshShares() error = %v", err)
	}

	items, err := store.ListChildren(context.Background(), "sw1", "0")
	if err != nil {
		t.Fatalf("ListChildren() error = %v", err)
	}
	if len(items) != 1 || items[0].FileID != "f1" {
		t.Fatalf("expected collapsed child f1 only, got %+v", items)
	}

	// resolveFullPath terminates at the root folder -> path has no "Movies" prefix.
	file, ok, err := store.FileWithFullPath(context.Background(), "f1")
	if err != nil {
		t.Fatalf("FileWithFullPath() error = %v", err)
	}
	if !ok {
		t.Fatal("expected file f1 to exist")
	}
	if file.Path != "/a.mkv" {
		t.Fatalf("Path = %q, want %q (root folder name must be dropped)", file.Path, "/a.mkv")
	}
}

func TestListChildrenNoCollapseWhenMultiRoot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := openTestStoreWithRootFolderID(t, dbPath)

	insertTestShare(t, store.db, testShareRow{
		ShareCode: "sw1", ReceiveCode: "rc1", ShareTitle: "Mix",
		Status: "ACTIVE", LastCrawledAt: 1,
	})
	// root_folder_id stays '' (default) for a multi-root share.
	insertTestFile(t, store.db, testFileRow{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "A", Path: "/A", IsDir: true, UpdatedAt: 10})
	insertTestFile(t, store.db, testFileRow{FileID: "d2", ShareCode: "sw1", ParentID: "0", Name: "B", Path: "/B", IsDir: true, UpdatedAt: 10})

	if err := store.RefreshShares(context.Background()); err != nil {
		t.Fatalf("RefreshShares() error = %v", err)
	}

	items, err := store.ListChildren(context.Background(), "sw1", "0")
	if err != nil {
		t.Fatalf("ListChildren() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected both root dirs (no collapse), got %d items: %+v", len(items), items)
	}
}

func TestLegacyIndexNoCollapse(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := openTestStore(t, dbPath) // legacy schema: no root_folder_id column

	insertTestShare(t, store.db, testShareRow{ShareCode: "sw1", ShareTitle: "Movies", Status: "ACTIVE", LastCrawledAt: 1})
	insertTestFile(t, store.db, testFileRow{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "Movies", Path: "/Movies", IsDir: true, UpdatedAt: 10})
	insertTestFile(t, store.db, testFileRow{FileID: "f1", ShareCode: "sw1", ParentID: "d1", Name: "a.mkv", Path: "/a.mkv", UpdatedAt: 20})

	if err := store.RefreshShares(context.Background()); err != nil {
		t.Fatalf("RefreshShares() error = %v", err)
	}

	// Legacy behavior: parent_id="0" lists the root dir, no collapse.
	items, err := store.ListChildren(context.Background(), "sw1", "0")
	if err != nil {
		t.Fatalf("ListChildren() error = %v", err)
	}
	if len(items) != 1 || items[0].FileID != "d1" {
		t.Fatalf("legacy: expected root dir d1, got %+v", items)
	}

	// Legacy behavior: resolveFullPath walks to "0" -> includes root folder name.
	file, _, err := store.FileWithFullPath(context.Background(), "f1")
	if err != nil {
		t.Fatalf("FileWithFullPath() error = %v", err)
	}
	if file.Path != "/Movies/a.mkv" {
		t.Fatalf("legacy Path = %q, want %q", file.Path, "/Movies/a.mkv")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -run 'TestListChildrenCollapsesSingleRootFolder|TestListChildrenNoCollapseWhenMultiRoot|TestLegacyIndexNoCollapse' -v`
Expected: FAIL — collapse/path-termination not implemented (collapse test gets `d1` back; path test gets `/Movies/a.mkv`).

- [ ] **Step 3: Collapse in `ListChildren`**

In `internal/index115/store.go`, edit `ListChildren` to remap the share root to the root folder when set:

```go
func (s *Store) ListChildren(ctx context.Context, shareCode, parentID string) ([]FileItem, error) {
	if parentID == "0" {
		if root := s.shares[shareCode].RootFolderID; root != "" {
			parentID = root
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT file_id, share_code, parent_id, name, path, size, is_dir, ext, sha1, COALESCE(updated_at, 0)
		FROM file
		WHERE share_code = ? AND parent_id = ?
		ORDER BY is_dir DESC, name ASC`, shareCode, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	meta := s.shares[shareCode]
	var items []FileItem
	for rows.Next() {
		item, err := scanFileItem(rows)
		if err != nil {
			return nil, err
		}
		applyShareMeta(&item, meta)
		items = append(items, item)
	}
	return items, rows.Err()
}
```

- [ ] **Step 4: Terminate `resolveFullPath` at the root folder**

In `internal/index115/store.go`, edit `resolveFullPath`:

```go
func (s *Store) resolveFullPath(ctx context.Context, item FileItem) string {
	rootFolderID := s.shares[item.ShareCode].RootFolderID
	segments := []string{item.Name}
	parentID := item.ParentID
	for i := 0; i < 64 && parentID != "" && parentID != "0" && parentID != rootFolderID; i++ {
		parent, ok, err := s.FileByID(ctx, parentID)
		if err != nil || !ok {
			break
		}
		segments = append([]string{parent.Name}, segments...)
		parentID = parent.ParentID
	}
	return "/" + strings.Join(segments, "/")
}
```

- [ ] **Step 5: Run the three tests to verify they pass**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -run 'TestListChildrenCollapsesSingleRootFolder|TestListChildrenNoCollapseWhenMultiRoot|TestLegacyIndexNoCollapse' -v`
Expected: PASS.

- [ ] **Step 6: Run the full consumer test suite**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /home/user/GolandProjects/PowerList
git add internal/index115/store.go internal/index115/store_test.go
git commit -m "feat(index115): collapse single-root share folder in browse + path"
```

---

## Final verification

- [ ] **Both suites green**

```bash
cd /home/user/workspace/five && go test ./...
cd /home/user/GolandProjects/PowerList && go test ./internal/index115/...
```
Expected: PASS in both.

- [ ] **End-to-end smoke (optional, once a new zip is shipped)**

After re-exporting and letting alist-tvbox swap the index: open a single-root share in the TVBox/index115 UI and confirm the redundant `2025年电影/2025年电影` level is gone — drilling into the share lists the root folder's children directly, and a file's play path is `/115分享索引/2025年电影/亡命之徒(2025)` (one segment, not two).

---

## Self-Review notes

- Spec coverage: schema column (Task 1), backfill method + performance/SQL (Task 2), export wiring + CLI mode (Task 3), consumer read + legacy detection (Task 4), browse collapse + path termination + unchanged search/ListShares/link (Task 5). Search is intentionally untouched and asserted unchanged via the legacy/multi-root tests.
- The `service.Browse`, `search.go`, `model.go`, `runtime.go` files are not modified — collapse is localized to `ListChildren`/`resolveFullPath`, and detection piggybacks on `RefreshShares` (already called from `OpenStoreRuntime` and every test).
- `root_folder_id` is never exposed in `/index115` API responses (no `FileItem`/`ShareSummary` change), matching the spec.
