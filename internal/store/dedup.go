package store

import (
	"context"
	"database/sql"
	"fmt"
)

// FindDuplicateShare reports whether shareCode is a duplicate of an older,
// already-indexed share with the same file_size (>= minSize). The canonical is
// the oldest same-size share by (last_crawled_at, id); shareCode is a duplicate
// only if that oldest share is not itself. This keeps the direction stable: the
// first-indexed share in a size group is never flagged as a duplicate of a later
// one. A share's size is only known after its first crawl page.
func (s *Store) FindDuplicateShare(ctx context.Context, shareCode string, fileSize, minSize int64) (string, bool, error) {
	if fileSize <= 0 || fileSize < minSize {
		return "", false, nil
	}
	var oldest string
	err := s.db.QueryRowContext(ctx, `SELECT share_code FROM share
		WHERE file_size = ? AND file_size >= ? AND file_size > 0
		  AND status IN ('ACTIVE','STALE','QUARANTINE')
		ORDER BY COALESCE(last_crawled_at,0) ASC, id ASC
		LIMIT 1`, fileSize, minSize).Scan(&oldest)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find duplicate share: %w", err)
	}
	if oldest == shareCode {
		return "", false, nil // shareCode is itself the canonical
	}
	return oldest, true, nil
}
