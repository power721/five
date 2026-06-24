package store

import (
	"context"
	"path/filepath"
	"testing"

	"five/internal/model"
)

func TestSQLiteStoreDeleteShareRemovesFilesCheckpointAndShare(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert sw1: %v", err)
	}
	// A second share must be left untouched.
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw2", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert sw2: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f1','sw1','0','a.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f2','sw1','0','b.mkv','mkv',2,0,1,'',1)`,
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('g1','sw2','0','c.mkv','mkv',3,0,1,'',1)`,
		`INSERT INTO crawl_checkpoint(share_code, cid, next_offset, active_depth, queue_json, visited_json, updated_at) VALUES('sw1','root',0,0,'[]','{}',1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	ok, err := s.DeleteShare(ctx, "sw1")
	if err != nil || !ok {
		t.Fatalf("DeleteShare sw1: ok=%v err=%v", ok, err)
	}

	for _, c := range []struct{ table, where string }{
		{"share", "share_code='sw1'"},
		{"file", "share_code='sw1'"},
		{"crawl_checkpoint", "share_code='sw1'"},
	} {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+c.table+" WHERE "+c.where).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", c.table, err)
		}
		if n != 0 {
			t.Fatalf("sw1 rows left in %s = %d, want 0", c.table, n)
		}
	}

	// sw2 must survive intact.
	if _, ok, err := s.GetShare(ctx, "sw2"); err != nil || !ok {
		t.Fatalf("sw2 must remain after deleting sw1: ok=%v err=%v", ok, err)
	}
	var n2 int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code='sw2'`).Scan(&n2); err != nil {
		t.Fatalf("count sw2 files: %v", err)
	}
	if n2 != 1 {
		t.Fatalf("sw2 file count = %d, want 1 (delete must be scoped)", n2)
	}
}

func TestSQLiteStoreDeleteShareWithNoFiles(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	ok, err := s.DeleteShare(ctx, "sw1")
	if err != nil || !ok {
		t.Fatalf("DeleteShare empty share: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetShare(ctx, "sw1"); err != nil || ok {
		t.Fatalf("sw1 must be gone after delete: ok=%v err=%v", ok, err)
	}
}

func TestSQLiteStoreDeleteShareUnknownReturnsFalse(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ok, err := s.DeleteShare(ctx, "does-not-exist")
	if err != nil || ok {
		t.Fatalf("DeleteShare unknown: ok=%v err=%v, want false/nil", ok, err)
	}
}
