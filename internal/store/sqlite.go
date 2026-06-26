package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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
	// Serialize all access through a single connection. SQLite allows only one
	// writer; with multiple pooled connections the crawler and indexer compete
	// for the write lock and lose with "database is locked (SQLITE_BUSY)", which
	// used to crash the daemon. One connection makes database/sql queue access
	// instead, eliminating cross-connection BUSY. Every Query fully drains+closes
	// its rows and no transaction issues a second db call, so this can't deadlock.
	db.SetMaxOpenConns(1)
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

// ExportTrimmed writes a single-file copy of the database containing only the
// file and share tables (with their indexes), for shipping to consumers.
// Crawler/indexer internals (checkpoints, events, manifest, kv) are excluded.
// Shares marked DEAD (e.g. cancelled) and the files that belonged to them are
// also dropped: a dead share's files are unreachable, so they must not ship.
type ExportTrimmedOptions struct {
	StripFileCrawledAt bool
}

// destPath is overwritten if it exists; the result has no -wal/-shm sidecar.
func (s *Store) ExportTrimmed(ctx context.Context, destPath string, options ...ExportTrimmedOptions) error {
	var opts ExportTrimmedOptions
	if len(options) > 0 {
		opts = options[0]
	}
	if dir := filepath.Dir(destPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	quoted := "'" + strings.ReplaceAll(destPath, "'", "''") + "'"
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("vacuum into %s: %w", destPath, err)
	}
	db, err := sql.Open("sqlite", destPath)
	if err != nil {
		return fmt.Errorf("open trimmed: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// DELETE journal so the subsequent VACUUM produces a sidecar-free file.
	for _, pragma := range []string{"PRAGMA journal_mode=DELETE;", "PRAGMA synchronous=NORMAL;"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS crawl_checkpoint;`,
		`DROP TABLE IF EXISTS index_event;`,
		`DROP TABLE IF EXISTS index_manifest;`,
		`DROP TABLE IF EXISTS kv;`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("trim %q: %w", stmt, err)
		}
	}
	// Prune dead and duplicate shares and the files that belonged to them before
	// vacuuming. Order matters: files reference share_code, so delete them first.
	for _, stmt := range []string{
		`DELETE FROM file WHERE share_code IN (SELECT share_code FROM share WHERE status IN ('DEAD','DUPLICATE'));`,
		`DELETE FROM share WHERE status IN ('DEAD','DUPLICATE');`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("prune dead/duplicate shares %q: %w", stmt, err)
		}
	}
	if opts.StripFileCrawledAt {
		// SQLite can't DROP COLUMN without leaving a duplicate index on this build,
		// so rebuild the table and recreate its indexes, like migrateFileCompositePK does.
		for _, stmt := range []string{
			`CREATE TABLE file_new (
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
				PRIMARY KEY (share_code, file_id)
			);`,
			`INSERT INTO file_new (file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, updated_at)
				SELECT file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, updated_at FROM file;`,
			`DROP TABLE file;`,
			`ALTER TABLE file_new RENAME TO file;`,
			`CREATE INDEX IF NOT EXISTS idx_file_share_parent ON file(share_code, parent_id);`,
			`CREATE INDEX IF NOT EXISTS idx_file_size ON file(size);`,
			`CREATE INDEX IF NOT EXISTS idx_file_id ON file(file_id);`,
		} {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("strip file columns %q: %w", stmt, err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, "VACUUM;"); err != nil {
		return fmt.Errorf("vacuum: %w", err)
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
		`CREATE TABLE IF NOT EXISTS share_group (
			group_id   INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			sort_order INTEGER NOT NULL
		);`,
		// PRIMARY KEY is (share_code, file_id), NOT file_id alone: the 115 cid is
		// unique within a share but NOT globally — the same folder shared via
		// multiple share links reuses one root cid. Keying on file_id alone let a
		// later crawl of a duplicate share steal the earlier share's root row.
		`CREATE TABLE IF NOT EXISTS file (
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
		);`,
		`CREATE INDEX IF NOT EXISTS idx_file_share_parent ON file(share_code, parent_id);`,
		`CREATE INDEX IF NOT EXISTS idx_file_size ON file(size);`,
		`CREATE TABLE IF NOT EXISTS crawl_checkpoint (
			share_code TEXT PRIMARY KEY,
			cid TEXT NOT NULL,
			next_offset INTEGER NOT NULL DEFAULT 0,
			active_depth INTEGER NOT NULL DEFAULT 0,
			queue_json TEXT NOT NULL,
			visited_json TEXT NOT NULL,
			updated_at INTEGER NOT NULL
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
		`CREATE INDEX IF NOT EXISTS idx_file_id ON file(file_id);`,
		`DROP INDEX IF EXISTS idx_file_ext;`,
		`DROP INDEX IF EXISTS idx_file_depth;`,
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
		{name: "group_id", ddl: "INTEGER"},
		{name: "duplicate_of", ddl: "TEXT NOT NULL DEFAULT ''"},
	}); err != nil {
		return fmt.Errorf("migrate share columns: %w", err)
	}
	if err := s.dropLegacyColumns(ctx); err != nil {
		return fmt.Errorf("drop legacy columns: %w", err)
	}
	if err := s.migrateFileCompositePK(ctx); err != nil {
		return fmt.Errorf("migrate file composite pk: %w", err)
	}
	if err := s.repairStolenRoots(ctx); err != nil {
		return fmt.Errorf("repair stolen roots: %w", err)
	}
	return nil
}

// filePKColumnCount reports how many columns the file table's primary key spans.
// Pre-fix databases keyed on file_id alone (1); the current schema keys on
// (share_code, file_id) (2). Used to decide whether the table needs rebuilding.
func (s *Store) filePKColumnCount(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(file)`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
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
			return 0, err
		}
		if pk > 0 {
			count++
		}
	}
	return count, rows.Err()
}

// migrateFileCompositePK rebuilds an existing file table that still keys on
// file_id alone (pre-fix databases) to the composite (share_code, file_id) key.
// Fresh databases already get the composite PK from the CREATE TABLE DDL, so
// this is a no-op for them. SQLite cannot ALTER a primary key in place, so the
// table is copied, dropped and renamed inside one transaction; indexes are
// recreated afterwards.
func (s *Store) migrateFileCompositePK(ctx context.Context) error {
	count, err := s.filePKColumnCount(ctx)
	if err != nil {
		return err
	}
	if count >= 2 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`CREATE TABLE file_new (
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
		);`,
		`INSERT INTO file_new (file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, updated_at, crawled_at)
			SELECT file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, updated_at, crawled_at FROM file;`,
		`DROP TABLE file;`,
		`ALTER TABLE file_new RENAME TO file;`,
		`CREATE INDEX IF NOT EXISTS idx_file_share_parent ON file(share_code, parent_id);`,
		`CREATE INDEX IF NOT EXISTS idx_file_size ON file(size);`,
		`CREATE INDEX IF NOT EXISTS idx_file_id ON file(file_id);`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rebuild file pk: %w", err)
		}
	}
	return tx.Commit()
}

// repairStolenRoots clones the missing root row back under every "victim" share
// in databases built before the composite-PK fix. A victim is a share that has
// file rows but no parent_id='0' row, because its root cid was stolen by another
// share under the old file_id-only key. The root's metadata is identical for
// every share linking the same folder, so cloning the holder's root row is
// correct. Idempotent (guarded by WHERE NOT EXISTS) and gated by kv so it runs
// once; on healthy/fresh databases the INSERT affects zero rows but still arms
// the gate. A victim whose stolen root no longer exists under any share (e.g.
// the holder was pruned) is left untouched here and needs a re-crawl.
func (s *Store) repairStolenRoots(ctx context.Context) error {
	if done, ok, err := s.LoadKV(ctx, "file_stolen_roots_repaired"); err != nil {
		return err
	} else if ok && done == "1" {
		return nil
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO file (file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, updated_at, crawled_at)
SELECT src.file_id, victim.share_code, src.parent_id, src.name, src.ext, src.size, src.is_dir, src.depth, src.sha1, src.updated_at, src.crawled_at
FROM (SELECT DISTINCT share_code, parent_id AS cid FROM file WHERE parent_id <> '0') victim
JOIN file src ON src.file_id = victim.cid AND src.parent_id = '0'
WHERE NOT EXISTS (SELECT 1 FROM file own WHERE own.share_code = victim.share_code AND own.parent_id = '0')
  AND NOT EXISTS (SELECT 1 FROM file already WHERE already.share_code = victim.share_code AND already.file_id = victim.cid);`)
	if err != nil {
		return fmt.Errorf("clone stolen roots: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("event=file_stolen_roots_repaired count=%d", n)
	}
	return s.SaveKV(ctx, "file_stolen_roots_repaired", "1")
}

// dropLegacyColumns removes columns that were dropped from the schema. SQLite
// requires a column's index to be dropped first, so each entry may name an
// index to remove beforehand. Only runs on databases that still carry the
// legacy column; fresh databases built from the current schema never have it.
// path was only ever the leaf "/name" segment (the real tree is parent_id), and
// active_path tracked crawler BFS state that is no longer stored.
func (s *Store) dropLegacyColumns(ctx context.Context) error {
	drops := []struct {
		table string
		col   string
		index string
	}{
		{"file", "path", "idx_file_share_path"},
		{"crawl_checkpoint", "active_path", ""},
	}
	for _, d := range drops {
		exists, err := s.columnExists(ctx, d.table, d.col)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if d.index != "" {
			if _, err := s.db.ExecContext(ctx, fmt.Sprintf("DROP INDEX IF EXISTS %s;", d.index)); err != nil {
				return fmt.Errorf("drop index %s: %w", d.index, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", d.table, d.col)); err != nil {
			return fmt.Errorf("drop column %s.%s: %w", d.table, d.col, err)
		}
	}
	return nil
}

func (s *Store) columnExists(ctx context.Context, table, col string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
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
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
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
		file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, updated_at, crawled_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(share_code, file_id) DO UPDATE SET
		parent_id=excluded.parent_id,
		name=excluded.name,
		ext=excluded.ext,
		size=excluded.size,
		is_dir=excluded.is_dir,
		depth=excluded.depth,
		sha1=excluded.sha1,
		updated_at=excluded.updated_at,
		crawled_at=excluded.crawled_at;`

	for _, f := range files {
		isDir := 0
		if f.IsDir {
			isDir = 1
		}
		if _, err := tx.ExecContext(ctx, upsertStmt,
			f.FileID, f.ShareCode, f.ParentID, f.Name, f.Ext, f.Size, isDir, f.Depth, f.SHA1, f.UpdatedAt, f.CrawledAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AllFiles(ctx context.Context) ([]model.File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, COALESCE(updated_at, 0), crawled_at FROM file ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.File
	for rows.Next() {
		var f model.File
		var isDir int
		if err := rows.Scan(&f.FileID, &f.ShareCode, &f.ParentID, &f.Name, &f.Ext, &f.Size, &isDir, &f.Depth, &f.SHA1, &f.UpdatedAt, &f.CrawledAt); err != nil {
			return nil, err
		}
		f.IsDir = isDir == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) CountFiles(ctx context.Context) (int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file`)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) FileByID(ctx context.Context, fileID string) (model.File, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT file_id, share_code, parent_id, name, ext, size, is_dir, depth, sha1, COALESCE(updated_at, 0), crawled_at
		FROM file WHERE file_id = ?`, fileID)
	var f model.File
	var isDir int
	err := row.Scan(&f.FileID, &f.ShareCode, &f.ParentID, &f.Name, &f.Ext, &f.Size, &isDir, &f.Depth, &f.SHA1, &f.UpdatedAt, &f.CrawledAt)
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO crawl_checkpoint(share_code, cid, next_offset, active_depth, queue_json, visited_json, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(share_code) DO UPDATE SET
		cid=excluded.cid,
		next_offset=excluded.next_offset,
		active_depth=excluded.active_depth,
		queue_json=excluded.queue_json,
		visited_json=excluded.visited_json,
		updated_at=excluded.updated_at;`,
		cp.ShareCode, cp.CID, cp.NextOffset, cp.ActiveDepth, string(queueJSON), string(visitedJSON), cp.UpdatedAt)
	return err
}

func (s *Store) LoadCheckpoint(ctx context.Context, shareCode string) (model.Checkpoint, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT share_code, cid, next_offset, active_depth, queue_json, visited_json, updated_at FROM crawl_checkpoint WHERE share_code = ?`, shareCode)
	var cp model.Checkpoint
	var queueJSON string
	var visitedJSON string
	err := row.Scan(&cp.ShareCode, &cp.CID, &cp.NextOffset, &cp.ActiveDepth, &queueJSON, &visitedJSON, &cp.UpdatedAt)
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

func (s *Store) DeleteKV(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE key = ?`, key)
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

// ApplyGroups reconciles the grouping overlay in a single transaction: it
// replaces share_group with the given groups (slice order = group_id/sort_order,
// 1-based) and reassigns each share's group_id by share_code match. group_id is
// cleared for every share first, so shares absent from the overlay end up NULL.
// A code that matches no share row is returned in dormant for the caller to warn
// (it takes effect once that share is imported and ApplyGroups runs again).
func (s *Store) ApplyGroups(ctx context.Context, groups []model.ShareGroup) (dormant []string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM share_group;`); err != nil {
		return nil, fmt.Errorf("clear share_group: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE share SET group_id = NULL;`); err != nil {
		return nil, fmt.Errorf("clear share.group_id: %w", err)
	}

	for i, g := range groups {
		groupID := int64(i + 1)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO share_group(group_id, name, sort_order) VALUES (?, ?, ?);`,
			groupID, g.Name, groupID); err != nil {
			return nil, fmt.Errorf("insert share_group %q: %w", g.Name, err)
		}
		for _, code := range g.ShareCodes {
			res, err := tx.ExecContext(ctx, `UPDATE share SET group_id = ? WHERE share_code = ?;`, groupID, code)
			if err != nil {
				return nil, fmt.Errorf("set group_id for %s: %w", code, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				dormant = append(dormant, code)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dormant, nil
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
		COALESCE(last_crawled_at, 0), COALESCE(last_error, ''), failure_count, retry_after_unix, version, duplicate_of
		FROM share WHERE share_code = ? ORDER BY id DESC LIMIT 1`, shareCode)
	var share model.Share
	err := row.Scan(&share.ShareCode, &share.ReceiveCode, &share.ShareTitle, &share.FileSize, &share.Status,
		&share.LastCrawledAt, &share.LastError, &share.FailureCount, &share.RetryAfterUnix, &share.Version, &share.DuplicateOf)
	if err == sql.ErrNoRows {
		return model.Share{}, false, nil
	}
	if err != nil {
		return model.Share{}, false, err
	}
	return share, true, nil
}

func (s *Store) CountFilesByShare(ctx context.Context, shareCode string) (int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code = ?`, shareCode)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ShareFileStats returns the number of indexed files (folders excluded) for a
// share and the sum of their sizes. A share with no indexed files yields (0, 0).
func (s *Store) ShareFileStats(ctx context.Context, shareCode string) (int, int64, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM file WHERE share_code = ? AND is_dir = 0`,
		shareCode)
	var count int
	var total int64
	if err := row.Scan(&count, &total); err != nil {
		return 0, 0, err
	}
	return count, total, nil
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

func (s *Store) CountShares(ctx context.Context) (int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM share`)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
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

// MarkShareCrawled records that a share's BFS crawl fully drained — i.e. its
// 115 snapshot has been completely indexed — and parks it at the terminal
// COMPLETED status so the scheduler stops re-queueing it. 115 shares are
// immutable snapshots, so a completed crawl never needs repeating; use
// ReactivateShare to force a re-crawl. Clears failure bookkeeping and bumps
// version. Updates 0 rows (no error) if the share is not registered.
func (s *Store) MarkShareCrawled(ctx context.Context, shareCode string, crawledAt int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share
		SET status='COMPLETED',
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
