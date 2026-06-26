package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"five/internal/model"
)

func TestSQLiteStoreMigrateUpsertCheckpointAndManifest(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().Unix()
	files := []model.File{
		{
			FileID:    "f1",
			ShareCode: "swf01d43zby",
			ParentID:  "0",
			Name:      "Avatar.mkv",
			Ext:       "mkv",
			Size:      1024,
			IsDir:     false,
			Depth:     1,
			SHA1:      "sha1",
			CrawledAt: now,
		},
		{
			FileID:    "d1",
			ShareCode: "swf01d43zby",
			ParentID:  "0",
			Name:      "Season 01",
			Ext:       "",
			Size:      0,
			IsDir:     true,
			Depth:     1,
			CrawledAt: now,
		},
	}

	if err := s.UpsertFiles(ctx, files); err != nil {
		t.Fatalf("upsert files: %v", err)
	}
	if err := s.UpsertFiles(ctx, files); err != nil {
		t.Fatalf("idempotent upsert files: %v", err)
	}

	allFiles, err := s.AllFiles(ctx)
	if err != nil {
		t.Fatalf("all files: %v", err)
	}
	if len(allFiles) != 2 {
		t.Fatalf("file count = %d, want 2", len(allFiles))
	}

	cp := model.Checkpoint{
		ShareCode: "swf01d43zby",
		CID:       "0",
		Queue: []model.CrawlTask{
			{CID: "1", Depth: 1},
		},
		Visited: map[string]bool{
			"0": true,
		},
		UpdatedAt: now,
	}
	if err := s.SaveCheckpoint(ctx, cp); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	loaded, ok, err := s.LoadCheckpoint(ctx, cp.ShareCode)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if !ok {
		t.Fatal("expected checkpoint to exist")
	}
	if loaded.Queue[0].CID != "1" {
		t.Fatalf("checkpoint queue cid = %q", loaded.Queue[0].CID)
	}
	if !loaded.Visited["0"] {
		t.Fatal("expected visited root")
	}

	manifest := model.IndexManifest{
		Version:   1,
		IndexPath: "data/bleve/index_000001",
		Status:    "READY",
		BuiltAt:   now,
		FileCount: 2,
	}
	if err := s.UpdateManifest(ctx, manifest); err != nil {
		t.Fatalf("update manifest: %v", err)
	}
	got, ok, err := s.LoadManifest(ctx)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if !ok {
		t.Fatal("expected manifest to exist")
	}
	if got.IndexPath != manifest.IndexPath {
		t.Fatalf("manifest path = %q", got.IndexPath)
	}
}

func TestSQLiteStoreCountsStatusAggregates(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "pw1", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert first share: %v", err)
	}
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw2", ReceiveCode: "pw2", Status: "STALE"}); err != nil {
		t.Fatalf("upsert second share: %v", err)
	}
	now := time.Now().Unix()
	files := []model.File{
		{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Ext: "mkv", CrawledAt: now},
		{FileID: "f2", ShareCode: "sw1", ParentID: "0", Name: "b.mkv", Ext: "mkv", CrawledAt: now},
		{FileID: "f3", ShareCode: "sw2", ParentID: "0", Name: "c.mkv", Ext: "mkv", CrawledAt: now},
	}
	if err := s.UpsertFiles(ctx, files); err != nil {
		t.Fatalf("upsert files: %v", err)
	}
	shareCount, err := s.CountShares(ctx)
	if err != nil {
		t.Fatalf("count shares: %v", err)
	}
	if shareCount != 2 {
		t.Fatalf("share count = %d, want 2", shareCount)
	}
	fileCount, err := s.CountFiles(ctx)
	if err != nil {
		t.Fatalf("count files: %v", err)
	}
	if fileCount != 3 {
		t.Fatalf("file count = %d, want 3", fileCount)
	}
}

