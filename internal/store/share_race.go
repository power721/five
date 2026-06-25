package store

import (
	"context"
	"database/sql"
	"fmt"
)

// PurgeIfOrphan removes the file and crawl_checkpoint rows for shareCode IF no
// share row exists. It cleans up after a delete that raced with an in-flight
// crawl: the crawler finished and re-wrote rows after DeleteShare committed.
// Returns true if it purged (share was gone), false if the share still exists.
func (s *Store) PurgeIfOrphan(ctx context.Context, shareCode string) (bool, error) {
	var tmp int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM share WHERE share_code = ?`, shareCode).Scan(&tmp)
	if err == nil {
		return false, nil // share still exists
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("purge orphan check: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM file WHERE share_code = ?;`,
		`DELETE FROM crawl_checkpoint WHERE share_code = ?;`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, shareCode); err != nil {
			return false, fmt.Errorf("purge orphan %q: %w", shareCode, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
