# Share Count Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `validate-share-counts` CLI mode that compares each registered share's 115 root `data.count` against the number of indexed `file` rows in SQLite and prints only mismatches plus a final summary.

**Architecture:** Keep the feature as a read-only CLI workflow in `cmd/115-indexer`. Put SQL counting logic in `internal/store`, put the per-share validation loop in a small helper file next to `backfill.go`, and wire the helper into `main.go` with the same 115 client/proxy conventions used by other direct-access modes.

**Tech Stack:** Go, `modernc.org/sqlite`, existing `api115.Client`, standard library `flag`, `fmt`, `io`, and Go `testing`.

---

### Task 1: Add Store File Count Query

**Files:**
- Modify: `internal/store/sqlite.go`
- Test: `internal/store/sqlite_test.go`

- [ ] **Step 1: Write the failing test**

Add this test near the existing share/file store tests in `internal/store/sqlite_test.go`:

```go
func TestSQLiteStoreCountFilesByShare(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Path: "/a.mkv", Ext: "mkv", CrawledAt: 1},
		{FileID: "f2", ShareCode: "sw1", ParentID: "0", Name: "b.mkv", Path: "/b.mkv", Ext: "mkv", CrawledAt: 1},
		{FileID: "f3", ShareCode: "sw2", ParentID: "0", Name: "c.mkv", Path: "/c.mkv", Ext: "mkv", CrawledAt: 1},
	}); err != nil {
		t.Fatalf("upsert files: %v", err)
	}

	got, err := s.CountFilesByShare(ctx, "sw1")
	if err != nil {
		t.Fatalf("count files sw1: %v", err)
	}
	if got != 2 {
		t.Fatalf("sw1 file count = %d, want 2", got)
	}

	got, err = s.CountFilesByShare(ctx, "missing")
	if err != nil {
		t.Fatalf("count files missing: %v", err)
	}
	if got != 0 {
		t.Fatalf("missing file count = %d, want 0", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store -run TestSQLiteStoreCountFilesByShare -v`

Expected: FAIL with a compile error that `CountFilesByShare` is undefined on `*Store`.

- [ ] **Step 3: Write minimal implementation**

Add this method in `internal/store/sqlite.go` near the other share/file query helpers:

```go
func (s *Store) CountFilesByShare(ctx context.Context, shareCode string) (int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code = ?`, shareCode)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store -run TestSQLiteStoreCountFilesByShare -v`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat: add per-share file count query"
```

### Task 2: Add a Testable Share Validation Helper

**Files:**
- Create: `cmd/115-indexer/validate.go`
- Create: `cmd/115-indexer/validate_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/115-indexer/validate_test.go` with a focused behavior test:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"five/internal/api115"
	"five/internal/model"
)

type fakeCountFetcher struct {
	responses map[string]api115.SnapResponse
	errs      map[string]error
	calls     []api115.ListRequest
}

func (f *fakeCountFetcher) List(ctx context.Context, req api115.ListRequest) (api115.SnapResponse, error) {
	f.calls = append(f.calls, req)
	if err := f.errs[req.ShareCode]; err != nil {
		return api115.SnapResponse{}, err
	}
	resp, ok := f.responses[req.ShareCode]
	if !ok {
		return api115.SnapResponse{}, nil
	}
	return resp, nil
}

type fakeCountStore struct {
	counts map[string]int
	errs   map[string]error
}

func (f fakeCountStore) CountFilesByShare(_ context.Context, shareCode string) (int, error) {
	if err := f.errs[shareCode]; err != nil {
		return 0, err
	}
	return f.counts[shareCode], nil
}

