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
	shareCount     int
	fileCount      int
	upsertedShares []model.Share
	checkpoints    map[string]model.Checkpoint
	listSharesErr  error
	allFilesErr    error
	reactivated    []string
	deleted        []string
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

func (f *fakeStore) DeleteShare(_ context.Context, shareCode string) (bool, error) {
	for i, share := range f.shares {
		if share.ShareCode == shareCode {
			f.shares = append(f.shares[:i], f.shares[i+1:]...)
			f.deleted = append(f.deleted, shareCode)
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
	srv := New(store, nil, &bytes.Buffer{})

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

func TestServerDeleteShare(t *testing.T) {
	// Share with no files -> deleted outright, 200.
	store := &fakeStore{
		shares: []model.Share{{ShareCode: "empty", Status: "ACTIVE"}},
	}
	srv := New(store, nil, &bytes.Buffer{})

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/shares/empty", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("empty share status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if len(store.deleted) != 1 || store.deleted[0] != "empty" {
		t.Fatalf("deleted = %#v, want [empty]", store.deleted)
	}

	// Share with files, no force -> 409, nothing deleted.
	store = &fakeStore{
		shares:   []model.Share{{ShareCode: "full", Status: "ACTIVE"}},
		fileStats: map[string]fakeFileStats{"full": {count: 5, total: 100}},
	}
	srv = New(store, nil, &bytes.Buffer{})
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/shares/full", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("full share no-force status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if len(store.deleted) != 0 {
		t.Fatalf("must not delete without force; deleted = %#v", store.deleted)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 409 body: %v", err)
	}
	if body["file_count"] != float64(5) {
		t.Fatalf("409 file_count = %v, want 5", body["file_count"])
	}

	// Same share with force=true -> 200, deleted.
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/shares/full?force=true", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("full share force status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if len(store.deleted) != 1 || store.deleted[0] != "full" {
		t.Fatalf("force deleted = %#v, want [full]", store.deleted)
	}

	// Unknown share -> 404.
	store = &fakeStore{}
	srv = New(store, nil, &bytes.Buffer{})
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/shares/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown share status = %d, want 404", rr.Code)
	}

	// Detail GET still works (routing must not swallow it).
	store = &fakeStore{shares: []model.Share{{ShareCode: "sw1", Status: "ACTIVE"}}}
	srv = New(store, nil, &bytes.Buffer{})
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/shares/sw1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("detail GET status = %d, want 200", rr.Code)
	}
}

func TestStatusReturnsShareAndFileCounts(t *testing.T) {
	store := &fakeStore{
		shareCount:    2,
		fileCount:     3,
		listSharesErr: errors.New("status must not load shares"),
		allFilesErr:   errors.New("status must not load files"),
	}
	srv := New(store, nil, nil)

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
}

func TestPostSharesAcceptsShareURL(t *testing.T) {
	store := &fakeStore{}
	var logs bytes.Buffer
	srv := New(store, nil, &logs)

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
	srv := New(store, nil, &logs)

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
	srv := New(store, nil, nil)

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
	srv := New(store, nil, nil)

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
	srv := New(store, nil, nil)

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

type fakeControl struct {
	paused bool
}

func (f *fakeControl) Pause()       { f.paused = true }
func (f *fakeControl) Resume()      { f.paused = false }
func (f *fakeControl) Paused() bool { return f.paused }

func doRequest(srv *Server, method, target string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr
}

func assertStateBody(t *testing.T, body string, want string) {
	t.Helper()
	var resp struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode state response %q: %v", body, err)
	}
	if resp.State != want {
		t.Fatalf("state = %q, want %q (body=%s)", resp.State, want, body)
	}
}

func TestCrawlerPauseResumeState(t *testing.T) {
	control := &fakeControl{}
	srv := New(&fakeStore{}, control, &bytes.Buffer{})

	// Initial state: running.
	rr := doRequest(srv, http.MethodGet, "/crawler/state")
	if rr.Code != http.StatusOK {
		t.Fatalf("state status = %d, want 200", rr.Code)
	}
	assertStateBody(t, rr.Body.String(), "running")

	// Pause -> 200, state=paused, control flipped on.
	rr = doRequest(srv, http.MethodPost, "/crawler/pause")
	if rr.Code != http.StatusOK {
		t.Fatalf("pause status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !control.paused {
		t.Fatal("control not paused after POST /crawler/pause")
	}
	assertStateBody(t, rr.Body.String(), "paused")
	rr = doRequest(srv, http.MethodGet, "/crawler/state")
	assertStateBody(t, rr.Body.String(), "paused")

	// Pause is idempotent.
	rr = doRequest(srv, http.MethodPost, "/crawler/pause")
	if rr.Code != http.StatusOK {
		t.Fatalf("idempotent pause status = %d, want 200", rr.Code)
	}

	// Resume -> 200, state=running, control flipped off.
	rr = doRequest(srv, http.MethodPost, "/crawler/resume")
	if rr.Code != http.StatusOK {
		t.Fatalf("resume status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if control.paused {
		t.Fatal("control still paused after POST /crawler/resume")
	}
	assertStateBody(t, rr.Body.String(), "running")

	// Wrong methods -> 405.
	if rr := doRequest(srv, http.MethodGet, "/crawler/pause"); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /crawler/pause status = %d, want 405", rr.Code)
	}
	if rr := doRequest(srv, http.MethodDelete, "/crawler/resume"); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /crawler/resume status = %d, want 405", rr.Code)
	}
	if rr := doRequest(srv, http.MethodPost, "/crawler/state"); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /crawler/state status = %d, want 405", rr.Code)
	}
}

func TestCrawlerPauseEndpointsReturn503WhenNoControl(t *testing.T) {
	srv := New(&fakeStore{}, nil, &bytes.Buffer{})
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/crawler/pause"},
		{http.MethodPost, "/crawler/resume"},
		{http.MethodGet, "/crawler/state"},
	}
	for _, c := range cases {
		rr := doRequest(srv, c.method, c.path)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status = %d, want 503", c.method, c.path, rr.Code)
		}
	}
}
