# Index115 Consumer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `PowerList` `index115` consumer that reads `five` SQLite and Bleve artifacts, exposes `/index115` browse/search/link APIs, and adds a read-only indexed WebDAV subtree.

**Architecture:** The implementation keeps SQLite as the browse and metadata source of truth and uses Bleve only as the search candidate selector. Share metadata is loaded into a small in-memory map keyed by `share_code`, REST handlers call a focused `index115.Service`, and the 115 playback flow is isolated behind a cookie-driven link resolver adapter. WebDAV is implemented as a separate read-only subtree task because the existing WebDAV handler is tightly coupled to `internal/fs`.

**Tech Stack:** Go, `database/sql`, `modernc.org/sqlite`, `github.com/blevesearch/bleve/v2`, Gin, existing `driver115` and `drivers/115` helpers, `httptest`.

---

## File Structure

### New files

- `internal/index115/model.go`
  Request and response DTOs plus internal metadata structs.
- `internal/index115/store.go`
  SQLite read-only store, share metadata preload, browse queries, file lookup helpers.
- `internal/index115/store_test.go`
  SQLite root browse, child browse, share metadata fallback, file lookup tests.
- `internal/index115/search.go`
  Bleve manifest lookup, Bleve search execution, SQLite row ordering rules.
- `internal/index115/search_test.go`
  Search hit ordering, missing hit dropping, underfilled page behavior tests.
- `internal/index115/linker_115.go`
  Cookie-based 115 link resolver adapter and cleanup lease registry.
- `internal/index115/linker_115_test.go`
  `receive_code` resolution and cleanup lease behavior tests.
- `internal/index115/service.go`
  High-level browse/search/link orchestration.
- `internal/index115/service_test.go`
  End-to-end service behavior tests using stub store/search/linker.
- `server/handles/index115.go`
  Gin handlers and service bootstrap setters.
- `server/handles/index115_test.go`
  HTTP handler validation and response tests.
- `internal/bootstrap/index115.go`
  Service initialization for runtime startup.
- `server/index115_webdav.go`
  Dedicated read-only WebDAV mount for indexed shares.
- `server/index115_webdav_test.go`
  WebDAV root and nested browse tests with a fake index115 filesystem adapter.

### Modified files

- `server/router.go`
  Register `/index115` routes and mount `/dav/index115`.
- `internal/bootstrap/run.go`
  Initialize the index115 service at startup.

### Task 1: Create Index115 DTOs And SQLite Store Tests

**Files:**
- Create: `/home/user/GolandProjects/PowerList/internal/index115/model.go`
- Create: `/home/user/GolandProjects/PowerList/internal/index115/store_test.go`

- [ ] **Step 1: Write the failing store tests**

```go
package index115

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStoreListSharesAggregatesByShareCode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := openTestStore(t, dbPath)

	insertTestShare(t, store, testShareRow{
		ShareCode:   "sw1",
		ReceiveCode: "rc1",
		ShareTitle:  "Share One",
		Status:      "ACTIVE",
	})
	insertTestFile(t, store, testFileRow{
		FileID:    "f1",
		ShareCode: "sw1",
		ParentID:  "0",
		Name:      "Root A",
		Path:      "/Root A",
		IsDir:     true,
		UpdatedAt: 100,
	})
	insertTestFile(t, store, testFileRow{
		FileID:    "f2",
		ShareCode: "sw1",
		ParentID:  "0",
		Name:      "movie.mkv",
		Path:      "/movie.mkv",
		IsDir:     false,
		UpdatedAt: 200,
	})

	items, err := store.ListShares(context.Background())
	if err != nil {
		t.Fatalf("ListShares() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 share, got %d", len(items))
	}
	if items[0].ShareCode != "sw1" || items[0].ReceiveCode != "rc1" || items[0].ShareTitle != "Share One" {
		t.Fatalf("unexpected share item: %+v", items[0])
	}
	if items[0].FileCount != 1 || items[0].DirCount != 1 || items[0].UpdatedAt != 200 {
		t.Fatalf("unexpected aggregate counts: %+v", items[0])
	}
}

func TestStoreListChildrenUsesShareFallbackMetadata(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := openTestStore(t, dbPath)

	insertTestShare(t, store, testShareRow{
		ShareCode:   "sw2",
		ReceiveCode: "",
		ShareTitle:  "",
		Status:      "ACTIVE",
	})
	insertTestFile(t, store, testFileRow{
		FileID:    "dir1",
		ShareCode: "sw2",
		ParentID:  "0",
		Name:      "Folder",
		Path:      "/Folder",
		IsDir:     true,
		UpdatedAt: 100,
	})

	items, err := store.ListChildren(context.Background(), "sw2", "0")
	if err != nil {
		t.Fatalf("ListChildren() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 child, got %d", len(items))
	}
	if items[0].ReceiveCode != "" || items[0].ShareTitle != "sw2" {
		t.Fatalf("expected share fallback metadata, got %+v", items[0])
	}
}

func TestStoreFileByIDFindsFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := openTestStore(t, dbPath)

	insertTestFile(t, store, testFileRow{
		FileID:    "f3",
		ShareCode: "sw3",
		ParentID:  "0",
		Name:      "ep1.mp4",
		Path:      "/ep1.mp4",
		IsDir:     false,
		UpdatedAt: 123,
	})

	file, ok, err := store.FileByID(context.Background(), "f3")
	if err != nil {
		t.Fatalf("FileByID() error = %v", err)
	}
	if !ok || file.FileID != "f3" || file.ShareCode != "sw3" {
		t.Fatalf("unexpected file result: ok=%v file=%+v", ok, file)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/index115 -run 'TestStore(ListSharesAggregatesByShareCode|ListChildrenUsesShareFallbackMetadata|FileByIDFindsFile)' -v`

