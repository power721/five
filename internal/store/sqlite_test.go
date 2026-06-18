package store

import (
	"context"
	"path/filepath"
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
			Path:      "/Avatar.mkv",
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
			Path:      "/Season 01",
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
	if len(pending) != 4 {
		t.Fatalf("pending events = %d, want 4", len(pending))
	}
	if err := s.MarkIndexEventsProcessed(ctx, []int64{pending[0].ID, pending[1].ID}); err != nil {
		t.Fatalf("mark processed: %v", err)
	}

	cp := model.Checkpoint{
		ShareCode: "swf01d43zby",
		CID:       "0",
		Queue: []model.CrawlTask{
			{CID: "1", Path: "/Season 01", Depth: 1},
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
