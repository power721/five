package adminhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"five/internal/logutil"
	"five/internal/model"
	"five/internal/shares"
)

type Store interface {
	ListShares(ctx context.Context) ([]model.Share, error)
	CountShares(ctx context.Context) (int, error)
	UpsertShare(ctx context.Context, share model.Share) error
	CountFiles(ctx context.Context) (int, error)
	CountPendingIndexEvents(ctx context.Context) (int, error)
	GetShare(ctx context.Context, shareCode string) (model.Share, bool, error)
	LoadCheckpoint(ctx context.Context, shareCode string) (model.Checkpoint, bool, error)
}

type StatusResponse struct {
	ShareCount         int `json:"share_count"`
	FileCount          int `json:"file_count"`
	PendingIndexEvents int `json:"pending_index_events"`
}

type ShareProgress struct {
	ShareCode    string `json:"share_code"`
	ReceiveCode  string `json:"receive_code"`
	Status       string `json:"status"`
	FailureCount int    `json:"failure_count"`
	LastError    string `json:"last_error"`
	QueueSize    int    `json:"queue_size,omitempty"`
	VisitedCount int    `json:"visited_count,omitempty"`
	NextOffset   int    `json:"next_offset,omitempty"`
	ActiveCID    string `json:"active_cid,omitempty"`
}

type Server struct {
	store  Store
	mux    *http.ServeMux
	logger *log.Logger
}

func New(store Store, logWriter io.Writer) *Server {
	s := &Server{
		store:  store,
		mux:    http.NewServeMux(),
		logger: logutil.New(logWriter),
	}
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/shares", s.handleShares)
	s.mux.HandleFunc("/shares/", s.handleShareDetail)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	shareCount, err := s.store.CountShares(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fileCount, err := s.store.CountFiles(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pendingIndexEvents, err := s.store.CountPendingIndexEvents(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, StatusResponse{
		ShareCount:         shareCount,
		FileCount:          fileCount,
		PendingIndexEvents: pendingIndexEvents,
	})
}

type addShareRequest struct {
	ShareURL    string `json:"share_url"`
	ShareCode   string `json:"share_code"`
	ReceiveCode string `json:"receive_code"`
}

type addSharesResponse struct {
	Added  int           `json:"added"`
	Shares []model.Share `json:"shares"`
}

func (s *Server) handleShares(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleShareList(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		s.handleShareBatchUpload(w, r)
		return
	}
	s.handleSingleShareAdd(w, r)
}

func (s *Server) handleSingleShareAdd(w http.ResponseWriter, r *http.Request) {
	var req addShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var share model.Share
	var err error
	switch {
	case req.ShareURL != "":
		share, err = shares.ParseURL(req.ShareURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case req.ShareCode != "":
		share = model.Share{
			ShareCode:   req.ShareCode,
			ReceiveCode: req.ReceiveCode,
			Status:      "ACTIVE",
		}
	default:
		http.Error(w, "share_url or share_code required", http.StatusBadRequest)
		return
	}
	if err := s.store.UpsertShare(r.Context(), share); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Printf("event=share_added share=%s receive_code=%q", share.ShareCode, share.ReceiveCode)
	writeJSON(w, http.StatusCreated, addSharesResponse{
		Added:  1,
		Shares: []model.Share{share},
	})
}

func (s *Server) handleShareBatchUpload(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, fmt.Sprintf("load uploaded file: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	parsed, err := shares.Parse(file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, share := range parsed {
		if err := s.store.UpsertShare(r.Context(), share); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.logger.Printf("event=share_batch_added count=%d", len(parsed))
	writeJSON(w, http.StatusCreated, addSharesResponse{
		Added:  len(parsed),
		Shares: parsed,
	})
}

func (s *Server) handleShareList(w http.ResponseWriter, r *http.Request) {
	shares, err := s.store.ListShares(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]ShareProgress, 0, len(shares))
	for _, share := range shares {
		out = append(out, ShareProgress{
			ShareCode:    share.ShareCode,
			ReceiveCode:  share.ReceiveCode,
			Status:       share.Status,
			FailureCount: share.FailureCount,
			LastError:    share.LastError,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleShareDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	shareCode := r.URL.Path[len("/shares/"):]
	share, ok, err := s.store.GetShare(r.Context(), shareCode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	progress := ShareProgress{
		ShareCode:    share.ShareCode,
		ReceiveCode:  share.ReceiveCode,
		Status:       share.Status,
		FailureCount: share.FailureCount,
		LastError:    share.LastError,
	}
	cp, ok, err := s.store.LoadCheckpoint(r.Context(), shareCode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ok {
		progress.QueueSize = len(cp.Queue)
		progress.VisitedCount = len(cp.Visited)
		progress.NextOffset = cp.NextOffset
		progress.ActiveCID = cp.CID
	}
	writeJSON(w, http.StatusOK, progress)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
