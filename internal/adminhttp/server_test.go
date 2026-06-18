package adminhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"five/internal/model"
)

type fakeStore struct {
	shares         []model.Share
	files          []model.File
	events         []model.IndexEvent
	upsertedShares []model.Share
	checkpoints    map[string]model.Checkpoint
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

func (f *fakeStore) GetShare(_ context.Context, shareCode string) (model.Share, bool, error) {
	for _, share := range f.shares {
		if share.ShareCode == shareCode {
			return share, true, nil
		}
	}
	return model.Share{}, false, nil
}

func (f *fakeStore) LoadCheckpoint(_ context.Context, shareCode string) (model.Checkpoint, bool, error) {
	cp, ok := f.checkpoints[shareCode]
	return cp, ok, nil
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
	srv := New(store, nil)

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
	var logs bytes.Buffer
	srv := New(store, &logs)

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
	if !strings.Contains(logs.String(), "share=swf01d43zby") || !strings.Contains(logs.String(), "event=share_added") {
		t.Fatalf("unexpected admin log: %q", logs.String())
	}
}

func TestSharesReturnsAllRegisteredShares(t *testing.T) {
	store := &fakeStore{
		shares: []model.Share{
			{ShareCode: "sw1", ReceiveCode: "a", Status: "ACTIVE", FailureCount: 0},
			{ShareCode: "sw2", ReceiveCode: "b", Status: "STALE", FailureCount: 2},
		},
	}
	srv := New(store, nil)

	req := httptest.NewRequest(http.MethodGet, "/shares", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d", rr.Code)
	}
	var body []ShareProgress
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode shares response: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("shares len = %d, want 2", len(body))
	}
	if body[1].ShareCode != "sw2" || body[1].Status != "STALE" || body[1].FailureCount != 2 {
		t.Fatalf("share[1] = %#v", body[1])
	}
}

func TestShareDetailReturnsCheckpointProgress(t *testing.T) {
	store := &fakeStore{
		shares: []model.Share{
			{ShareCode: "sw1", ReceiveCode: "a", Status: "ACTIVE", FailureCount: 1, LastError: "timeout"},
		},
		checkpoints: map[string]model.Checkpoint{
			"sw1": {
				ShareCode:  "sw1",
				CID:        "123",
				NextOffset: 200,
				Queue: []model.CrawlTask{
					{CID: "d1"},
					{CID: "d2"},
				},
				Visited: map[string]bool{
					"0":  true,
					"d0": true,
					"d1": true,
				},
			},
		},
	}
	srv := New(store, nil)

	req := httptest.NewRequest(http.MethodGet, "/shares/sw1", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d", rr.Code)
	}
	var body ShareProgress
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode share detail: %v", err)
	}
	if body.ShareCode != "sw1" {
		t.Fatalf("share_code = %q", body.ShareCode)
	}
	if body.QueueSize != 2 {
		t.Fatalf("queue_size = %d, want 2", body.QueueSize)
	}
	if body.VisitedCount != 3 {
		t.Fatalf("visited_count = %d, want 3", body.VisitedCount)
	}
	if body.NextOffset != 200 {
		t.Fatalf("next_offset = %d, want 200", body.NextOffset)
	}
	if body.LastError != "timeout" {
		t.Fatalf("last_error = %q", body.LastError)
	}
}