func TestSQLiteShareSchemaDoesNotKeepRootFolderOrMountPath(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(share)`)
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		columns[name] = true
	}
	if columns["root_folder_id"] {
		t.Fatal("share schema should not contain root_folder_id")
	}
	if columns["mount_path"] {
		t.Fatal("share schema should not contain mount_path")
	}
}

func TestSQLiteStoreShareRegistryAndStateTransitions(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	share := model.Share{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
		Status:      "ACTIVE",
	}
	if err := s.UpsertShare(ctx, share); err != nil {
		t.Fatalf("upsert share: %v", err)
	}

	due, err := s.ListSharesForCrawl(ctx, time.Now().Unix())
	if err != nil {
		t.Fatalf("list shares for crawl: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due shares = %d, want 1", len(due))
	}
	allShares, err := s.ListShares(ctx)
	if err != nil {
		t.Fatalf("list all shares: %v", err)
	}
	if len(allShares) != 1 || allShares[0].ShareCode != share.ShareCode {
		t.Fatalf("all shares = %#v", allShares)
	}

	if err := s.RecordShareFailure(ctx, share.ShareCode, "timeout"); err != nil {
		t.Fatalf("record share failure: %v", err)
	}
	got, ok, err := s.GetShare(ctx, share.ShareCode)
	if err != nil {
		t.Fatalf("get share: %v", err)
	}
	if !ok {
		t.Fatal("expected share to exist")
	}
	if got.Status != "STALE" {
		t.Fatalf("status after first failure = %q, want STALE", got.Status)
	}

	if err := s.RecordShareFailure(ctx, share.ShareCode, "timeout"); err != nil {
		t.Fatalf("record second share failure: %v", err)
	}
	if err := s.RecordShareFailure(ctx, share.ShareCode, "timeout"); err != nil {
		t.Fatalf("record third share failure: %v", err)
	}
	got, _, err = s.GetShare(ctx, share.ShareCode)
	if err != nil {
		t.Fatalf("get share after repeated failure: %v", err)
	}
	if got.Status != "QUARANTINE" {
		t.Fatalf("status after repeated failure = %q, want QUARANTINE", got.Status)
	}
	allShares, err = s.ListShares(ctx)
	if err != nil {
		t.Fatalf("list all shares after repeated failure: %v", err)
	}
	if len(allShares) != 1 || allShares[0].Status != "QUARANTINE" {
		t.Fatalf("all shares after repeated failure = %#v", allShares)
	}
	due, err = s.ListSharesForCrawl(ctx, time.Now().Unix())
	if err != nil {
		t.Fatalf("list shares for crawl after quarantine: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due shares after quarantine = %d, want 0", len(due))
	}

	got.RetryAfterUnix = time.Now().Unix() - 1
	if _, err := s.db.ExecContext(ctx, `UPDATE share SET retry_after_unix=? WHERE share_code=?`, got.RetryAfterUnix, share.ShareCode); err != nil {
		t.Fatalf("force retry after: %v", err)
	}
	due, err = s.ListSharesForCrawl(ctx, time.Now().Unix())
	if err != nil {
		t.Fatalf("list shares for crawl after retry due: %v", err)
	}
	if len(due) != 1 || due[0].ShareCode != share.ShareCode {
		t.Fatalf("due shares after retry due = %#v", due)
	}

	if err := s.MarkShareCrawled(ctx, share.ShareCode, time.Now().Unix()); err != nil {
		t.Fatalf("mark share crawled: %v", err)
	}
	got, _, err = s.GetShare(ctx, share.ShareCode)
	if err != nil {
		t.Fatalf("get share after crawl: %v", err)
	}
	if got.Status != "ACTIVE" {
		t.Fatalf("status after mark crawled = %q, want ACTIVE", got.Status)
	}

	if err := s.MarkShareDead(ctx, share.ShareCode, "invalid receive code"); err != nil {
		t.Fatalf("mark share dead: %v", err)
	}
	got, _, err = s.GetShare(ctx, share.ShareCode)
	if err != nil {
		t.Fatalf("get share after dead: %v", err)
	}
	if got.Status != "DEAD" {
		t.Fatalf("status after mark dead = %q, want DEAD", got.Status)
	}
}

func TestSQLiteStoreKVAndCookieStore(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.SaveKV(ctx, "115_cookie", "sessionid=abc123"); err != nil {
		t.Fatalf("save kv: %v", err)
	}
	value, ok, err := s.LoadKV(ctx, "115_cookie")
	if err != nil {
		t.Fatalf("load kv: %v", err)
	}
	if !ok || value != "sessionid=abc123" {
		t.Fatalf("kv value = %q ok=%v", value, ok)
	}

	cookies := NewCookieStore(s, "")
	cookies.Save("sessionid=xyz789")
	if got := cookies.Load(); got != "sessionid=xyz789" {
		t.Fatalf("cookie store value = %q", got)
	}
}

func TestSQLiteStoreUpdateShareMeta(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw68wz93ncb", ReceiveCode: "6666", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}

	if err := s.UpdateShareMeta(ctx, "sw68wz93ncb", "6666", "电影-欧美高清3.89T", 4273516964003); err != nil {
		t.Fatalf("update share meta: %v", err)
	}

	got, ok, err := s.GetShare(ctx, "sw68wz93ncb")
	if err != nil {
		t.Fatalf("get share: %v", err)
	}
	if !ok {
		t.Fatal("expected share to exist")
	}
	if got.ShareTitle != "电影-欧美高清3.89T" {
		t.Fatalf("share_title = %q", got.ShareTitle)
	}
	if got.FileSize != 4273516964003 {
		t.Fatalf("file_size = %d", got.FileSize)
	}

	// Re-importing the share (import-shares re-run) must not wipe backfilled metadata.
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw68wz93ncb", ReceiveCode: "6666", Status: "ACTIVE"}); err != nil {
		t.Fatalf("re-upsert share: %v", err)
	}
	got, _, err = s.GetShare(ctx, "sw68wz93ncb")
	if err != nil {
		t.Fatalf("get share after reimport: %v", err)
	}
	if got.ShareTitle != "电影-欧美高清3.89T" || got.FileSize != 4273516964003 {
		t.Fatalf("metadata wiped by reimport: title=%q size=%d", got.ShareTitle, got.FileSize)
	}

	// Backfilling a share that was never imported should register it.
	if err := s.UpdateShareMeta(ctx, "swnew", "pw", "New Share", 42); err != nil {
		t.Fatalf("update share meta for new share: %v", err)
	}
	got, ok, err = s.GetShare(ctx, "swnew")
	if err != nil {
		t.Fatalf("get new share: %v", err)
	}
	if !ok || got.ShareTitle != "New Share" || got.FileSize != 42 {
		t.Fatalf("new share meta = %#v ok=%v", got, ok)
	}
}

func TestSQLiteStoreCountFilesByShare(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Ext: "mkv", CrawledAt: 1},
		{FileID: "f2", ShareCode: "sw1", ParentID: "0", Name: "b.mkv", Ext: "mkv", CrawledAt: 1},
		{FileID: "f3", ShareCode: "sw2", ParentID: "0", Name: "c.mkv", Ext: "mkv", CrawledAt: 1},
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

func TestSQLiteStoreShareFileStats(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Ext: "mkv", Size: 1000, IsDir: false, CrawledAt: 1},
		{FileID: "f2", ShareCode: "sw1", ParentID: "0", Name: "b.mkv", Ext: "mkv", Size: 2500, IsDir: false, CrawledAt: 1},
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "sub", Ext: "", Size: 0, IsDir: true, CrawledAt: 1},
		{FileID: "f3", ShareCode: "sw2", ParentID: "0", Name: "c.mkv", Ext: "mkv", Size: 9999, IsDir: false, CrawledAt: 1},
	}); err != nil {
		t.Fatalf("upsert files: %v", err)
	}

	count, total, err := s.ShareFileStats(ctx, "sw1")
	if err != nil {
		t.Fatalf("share file stats sw1: %v", err)
	}
	if count != 2 {
		t.Fatalf("sw1 file count = %d, want 2 (dirs excluded)", count)
	}
	if total != 3500 {
		t.Fatalf("sw1 total size = %d, want 3500", total)
	}

	// Missing/empty share yields zero totals rather than an error.
	count, total, err = s.ShareFileStats(ctx, "missing")
	if err != nil {
		t.Fatalf("share file stats missing: %v", err)
	}
	if count != 0 || total != 0 {
		t.Fatalf("missing stats = (%d, %d), want (0, 0)", count, total)
	}
}

func TestSQLiteStoreExportSnapshotIsSelfContained(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "rc1", Status: "ACTIVE"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}
	if err := s.UpdateShareMeta(ctx, "sw1", "rc1", "Library One", 12345); err != nil {
		t.Fatalf("update share meta: %v", err)
	}
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Ext: "mkv", CrawledAt: 1},
	}); err != nil {
		t.Fatalf("upsert files: %v", err)
	}

	snapPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.ExportSnapshot(ctx, snapPath); err != nil {
		t.Fatalf("export snapshot: %v", err)
	}
	s.Close()

	// Prove self-containment: copy ONLY the snapshot file (no -wal/-shm sidecars)
	// to a fresh location and confirm all data is present. If anything were still
	// in a WAL sidecar, this isolated copy would be incomplete.
	data, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	isolated := filepath.Join(t.TempDir(), "isolated.db")
	if err := os.WriteFile(isolated, data, 0o644); err != nil {
		t.Fatalf("write isolated copy: %v", err)
	}

	snap, err := sql.Open("sqlite", isolated)
	if err != nil {
		t.Fatalf("open isolated snapshot: %v", err)
	}
	defer snap.Close()

	var files int
	if err := snap.QueryRowContext(ctx, "SELECT COUNT(*) FROM file").Scan(&files); err != nil {
		t.Fatalf("count files in snapshot: %v", err)
	}
	if files != 1 {
		t.Fatalf("snapshot file count = %d, want 1", files)
	}
	var title string
	var size int64
	if err := snap.QueryRowContext(ctx, "SELECT share_title, file_size FROM share WHERE share_code='sw1'").Scan(&title, &size); err != nil {
		t.Fatalf("query snapshot share: %v", err)
	}
	if title != "Library One" || size != 12345 {
		t.Fatalf("snapshot share meta = (%q, %d), want (Library One, 12345)", title, size)
	}
}

func TestSQLiteStoreMigrationAddsShareMetaColumnsToExistingDB(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	// Simulate a pre-existing deployment whose share table predates the new columns.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE share (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		share_code TEXT NOT NULL,
		receive_code TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'ACTIVE',
		last_crawled_at INTEGER,
		last_error TEXT,
		failure_count INTEGER NOT NULL DEFAULT 0,
		retry_after_unix INTEGER NOT NULL DEFAULT 0,
		version INTEGER NOT NULL DEFAULT 0,
		UNIQUE(share_code, receive_code)
	)`); err != nil {
		t.Fatalf("create legacy share: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO share(share_code, receive_code) VALUES('sw1','rc1')`); err != nil {
		t.Fatalf("seed legacy share: %v", err)
	}
	raw.Close()

	// Opening through the store must migrate the table in place.
	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store (migrate): %v", err)
	}
	defer s.Close()

	got, ok, err := s.GetShare(ctx, "sw1")
	if err != nil {
		t.Fatalf("get migrated share: %v", err)
	}
	if !ok {
		t.Fatal("expected legacy share to survive migration")
	}
	if got.ShareTitle != "" || got.FileSize != 0 {
		t.Fatalf("expected default meta, got title=%q size=%d", got.ShareTitle, got.FileSize)
	}
	if err := s.UpdateShareMeta(ctx, "sw1", "rc1", "T", 7); err != nil {
		t.Fatalf("update meta after migrate: %v", err)
	}
	got, _, _ = s.GetShare(ctx, "sw1")
	if got.ShareTitle != "T" || got.FileSize != 7 {
		t.Fatalf("post-migrate update failed: %#v", got)
	}
}

