package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"five/internal/model"
)

// TestUpsertFilesKeepsRootPerShareForSharedCID guards the fix for the bug where
// the 115 cid (file_id) is NOT globally unique: several share links can point at
// the same underlying folder and therefore share its root cid. The file table
// must key on (share_code, file_id), and UpsertFiles must scope its conflict to
// that pair — otherwise a later crawl of the duplicate share steals the earlier
// share's root row (ON CONFLICT(file_id) reassigning share_code), leaving it with
// no parent_id='0' row and appearing empty in the consumer.
func TestUpsertFilesKeepsRootPerShareForSharedCID(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	const rootCID = "3045342569775342059"
	// Share A is crawled first: it owns the shared root folder + a child.
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: rootCID, ShareCode: "swA", ParentID: "0", Name: "原盘精选", IsDir: true, Depth: 1, CrawledAt: 1},
		{FileID: "childA", ShareCode: "swA", ParentID: rootCID, Name: "A.mkv", Ext: "mkv", Depth: 2, CrawledAt: 1},
	}); err != nil {
		t.Fatalf("upsert share A: %v", err)
	}
	// Share B is crawled later and points at the SAME root cid.
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: rootCID, ShareCode: "swB", ParentID: "0", Name: "原盘精选", IsDir: true, Depth: 1, CrawledAt: 2},
		{FileID: "childB", ShareCode: "swB", ParentID: rootCID, Name: "B.mkv", Ext: "mkv", Depth: 2, CrawledAt: 2},
	}); err != nil {
		t.Fatalf("upsert share B: %v", err)
	}

	// Both shares must retain their OWN root row; neither steals the other's.
	for _, sc := range []string{"swA", "swB"} {
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM file WHERE share_code=? AND parent_id='0'`, sc).Scan(&n); err != nil {
			t.Fatalf("count roots %s: %v", sc, err)
		}
		if n != 1 {
			t.Fatalf("share %s root rows = %d, want 1 (shared root cid was stolen by the other share)", sc, n)
		}
	}
	// Each share's child still resolves under its own share.
	var children int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM file WHERE parent_id=?`, rootCID).Scan(&children); err != nil {
		t.Fatalf("count children of shared root: %v", err)
	}
	if children != 2 {
		t.Fatalf("children under shared root = %d, want 2", children)
	}
}

// TestMigrationClonesStolenRootBackToVictims guards the in-migration repair for
// databases built before the composite-PK fix. Such a DB can already contain a
// "stolen root": one share (the holder) owns the row for a shared root cid, while
// another share (the victim) references that cid as a parent but has no root row
// of its own — so it renders empty. On Open the migration must rebuild the PK to
// (share_code, file_id) and clone the stolen root back under every victim.
func TestMigrationClonesStolenRootBackToVictims(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	// Simulate a pre-fix database: single-column PK and a stolen-root state.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE file (
		file_id TEXT PRIMARY KEY,
		share_code TEXT NOT NULL,
		parent_id TEXT NOT NULL,
		name TEXT NOT NULL,
		ext TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		is_dir INTEGER NOT NULL DEFAULT 0,
		depth INTEGER NOT NULL DEFAULT 0,
		sha1 TEXT NOT NULL DEFAULT '',
		updated_at INTEGER,
		crawled_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy file table: %v", err)
	}
	const rootCID = "3045342569775342059"
	// swB is the holder of the shared (stolen) root.
	if _, err := raw.ExecContext(ctx, `INSERT INTO file(file_id, share_code, parent_id, name, is_dir, depth, crawled_at)
		VALUES(?, 'swB', '0', '原盘精选', 1, 1, 2)`, rootCID); err != nil {
		t.Fatalf("seed holder root: %v", err)
	}
	// swA is the victim: its child references the stolen root, but swA has no root row.
	if _, err := raw.ExecContext(ctx, `INSERT INTO file(file_id, share_code, parent_id, name, is_dir, depth, crawled_at)
		VALUES('childA', 'swA', ?, 'A.mkv', 0, 2, 1)`, rootCID); err != nil {
		t.Fatalf("seed victim child: %v", err)
	}
	raw.Close()

	// Opening migrates the PK and clones the stolen root back under the victim.
	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open (migrate): %v", err)
	}
	defer s.Close()

	// swA must now have its own root row cloned from the holder.
	var name string
	if err := s.db.QueryRowContext(ctx,
		`SELECT name FROM file WHERE share_code='swA' AND parent_id='0'`).Scan(&name); err != nil {
		t.Fatalf("swA root after repair missing: %v", err)
	}
	if name != "原盘精选" {
		t.Fatalf("swA cloned root name = %q, want 原盘精选", name)
	}
	// swB keeps its own root too (the clone is additive, not a move).
	var holderRoots int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM file WHERE share_code='swB' AND parent_id='0'`).Scan(&holderRoots); err != nil {
		t.Fatalf("count swB roots: %v", err)
	}
	if holderRoots != 1 {
		t.Fatalf("swB root rows after repair = %d, want 1", holderRoots)
	}
}
