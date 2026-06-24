package store

import (
	"context"
	"fmt"
)

// DeleteShare removes a share and all of its data: its crawled files, crawl
// checkpoint, and the share row itself, in a single transaction. Returns false
// if no share row matched shareCode. It does NOT guard against a nonzero file
// count — the caller (adminhttp) decides whether to allow deleting a share that
// still has files via its force flag. Order matters: files and checkpoint are
// deleted before the share row that owns them.
func (s *Store) DeleteShare(ctx context.Context, shareCode string) (bool, error) {
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
			return false, fmt.Errorf("delete share %q: %w", shareCode, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM share WHERE share_code = ?;`, shareCode)
	if err != nil {
		return false, fmt.Errorf("delete share %q: %w", shareCode, err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
