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

// DedupeAction records one share marked as a duplicate by DedupeSharesBySize.
type DedupeAction struct {
	Loser     string
	Canonical string
	FileCount int64
}

// DedupeSharesBySize scans crawlable shares whose file_size >= minSize, groups by
// file_size, and for each group with >1 share marks all but the oldest as
// DUPLICATE of the oldest and deletes their files. With apply=false it returns
// the planned actions without mutating. Canonical order matches
// FindDuplicateShare: oldest by (last_crawled_at, id).
func (s *Store) DedupeSharesBySize(ctx context.Context, minSize int64, apply bool) ([]DedupeAction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT share_code, file_size, COALESCE(last_crawled_at,0), id
		FROM share
		WHERE file_size >= ? AND file_size > 0 AND status IN ('ACTIVE','STALE','QUARANTINE')
		ORDER BY file_size, COALESCE(last_crawled_at,0) ASC, id ASC`, minSize)
	if err != nil {
		return nil, fmt.Errorf("dedupe scan: %w", err)
	}
	defer rows.Close()

	groups := map[int64][]string{}
	for rows.Next() {
		var code string
		var size, crawled, id int64
		if err := rows.Scan(&code, &size, &crawled, &id); err != nil {
			return nil, err
		}
		groups[size] = append(groups[size], code)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var actions []DedupeAction
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		canonical := members[0] // oldest within the size group (ORDER BY last_crawled_at, id)
		for _, loser := range members[1:] {
			var fc int64
			_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file WHERE share_code = ?`, loser).Scan(&fc)
			actions = append(actions, DedupeAction{Loser: loser, Canonical: canonical, FileCount: fc})
		}
	}
	if !apply {
		return actions, nil
	}
	for _, a := range actions {
		if err := s.MarkShareDuplicate(ctx, a.Loser, a.Canonical); err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM file WHERE share_code = ?`, a.Loser); err != nil {
			return nil, err
		}
	}
	return actions, nil
}
