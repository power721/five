# 115 Index Publish (`five`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `./five -mode export-db -out 115.index.zip` produce a zip containing a trimmed `index.db` (only `file`+`share`) and the READY bleve index under `bleve/`.

**Architecture:** Add `Store.ExportTrimmed` (VACUUM INTO → DROP crawler tables → VACUUM, single-file). Add `buildPackage` in the CLI to zip the trimmed DB + bleve dir. Rewire the `export-db` mode to resolve the bleve source (manifest or newest `index_*`) and call both.

**Tech Stack:** Go 1.26, `database/sql` + `modernc.org/sqlite` v1.39.1, `archive/zip`, bleve v2.6.0.

**Spec:** `docs/superpowers/specs/2026-06-21-115-index-publish-design.md`

---

## File structure

- Modify `internal/store/sqlite.go` — add `ExportTrimmed`.
- Modify `internal/store/sqlite_test.go` — add trim test.
- Create `cmd/115-indexer/package.go` — `buildPackage` + `newestBleveIndex` + `addFileToZip`.
- Create `cmd/115-indexer/package_test.go` — zip-layout + newest-index tests.
- Modify `cmd/115-indexer/main.go` — rewire `export-db` case.

---

### Task 1: `Store.ExportTrimmed` (file + share only)

**Files:**
- Modify: `internal/store/sqlite.go` (append after `ExportSnapshot`, ~line 73)
- Test: `internal/store/sqlite_test.go` (append)

- [ ] **Step 1: Write failing test**

Append to `sqlite_test.go`:

