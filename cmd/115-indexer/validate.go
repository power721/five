package main

import (
	"context"
	"fmt"
	"io"

	"five/internal/api115"
	"five/internal/model"
)

type shareFileCounter interface {
	CountFilesByShare(ctx context.Context, shareCode string) (int, error)
}

type validationSummary struct {
	Validated  int
	Mismatched int
	Failed     int
}

func validateShareCounts(ctx context.Context, fetcher snapFetcher, store shareFileCounter, shareList []model.Share, out io.Writer) (validationSummary, error) {
	var summary validationSummary
	for _, sh := range shareList {
		resp, err := fetcher.List(ctx, api115.ListRequest{
			ShareCode:   sh.ShareCode,
			ReceiveCode: sh.ReceiveCode,
			CID:         "0",
			Offset:      0,
			Limit:       1,
		})
		if err != nil {
			summary.Failed++
			fmt.Fprintf(out, "share=%s validate_failed error=%q\n", sh.ShareCode, err.Error())
			continue
		}
		if !resp.ValidShare() {
			summary.Failed++
			fmt.Fprintf(out, "share=%s validate_failed error=%q\n", sh.ShareCode, "dead or invalid share")
			continue
		}
		dbCount, err := store.CountFilesByShare(ctx, sh.ShareCode)
		if err != nil {
			summary.Failed++
			fmt.Fprintf(out, "share=%s validate_failed error=%q\n", sh.ShareCode, err.Error())
			continue
		}
		summary.Validated++
		if resp.Data.Count != dbCount {
			summary.Mismatched++
			title := sh.ShareTitle
			if resp.Data.ShareInfo.ShareTitle != "" {
				title = resp.Data.ShareInfo.ShareTitle
			}
			fmt.Fprintf(out, "share=%s api_count=%d db_count=%d title=%q\n", sh.ShareCode, resp.Data.Count, dbCount, title)
		}
	}
	fmt.Fprintf(out, "validated=%d mismatched=%d failed=%d\n", summary.Validated, summary.Mismatched, summary.Failed)
	return summary, nil
}