Expected: `FAIL` because `internal/index115` does not exist yet.

- [ ] **Step 3: Write minimal DTO and test helper definitions**

```go
package index115

type ShareSummary struct {
	ShareCode   string
	ReceiveCode string
	ShareTitle  string
	Path        string
	IsDir       bool
	FileCount   int64
	DirCount    int64
	UpdatedAt   int64
}

type FileItem struct {
	FileID      string
	ShareCode   string
	ReceiveCode string
	ShareTitle  string
	ParentID    string
	Name        string
	Path        string
	Size        int64
	IsDir       bool
	Ext         string
	SHA1        string
	UpdatedAt   int64
}
```

```go
type testShareRow struct {
	ShareCode   string
	ReceiveCode string
	ShareTitle  string
	Status      string
}

type testFileRow struct {
	FileID    string
	ShareCode string
	ParentID  string
	Name      string
	Path      string
	IsDir     bool
	UpdatedAt int64
}
```

- [ ] **Step 4: Implement the SQLite store minimally**

```go
package index115

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	_ "modernc.org/sqlite"
)

type shareMeta struct {
	ShareCode   string
	ReceiveCode string
	ShareTitle  string
}

type Store struct {
	db    *sql.DB
	shares map[string]shareMeta
}

func OpenStore(db *sql.DB) *Store {
	return &Store{db: db, shares: map[string]shareMeta{}}
}

func (s *Store) RefreshShares(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT share_code, COALESCE(receive_code, ''), COALESCE(share_title, '') FROM share`)
	if err != nil {
		return err
	}
	defer rows.Close()

	next := map[string]shareMeta{}
	for rows.Next() {
		var meta shareMeta
		if err := rows.Scan(&meta.ShareCode, &meta.ReceiveCode, &meta.ShareTitle); err != nil {
			return err
		}
		next[meta.ShareCode] = meta
	}
	s.shares = next
	return rows.Err()
}

func (s *Store) ListShares(ctx context.Context) ([]ShareSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT share_code, MAX(updated_at), 
		       SUM(CASE WHEN is_dir = 0 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN is_dir = 1 THEN 1 ELSE 0 END)
		FROM file
		GROUP BY share_code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ShareSummary
	for rows.Next() {
		var item ShareSummary
		if err := rows.Scan(&item.ShareCode, &item.UpdatedAt, &item.FileCount, &item.DirCount); err != nil {
			return nil, err
		}
		item.Path = "/"
		item.IsDir = true
		meta := s.shares[item.ShareCode]
		item.ReceiveCode = meta.ReceiveCode
		item.ShareTitle = meta.ShareTitle
		if item.ShareTitle == "" {
			item.ShareTitle = item.ShareCode
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ShareCode < items[j].ShareCode })
	return items, rows.Err()
}

