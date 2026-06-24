package main

import (
	"context"
	"fmt"
	"io"

	"five/internal/model"
)

// dedupeStore is the subset of *store.Store used by dedupe-share-titles.
type dedupeStore interface {
	DedupeShareTitles(ctx context.Context, dryRun bool) ([]model.ShareRename, error)
}

// runDedupeShareTitles plans (and, when apply is set, commits) renames for
// duplicate share titles, printing each rename and a one-line summary.
func runDedupeShareTitles(ctx context.Context, store dedupeStore, apply bool, out io.Writer) error {
	renames, err := store.DedupeShareTitles(ctx, !apply)
	if err != nil {
		return err
	}
	for _, r := range renames {
		fmt.Fprintf(out, "share %s: %q -> %q\n", r.ShareCode, r.From, r.To)
	}
	switch {
	case apply:
		fmt.Fprintf(out, "renamed %d shares\n", len(renames))
	case len(renames) > 0:
		fmt.Fprintf(out, "would rename %d shares; re-run with -apply to commit\n", len(renames))
	default:
		fmt.Fprintln(out, "no duplicate titles found")
	}
	return nil
}