func TestSQLiteStoreMigrationDropsLegacyPathColumn(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	// Simulate a pre-migration DB: file carried a redundant path column (only ever
	// "/name") plus its index.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE file (
		file_id TEXT PRIMARY KEY,
		share_code TEXT NOT NULL,
		parent_id TEXT NOT NULL,
		name TEXT NOT NULL,
		path TEXT NOT NULL,
		ext TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		is_dir INTEGER NOT NULL DEFAULT 0,
		depth INTEGER NOT NULL DEFAULT 0,
		sha1 TEXT NOT NULL DEFAULT '',
		updated_at INTEGER,
		crawled_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy file: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE INDEX idx_file_share_path ON file(share_code, path)`); err != nil {
		t.Fatalf("create legacy index: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO file(file_id, share_code, parent_id, name, path, crawled_at) VALUES('f1','sw1','0','a.mkv','/a.mkv',1)`); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}
	raw.Close()

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store (migrate): %v", err)
	}
	defer s.Close()

	if exists, err := s.columnExists(ctx, "file", "path"); err != nil {
		t.Fatalf("columnExists: %v", err)
	} else if exists {
		t.Fatal("legacy path column should be dropped after migration")
	}
	// The row must survive the column drop.
	got, ok, err := s.FileByID(ctx, "f1")
	if err != nil || !ok {
		t.Fatalf("file f1 after migrate: ok=%v err=%v", ok, err)
	}
	if got.Name != "a.mkv" {
		t.Fatalf("name after migrate = %q, want a.mkv", got.Name)
	}
	// The legacy index must be gone too.
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_file_share_path'`).Scan(&n); err != nil {
		t.Fatalf("count legacy index: %v", err)
	}
	if n != 0 {
		t.Fatalf("legacy idx_file_share_path should be dropped, found %d", n)
	}
}

