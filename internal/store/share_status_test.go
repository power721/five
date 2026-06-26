package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"five/internal/model"
)

func TestSQLiteStoreMarkShareShelvedParksShareAndKeepsFiles(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f1','sw1','0','a.mkv','mkv',1,0,1,'',1)`); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if err := s.MarkShareShelved(ctx, "sw1", "RETRYABLE: empty data with nonzero count"); err != nil {
		t.Fatalf("shelve: %v", err)
	}

	got, ok, err := s.GetShare(ctx, "sw1")
	if err != nil || !ok {
		t.Fatalf("get share: %v ok=%v", err, ok)
	}
	if got.Status != "QUARANTINE" {
		t.Fatalf("status = %q, want QUARANTINE (shelving must not DEAD-prune)", got.Status)
	}

	now := time.Now().Unix()
	// Parked: not due now (far-future retry_after)...
	due, err := s.ListSharesForCrawl(ctx, now)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	for _, sh := range due {
		if sh.ShareCode == "sw1" {
			t.Fatal("shelved share must not be due for crawl now")
		}
	}
	// ...yet recoverable past the shelf window (not DEAD-gone).
	due, err = s.ListSharesForCrawl(ctx, now+shelvedRetryAfterSeconds+1)
	if err != nil {
		t.Fatalf("list due far future: %v", err)
	}
	var found bool
	for _, sh := range due {
		if sh.ShareCode == "sw1" {
			found = true
		}
	}
	if !found {
		t.Fatal("shelved share should become due again past its shelf window (recoverable, not pruned)")
	}

	// Crawled files must survive shelving.
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code='sw1'`).Scan(&n); err != nil {
		t.Fatalf("count files: %v", err)
	}
	if n != 1 {
		t.Fatalf("file count = %d, want 1 (shelving must keep files)", n)
	}
}

func TestSQLiteStoreReactivateShareClearsShelfAndReschedules(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}
	if err := s.MarkShareShelved(ctx, "sw1", "RETRYABLE: empty data with nonzero count"); err != nil {
		t.Fatalf("shelve: %v", err)
	}

	now := time.Now().Unix()
	due, err := s.ListSharesForCrawl(ctx, now)
	if err != nil {
		t.Fatalf("list due before: %v", err)
	}
	for _, sh := range due {
		if sh.ShareCode == "sw1" {
			t.Fatal("shelved share must not be due before reactivation")
		}
	}

	ok, err := s.ReactivateShare(ctx, "sw1")
	if err != nil || !ok {
		t.Fatalf("reactivate: ok=%v err=%v", ok, err)
	}

	got, _, err := s.GetShare(ctx, "sw1")
	if err != nil {
		t.Fatalf("get share: %v", err)
	}
	if got.Status != "ACTIVE" || got.FailureCount != 0 || got.RetryAfterUnix != 0 || got.LastError != "" {
		t.Fatalf("after reactivation = status=%q failure_count=%d retry_after=%d last_error=%q, want ACTIVE/zeroed",
			got.Status, got.FailureCount, got.RetryAfterUnix, got.LastError)
	}

	// Reactivated: due again now.
	due, err = s.ListSharesForCrawl(ctx, now)
	if err != nil {
		t.Fatalf("list due after: %v", err)
	}
	var found bool
	for _, sh := range due {
		if sh.ShareCode == "sw1" {
			found = true
		}
	}
	if !found {
		t.Fatal("reactivated share must be due for crawl again")
	}

	// Reactivating an unknown share reports not-found, not an error.
	ok, err = s.ReactivateShare(ctx, "does-not-exist")
	if err != nil || ok {
		t.Fatalf("reactivate unknown: ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestSQLiteStoreCompletedShareExcludedFromCrawl(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}
	// ACTIVE → due now.
	due, err := s.ListSharesForCrawl(ctx, time.Now().Unix())
	if err != nil {
		t.Fatalf("list due before: %v", err)
	}
	if len(due) != 1 || due[0].ShareCode != "sw1" {
		t.Fatalf("due before = %#v, want [sw1]", due)
	}

	// A complete crawl parks it at COMPLETED: no longer due.
	if err := s.MarkShareCrawled(ctx, "sw1", time.Now().Unix()); err != nil {
		t.Fatalf("mark crawled: %v", err)
	}
	due, err = s.ListSharesForCrawl(ctx, time.Now().Unix())
	if err != nil {
		t.Fatalf("list due after: %v", err)
	}
	for _, sh := range due {
		if sh.ShareCode == "sw1" {
			t.Fatalf("COMPLETED share must not be due for crawl; got %#v", sh)
		}
	}
}

func TestSQLiteStoreReactivateShareResetsCompleted(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}
	if err := s.MarkShareCrawled(ctx, "sw1", 123); err != nil {
		t.Fatalf("mark crawled: %v", err)
	}
	if got, _, _ := s.GetShare(ctx, "sw1"); got.Status != "COMPLETED" {
		t.Fatalf("precondition: status = %q, want COMPLETED", got.Status)
	}

	ok, err := s.ReactivateShare(ctx, "sw1")
	if err != nil || !ok {
		t.Fatalf("reactivate: ok=%v err=%v", ok, err)
	}
	got, _, err := s.GetShare(ctx, "sw1")
	if err != nil {
		t.Fatalf("get share: %v", err)
	}
	if got.Status != "ACTIVE" || got.RetryAfterUnix != 0 {
		t.Fatalf("after reactivation = status=%q retry_after=%d, want ACTIVE/0", got.Status, got.RetryAfterUnix)
	}

	// Due for crawl again after reactivation.
	due, err := s.ListSharesForCrawl(ctx, time.Now().Unix())
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	var found bool
	for _, sh := range due {
		if sh.ShareCode == "sw1" {
			found = true
		}
	}
	if !found {
		t.Fatal("reactivated COMPLETED share must be due for crawl again")
	}
}
