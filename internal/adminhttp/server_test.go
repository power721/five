package adminhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"five/internal/model"
)

type fakeStore struct {
	shares         []model.Share
	crawlShares    []model.Share
	files          []model.File
	events         []model.IndexEvent
	shareCount     int
	fileCount      int
	eventCount     int
	upsertedShares []model.Share
	checkpoints    map[string]model.Checkpoint
	listSharesErr  error
	allFilesErr    error
	eventsErr      error
	reactivated    []string
	fileStats      map[string]fakeFileStats
}

type fakeFileStats struct {
	count int
	total int64
}

func (f *fakeStore) ListSharesForCrawl(context.Context, int64) ([]model.Share, error) {
	return f.crawlShares, nil
}

func (f *fakeStore) ListShares(context.Context) ([]model.Share, error) {
	if f.listSharesErr != nil {
		return nil, f.listSharesErr
	}
	return f.shares, nil
}

func (f *fakeStore) CountShares(context.Context) (int, error) {
	return f.shareCount, nil
}

func (f *fakeStore) UpsertShare(_ context.Context, share model.Share) error {
	f.upsertedShares = append(f.upsertedShares, share)
	return nil
}

func (f *fakeStore) AllFiles(context.Context) ([]model.File, error) {
	if f.allFilesErr != nil {
		return nil, f.allFilesErr
	}
	return f.files, nil
}

func (f *fakeStore) CountFiles(context.Context) (int, error) {
	return f.fileCount, nil
}

func (f *fakeStore) ShareFileStats(_ context.Context, shareCode string) (int, int64, error) {
	if st, ok := f.fileStats[shareCode]; ok {
		return st.count, st.total, nil
	}
	return 0, 0, nil
}

func (f *fakeStore) PendingIndexEvents(context.Context, int) ([]model.IndexEvent, error) {
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	return f.events, nil
}

func (f *fakeStore) CountPendingIndexEvents(context.Context) (int, error) {
	return f.eventCount, nil
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

func (f *fakeStore) ReactivateShare(_ context.Context, shareCode string) (bool, error) {
	for i, share := range f.shares {
		if share.ShareCode == shareCode {
			f.shares[i].Status = "ACTIVE"
			f.shares[i].FailureCount = 0
			f.shares[i].RetryAfterUnix = 0
			f.shares[i].LastError = ""
			f.reactivated = append(f.reactivated, shareCode)
			return true, nil
		}
	}
	return false, nil
}

func TestServerReactivateShare(t *testing.T) {
	store := &fakeStore{
		shares: []model.Share{
			{ShareCode: "sw1", Status: "QUARANTINE", FailureCount: 9, RetryAfterUnix: 9999999999, LastError: "empty data"},
		},
	}
	srv := New(store, &bytes.Buffer{})

	req := httptest.NewRequest(http.MethodPost, "/shares/sw1/reactivate", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if len(store.reactivated) != 1 || store.reactivated[0] != "sw1" {
		t.Fatalf("reactivated = %#v, want [sw1]", store.reactivated)
	}
	if store.shares[0].Status != "ACTIVE" || store.shares[0].FailureCount != 0 || store.shares[0].RetryAfterUnix != 0 {
		t.Fatalf("share not reset: %+v", store.shares[0])
	}

	// Unknown share -> 404.
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/shares/nope/reactivate", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown share status = %d, want 404", rr.Code)
	}

	// Detail GET still works (routing must not swallow it).
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/shares/sw1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("detail GET status = %d, want 200", rr.Code)
	}

	// Wrong method on reactivate -> 405.
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/shares/sw1/reactivate", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET reactivate status = %d, want 405", rr.Code)
	}
}

func TestStatusReturnsShareFileAndEventCounts(t *testing.T) {
	store := &fakeStore{
		shareCount:    2,
		fileCount:     3,
		eventCount:    2,
		listSharesErr: errors.New("status must not load shares"),
		allFilesErr:   errors.New("status must not load files"),
		eventsErr:     errors.New("status must not load events"),
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

func TestPostSharesAcceptsUploadedSharesFile(t *testing.T) {
	store := &fakeStore{}
	var logs bytes.Buffer
	srv := New(store, &logs)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "115_shares.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = part.Write([]byte("https://115.com/s/swf01d43zby?password=echo\n/mnt swf01d43zby 0 echo\nhttps://115.com/s/abc123xyz99\n"))
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/shares", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want 201", rr.Code)
	}
	if len(store.upsertedShares) != 2 {
		t.Fatalf("upserted shares = %d, want 2", len(store.upsertedShares))
	}
	if store.upsertedShares[0].ShareCode != "swf01d43zby" || store.upsertedShares[1].ShareCode != "abc123xyz99" {
		t.Fatalf("upserted shares = %#v", store.upsertedShares)
	}
	if !strings.Contains(logs.String(), "event=share_batch_added") {
		t.Fatalf("unexpected batch admin log: %q", logs.String())
	}
}

func TestSharesReturnsAllRegisteredShares(t *testing.T) {
	store := &fakeStore{
		shares: []model.Share{
			{ShareCode: "sw1", ReceiveCode: "a", Status: "ACTIVE", FailureCount: 0},
			{ShareCode: "sw2", ReceiveCode: "b", Status: "STALE", FailureCount: 2},
		},
		crawlShares: []model.Share{
			{ShareCode: "sw1", ReceiveCode: "a", Status: "ACTIVE", FailureCount: 0},
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

func TestShareDetailReturnsLinkFileCountAndTotalSize(t *testing.T) {
	store := &fakeStore{
		shares: []model.Share{
			{ShareCode: "sw1", ReceiveCode: "echo", Status: "ACTIVE"},
		},
		fileStats: map[string]fakeFileStats{
			"sw1": {count: 2, total: 3500},
		},
	}
	srv := New(store, nil)

	req := httptest.NewRequest(http.MethodGet, "/shares/sw1", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rr.Code)
	}
	var body ShareDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode share detail: %v", err)
	}
	if body.Link != "https://115.com/s/sw1?password=echo" {
		t.Fatalf("link = %q, want https://115.com/s/sw1?password=echo", body.Link)
	}
	if body.FileCount != 2 {
		t.Fatalf("fileCount = %d, want 2", body.FileCount)
	}
	if body.TotalFileSize != 3500 {
		t.Fatalf("totalFileSize = %d, want 3500", body.TotalFileSize)
	}
	// Existing detail fields are preserved.
	if body.ShareCode != "sw1" || body.Status != "ACTIVE" {
		t.Fatalf("existing fields lost: %#v", body)
	}
}
