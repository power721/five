package adminhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"five/internal/model"
)

type fakeStore struct {
	shares         []model.Share
	files          []model.File
	events         []model.IndexEvent
	upsertedShares []model.Share
}

func (f *fakeStore) ListSharesForCrawl(context.Context, int64) ([]model.Share, error) {
	return f.shares, nil
}

func (f *fakeStore) UpsertShare(_ context.Context, share model.Share) error {
	f.upsertedShares = append(f.upsertedShares, share)
	return nil
}

func (f *fakeStore) AllFiles(context.Context) ([]model.File, error) {
	return f.files, nil
}

func (f *fakeStore) PendingIndexEvents(context.Context, int) ([]model.IndexEvent, error) {
	return f.events, nil
}

func TestStatusReturnsShareFileAndEventCounts(t *testing.T) {
	store := &fakeStore{
		shares: []model.Share{
			{ShareCode: "sw1", Status: "ACTIVE"},
			{ShareCode: "sw2", Status: "STALE"},
		},
		files: []model.File{
			{FileID: "f1"},
			{FileID: "f2"},
			{FileID: "f3"},
		},
		events: []model.IndexEvent{
			{ID: 1}, {ID: 2},
		},
	}
	srv := New(store)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d", rr.Code)
	}
	var body StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if body.ShareCount != 2 {
		t.Fatalf("share_count = %d, want 2", body.ShareCount)
	}
	if body.FileCount != 3 {
		t.Fatalf("file_count = %d, want 3", body.FileCount)
	}
	if body.PendingIndexEvents != 2 {
		t.Fatalf("pending_index_events = %d, want 2", body.PendingIndexEvents)
	}
}

func TestPostSharesAcceptsShareURL(t *testing.T) {
	store := &fakeStore{}
	srv := New(store)

	body := bytes.NewBufferString(`{"share_url":"https://115.com/s/swf01d43zby?password=echo"}`)
	req := httptest.NewRequest(http.MethodPost, "/shares", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want 201", rr.Code)
	}
	if len(store.upsertedShares) != 1 {
		t.Fatalf("upserted shares = %d, want 1", len(store.upsertedShares))
	}
	if store.upsertedShares[0].ShareCode != "swf01d43zby" {
		t.Fatalf("share_code = %q", store.upsertedShares[0].ShareCode)
	}
	if store.upsertedShares[0].ReceiveCode != "echo" {
		t.Fatalf("receive_code = %q", store.upsertedShares[0].ReceiveCode)
	}
}
