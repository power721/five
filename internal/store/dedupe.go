package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"five/internal/model"
)

// planShareRenames assigns globally-unique titles to duplicate share-title
// groups. shares MUST be in id-ASC order (ListShares returns this order). The
// lowest-id share in each duplicate group keeps its bare title; the rest get
// <title><n> for n=1,2,3…, skipping any candidate already used by any share so
// the result never creates a new collision. Empty/whitespace titles are skipped.
func planShareRenames(shares []model.Share) []model.ShareRename {
	used := make(map[string]struct{})
	for _, sh := range shares {
		used[strings.TrimSpace(sh.ShareTitle)] = struct{}{}
	}

	type group struct {
		base    string
		members []model.Share
	}
	groups := map[string]*group{}
	var order []string
	for _, sh := range shares {
		t := strings.TrimSpace(sh.ShareTitle)
		if t == "" {
			continue
		}
		g, ok := groups[t]
		if !ok {
			g = &group{base: t}
			groups[t] = g
			order = append(order, t)
		}
		g.members = append(g.members, sh)
	}

	var renames []model.ShareRename
	for _, base := range order {
		g := groups[base]
		if len(g.members) <= 1 {
			continue
		}
		// members[0] (lowest id) keeps the bare title; rename the rest.
		for _, sh := range g.members[1:] {
			n := 1
			for {
				candidate := base + strconv.Itoa(n)
				if _, taken := used[candidate]; !taken {
					used[candidate] = struct{}{}
					renames = append(renames, model.ShareRename{
						ShareCode: sh.ShareCode,
						From:      sh.ShareTitle,
						To:        candidate,
					})
					break
				}
				n++
			}
		}
	}
	return renames
}

// RenameShareTitle sets share_title for the row with share_code, leaving
// file_size, status, and version untouched. Returns (false, nil) when no share
// matches shareCode (so callers can answer 404). Used by DedupeShareTitles and
// the rename API.
func (s *Store) RenameShareTitle(ctx context.Context, shareCode, newTitle string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE share SET share_title=? WHERE share_code=?`, newTitle, shareCode)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// FindShareTitleConflict reports whether a share other than excludeCode already
// holds newTitle. Both sides are whitespace-trimmed before comparing, matching
// the grouping used by DedupeShareTitles and the rename endpoint. Returns the
// colliding share_code and ok=true when a conflict exists.
func (s *Store) FindShareTitleConflict(ctx context.Context, newTitle, excludeCode string) (string, bool, error) {
	title := strings.TrimSpace(newTitle)
	var code string
	err := s.db.QueryRowContext(ctx,
		`SELECT share_code FROM share WHERE TRIM(share_title) = ? AND share_code <> ? LIMIT 1`,
		title, excludeCode).Scan(&code)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return code, true, nil
}

// DedupeShareTitles plans title renames over all shares and, unless dryRun,
// applies them. Returns the planned/applied renames. Idempotent: re-running
// re-plans on the current state, so a partial apply completes on the next run.
func (s *Store) DedupeShareTitles(ctx context.Context, dryRun bool) ([]model.ShareRename, error) {
	shares, err := s.ListShares(ctx)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	renames := planShareRenames(shares)
	if dryRun {
		return renames, nil
	}
	for _, r := range renames {
		if _, err := s.RenameShareTitle(ctx, r.ShareCode, r.To); err != nil {
			return nil, fmt.Errorf("rename share %s: %w", r.ShareCode, err)
		}
	}
	return renames, nil
}
