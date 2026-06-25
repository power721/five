package store

import (
	"context"
	"testing"

	"five/internal/model"
)

func TestPurgeIfOrphan(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()
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