func TestSQLiteStoreDropsUnusedFileIndexes(t *testing.T) {
	ctx := context.Background()

	fresh, cleanup := openTestStore(t)
	defer cleanup()
	assertFileIndex(t, fresh.db, "idx_file_share_parent", true)
	assertFileIndex(t, fresh.db, "idx_file_size", true)
	assertFileIndex(t, fresh.db, "idx_file_id", true)
	assertFileIndex(t, fresh.db, "idx_file_ext", false)
	assertFileIndex(t, fresh.db, "idx_file_depth", false)

	dbPath := filepath.Join(t.TempDir(), "index.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE file (
		file_id TEXT NOT NULL,
		share_code TEXT NOT NULL,
		parent_id TEXT NOT NULL,
		name TEXT NOT NULL,
		ext TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		is_dir INTEGER NOT NULL DEFAULT 0,
		depth INTEGER NOT NULL DEFAULT 0,
		sha1 TEXT NOT NULL DEFAULT '',
		updated_at INTEGER,
		crawled_at INTEGER NOT NULL,
		PRIMARY KEY (share_code, file_id)
	)`); err != nil {
		t.Fatalf("create file: %v", err)
	}
	for _, stmt := range []string{
		`CREATE INDEX idx_file_share_parent ON file(share_code, parent_id)`,
		`CREATE INDEX idx_file_ext ON file(ext)`,
		`CREATE INDEX idx_file_depth ON file(depth)`,
		`CREATE INDEX idx_file_size ON file(size)`,
		`CREATE INDEX idx_file_id ON file(file_id)`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("create index %q: %v", stmt, err)
		}
	}
	raw.Close()

	migrated, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer migrated.Close()
	assertFileIndex(t, migrated.db, "idx_file_share_parent", true)
	assertFileIndex(t, migrated.db, "idx_file_size", true)
	assertFileIndex(t, migrated.db, "idx_file_id", true)
	assertFileIndex(t, migrated.db, "idx_file_ext", false)
	assertFileIndex(t, migrated.db, "idx_file_depth", false)
}

func assertFileIndex(t *testing.T, db *sql.DB, name string, want bool) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='file' AND name=?`, name).Scan(&n); err != nil {
		t.Fatalf("count index %s: %v", name, err)
	}
	if got := n == 1; got != want {
		t.Fatalf("index %s exists = %v, want %v", name, got, want)
	}
}

