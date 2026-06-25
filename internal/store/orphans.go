package store

import (
	"context"
	"fmt"
)

// OrphanShare is a group of file rows whose share_code no longer has a share row.
type OrphanShare struct {
	ShareCode string
	FileCount int64
}

// OrphanShares lists orphan file groups (share_code + file count) for dry-run
// review before cleanup.
func (s *Store) OrphanShares(ctx context.Context) ([]OrphanShare, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT share_code, COUNT(*)
		FROM file
		WHERE share_code NOT IN (SELECT share_code FROM share)
		GROUP BY share_code`)
	if err != nil {
		return nil, fmt.Errorf("list orphans: %w", err)
	}
	defer rows.Close()
	var out []OrphanShare
	for rows.Next() {
		var o OrphanShare
		if err := rows.Scan(&o.ShareCode, &o.FileCount); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteOrphans removes file and crawl_checkpoint rows whose share_code is not in
// the share table. Returns the total number of rows deleted.
func (s *Store) DeleteOrphans(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM file WHERE share_code NOT IN (SELECT share_code FROM share);`)
	if err != nil {
		return 0, fmt.Errorf("delete orphan files: %w", err)
	}
	files, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	res, err = tx.ExecContext(ctx, `DELETE FROM crawl_checkpoint WHERE share_code NOT IN (SELECT share_code FROM share);`)
	if err != nil {
		return 0, fmt.Errorf("delete orphan checkpoints: %w", err)
	}
	cps, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return files + cps, nil
}