```go
func TestExportTrimmedKeepsOnlyFileAndShare(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "rc1"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Path: "/a.mkv", Ext: "mkv", CrawledAt: 1},
	}); err != nil {
		t.Fatalf("upsert files: %v", err)
	}
	// Populate the tables that must be dropped.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO crawl_checkpoint(share_code,cid,next_offset,active_path,active_depth,queue_json,visited_json,updated_at) VALUES('sw1','0',0,'',0,'[]','{}',1)`); err != nil {
		t.Fatalf("insert checkpoint: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO index_event(file_id,op,created_at) VALUES('f1','upsert',1)`); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if err := s.UpdateManifest(ctx, model.IndexManifest{Version: 1, IndexPath: "x", Status: "READY", BuiltAt: 1, FileCount: 1}); err != nil {
		t.Fatalf("update manifest: %v", err)
	}
	if err := s.SaveKV(ctx, "k", "v"); err != nil {
		t.Fatalf("save kv: %v", err)
	}

	trimmed := filepath.Join(t.TempDir(), "trimmed.db")
	if err := s.ExportTrimmed(ctx, trimmed); err != nil {
		t.Fatalf("export trimmed: %v", err)
	}
	s.Close()

	// Prove self-containment: copy only the .db (no sidecars) to a fresh path.
	data, err := os.ReadFile(trimmed)
	if err != nil {
		t.Fatalf("read trimmed: %v", err)
	}
	isolated := filepath.Join(t.TempDir(), "isolated.db")
	if err := os.WriteFile(isolated, data, 0o644); err != nil {
		t.Fatalf("write isolated: %v", err)
	}
	db, err := sql.Open("sqlite", isolated)
	if err != nil {
		t.Fatalf("open isolated: %v", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if got := strings.Join(tables, ","); got != "file,share" {
		t.Fatalf("tables = %q, want file,share", got)
	}

	var files, shares int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM file").Scan(&files)
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM share").Scan(&shares)
	if files != 1 || shares != 1 {
		t.Fatalf("counts files=%d shares=%d, want 1/1", files, shares)
	}

	var idx int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='file' AND name LIKE 'idx_file_%'").Scan(&idx)
	if idx != 5 {
		t.Fatalf("file indexes = %d, want 5", idx)
	}
}
```

- [ ] **Step 2: Run, verify it fails**

Run: `go test ./internal/store/ -run TestExportTrimmedKeepsOnlyFileAndShare -v`
Expected: FAIL — `s.ExportTrimmed undefined`.

- [ ] **Step 3: Implement `ExportTrimmed`**

Append to `internal/store/sqlite.go` after `ExportSnapshot`:

```go
// ExportTrimmed writes a single-file copy of the database containing only the
// file and share tables (with their indexes), for shipping to consumers.
// Crawler/indexer internals (checkpoints, events, manifest, kv) are excluded.
// destPath is overwritten if it exists; the result has no -wal/-shm sidecar.
func (s *Store) ExportTrimmed(ctx context.Context, destPath string) error {
	if dir := filepath.Dir(destPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	quoted := "'" + strings.ReplaceAll(destPath, "'", "''") + "'"
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("vacuum into %s: %w", destPath, err)
	}
	db, err := sql.Open("sqlite", destPath)
	if err != nil {
		return fmt.Errorf("open trimmed: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// DELETE journal so the subsequent VACUUM produces a sidecar-free file.
	for _, pragma := range []string{"PRAGMA journal_mode=DELETE;", "PRAGMA synchronous=NORMAL;"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS crawl_checkpoint;`,
		`DROP TABLE IF EXISTS index_event;`,
		`DROP TABLE IF EXISTS index_manifest;`,
		`DROP TABLE IF EXISTS kv;`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("trim %q: %w", stmt, err)
		}
	}
	if _, err := db.ExecContext(ctx, "VACUUM;"); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run, verify pass + no regression**

Run: `go test ./internal/store/ -v`
Expected: PASS (incl. existing `TestSQLiteStoreExportSnapshotIsSelfContained`).

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat(store): ExportTrimmed ships only file+share tables"
```

---

### Task 2: `buildPackage` + `newestBleveIndex` (CLI zip)

**Files:**
- Create: `cmd/115-indexer/package.go`
- Create: `cmd/115-indexer/package_test.go`

- [ ] **Step 1: Write failing tests**

`cmd/115-indexer/package_test.go`:

```go
package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPackageZipLayout(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	if err := os.WriteFile(dbPath, []byte("DB"), 0o644); err != nil {
		t.Fatal(err)
	}
	bleveDir := filepath.Join(dir, "bleve")
	os.MkdirAll(filepath.Join(bleveDir, "store"), 0o755)
	os.WriteFile(filepath.Join(bleveDir, "index_meta.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(bleveDir, "store", "seg"), []byte("seg"), 0o644)

	zipPath := filepath.Join(dir, "out", "115.index.zip")
	if err := buildPackage(dbPath, bleveDir, zipPath); err != nil {
		t.Fatalf("buildPackage: %v", err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	want := map[string]bool{"index.db": false, "bleve/index_meta.json": false, "bleve/store/seg": false}
	for _, f := range zr.File {
		if _, ok := want[f.Name]; !ok {
			t.Errorf("unexpected entry %q", f.Name)
			continue
		}
		want[f.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing entry %q", name)
		}
	}
}

func TestNewestBleveIndex(t *testing.T) {
	root := t.TempDir()
	if got := newestBleveIndex(root); got != "" {
		t.Fatalf("empty root = %q, want \"\"", got)
	}
	os.MkdirAll(filepath.Join(root, "index_000001_building"), 0o755)
	os.MkdirAll(filepath.Join(root, "index_000007"), 0o755)
	os.MkdirAll(filepath.Join(root, "index_000042"), 0o755)
	got := newestBleveIndex(root)
	want := filepath.Join(root, "index_000042")
	if got != want {
		t.Fatalf("newest = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./cmd/115-indexer/ -run 'TestBuildPackageZipLayout|TestNewestBleveIndex' -v`
Expected: FAIL — `buildPackage`/`newestBleveIndex` undefined.

- [ ] **Step 3: Implement**

`cmd/115-indexer/package.go`:

```go
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// buildPackage writes zipDestPath containing index.db (from trimmedDBPath) and
// the contents of bleveSrcDir under "bleve/". Flat layout: extracts to
// <dir>/index.db + <dir>/bleve/.
func buildPackage(trimmedDBPath, bleveSrcDir, zipDestPath string) error {
	if dir := filepath.Dir(zipDestPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	out, err := os.Create(zipDestPath)
	if err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()

	if err := addFileToZip(zw, trimmedDBPath, "index.db"); err != nil {
		return fmt.Errorf("add index.db: %w", err)
	}
	err = filepath.Walk(bleveSrcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(bleveSrcDir, path)
		if err != nil {
			return err
		}
		return addFileToZip(zw, path, filepath.ToSlash(filepath.Join("bleve", rel)))
	})
	if err != nil {
		return fmt.Errorf("walk bleve: %w", err)
	}
	return nil
}

func addFileToZip(zw *zip.Writer, src, name string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// newestBleveIndex returns the newest non-building "index_%06d" dir under root,
// or "" if none.
func newestBleveIndex(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var newest string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "index_") || strings.HasSuffix(e.Name(), "_building") {
			continue
		}
		if newest == "" || e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return ""
	}
	return filepath.Join(root, newest)
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./cmd/115-indexer/ -run 'TestBuildPackageZipLayout|TestNewestBleveIndex' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/115-indexer/package.go cmd/115-indexer/package_test.go
git commit -m "feat(cli): buildPackage zips trimmed db + bleve"
```

---

### Task 3: Rewire `export-db` mode

**Files:**
- Modify: `cmd/115-indexer/main.go:320-331` (the `case "export-db":` block)

- [ ] **Step 1: Replace the case block**

Replace the entire `case "export-db":` ... through the `fmt.Fprintln(os.Stdout, msg)` block with:

```go
	case "export-db":
		if *outPath == "" {
			log.Fatal("export-db mode requires -out")
		}
		manifest, ok, err := s.LoadManifest(ctx)
		if err != nil {
			log.Fatalf("load manifest: %v", err)
		}
		var bleveSrc string
		switch {
		case ok && manifest.Status == "READY":
			bleveSrc = manifest.IndexPath
		default:
			bleveSrc = newestBleveIndex(*blevePath)
			if bleveSrc == "" {
				log.Fatal("no READY bleve index; run rebuild-index first")
			}
			log.Printf("warning: no READY manifest; using bleve index %s", bleveSrc)
		}
		tmp, err := os.MkdirTemp("", "five-export-")
		if err != nil {
			log.Fatalf("temp dir: %v", err)
		}
		defer os.RemoveAll(tmp)
		trimmedDB := filepath.Join(tmp, "index.db")
		if err := s.ExportTrimmed(ctx, trimmedDB); err != nil {
			log.Fatalf("export trimmed: %v", err)
		}
		if err := buildPackage(trimmedDB, bleveSrc, *outPath); err != nil {
			log.Fatalf("build package: %v", err)
		}
		fmt.Fprintf(os.Stdout, "packaged index to %s (db trimmed to file+share; bleve from %s)\n", *outPath, bleveSrc)
```

- [ ] **Step 2: Build**

Run: `go build ./cmd/115-indexer/`
Expected: compiles clean.

- [ ] **Step 3: Whole-module test**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 4: Manual smoke on real data**

Run:
```bash
go build -o five ./cmd/115-indexer/
./five -mode rebuild-index -db data/index.db -bleve data/bleve   # ensure a READY index exists
./five -mode export-db -db data/index.db -bleve data/bleve -out /tmp/115.index.zip
unzip -l /tmp/115.index.zip | head
sqlite3 /tmp/__check.db <<'EOF'   # optional: verify tables after extracting index.db manually
EOF
```
Expected: zip lists `index.db` + `bleve/...`; no `-wal`/`-shm` inside.

- [ ] **Step 5: Commit**

```bash
git add cmd/115-indexer/main.go
git commit -m "feat(cli): export-db packages trimmed db + bleve into zip"
```

---

### Task 4: README runbook

**Files:**
- Modify: `README.md` (append a "Distribute the index" section)

- [ ] **Step 1: Append runbook**

Append:

```markdown
## Distribute the index

Package a self-contained index for downstream consumers (alist-tvbox / PowerList):

```bash
go run ./cmd/115-indexer -mode export-db -db data/index.db -bleve data/bleve -out 115.index.zip
```

`115.index.zip` contains a trimmed `index.db` (only `file` and `share` tables)
and the READY bleve index under `bleve/`. It extracts to `index.db` + `bleve/`.

Manual publish to 115:

1. Upload `115.index.zip` to the publishing 115 account and create a share.
2. Overwrite the version pointer (`https://d.example.com/115.version.txt`) with one
   line `shareCode:receiveCode` (e.g. `swf01d43zby:6666`).
```
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: index distribution runbook"
```

---

## Self-review

- **Spec coverage:** trimmed DB + zip layout (Tasks 1–3), bleve source resolution (Task 3), runbook (Task 4). All spec sections for the `five` side covered.
- **Placeholders:** none — every step has complete code/commands.
- **Type consistency:** `ExportTrimmed(ctx, destPath)`, `buildPackage(trimmedDBPath, bleveSrcDir, zipDestPath)`, `newestBleveIndex(root)` signatures match across tasks/tests/main.