func TestExportTrimmedKeepsFileShareAndShareGroup(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "rc1"}); err != nil {
		t.Fatalf("upsert share: %v", err)
	}
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Ext: "mkv", CrawledAt: 1},
	}); err != nil {
		t.Fatalf("upsert files: %v", err)
	}
	// Populate the tables that must be dropped.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO crawl_checkpoint(share_code,cid,next_offset,active_depth,queue_json,visited_json,updated_at) VALUES('sw1','0',0,0,'[]','{}',1)`); err != nil {
		t.Fatalf("insert checkpoint: %v", err)
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

	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
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
	if got := strings.Join(tables, ","); got != "file,share,share_group" {
		t.Fatalf("tables = %q, want file,share,share_group", got)
	}

	var files, shares int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM file").Scan(&files)
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM share").Scan(&shares)
	if files != 1 || shares != 1 {
		t.Fatalf("counts files=%d shares=%d, want 1/1", files, shares)
	}

	for _, name := range []string{"idx_file_share_parent", "idx_file_size", "idx_file_id"} {
		var idx int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='file' AND name=?", name).Scan(&idx); err != nil {
			t.Fatalf("count index %s: %v", name, err)
		}
		if idx != 1 {
			t.Fatalf("index %s count = %d, want 1", name, idx)
		}
	}
}

