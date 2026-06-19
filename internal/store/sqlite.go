package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"five/internal/model"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.execPragmas(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// ExportSnapshot writes a fully-checkpointed, self-contained single-file copy of
// the database to destPath using VACUUM INTO. The result needs no -wal/-shm
// sidecar, so it can be archived on its own and shipped to a consumer without
// risking a near-empty database. destPath must not already exist; an existing
// file is removed first.
func (s *Store) ExportSnapshot(ctx context.Context, destPath string) error {
	if dir := filepath.Dir(destPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	// VACUUM INTO takes a string expression; bind it as a quoted literal so the
	// path is escaped safely (no driver-specific parameter quirks).
	quoted := "'" + strings.ReplaceAll(destPath, "'", "''") + "'"
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("vacuum into %s: %w", destPath, err)
	}
	return nil
}

func (s *Store) execPragmas(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA temp_store=MEMORY;",
		"PRAGMA busy_timeout=5000;",
	}
	for _, stmt := range pragmas {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS share (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			share_code TEXT NOT NULL,
			receive_code TEXT NOT NULL,
			share_title TEXT NOT NULL DEFAULT '',
			file_size INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'ACTIVE',
			last_crawled_at INTEGER,
			last_error TEXT,
			failure_count INTEGER NOT NULL DEFAULT 0,
			retry_after_unix INTEGER NOT NULL DEFAULT 0,
			version INTEGER NOT NULL DEFAULT 0,
			UNIQUE(share_code, receive_code)
		);`,
		`CREATE TABLE IF NOT EXISTS file (
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
		);`,
		`CREATE INDEX IF NOT EXISTS idx_file_share_parent ON file(share_code, parent_id);`,
		`CREATE INDEX IF NOT EXISTS idx_file_share_path ON file(share_code, path);`,
		`CREATE INDEX IF NOT EXISTS idx_file_ext ON file(ext);`,
		`CREATE INDEX IF NOT EXISTS idx_file_depth ON file(depth);`,
		`CREATE INDEX IF NOT EXISTS idx_file_size ON file(size);`,
		`CREATE TABLE IF NOT EXISTS crawl_checkpoint (
			share_code TEXT PRIMARY KEY,
			cid TEXT NOT NULL,
			next_offset INTEGER NOT NULL DEFAULT 0,
			active_path TEXT NOT NULL DEFAULT '',
			active_depth INTEGER NOT NULL DEFAULT 0,
			queue_json TEXT NOT NULL,
			visited_json TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS index_event (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file_id TEXT NOT NULL,
			op TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			processed_at INTEGER
		);`,
		`CREATE TABLE IF NOT EXISTS index_manifest (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			version INTEGER NOT NULL,
			index_path TEXT NOT NULL,
			status TEXT NOT NULL,
			built_at INTEGER NOT NULL,
			file_count INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Add columns introduced after the first release to already-deployed databases.
	if err := s.ensureColumns(ctx, "share", []columnDef{
		{name: "share_title", ddl: "TEXT NOT NULL DEFAULT ''"},
		{name: "file_size", ddl: "INTEGER NOT NULL DEFAULT 0"},
	}); err != nil {
		return fmt.Errorf("migrate share columns: %w", err)
	}
	return nil
}

type columnDef struct {
	name string
	ddl  string
}

// ensureColumns adds any missing columns to an existing table. Table and column
// names are compile-time constants, so the formatted DDL is safe.
func (s *Store) ensureColumns(ctx context.Context, table string, cols []columnDef) error {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notNull int
			dflt    any
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, col := range cols {
		if existing[col.name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col.name, col.ddl)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertFiles(ctx context.Context, files []model.File) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	upsertStmt := `INSERT INTO file (
		file_id, share_code, parent_id, name, path, ext, size, is_dir, depth, sha1, updated_at, crawled_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(file_id) DO UPDATE SET
		share_code=excluded.share_code,
		parent_id=excluded.parent_id,
		name=excluded.name,
		path=excluded.path,
		ext=excluded.ext,
		size=excluded.size,
		is_dir=excluded.is_dir,
		depth=excluded.depth,
		sha1=excluded.sha1,
		updated_at=excluded.updated_at,
		crawled_at=excluded.crawled_at;`
	eventStmt := `INSERT INTO index_event(file_id, op, created_at) VALUES (?, 'upsert', ?);`

	for _, f := range files {
		isDir := 0
		if f.IsDir {
			isDir = 1
		}
		if _, err := tx.ExecContext(ctx, upsertStmt,
			f.FileID, f.ShareCode, f.ParentID, f.Name, f.Path, f.Ext, f.Size, isDir, f.Depth, f.SHA1, f.UpdatedAt, f.CrawledAt,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, eventStmt, f.FileID, f.CrawledAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AllFiles(ctx context.Context) ([]model.File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_id, share_code, parent_id, name, path, ext, size, is_dir, depth, sha1, COALESCE(updated_at, 0), crawled_at FROM file ORDER BY path ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.File
	for rows.Next() {
		var f model.File
		var isDir int
		if err := rows.Scan(&f.FileID, &f.ShareCode, &f.ParentID, &f.Name, &f.Path, &f.Ext, &f.Size, &isDir, &f.Depth, &f.SHA1, &f.UpdatedAt, &f.CrawledAt); err != nil {
			return nil, err
		}
		f.IsDir = isDir == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) FileByID(ctx context.Context, fileID string) (model.File, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT file_id, share_code, parent_id, name, path, ext, size, is_dir, depth, sha1, COALESCE(updated_at, 0), crawled_at
		FROM file WHERE file_id = ?`, fileID)
	var f model.File
	var isDir int
	err := row.Scan(&f.FileID, &f.ShareCode, &f.ParentID, &f.Name, &f.Path, &f.Ext, &f.Size, &isDir, &f.Depth, &f.SHA1, &f.UpdatedAt, &f.CrawledAt)
	if err == sql.ErrNoRows {
		return model.File{}, false, nil
	}
	if err != nil {
		return model.File{}, false, err
	}
	f.IsDir = isDir == 1
	return f, true, nil
}

func (s *Store) SaveCheckpoint(ctx context.Context, cp model.Checkpoint) error {
	queueJSON, err := json.Marshal(cp.Queue)
	if err != nil {
		return err
	}
	visitedJSON, err := json.Marshal(cp.Visited)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO crawl_checkpoint(share_code, cid, next_offset, active_path, active_depth, queue_json, visited_json, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(share_code) DO UPDATE SET
		cid=excluded.cid,
		next_offset=excluded.next_offset,
		active_path=excluded.active_path,
		active_depth=excluded.active_depth,
		queue_json=excluded.queue_json,
		visited_json=excluded.visited_json,
		updated_at=excluded.updated_at;`,
		cp.ShareCode, cp.CID, cp.NextOffset, cp.ActivePath, cp.ActiveDepth, string(queueJSON), string(visitedJSON), cp.UpdatedAt)
	return err
}

func (s *Store) LoadCheckpoint(ctx context.Context, shareCode string) (model.Checkpoint, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT share_code, cid, next_offset, active_path, active_depth, queue_json, visited_json, updated_at FROM crawl_checkpoint WHERE share_code = ?`, shareCode)
	var cp model.Checkpoint
	var queueJSON string
	var visitedJSON string
	err := row.Scan(&cp.ShareCode, &cp.CID, &cp.NextOffset, &cp.ActivePath, &cp.ActiveDepth, &queueJSON, &visitedJSON, &cp.UpdatedAt)
	if err == sql.ErrNoRows {
		return model.Checkpoint{}, false, nil
	}
	if err != nil {
		return model.Checkpoint{}, false, err
	}
	if err := json.Unmarshal([]byte(queueJSON), &cp.Queue); err != nil {
		return model.Checkpoint{}, false, err
	}
	if err := json.Unmarshal([]byte(visitedJSON), &cp.Visited); err != nil {
		return model.Checkpoint{}, false, err
	}
	return cp, true, nil
}

func (s *Store) PendingIndexEvents(ctx context.Context, limit int) ([]model.IndexEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, file_id, op FROM index_event WHERE processed_at IS NULL ORDER BY id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []model.IndexEvent
	for rows.Next() {
		var e model.IndexEvent
		if err := rows.Scan(&e.ID, &e.FileID, &e.Op); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) MarkIndexEventsProcessed(ctx context.Context, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE index_event SET processed_at = unixepoch() WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpdateManifest(ctx context.Context, m model.IndexManifest) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO index_manifest(id, version, index_path, status, built_at, file_count)
	VALUES (1, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		version=excluded.version,
		index_path=excluded.index_path,
		status=excluded.status,
		built_at=excluded.built_at,
		file_count=excluded.file_count;`,
		m.Version, m.IndexPath, m.Status, m.BuiltAt, m.FileCount)
	return err
}

func (s *Store) LoadManifest(ctx context.Context) (model.IndexManifest, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT version, index_path, status, built_at, file_count FROM index_manifest WHERE id = 1`)
	var m model.IndexManifest
	err := row.Scan(&m.Version, &m.IndexPath, &m.Status, &m.BuiltAt, &m.FileCount)
	if err == sql.ErrNoRows {
		return model.IndexManifest{}, false, nil
	}
	if err != nil {
		return model.IndexManifest{}, false, err
	}
	return m, true, nil
}

func (s *Store) LoadKV(ctx context.Context, key string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key)
	var value string
	err := row.Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (s *Store) SaveKV(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) UpsertShare(ctx context.Context, share model.Share) error {
	if share.Status == "" {
		share.Status = "ACTIVE"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO share(
		share_code, receive_code, status, last_crawled_at, last_error, failure_count, retry_after_unix, version
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(share_code, receive_code) DO UPDATE SET
		status=excluded.status,
		last_crawled_at=excluded.last_crawled_at,
		last_error=excluded.last_error,
		failure_count=excluded.failure_count,
		retry_after_unix=excluded.retry_after_unix,
		version=excluded.version;`,
		share.ShareCode, share.ReceiveCode, share.Status,
		nullableInt64(share.LastCrawledAt), share.LastError, share.FailureCount, share.RetryAfterUnix, share.Version)
	return err
}

// UpdateShareMeta records share metadata fetched from 115 (the share title and
// total size). It registers the share if it is not present yet, and never touches
// the crawl status/bookkeeping columns, so it is safe to re-run and is not undone
// by a later import-shares.
func (s *Store) UpdateShareMeta(ctx context.Context, shareCode, receiveCode, title string, fileSize int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO share(share_code, receive_code, share_title, file_size, status)
	VALUES (?, ?, ?, ?, 'ACTIVE')
	ON CONFLICT(share_code, receive_code) DO UPDATE SET
		share_title=excluded.share_title,
		file_size=excluded.file_size;`,
		shareCode, receiveCode, title, fileSize)
	return err
}

func (s *Store) GetShare(ctx context.Context, shareCode string) (model.Share, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT share_code, receive_code, share_title, file_size, status,
		COALESCE(last_crawled_at, 0), COALESCE(last_error, ''), failure_count, retry_after_unix, version
		FROM share WHERE share_code = ? ORDER BY id DESC LIMIT 1`, shareCode)
	var share model.Share
	err := row.Scan(&share.ShareCode, &share.ReceiveCode, &share.ShareTitle, &share.FileSize, &share.Status,
		&share.LastCrawledAt, &share.LastError, &share.FailureCount, &share.RetryAfterUnix, &share.Version)
	if err == sql.ErrNoRows {
		return model.Share{}, false, nil
	}
	if err != nil {
		return model.Share{}, false, err
	}
	return share, true, nil
}

func (s *Store) ListShares(ctx context.Context) ([]model.Share, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT share_code, receive_code, share_title, file_size, status,
		COALESCE(last_crawled_at, 0), COALESCE(last_error, ''), failure_count, retry_after_unix, version
		FROM share
		ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shares []model.Share
	for rows.Next() {
		var share model.Share
		if err := rows.Scan(&share.ShareCode, &share.ReceiveCode, &share.ShareTitle, &share.FileSize, &share.Status,
			&share.LastCrawledAt, &share.LastError, &share.FailureCount, &share.RetryAfterUnix, &share.Version); err != nil {
			return nil, err
		}
		shares = append(shares, share)
	}
	return shares, rows.Err()
}

func (s *Store) ListSharesForCrawl(ctx context.Context, now int64) ([]model.Share, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT share_code, receive_code, share_title, file_size, status,
		COALESCE(last_crawled_at, 0), COALESCE(last_error, ''), failure_count, retry_after_unix, version
		FROM share
		WHERE status IN ('ACTIVE', 'STALE', 'QUARANTINE')
		  AND retry_after_unix <= ?
		ORDER BY COALESCE(last_crawled_at, 0) ASC, id ASC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shares []model.Share
	for rows.Next() {
		var share model.Share
		if err := rows.Scan(&share.ShareCode, &share.ReceiveCode, &share.ShareTitle, &share.FileSize, &share.Status,
			&share.LastCrawledAt, &share.LastError, &share.FailureCount, &share.RetryAfterUnix, &share.Version); err != nil {
			return nil, err
		}
		shares = append(shares, share)
	}
	return shares, rows.Err()
}

func (s *Store) MarkShareCrawled(ctx context.Context, shareCode string, crawledAt int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share
		SET status='ACTIVE',
			last_crawled_at=?,
			last_error='',
			failure_count=0,
			retry_after_unix=0,
			version=version+1
		WHERE share_code = ?`, crawledAt, shareCode)
	return err
}

func (s *Store) RecordShareFailure(ctx context.Context, shareCode, errText string) error {
	share, ok, err := s.GetShare(ctx, shareCode)
	if err != nil {
		return err
	}
	if !ok {
		return sql.ErrNoRows
	}
	share.FailureCount++
	share.LastError = errText
	share.RetryAfterUnix = timeNowUnix() + backoffSeconds(share.FailureCount)
	if share.FailureCount >= 3 {
		share.Status = "QUARANTINE"
	} else {
		share.Status = "STALE"
	}
	_, err = s.db.ExecContext(ctx, `UPDATE share
		SET status=?,
			last_error=?,
			failure_count=?,
			retry_after_unix=?
		WHERE share_code=?`,
		share.Status, share.LastError, share.FailureCount, share.RetryAfterUnix, shareCode)
	return err
}

func (s *Store) MarkShareDead(ctx context.Context, shareCode, errText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share
		SET status='DEAD',
			last_error=?,
			retry_after_unix=0
		WHERE share_code = ?`, errText, shareCode)
	return err
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func backoffSeconds(failureCount int) int64 {
	seconds := int64(60)
	for i := 1; i < failureCount; i++ {
		seconds *= 2
	}
	if seconds > 3600 {
		return 3600
	}
	return seconds
}

var timeNowUnix = func() int64 {
	return unixNow()
}
