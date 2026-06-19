package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"five/internal/api115"
	"five/internal/model"
)

type fakeCountFetcher struct {
	responses map[string]api115.SnapResponse
	errs      map[string]error
	calls     []api115.ListRequest
}

func (f *fakeCountFetcher) List(_ context.Context, req api115.ListRequest) (api115.SnapResponse, error) {
	f.calls = append(f.calls, req)
	if err := f.errs[req.ShareCode]; err != nil {
		return api115.SnapResponse{}, err
	}
	resp, ok := f.responses[req.ShareCode]
	if !ok {
		return api115.SnapResponse{}, nil
	}
	return resp, nil
}

type fakeCountStore struct {
	counts map[string]int
	errs   map[string]error
}

func (f fakeCountStore) CountFilesByShare(_ context.Context, shareCode string) (int, error) {
	if err := f.errs[shareCode]; err != nil {
		return 0, err
	}
	return f.counts[shareCode], nil
}

func TestValidateShareCountsReportsMismatchAndContinuesAfterFailure(t *testing.T) {
	fetcher := &fakeCountFetcher{
		responses: map[string]api115.SnapResponse{
			"match": {
				State: true,
				Data: api115.SnapData{
					Count: 2,
					ShareInfo: api115.SnapShareInfo{
						ShareState: 1,
						ShareTitle: "Match Share",
					},
				},
			},
			"mismatch": {
				State: true,
				Data: api115.SnapData{
					Count: 5,
					ShareInfo: api115.SnapShareInfo{
						ShareState: 1,
						ShareTitle: "Mismatch Share",
					},
				},
			},
		},
		errs: map[string]error{
			"broken": errors.New("upstream timeout"),
		},
	}
	store := fakeCountStore{
		counts: map[string]int{
			"match":    2,
			"mismatch": 3,
		},
	}
	shares := []model.Share{
		{ShareCode: "match", ReceiveCode: "aa", ShareTitle: "Match Share"},
		{ShareCode: "mismatch", ReceiveCode: "bb", ShareTitle: "Mismatch Share"},
		{ShareCode: "broken", ReceiveCode: "cc", ShareTitle: "Broken Share"},
	}

	var out bytes.Buffer
	summary, err := validateShareCounts(context.Background(), fetcher, store, shares, &out)
	if err != nil {
		t.Fatalf("validate share counts: %v", err)
	}
	if summary.Validated != 2 || summary.Mismatched != 1 || summary.Failed != 1 {
		t.Fatalf("summary = %#v", summary)
	}

	text := out.String()
	if strings.Contains(text, "share=match api_count=") {
		t.Fatalf("matched share should not be printed: %q", text)
	}
	if !strings.Contains(text, `share=mismatch api_count=5 db_count=3 title="Mismatch Share"`) {
		t.Fatalf("missing mismatch output: %q", text)
	}
	if !strings.Contains(text, `share=broken validate_failed error="upstream timeout"`) {
		t.Fatalf("missing failure output: %q", text)
	}
	if !strings.Contains(text, "validated=2 mismatched=1 failed=1") {
		t.Fatalf("missing summary output: %q", text)
	}
	if len(fetcher.calls) != 3 {
		t.Fatalf("fetch calls = %d, want 3", len(fetcher.calls))
	}
	for _, call := range fetcher.calls {
		if call.CID != "0" || call.Offset != 0 || call.Limit != 1 {
			t.Fatalf("unexpected request = %#v", call)
		}
	}
}

func TestValidateShareCountsReportsStoreFailure(t *testing.T) {
	fetcher := &fakeCountFetcher{
		responses: map[string]api115.SnapResponse{
			"sw1": {
				State: true,
				Data: api115.SnapData{
					Count: 1,
					ShareInfo: api115.SnapShareInfo{
						ShareState: 1,
						ShareTitle: "One",
					},
				},
			},
		},
	}
	store := fakeCountStore{
		errs: map[string]error{
			"sw1": errors.New("db busy"),
		},
	}

	var out bytes.Buffer
	summary, err := validateShareCounts(context.Background(), fetcher, store, []model.Share{{ShareCode: "sw1", ReceiveCode: "pw"}}, &out)
	if err != nil {
		t.Fatalf("validate share counts: %v", err)
	}
	if summary.Validated != 0 || summary.Mismatched != 0 || summary.Failed != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if !strings.Contains(out.String(), `share=sw1 validate_failed error="db busy"`) {
		t.Fatalf("output = %q", out.String())
	}
}