func TestExportTrimmedPrunesDeadSharesAndTheirFiles(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// An alive share with a file — must survive the export.
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "rc1"}); err != nil {
		t.Fatalf("upsert share sw1: %v", err)
	}
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Ext: "mkv", CrawledAt: 1},
	}); err != nil {
		t.Fatalf("upsert files sw1: %v", err)
	}
	// A dead (cancelled) share with two files — share and files must be pruned.
	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw2", ReceiveCode: "rc2"}); err != nil {
		t.Fatalf("upsert share sw2: %v", err)
	}
	if err := s.MarkShareDead(ctx, "sw2", "DEAD_SHARE: 分享已取消"); err != nil {
		t.Fatalf("mark share dead: %v", err)
	}
	if err := s.UpsertFiles(ctx, []model.File{
		{FileID: "f2", ShareCode: "sw2", ParentID: "0", Name: "b.mkv", Ext: "mkv", CrawledAt: 1},
		{FileID: "f3", ShareCode: "sw2", ParentID: "0", Name: "c.mkv", Ext: "mkv", CrawledAt: 1},
	}); err != nil {
		t.Fatalf("upsert files sw2: %v", err)
	}

	trimmed := filepath.Join(t.TempDir(), "trimmed.db")
	if err := s.ExportTrimmed(ctx, trimmed); err != nil {
		t.Fatalf("export trimmed: %v", err)
	}
	s.Close()

	db, err := sql.Open("sqlite", trimmed)
	if err != nil {
		t.Fatalf("open trimmed: %v", err)
	}
	defer db.Close()

	var shares int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM share").Scan(&shares)
	if shares != 1 {
		t.Fatalf("shares = %d, want 1 (dead share must be pruned)", shares)
	}
	var deadCode string
	db.QueryRowContext(ctx, "SELECT share_code FROM share").Scan(&deadCode)
	if deadCode != "sw1" {
		t.Fatalf("remaining share = %q, want sw1", deadCode)
	}

	var files int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM file").Scan(&files)
	if files != 1 {
		t.Fatalf("files = %d, want 1 (dead share's files must be pruned)", files)
	}
	var fileID string
	db.QueryRowContext(ctx, "SELECT file_id FROM file").Scan(&fileID)
	if fileID != "f1" {
		t.Fatalf("remaining file = %q, want f1", fileID)
	}
}

