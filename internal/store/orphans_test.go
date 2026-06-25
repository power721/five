package store

import (
	"context"
	"testing"

	"five/internal/model"
)

func TestOrphans(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "alive", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	seeds := []string{
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fa','alive','0','a.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f1','ghost','0','g.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f2','ghost','0','h.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO crawl_checkpoint(share_code, cid, next_offset, active_depth, queue_json, visited_json, updated_at) VALUES('ghost','0',0,0,'[]','{}',1)`,
	}
	for _, q := range seeds {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	orphans, err := s.OrphanShares(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].ShareCode != "ghost" || orphans[0].FileCount != 2 {
		t.Fatalf("orphans = %#v", orphans)
	}

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
