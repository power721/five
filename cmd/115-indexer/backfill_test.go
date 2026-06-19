package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"five/internal/api115"
	"five/internal/model"
)

type fakeSnapFetcher struct {
	responses map[string]api115.SnapResponse
	errs      map[string]error
	calls     []api115.ListRequest
}

func (f *fakeSnapFetcher) List(ctx context.Context, req api115.ListRequest) (api115.SnapResponse, error) {
	f.calls = append(f.calls, req)
	if err := f.errs[req.ShareCode]; err != nil {
		return api115.SnapResponse{}, err
	}
	return f.responses[req.ShareCode], nil
}

type metaUpdate struct {
	receiveCode string
	title       string
	size        int64
}

type fakeMetaStore struct {
	updates map[string]metaUpdate
}

func (s *fakeMetaStore) UpdateShareMeta(ctx context.Context, shareCode, receiveCode, title string, fileSize int64) error {
	if s.updates == nil {
		s.updates = map[string]metaUpdate{}
	}
	s.updates[shareCode] = metaUpdate{receiveCode: receiveCode, title: title, size: fileSize}
	return nil
}

func TestBackfillShareMetaUpdatesValidSharesAndSkipsInvalid(t *testing.T) {
	good := api115.SnapResponse{State: true}
	good.Data.ShareInfo.ShareState = 1
	good.Data.ShareInfo.ShareTitle = "电影-欧美高清3.89T"
	good.Data.ShareInfo.FileSize = 4273516964003

	dead := api115.SnapResponse{State: true}
	dead.Data.ShareInfo.ShareState = 2 // forbidden / not state 1

	fetcher := &fakeSnapFetcher{
		responses: map[string]api115.SnapResponse{
			"swgood": good,
			"swdead": dead,
		},
		errs: map[string]error{
			"swerr": errors.New("boom"),
		},
	}
	store := &fakeMetaStore{}

	shares := []model.Share{
		{ShareCode: "swgood", ReceiveCode: "6666"},
		{ShareCode: "swdead", ReceiveCode: "rc"},
		{ShareCode: "swerr", ReceiveCode: "rc"},
	}

	var out bytes.Buffer
	updated, err := backfillShareMeta(context.Background(), fetcher, store, shares, 0, &out)
	if err != nil {
		t.Fatalf("backfillShareMeta error = %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}

	got, ok := store.updates["swgood"]
	if !ok || got.title != "电影-欧美高清3.89T" || got.size != 4273516964003 || got.receiveCode != "6666" {
		t.Fatalf("swgood update = %#v ok=%v", got, ok)
	}
	if _, ok := store.updates["swdead"]; ok {
		t.Fatal("dead share should not be updated")
	}
	if _, ok := store.updates["swerr"]; ok {
		t.Fatal("errored share should not be updated")
	}

	// The snap request must target the share root with a minimal page.
	if len(fetcher.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(fetcher.calls))
	}
	first := fetcher.calls[0]
	if first.ShareCode != "swgood" || first.ReceiveCode != "6666" || first.CID != "0" || first.Limit != 1 {
		t.Fatalf("unexpected first call: %#v", first)
	}
}