func TestApplyGroupsAssignsMembershipAndDormant(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()

	for _, sc := range []string{"sw1", "sw2"} {
		if err := s.UpsertShare(ctx, model.Share{ShareCode: sc, ReceiveCode: "r", Status: "ACTIVE"}); err != nil {
			t.Fatalf("upsert %s: %v", sc, err)
		}
	}

	// sw1 grouped; sw3 grouped but absent from the index (dormant); sw2 ungrouped.
	groups := []model.ShareGroup{
		{Name: "欧美剧", ShareCodes: []string{"sw1", "sw3"}},
		{Name: "纪录片", ShareCodes: []string{}},
	}
	dormant, err := s.ApplyGroups(ctx, groups)
	if err != nil {
		t.Fatalf("ApplyGroups() error = %v", err)
	}
	if !reflect.DeepEqual(dormant, []string{"sw3"}) {
		t.Fatalf("dormant = %v, want [sw3]", dormant)
	}

	var g1, g2 int
	var sw1Group, sw2Group sql.NullInt64
	mustScan(t, s, `SELECT group_id FROM share_group WHERE name='欧美剧'`, &g1)
	mustScan(t, s, `SELECT group_id FROM share_group WHERE name='纪录片'`, &g2)
	if g1 != 1 || g2 != 2 {
		t.Fatalf("group ids = %d,%d, want 1,2", g1, g2)
	}
	mustScan(t, s, `SELECT group_id FROM share WHERE share_code='sw1'`, &sw1Group)
	mustScan(t, s, `SELECT group_id FROM share WHERE share_code='sw2'`, &sw2Group)
	if !sw1Group.Valid || sw1Group.Int64 != 1 {
		t.Fatalf("sw1 group_id = %+v, want 1", sw1Group)
	}
	if sw2Group.Valid {
		t.Fatalf("sw2 group_id = %+v, want NULL", sw2Group)
	}
}

func TestApplyGroupsReappliesAndClearsRemovedMembers(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "r", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyGroups(ctx, []model.ShareGroup{{Name: "欧美剧", ShareCodes: []string{"sw1"}}}); err != nil {
		t.Fatal(err)
	}
	// Re-apply with sw1 removed from all groups -> group_id must be NULL again.
	if _, err := s.ApplyGroups(ctx, []model.ShareGroup{{Name: "其他", ShareCodes: []string{}}}); err != nil {
		t.Fatal(err)
	}
	var g sql.NullInt64
	mustScan(t, s, `SELECT group_id FROM share WHERE share_code='sw1'`, &g)
	if g.Valid {
		t.Fatalf("sw1 group_id = %+v, want NULL after re-apply", g)
	}
}

func TestExportTrimmedRetainsShareGroup(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "r", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyGroups(ctx, []model.ShareGroup{{Name: "欧美剧", ShareCodes: []string{"sw1"}}}); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "trimmed.db")
	if err := s.ExportTrimmed(ctx, dest); err != nil {
		t.Fatalf("ExportTrimmed() error = %v", err)
	}
	db, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"share_group", "share", "file"} {
		var n int
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n); err != nil {
			t.Fatalf("trimmed db missing table %s: %v", table, err)
		}
	}
	var hasCol int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('share') WHERE name='group_id'`).Scan(&hasCol); err != nil {
		t.Fatal(err)
	}
	if hasCol != 1 {
		t.Fatalf("trimmed share.group_id missing")
	}
}

func TestExportTrimmedKeepsCrawledAtByDefault(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "r", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertFiles(ctx, []model.File{{
		FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "movie.mkv",
		Ext: "mkv", Size: 100, SHA1: "abc", CrawledAt: 1, UpdatedAt: 2,
	}}); err != nil {
		t.Fatalf("upsert files: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "trimmed.db")
	if err := s.ExportTrimmed(ctx, dest); err != nil {
		t.Fatalf("ExportTrimmed() error = %v", err)
	}
	db, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// crawled_at is kept by default so an exported DB can be reopened by five
	// for rebuild-index; updated_at is kept for consumers.
	var crawled, updated int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('file') WHERE name='crawled_at'`).Scan(&crawled); err != nil {
		t.Fatal(err)
	}
	if crawled != 1 {
		t.Fatalf("trimmed file table missing crawled_at")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('file') WHERE name='updated_at'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != 1 {
		t.Fatalf("trimmed file table missing updated_at (consumers use it)")
	}
	// File data and timestamps survive.
	var name string
	var crawledAt, updatedAt int64
	if err := db.QueryRowContext(ctx, `SELECT name, crawled_at, updated_at FROM file WHERE file_id='f1'`).Scan(&name, &crawledAt, &updatedAt); err != nil {
		t.Fatalf("read file after export: %v", err)
	}
	if name != "movie.mkv" || crawledAt != 1 || updatedAt != 2 {
		t.Fatalf("file = %q crawled_at=%d updated_at=%d, want movie.mkv / 1 / 2", name, crawledAt, updatedAt)
	}
}