func TestValidateShareCountsReportsMismatchAndContinuesAfterFailure(t *testing.T) {
	fetcher := &fakeCountFetcher{
		responses: map[string]api115.SnapResponse{
			"match": {
				State: true,
				Data: api115.SnapData{
					Count: 2,
					ShareInfo: api115.SnapShareInfo{
						ShareState: 1,
						ShareTitle: "Match Share",
					},
				},
			},
			"mismatch": {
				State: true,
				Data: api115.SnapData{
					Count: 5,
					ShareInfo: api115.SnapShareInfo{
						ShareState: 1,
						ShareTitle: "Mismatch Share",
					},
				},
			},
		},
		errs: map[string]error{
			"broken": errors.New("upstream timeout"),
		},
	}
	store := fakeCountStore{
		counts: map[string]int{
			"match":    2,
			"mismatch": 3,
		},
	}
	shares := []model.Share{
		{ShareCode: "match", ReceiveCode: "aa", ShareTitle: "Match Share"},
		{ShareCode: "mismatch", ReceiveCode: "bb", ShareTitle: "Mismatch Share"},
		{ShareCode: "broken", ReceiveCode: "cc", ShareTitle: "Broken Share"},
	}

	var out bytes.Buffer
	summary, err := validateShareCounts(context.Background(), fetcher, store, shares, &out)
	if err != nil {
		t.Fatalf("validate share counts: %v", err)
	}
	if summary.Validated != 2 || summary.Mismatched != 1 || summary.Failed != 1 {
		t.Fatalf("summary = %#v", summary)
	}

	text := out.String()
	if strings.Contains(text, "share=match api_count=") {
		t.Fatalf("matched share should not be printed: %q", text)
	}
	if !strings.Contains(text, `share=mismatch api_count=5 db_count=3 title="Mismatch Share"`) {
		t.Fatalf("missing mismatch output: %q", text)
	}
	if !strings.Contains(text, `share=broken validate_failed error="upstream timeout"`) {
		t.Fatalf("missing failure output: %q", text)
	}
	if !strings.Contains(text, "validated=2 mismatched=1 failed=1") {
		t.Fatalf("missing summary output: %q", text)
	}
	if len(fetcher.calls) != 3 {
		t.Fatalf("fetch calls = %d, want 3", len(fetcher.calls))
	}
	for _, call := range fetcher.calls {
		if call.CID != "0" || call.Offset != 0 || call.Limit != 1 {
			t.Fatalf("unexpected request = %#v", call)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/115-indexer -run TestValidateShareCountsReportsMismatchAndContinuesAfterFailure -v`

Expected: FAIL with compile errors that `validateShareCounts` is undefined and the count-store interface is missing.

- [ ] **Step 3: Write minimal implementation**

Create `cmd/115-indexer/validate.go` with this implementation:

```go
package main

import (
	"context"
	"fmt"
	"io"

	"five/internal/api115"
	"five/internal/model"
)

type shareFileCounter interface {
	CountFilesByShare(ctx context.Context, shareCode string) (int, error)
}

type validationSummary struct {
	Validated  int
	Mismatched int
	Failed     int
}

func validateShareCounts(ctx context.Context, fetcher snapFetcher, store shareFileCounter, shareList []model.Share, out io.Writer) (validationSummary, error) {
	var summary validationSummary
	for _, sh := range shareList {
		resp, err := fetcher.List(ctx, api115.ListRequest{
			ShareCode:   sh.ShareCode,
			ReceiveCode: sh.ReceiveCode,
			CID:         "0",
			Offset:      0,
			Limit:       1,
		})
		if err != nil {
			summary.Failed++
			fmt.Fprintf(out, "share=%s validate_failed error=%q\n", sh.ShareCode, err.Error())
			continue
		}
		if !resp.ValidShare() {
			summary.Failed++
			fmt.Fprintf(out, "share=%s validate_failed error=%q\n", sh.ShareCode, "dead or invalid share")
			continue
		}
		dbCount, err := store.CountFilesByShare(ctx, sh.ShareCode)
		if err != nil {
			summary.Failed++
			fmt.Fprintf(out, "share=%s validate_failed error=%q\n", sh.ShareCode, err.Error())
			continue
		}
		summary.Validated++
		if resp.Data.Count != dbCount {
			summary.Mismatched++
			title := sh.ShareTitle
			if resp.Data.ShareInfo.ShareTitle != "" {
				title = resp.Data.ShareInfo.ShareTitle
			}
			fmt.Fprintf(out, "share=%s api_count=%d db_count=%d title=%q\n", sh.ShareCode, resp.Data.Count, dbCount, title)
		}
	}
	fmt.Fprintf(out, "validated=%d mismatched=%d failed=%d\n", summary.Validated, summary.Mismatched, summary.Failed)
	return summary, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/115-indexer -run TestValidateShareCountsReportsMismatchAndContinuesAfterFailure -v`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/115-indexer/validate.go cmd/115-indexer/validate_test.go
git commit -m "feat: add share count validation helper"
```

### Task 3: Wire the Validation Mode Into the CLI

**Files:**
- Modify: `cmd/115-indexer/main.go`
- Modify: `cmd/115-indexer/config_test.go`
- Test: `cmd/115-indexer/validate_test.go`

- [ ] **Step 1: Write the failing tests**

First, extend `TestNeedsProxyForMode` in `cmd/115-indexer/config_test.go` so proxy-required modes include the new validator:

```go
for _, mode := range []string{"crawl", "run-scheduler-once", "daemon", "validate-share-counts"} {
	if !needsProxy(mode) {
		t.Fatalf("needsProxy(%q) = false, want true", mode)
	}
}
```

Second, add a lightweight integration-oriented helper test in `cmd/115-indexer/validate_test.go`:

```go
func TestValidateShareCountsReportsStoreFailure(t *testing.T) {
	fetcher := &fakeCountFetcher{
		responses: map[string]api115.SnapResponse{
			"sw1": {
				State: true,
				Data: api115.SnapData{
					Count: 1,
					ShareInfo: api115.SnapShareInfo{
						ShareState: 1,
						ShareTitle: "One",
					},
				},
			},
		},
	}
	store := fakeCountStore{
		errs: map[string]error{
			"sw1": errors.New("db busy"),
		},
	}

	var out bytes.Buffer
	summary, err := validateShareCounts(context.Background(), fetcher, store, []model.Share{{ShareCode: "sw1", ReceiveCode: "pw"}}, &out)
	if err != nil {
		t.Fatalf("validate share counts: %v", err)
	}
	if summary.Validated != 0 || summary.Mismatched != 0 || summary.Failed != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if !strings.Contains(out.String(), `share=sw1 validate_failed error="db busy"`) {
		t.Fatalf("output = %q", out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/115-indexer -run 'TestNeedsProxyForMode|TestValidateShareCountsReportsStoreFailure' -v`

Expected: FAIL because `needsProxy` does not yet treat `validate-share-counts` as direct-access mode.

- [ ] **Step 3: Write minimal implementation**

In `cmd/115-indexer/main.go`:

1. Add a `case "validate-share-counts":` branch after `backfill-share-meta`.
2. Build the 115 client the same way as `backfill-share-meta`, including optional proxy wiring.
3. Load shares from the database with `s.ListShares(ctx)`.
4. Call `validateShareCounts(ctx, client, s, shares, os.Stdout)`.
5. Treat any setup failure as fatal.

Use this branch body:

```go
	case "validate-share-counts":
		shares, err := s.ListShares(ctx)
		if err != nil {
			log.Fatalf("list shares: %v", err)
		}
		client := &api115.Client{
			HTTPClient:  &http.Client{Timeout: 20 * time.Second},
			Cookie:      *cookie,
			CookieStore: cookieStore,
			UserAgent:   *userAgent,
		}
		if cfg, perr := resolveProxyConfig(*proxyKey, *proxyPassword, *envFile); perr == nil {
			proxyMgr := proxy.New(proxy.Config{})
			provider := newProxyProvider(cfg)
			validator := &proxy.HTTPValidator{UserAgent: *userAgent, Cookie: *cookie}
			client.ProxyPool = proxyAccess{manager: proxyMgr, provider: provider, validator: validator}
		} else {
			log.Printf("event=validate_direct reason=%q", perr.Error())
		}
		if _, err := validateShareCounts(ctx, client, s, shares, os.Stdout); err != nil {
			log.Fatalf("validate share counts: %v", err)
		}
```

Then update `needsProxy` so it returns `true` for `validate-share-counts`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/115-indexer -run 'TestNeedsProxyForMode|TestValidateShareCountsReportsStoreFailure|TestValidateShareCountsReportsMismatchAndContinuesAfterFailure' -v`

Expected: PASS

- [ ] **Step 5: Run the relevant package test suites**

Run: `go test ./cmd/115-indexer ./internal/store`

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/115-indexer/main.go cmd/115-indexer/config_test.go cmd/115-indexer/validate.go cmd/115-indexer/validate_test.go internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat: add share count validation mode"
```
