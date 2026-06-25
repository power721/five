package store

import (
	"context"
	"testing"

	"five/internal/model"
)

func TestMarkShareDuplicateSetsStatusAndCanonical(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()
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
	s, cleanup := openTestStore(t)
	defer cleanup()
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
	s, cleanup := openTestStore(t)
	defer cleanup()
	mk := func(code string, size int64, crawled int64) {
		t.Helper()
		if err := s.UpsertShare(ctx, model.Share{ShareCode: code, ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
			t.Fatal(err)
		}
		// UpsertShare does not persist file_size (UpdateShareMeta's job); set it directly.
		if _, err := s.db.ExecContext(ctx, `UPDATE share SET last_crawled_at=?, file_size=? WHERE share_code=?`, crawled, size, code); err != nil {
			t.Fatal(err)
		}
	}
	mk("canon", 2<<30, 100) // oldest -> canonical
	mk("late", 2<<30, 200)  // same size, later -> the duplicate
	mk("other", 3<<30, 150) // different size -> not a dup

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

func TestDedupeSharesBySize(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()
	mk := func(code string, size int64, crawled int64) {
		t.Helper()
		if err := s.UpsertShare(ctx, model.Share{ShareCode: code, ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE share SET last_crawled_at=?, file_size=? WHERE share_code=?`, crawled, size, code); err != nil {
			t.Fatal(err)
		}
	}
	mk("keep", 2<<30, 100) // oldest -> canonical
	mk("lose", 2<<30, 200) // same size, later -> loser
	mk("solo", 3<<30, 150) // unique -> untouched
	if _, err := s.db.ExecContext(ctx, `INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f1','lose','0','a.mkv','mkv',1,0,1,'',1)`); err != nil {
		t.Fatal(err)
	}

	// dry-run: reports the action, changes nothing.
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

	// apply: marks loser DUPLICATE + deletes its files.
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
	keep, _, _ := s.GetShare(ctx, "keep")
	if keep.Status != "ACTIVE" {
		t.Fatalf("canonical mutated: %+v", keep)
	}
}