func TestExportTrimmedCanStripCrawledAt(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "r", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertFiles(ctx, []model.File{{
		FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "movie.mkv",
		Ext: "mkv", Size: 100, SHA1: "abc", CrawledAt: 1, UpdatedAt: 2,
	}}); err != nil {
		t.Fatalf("upsert files: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "trimmed.db")
	if err := s.ExportTrimmed(ctx, dest, ExportTrimmedOptions{StripFileCrawledAt: true}); err != nil {
		t.Fatalf("ExportTrimmed() error = %v", err)
	}
	db, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var crawled, updated int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('file') WHERE name='crawled_at'`).Scan(&crawled); err != nil {
		t.Fatal(err)
	}
	if crawled != 0 {
		t.Fatalf("trimmed file table still has crawled_at")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('file') WHERE name='updated_at'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != 1 {
		t.Fatalf("trimmed file table missing updated_at")
	}
}

func openTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return s, func() { _ = s.Close() }
}

func mustScan(t *testing.T, s *Store, query string, dest ...any) {
	t.Helper()
	if err := s.db.QueryRow(query).Scan(dest...); err != nil {
		t.Fatalf("scan %q: %v", query, err)
	}
}

func TestMigrateAddsDuplicateOfColumn(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	var typ string
	err = s.db.QueryRowContext(ctx, "SELECT type FROM pragma_table_info('share') WHERE name='duplicate_of'").Scan(&typ)
	if err != nil {
		t.Fatalf("duplicate_of column missing: %v", err)
	}
	if typ != "TEXT" {
		t.Fatalf("duplicate_of type = %q, want TEXT", typ)
	}
}

func TestExportTrimmedPrunesDuplicateAndDead(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()
	for _, sh := range []model.Share{
		{ShareCode: "alive", ReceiveCode: "p", Status: "ACTIVE"},
		{ShareCode: "dead", ReceiveCode: "p", Status: "DEAD"},
		{ShareCode: "dup", ReceiveCode: "p", Status: "DUPLICATE"},
	} {
		if err := s.UpsertShare(ctx, sh); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fa','alive','0','a.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fd','dead','0','d.mkv','mkv',1,0,1,'',1)`,
		`INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('fx','dup','0','x.mkv','mkv',1,0,1,'',1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "trimmed.db")
	if err := s.ExportTrimmed(ctx, dest); err != nil {
		t.Fatalf("export: %v", err)
	}
	d, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, code := range []string{"dead", "dup"} {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM share WHERE share_code=?`, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("share %s survived export, want pruned", code)
		}
	}
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM share WHERE share_code='alive'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("alive share = %d, want 1", n)
	}
}

func TestOpenTrimmedDBKeepsCrawledAtByDefault(t *testing.T) {
	ctx := context.Background()
	src, cleanup := openTestStore(t)
	defer cleanup()
	if err := src.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "p", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.db.ExecContext(ctx, `INSERT INTO file(file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, crawled_at) VALUES('f1','sw1','0','a.mkv','mkv',1,0,1,'',1)`); err != nil {
		t.Fatal(err)
	}
	trimmed := filepath.Join(t.TempDir(), "trimmed.db")
	if err := src.ExportTrimmed(ctx, trimmed); err != nil {
		t.Fatalf("export: %v", err)
	}
	// Sanity: the default exported DB keeps crawled_at for rebuild-index.
	d, err := sql.Open("sqlite", trimmed)
	if err != nil {
		t.Fatal(err)
	}
	var has int
	_ = d.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('file') WHERE name='crawled_at'").Scan(&has)
	d.Close()
	if has != 1 {
		t.Fatalf("trimmed file table missing crawled_at")
	}

	// The real assertion: opening the exported DB must not fail on file.crawled_at.
	s2, err := Open(ctx, trimmed)
	if err != nil {
		t.Fatalf("Open trimmed DB failed: %v", err)
	}
	defer s2.Close()
	if err := s2.db.QueryRowContext(ctx, `SELECT crawled_at FROM file WHERE share_code='sw1' LIMIT 1`).Scan(new(int64)); err != nil {
		t.Fatalf("read crawled_at after open: %v", err)
	}
}
