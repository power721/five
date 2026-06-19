package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"five/internal/api115"
	"five/internal/model"
)

// snapFetcher is the subset of *api115.Client used to read share metadata.
type snapFetcher interface {
	List(ctx context.Context, req api115.ListRequest) (api115.SnapResponse, error)
}

// shareMetaWriter is the subset of *store.Store used to persist share metadata.
type shareMetaWriter interface {
	UpdateShareMeta(ctx context.Context, shareCode, receiveCode, title string, fileSize int64) error
}

// backfillShareMeta fetches the share root snapshot for each share and records
// its title and total size. Dead/invalid shares and fetch failures are logged
// and skipped so one bad share does not abort the run. Returns the number of
// shares whose metadata was persisted.
func backfillShareMeta(ctx context.Context, fetcher snapFetcher, store shareMetaWriter, shareList []model.Share, delay time.Duration, out io.Writer) (int, error) {
	updated := 0
	for i, sh := range shareList {
		if i > 0 && delay > 0 {
			select {
			case <-ctx.Done():
				return updated, ctx.Err()
			case <-time.After(delay):
			}
		}
		resp, err := fetcher.List(ctx, api115.ListRequest{
			ShareCode:   sh.ShareCode,
			ReceiveCode: sh.ReceiveCode,
			CID:         "0",
			Offset:      0,
			Limit:       1,
		})
		if err != nil {
			fmt.Fprintf(out, "share %s: fetch failed: %v\n", sh.ShareCode, err)
			continue
		}
		if !resp.ValidShare() {
			fmt.Fprintf(out, "share %s: dead or invalid (share_state=%d), skipped\n", sh.ShareCode, resp.Data.ShareInfo.ShareState)
			continue
		}
		info := resp.Data.ShareInfo
		if err := store.UpdateShareMeta(ctx, sh.ShareCode, sh.ReceiveCode, info.ShareTitle, info.FileSize); err != nil {
			return updated, fmt.Errorf("update share meta %s: %w", sh.ShareCode, err)
		}
		updated++
		fmt.Fprintf(out, "share %s: title=%q size=%d\n", sh.ShareCode, info.ShareTitle, info.FileSize)
	}
	return updated, nil
}
