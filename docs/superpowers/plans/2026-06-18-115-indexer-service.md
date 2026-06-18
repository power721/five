# 115 Indexer Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone Go service that indexes 115 share snapshots into SQLite and Bleve while exposing stable read-only artifacts for external browse and search apps.

**Architecture:** The service has a 115 API client, SQLite store, resumable BFS crawler, and Bleve index builder. SQLite is the source of truth; Bleve is derived and rebuildable. Browse and search servers are intentionally out of scope.

**Tech Stack:** Go 1.26, `database/sql`, `modernc.org/sqlite`, `github.com/blevesearch/bleve/v2`, standard `net/http` and `httptest`.

---

### Task 1: Project Skeleton and Snap API Parser

**Files:**
- Create: `go.mod`
- Create: `internal/model/model.go`
- Create: `internal/api115/snap_test.go`
- Create: `internal/api115/snap.go`

- [ ] **Step 1: Write failing tests for 115 snap parsing**

Create tests that parse the provided 115 response shape, preserve large numeric ids as strings, and map file and directory nodes into internal file records.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api115 -v`

Expected: FAIL because `internal/api115` implementation does not exist.

- [ ] **Step 3: Implement snap parser and request client**

Implement JSON structs, `NodeID`, `IsDir`, `ToFile`, and a small `Client.List` method.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api115 -v`

Expected: PASS.

### Task 2: SQLite Store

**Files:**
- Create: `internal/store/sqlite_test.go`
- Create: `internal/store/sqlite.go`

- [ ] **Step 1: Write failing store tests**

Test migration, idempotent file upsert, checkpoint save/load, unprocessed index events, and index manifest update.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store -v`

Expected: FAIL because store implementation does not exist.

- [ ] **Step 3: Implement SQLite store**

Implement schema migration, `UpsertFiles`, `SaveCheckpoint`, `LoadCheckpoint`, `PendingIndexEvents`, `MarkIndexEventsProcessed`, `AllFiles`, and `UpdateManifest`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store -v`

Expected: PASS.

### Task 3: Resumable BFS Crawler

**Files:**
- Create: `internal/crawler/crawler_test.go`
- Create: `internal/crawler/crawler.go`

- [ ] **Step 1: Write failing crawler tests**

Use a fake 115 lister and fake store to prove BFS visits root, writes files, enqueues directories, and saves checkpoints.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/crawler -v`

Expected: FAIL because crawler implementation does not exist.

- [ ] **Step 3: Implement crawler**

Implement `Crawler.CrawlShare` with queue restoration, page iteration, idempotent writes, directory enqueueing, and checkpoint persistence.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/crawler -v`

Expected: PASS.

### Task 4: Bleve Index Builder

**Files:**
- Create: `internal/searchindex/indexer_test.go`
- Create: `internal/searchindex/indexer.go`

- [ ] **Step 1: Write failing Bleve tests**

Test full rebuild from SQLite-like file provider and verify the resulting index can find a known filename.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/searchindex -v`

Expected: FAIL because index builder does not exist.

- [ ] **Step 3: Implement Bleve index builder**

Implement `Rebuild` into a building directory, rename to version directory, and return manifest metadata.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/searchindex -v`

Expected: PASS.

### Task 5: CLI Entrypoint

**Files:**
- Create: `cmd/115-indexer/main.go`
- Create: `README.md`

- [ ] **Step 1: Add CLI smoke test by building**

Run: `go test ./...`

Expected: PASS after all packages compile.

- [ ] **Step 2: Implement CLI commands**

Implement flags for `-db`, `-bleve`, `-share-code`, `-receive-code`, `-shares-file`, `-mode crawl|import-shares|rebuild-index`.

- [ ] **Step 3: Verify full repository**

Run: `go test ./...`

Expected: PASS.

## Self-Review

- The plan implements only the indexer service.
- Browse and search APIs are excluded.
- External consumers get SQLite and Bleve artifacts.
- 115 snap API field mapping is covered by tests.
- No placeholders remain in implementation scope.