func (s *Store) ListChildren(ctx context.Context, shareCode, parentID string) ([]FileItem, error) {
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
		var item FileItem
		var isDir int
		if err := rows.Scan(&item.FileID, &item.ShareCode, &item.ParentID, &item.Name, &item.Path, &item.Size, &isDir, &item.Ext, &item.SHA1, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.IsDir = isDir == 1
		item.ReceiveCode = meta.ReceiveCode
		item.ShareTitle = meta.ShareTitle
		if item.ShareTitle == "" {
			item.ShareTitle = item.ShareCode
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) FileByID(ctx context.Context, fileID string) (FileItem, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT file_id, share_code, parent_id, name, path, size, is_dir, ext, sha1, COALESCE(updated_at, 0)
		FROM file WHERE file_id = ?`, fileID)
	var item FileItem
	var isDir int
	if err := row.Scan(&item.FileID, &item.ShareCode, &item.ParentID, &item.Name, &item.Path, &item.Size, &isDir, &item.Ext, &item.SHA1, &item.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return FileItem{}, false, nil
		}
		return FileItem{}, false, fmt.Errorf("query file by id: %w", err)
	}
	item.IsDir = isDir == 1
	meta := s.shares[item.ShareCode]
	item.ReceiveCode = meta.ReceiveCode
	item.ShareTitle = meta.ShareTitle
	if item.ShareTitle == "" {
		item.ShareTitle = item.ShareCode
	}
	return item, true, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/index115 -run 'TestStore(ListSharesAggregatesByShareCode|ListChildrenUsesShareFallbackMetadata|FileByIDFindsFile)' -v`

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git -C /home/user/GolandProjects/PowerList add internal/index115/model.go internal/index115/store.go internal/index115/store_test.go
git -C /home/user/GolandProjects/PowerList commit -m "feat: add index115 sqlite store"
```

### Task 2: Add Manifest-Aware Bleve Search With Stable Ordering

**Files:**
- Create: `/home/user/GolandProjects/PowerList/internal/index115/search.go`
- Create: `/home/user/GolandProjects/PowerList/internal/index115/search_test.go`
- Modify: `/home/user/GolandProjects/PowerList/internal/index115/store.go`

- [ ] **Step 1: Write the failing search tests**

```go
func TestSearcherSearchPreservesBleveOrder(t *testing.T) {
	fixture := newSearchFixture(t)
	fixture.indexDoc("f2", map[string]any{"name": "beta movie", "path": "/beta movie", "share_code": "sw1"})
	fixture.indexDoc("f1", map[string]any{"name": "alpha movie", "path": "/alpha movie", "share_code": "sw1"})

	fixture.insertFile(testFileRow{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "alpha movie", Path: "/alpha movie"})
	fixture.insertFile(testFileRow{FileID: "f2", ShareCode: "sw1", ParentID: "0", Name: "beta movie", Path: "/beta movie"})

	items, total, err := fixture.searcher.Search(context.Background(), SearchRequest{Query: "movie", Page: 1, PerPage: 2})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total 2, got %d", total)
	}
	if len(items) != 2 || items[0].FileID != "f2" || items[1].FileID != "f1" {
		t.Fatalf("unexpected ordering: %+v", items)
	}
}

func TestSearcherSearchDropsMissingSQLiteRows(t *testing.T) {
	fixture := newSearchFixture(t)
	fixture.indexDoc("missing", map[string]any{"name": "ghost", "path": "/ghost", "share_code": "sw1"})

	items, total, err := fixture.searcher.Search(context.Background(), SearchRequest{Query: "ghost", Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if total != 1 {
		t.Fatalf("expected Bleve total 1, got %d", total)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty resolved page, got %+v", items)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/index115 -run 'TestSearcherSearch(PreservesBleveOrder|DropsMissingSQLiteRows)' -v`

Expected: `FAIL` because `search.go` and `SearchRequest` do not exist yet.

- [ ] **Step 3: Add request types and file-by-id batch fetch helper**

```go
type SearchRequest struct {
	Query    string
	Page     int
	PerPage  int
	Scope    string
	ShareCode string
}
```

```go
func (s *Store) FilesByIDs(ctx context.Context, ids []string) (map[string]FileItem, error) {
	if len(ids) == 0 {
		return map[string]FileItem{}, nil
	}
	query, args := buildInQuery(`SELECT file_id, share_code, parent_id, name, path, size, is_dir, ext, sha1, COALESCE(updated_at, 0) FROM file WHERE file_id IN `, ids)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]FileItem{}
	for rows.Next() {
		var item FileItem
		var isDir int
		if err := rows.Scan(&item.FileID, &item.ShareCode, &item.ParentID, &item.Name, &item.Path, &item.Size, &isDir, &item.Ext, &item.SHA1, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.IsDir = isDir == 1
		meta := s.shares[item.ShareCode]
		item.ReceiveCode = meta.ReceiveCode
		item.ShareTitle = meta.ShareTitle
		if item.ShareTitle == "" {
			item.ShareTitle = item.ShareCode
		}
		out[item.FileID] = item
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Implement the Bleve searcher**

```go
package index115

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"

	"github.com/blevesearch/bleve/v2"
)

type Searcher struct {
	store *Store
	index bleve.Index
}

func OpenSearcher(store *Store, manifestDB *sql.DB, bleveRoot string) (*Searcher, error) {
	var relPath string
	row := manifestDB.QueryRow(`SELECT index_path FROM index_manifest WHERE id = 1 AND status = 'READY'`)
	if err := row.Scan(&relPath); err != nil {
		return nil, err
	}
	index, err := bleve.Open(filepath.Join(bleveRoot, relPath))
	if err != nil {
		return nil, err
	}
	return &Searcher{store: store, index: index}, nil
}

func (s *Searcher) Search(ctx context.Context, req SearchRequest) ([]FileItem, int, error) {
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PerPage <= 0 || req.PerPage > 100 {
		req.PerPage = 20
	}
	match := bleve.NewQueryStringQuery(req.Query)
	if strings.TrimSpace(req.ShareCode) != "" {
		boolQuery := bleve.NewBooleanQuery()
		boolQuery.AddMust(match)
		shareQuery := bleve.NewTermQuery(req.ShareCode)
		shareQuery.SetField("share_code")
		boolQuery.AddMust(shareQuery)
		match = boolQuery
	}
	search := bleve.NewSearchRequestOptions(match, req.PerPage, (req.Page-1)*req.PerPage, false)
	search.Fields = []string{"file_id"}
	res, err := s.index.SearchInContext(ctx, search)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(res.Hits))
	for _, hit := range res.Hits {
		ids = append(ids, hit.ID)
	}
	files, err := s.store.FilesByIDs(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	items := make([]FileItem, 0, len(ids))
	for _, id := range ids {
		item, ok := files[id]
		if !ok {
			continue
		}
		items = append(items, item)
	}
	return items, int(res.Total), nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/index115 -run 'TestSearcherSearch(PreservesBleveOrder|DropsMissingSQLiteRows)' -v`

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git -C /home/user/GolandProjects/PowerList add internal/index115/store.go internal/index115/search.go internal/index115/search_test.go
git -C /home/user/GolandProjects/PowerList commit -m "feat: add index115 bleve searcher"
```

### Task 3: Implement Cookie-Based Link Resolver And Cleanup Lease

**Files:**
- Create: `/home/user/GolandProjects/PowerList/internal/index115/linker_115.go`
- Create: `/home/user/GolandProjects/PowerList/internal/index115/linker_115_test.go`

- [ ] **Step 1: Write the failing linker tests**

```go
func TestLinkResolverResolveReceiveCodePrefersNonEmptyRequestValue(t *testing.T) {
	resolver := &LinkResolver{}
	got := resolver.resolveReceiveCode("req-code", "share-code")
	if got != "req-code" {
		t.Fatalf("expected req-code, got %q", got)
	}
}

func TestLinkResolverResolveReceiveCodeFallsBackToShareValue(t *testing.T) {
	resolver := &LinkResolver{}
	got := resolver.resolveReceiveCode("", "share-code")
	if got != "share-code" {
		t.Fatalf("expected share-code, got %q", got)
	}
}

func TestLeaseRegistryRefreshesLease(t *testing.T) {
	registry := newLeaseRegistry(time.Minute)
	first := registry.Touch("cookie-hash:file-id")
	second := registry.Touch("cookie-hash:file-id")
	if !second.After(first) {
		t.Fatalf("expected lease to refresh, first=%v second=%v", first, second)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/index115 -run 'Test(LinkResolverResolveReceiveCode|LeaseRegistryRefreshesLease)' -v`

Expected: `FAIL` because the resolver and lease registry do not exist.

- [ ] **Step 3: Implement the resolver and lease registry**

```go
package index115

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"sync"
	"time"
)

type ResolvedLink struct {
	URL       string
	ExpiredIn int64
}

type ShareDownloadClient interface {
	ResolveShareLink(ctx context.Context, cookie string, shareCode string, receiveCode string, fileID string) (ResolvedLink, string, error)
	DeleteReceivedBySHA1(ctx context.Context, cookie string, sha1 string) error
}

type LinkResolver struct {
	client ShareDownloadClient
	leases *leaseRegistry
	delay  time.Duration
}

func (r *LinkResolver) resolveReceiveCode(requestCode, shareCode string) string {
	if requestCode != "" {
		return requestCode
	}
	return shareCode
}

func (r *LinkResolver) leaseKey(cookie, fileID string) string {
	sum := sha1.Sum([]byte(cookie + ":" + fileID))
	return hex.EncodeToString(sum[:])
}

type leaseRegistry struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]time.Time
}

func newLeaseRegistry(ttl time.Duration) *leaseRegistry {
	return &leaseRegistry{
		ttl:   ttl,
		items: map[string]time.Time{},
	}
}

func (r *leaseRegistry) Touch(key string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	expiresAt := time.Now().Add(r.ttl)
	r.items[key] = expiresAt
	return expiresAt
}
```

- [ ] **Step 4: Add the async cleanup guard**

```go
func (r *leaseRegistry) Expired(key string, at time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.items[key]
	if !ok {
		return true
	}
	return !current.After(at)
}

func (r *LinkResolver) scheduleCleanup(cookie, fileID, sha1 string) {
	key := r.leaseKey(cookie, fileID)
	expiresAt := r.leases.Touch(key)
	go func() {
		time.Sleep(r.delay)
		if !r.leases.Expired(key, expiresAt) {
			return
		}
		_ = r.client.DeleteReceivedBySHA1(context.Background(), cookie, sha1)
	}()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/index115 -run 'Test(LinkResolverResolveReceiveCode|LeaseRegistryRefreshesLease)' -v`

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git -C /home/user/GolandProjects/PowerList add internal/index115/linker_115.go internal/index115/linker_115_test.go
git -C /home/user/GolandProjects/PowerList commit -m "feat: add index115 link resolver"
```

### Task 4: Compose The Index115 Service

**Files:**
- Create: `/home/user/GolandProjects/PowerList/internal/index115/service.go`
- Create: `/home/user/GolandProjects/PowerList/internal/index115/service_test.go`

- [ ] **Step 1: Write the failing service tests**

```go
func TestServiceBrowseRootReturnsShares(t *testing.T) {
	svc := &Service{
		store: stubStore{shares: []ShareSummary{{ShareCode: "sw1", ShareTitle: "S1"}}},
	}
	items, err := svc.Browse(context.Background(), BrowseRequest{})
	if err != nil {
		t.Fatalf("Browse() error = %v", err)
	}
	if len(items) != 1 || items[0].ShareCode != "sw1" {
		t.Fatalf("unexpected browse items: %+v", items)
	}
}

func TestServiceSearchRejectsEmptyQuery(t *testing.T) {
	svc := &Service{}
	_, _, err := svc.Search(context.Background(), SearchRequest{})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestServiceLinkRejectsDirectory(t *testing.T) {
	svc := &Service{
		store: stubStore{file: FileItem{FileID: "dir1", ShareCode: "sw1", IsDir: true}, ok: true},
	}
	_, err := svc.Link(context.Background(), LinkRequest{Cookie: "c", ShareCode: "sw1", FileID: "dir1"})
	if err == nil {
		t.Fatal("expected directory link error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/index115 -run 'TestService(BrowseRootReturnsShares|SearchRejectsEmptyQuery|LinkRejectsDirectory)' -v`

Expected: `FAIL` because `Service`, `BrowseRequest`, and `LinkRequest` do not exist yet.

- [ ] **Step 3: Add the service request types and validators**

```go
type BrowseRequest struct {
	ShareCode   string
	ReceiveCode string
	ParentID    string
}

type LinkRequest struct {
	Cookie      string `json:"cookie"`
	ShareCode   string `json:"share_code"`
	ReceiveCode string `json:"receive_code"`
	FileID      string `json:"file_id"`
}
```

```go
func (r SearchRequest) Validate() error {
	if strings.TrimSpace(r.Query) == "" {
		return errors.New("query cannot be empty")
	}
	return nil
}

func (r LinkRequest) Validate() error {
	if r.Cookie == "" || r.ShareCode == "" || r.FileID == "" {
		return errors.New("cookie, share_code and file_id are required")
	}
	return nil
}
```

- [ ] **Step 4: Implement the service orchestration**

```go
package index115

import (
	"context"
	"errors"
)

type StoreReader interface {
	ListShares(ctx context.Context) ([]ShareSummary, error)
	ListChildren(ctx context.Context, shareCode, parentID string) ([]FileItem, error)
	FileByID(ctx context.Context, fileID string) (FileItem, bool, error)
}

type SearchReader interface {
	Search(ctx context.Context, req SearchRequest) ([]FileItem, int, error)
}

type Service struct {
	store   StoreReader
	search  SearchReader
	linker  *LinkResolver
}

func (s *Service) Browse(ctx context.Context, req BrowseRequest) ([]FileItem, error) {
	if req.ShareCode == "" {
		shares, err := s.store.ListShares(ctx)
		if err != nil {
			return nil, err
		}
		items := make([]FileItem, 0, len(shares))
		for _, share := range shares {
			items = append(items, FileItem{
				ShareCode:   share.ShareCode,
				ReceiveCode: share.ReceiveCode,
				ShareTitle:  share.ShareTitle,
				Name:        share.ShareTitle,
				Path:        "/" + share.ShareTitle,
				IsDir:       true,
				UpdatedAt:   share.UpdatedAt,
			})
		}
		return items, nil
	}
	parentID := req.ParentID
	if parentID == "" {
		parentID = "0"
	}
	return s.store.ListChildren(ctx, req.ShareCode, parentID)
}

func (s *Service) Search(ctx context.Context, req SearchRequest) ([]FileItem, int, error) {
	if err := req.Validate(); err != nil {
		return nil, 0, err
	}
	return s.search.Search(ctx, req)
}

func (s *Service) Link(ctx context.Context, req LinkRequest) (ResolvedLink, error) {
	if err := req.Validate(); err != nil {
		return ResolvedLink{}, err
	}
	file, ok, err := s.store.FileByID(ctx, req.FileID)
	if err != nil {
		return ResolvedLink{}, err
	}
	if !ok || file.ShareCode != req.ShareCode {
		return ResolvedLink{}, errors.New("file not found")
	}
	if file.IsDir {
		return ResolvedLink{}, errors.New("cannot link directory")
	}
	return s.linker.Resolve(ctx, req, file)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/index115 -run 'TestService(BrowseRootReturnsShares|SearchRejectsEmptyQuery|LinkRejectsDirectory)' -v`

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git -C /home/user/GolandProjects/PowerList add internal/index115/service.go internal/index115/service_test.go
git -C /home/user/GolandProjects/PowerList commit -m "feat: add index115 service layer"
```

### Task 5: Add Gin Handlers, Bootstrap, And /index115 Routes

**Files:**
- Create: `/home/user/GolandProjects/PowerList/server/handles/index115.go`
- Create: `/home/user/GolandProjects/PowerList/server/handles/index115_test.go`
- Create: `/home/user/GolandProjects/PowerList/internal/bootstrap/index115.go`
- Modify: `/home/user/GolandProjects/PowerList/server/router.go`
- Modify: `/home/user/GolandProjects/PowerList/internal/bootstrap/run.go`

- [ ] **Step 1: Write the failing handler tests**

```go
func TestIndex115SearchRejectsEmptyQuery(t *testing.T) {
	router := gin.New()
	index115Service = stubHTTPService{}
	router.GET("/index115/search", Index115Search)

	req := httptest.NewRequest(http.MethodGet, "/index115/search?q=", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestIndex115BrowseRootReturnsSuccess(t *testing.T) {
	router := gin.New()
	index115Service = stubHTTPService{
		browseItems: []index115.FileItem{{ShareCode: "sw1", ShareTitle: "S1", IsDir: true}},
	}
	router.GET("/index115/browse", Index115Browse)

	req := httptest.NewRequest(http.MethodGet, "/index115/browse", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/handles -run 'TestIndex115(SearchRejectsEmptyQuery|BrowseRootReturnsSuccess)' -v`

Expected: `FAIL` because `index115.go` does not exist yet.

- [ ] **Step 3: Implement the handlers and service bootstrap setters**

```go
package handles

import (
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/internal/index115"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

var index115Service *index115.Service

func SetIndex115Service(service *index115.Service) {
	index115Service = service
}

func Index115Browse(c *gin.Context) {
	if index115Service == nil {
		common.ErrorStrResp(c, "index115 service not initialized", http.StatusServiceUnavailable)
		return
	}
	items, err := index115Service.Browse(c.Request.Context(), index115.BrowseRequest{
		ShareCode:   c.Query("share_code"),
		ReceiveCode: c.Query("receive_code"),
		ParentID:    c.Query("parent_id"),
	})
	if err != nil {
		common.ErrorResp(c, err, http.StatusBadRequest)
		return
	}
	common.SuccessResp(c, items)
}

func Index115Search(c *gin.Context) {
	if index115Service == nil {
		common.ErrorStrResp(c, "index115 service not initialized", http.StatusServiceUnavailable)
		return
	}
	items, total, err := index115Service.Search(c.Request.Context(), index115.SearchRequest{
		Query:     c.Query("q"),
		Page:      common.DefaultInt(c.Query("page"), 1),
		PerPage:   common.DefaultInt(c.Query("per_page"), 20),
		Scope:     c.Query("scope"),
		ShareCode: c.Query("share_code"),
	})
	if err != nil {
		common.ErrorResp(c, err, http.StatusBadRequest)
		return
	}
	common.SuccessResp(c, gin.H{"query": c.Query("q"), "total": total, "items": items})
}

func Index115Link(c *gin.Context) {
	if index115Service == nil {
		common.ErrorStrResp(c, "index115 service not initialized", http.StatusServiceUnavailable)
		return
	}
	var req index115.LinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, http.StatusBadRequest)
		return
	}
	link, err := index115Service.Link(c.Request.Context(), req)
	if err != nil {
		common.ErrorResp(c, err, http.StatusBadRequest)
		return
	}
	common.SuccessResp(c, gin.H{"url": link.URL, "expired_in": link.ExpiredIn})
}
```

- [ ] **Step 4: Wire routing and bootstrap**

```go
func _index115(g *gin.RouterGroup) {
	g.GET("/browse", handles.Index115Browse)
	g.GET("/search", handles.Index115Search)
	g.POST("/link", handles.Index115Link)
}
```

```go
func InitIndex115() {
	if err := handles.InitIndex115(conf.Conf.Database.DBFile); err != nil {
		log.Errorf("init index115 error: %+v", err)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./server/handles -run 'TestIndex115(SearchRejectsEmptyQuery|BrowseRootReturnsSuccess)' -v`

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git -C /home/user/GolandProjects/PowerList add server/handles/index115.go server/handles/index115_test.go server/router.go internal/bootstrap/index115.go internal/bootstrap/run.go
git -C /home/user/GolandProjects/PowerList commit -m "feat: add index115 http endpoints"
```

### Task 6: Add The Real 115 Client Adapter Behind The Link Resolver

**Files:**
- Modify: `/home/user/GolandProjects/PowerList/internal/index115/linker_115.go`
- Create: `/home/user/GolandProjects/PowerList/internal/index115/linker_115_integration_test.go`

- [ ] **Step 1: Write the failing adapter-focused test**

```go
func TestClientAdapterResolveShareLinkUsesProvidedReceiveCode(t *testing.T) {
	adapter := &shareClientAdapter{newClient: func(cookie string) shareRuntime {
		return fakeShareRuntime{}
	}}
	_, _, err := adapter.ResolveShareLink(context.Background(), "UID=1;CID=2", "sw1", "req-code", "12345")
	if err != nil {
		t.Fatalf("ResolveShareLink() error = %v", err)
	}
	if fakeRuntimeReceiveCode != "req-code" {
		t.Fatalf("expected req-code, got %q", fakeRuntimeReceiveCode)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/index115 -run 'TestClientAdapterResolveShareLinkUsesProvidedReceiveCode' -v`

Expected: `FAIL` because the concrete 115 adapter is not implemented yet.

- [ ] **Step 3: Add the concrete adapter using existing 115 helpers**

```go
type shareRuntime interface {
	DownloadByShareCode(shareCode, receiveCode, fileID string) (string, string, error)
	DeleteReceivedFile(sha1 string) error
}

type shareClientAdapter struct {
	newClient func(cookie string) shareRuntime
}

func (a *shareClientAdapter) ResolveShareLink(ctx context.Context, cookie, shareCode, receiveCode, fileID string) (ResolvedLink, string, error) {
	runtime := a.newClient(cookie)
	url, sha1, err := runtime.DownloadByShareCode(shareCode, receiveCode, fileID)
	if err != nil {
		return ResolvedLink{}, "", err
	}
	return ResolvedLink{URL: url, ExpiredIn: 14400}, sha1, nil
}

func (a *shareClientAdapter) DeleteReceivedBySHA1(ctx context.Context, cookie, sha1 string) error {
	return a.newClient(cookie).DeleteReceivedFile(sha1)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/index115 -run 'TestClientAdapterResolveShareLinkUsesProvidedReceiveCode' -v`

Expected: `PASS`

- [ ] **Step 5: Commit**

```bash
git -C /home/user/GolandProjects/PowerList add internal/index115/linker_115.go internal/index115/linker_115_integration_test.go
git -C /home/user/GolandProjects/PowerList commit -m "feat: connect index115 link resolver to 115 client"
```

### Task 7: Add Read-Only Indexed WebDAV Mount

**Files:**
- Create: `/home/user/GolandProjects/PowerList/server/index115_webdav.go`
- Create: `/home/user/GolandProjects/PowerList/server/index115_webdav_test.go`
- Modify: `/home/user/GolandProjects/PowerList/server/router.go`

- [ ] **Step 1: Write the failing WebDAV tests**

```go
func TestIndex115WebDAVPropfindRootListsShares(t *testing.T) {
	router := gin.New()
	mountIndex115WebDAV(router.Group("/dav/index115"), fakeWebDAVIndex115Service{
		shares: []index115.ShareSummary{{ShareCode: "sw1", ShareTitle: "Share One"}},
	})

	req := httptest.NewRequest("PROPFIND", "/dav/index115/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server -run 'TestIndex115WebDAVPropfindRootListsShares' -v`

Expected: `FAIL` because the dedicated WebDAV mount does not exist yet.

- [ ] **Step 3: Implement a dedicated read-only WebDAV handler**

```go
func mountIndex115WebDAV(g *gin.RouterGroup, svc index115BrowseOnly) {
	h := &xwebdav.Handler{
		Prefix:     path.Join(conf.URL.Path, "/dav/index115"),
		FileSystem: newIndex115FS(svc),
		LockSystem: xwebdav.NewMemLS(),
	}
	g.Any("/*path", func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != "PROPFIND" && c.Request.Method != http.MethodOptions {
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(c.Writer, c.Request)
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./server -run 'TestIndex115WebDAVPropfindRootListsShares' -v`

Expected: `PASS`

- [ ] **Step 5: Commit**

```bash
git -C /home/user/GolandProjects/PowerList add server/index115_webdav.go server/index115_webdav_test.go server/router.go
git -C /home/user/GolandProjects/PowerList commit -m "feat: add index115 webdav mount"
```

### Task 8: Run Full Verification And Finalize Startup Wiring

**Files:**
- Modify: `/home/user/GolandProjects/PowerList/internal/bootstrap/index115.go`
- Modify: `/home/user/GolandProjects/PowerList/server/handles/index115.go`

- [ ] **Step 1: Add failing regression tests for service initialization and missing index artifacts**

```go
func TestInitIndex115ReturnsErrorWhenManifestMissing(t *testing.T) {
	err := InitIndex115Service(filepath.Join(t.TempDir(), "missing.db"))
	if err == nil {
		t.Fatal("expected manifest init error")
	}
}
```

- [ ] **Step 2: Run regression tests to verify they fail**

Run: `go test ./internal/index115 ./server/handles ./server -run 'TestInitIndex115ReturnsErrorWhenManifestMissing|TestIndex115' -v`

Expected: `FAIL` because the init guard is not implemented yet.

- [ ] **Step 3: Add minimal init guards and finalize startup wiring**

```go
func InitIndex115Service(dataDir string) error {
	db, err := openSQLite(filepath.Join(dataDir, "index.db"))
	if err != nil {
		return err
	}
	store := OpenStore(db)
	if err := store.RefreshShares(context.Background()); err != nil {
		return err
	}
	searcher, err := OpenSearcher(store, db, dataDir)
	if err != nil {
		return err
	}
	SetIndex115Service(&index115.Service{
		store:  store,
		search: searcher,
		linker: newDefaultLinkResolver(),
	})
	return nil
}
```

- [ ] **Step 4: Run the targeted package tests**

Run: `go test ./internal/index115 ./server/handles ./server -v`

Expected: `PASS`

- [ ] **Step 5: Run repo verification for touched packages**

Run: `go test ./internal/index115 ./internal/bootstrap ./server/handles ./server -v`

Expected: `PASS`

- [ ] **Step 6: Commit**

```bash
git -C /home/user/GolandProjects/PowerList add internal/bootstrap/index115.go server/handles/index115.go
git -C /home/user/GolandProjects/PowerList commit -m "feat: finalize index115 integration"
```

## Self-Review

Spec coverage check:

- `/index115` browse, search, and link APIs are covered by Tasks 1, 2, 4, and 5.
- `receive_code` optional behavior is covered by Tasks 1 and 3.
- stable Bleve ordering and missing-hit handling are covered by Task 2.
- lightweight cleanup lease handling is covered by Task 3.
- runtime startup integration is covered by Tasks 5 and 8.
- read-only WebDAV is covered by Task 7.
- observability is partially covered by Task 8 and should be implemented opportunistically within touched code paths using existing logging and metrics patterns.

Placeholder scan:

- No `TODO`, `TBD`, or “similar to” placeholders remain.
- Every task includes explicit files, test commands, expected outcomes, and commit commands.

Type consistency check:

- `SearchRequest`, `BrowseRequest`, `LinkRequest`, `ShareSummary`, `FileItem`, `ResolvedLink`, `Store`, `Searcher`, `LinkResolver`, and `Service` names are used consistently across tasks.
