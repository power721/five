package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
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

	pending, err := s.PendingIndexEvents(ctx, 10)
	if err != nil {
		t.Fatalf("pending events: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending events = %d, want 2", len(pending))
	}
	if err := s.MarkIndexEventsProcessed(ctx, []int64{pending[0].ID, pending[1].ID}); err != nil {
		t.Fatalf("mark processed: %v", err)
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

func TestSQLiteStoreDoesNotQueueIndexEventsForUnchangedFiles(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().Unix()
	files := []model.File{
		{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Ext: "mkv", Size: 1, CrawledAt: now},
		{FileID: "f2", ShareCode: "sw1", ParentID: "0", Name: "b.mkv", Ext: "mkv", Size: 2, CrawledAt: now},
	}
	if err := s.UpsertFiles(ctx, files); err != nil {
		t.Fatalf("initial upsert files: %v", err)
	}
	if err := s.UpsertFiles(ctx, files); err != nil {
		t.Fatalf("unchanged upsert files: %v", err)
	}

	pending, err := s.PendingIndexEvents(ctx, 10)
	if err != nil {
		t.Fatalf("pending events: %v", err)
	}
	if len(pending) != len(files) {
		t.Fatalf("pending events = %d, want %d", len(pending), len(files))
	}
}

func TestSQLiteStoreDoesNotQueueDuplicatePendingUpsertEvents(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().Unix()
	file := model.File{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", Ext: "mkv", CrawledAt: now}
	if err := s.UpsertFiles(ctx, []model.File{file}); err != nil {
		t.Fatalf("initial upsert file: %v", err)
	}
	file.Name = "renamed.mkv"
	if err := s.UpsertFiles(ctx, []model.File{file}); err != nil {
		t.Fatalf("changed upsert file: %v", err)
	}

	pending, err := s.PendingIndexEvents(ctx, 10)
	if err != nil {
		t.Fatalf("pending events: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending events = %d, want 1", len(pending))
	}
}

func TestSQLiteStoreCoalescesExistingPendingUpsertEventsOnOpen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO index_event(file_id, op, created_at) VALUES
		('f1', 'upsert', 1),
		('f1', 'upsert', 2),
		('f2', 'upsert', 3),
		('f2', 'upsert', 4),
		('f3', 'upsert', 5)`)
	if err != nil {
		t.Fatalf("insert duplicate pending events: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	pending, err := reopened.PendingIndexEvents(ctx, 10)
	if err != nil {
		t.Fatalf("pending events: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("pending events = %d, want 3", len(pending))
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
	pending, err := s.PendingIndexEvents(ctx, 10)
	if err != nil {
		t.Fatalf("pending events: %v", err)
	}
	if err := s.MarkIndexEventsProcessed(ctx, []int64{pending[0].ID}); err != nil {
		t.Fatalf("mark first event processed: %v", err)
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
	pendingCount, err := s.CountPendingIndexEvents(ctx)
	if err != nil {
		t.Fatalf("count pending index events: %v", err)
	}
	if pendingCount != 2 {
		t.Fatalf("pending index events = %d, want 2", pendingCount)
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

func TestExportTrimmedKeepsOnlyFileAndShare(t *testing.T) {
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
	if _, err := s.db.ExecContext(ctx, `INSERT INTO index_event(file_id,op,created_at) VALUES('f1','upsert',1)`); err != nil {
		t.Fatalf("insert event: %v", err)
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
	if got := strings.Join(tables, ","); got != "file,share" {
		t.Fatalf("tables = %q, want file,share", got)
	}

	var files, shares int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM file").Scan(&files)
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM share").Scan(&shares)
	if files != 1 || shares != 1 {
		t.Fatalf("counts files=%d shares=%d, want 1/1", files, shares)
	}

	var idx int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='file' AND name LIKE 'idx_file_%'").Scan(&idx)
	if idx != 4 {
		t.Fatalf("file indexes = %d, want 4", idx)
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
