package adminhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"five/internal/model"
	"five/internal/shares"
)

type Store interface {
	ListSharesForCrawl(ctx context.Context, now int64) ([]model.Share, error)
	UpsertShare(ctx context.Context, share model.Share) error
	AllFiles(ctx context.Context) ([]model.File, error)
	PendingIndexEvents(ctx context.Context, limit int) ([]model.IndexEvent, error)
}

type StatusResponse struct {
	ShareCount         int `json:"share_count"`
	FileCount          int `json:"file_count"`
	PendingIndexEvents int `json:"pending_index_events"`
}

type Server struct {
	store Store
	mux   *http.ServeMux
}

func New(store Store) *Server {
	s := &Server{
		store: store,
		mux:   http.NewServeMux(),
	}
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/shares", s.handleShares)
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
	shareList, err := s.store.ListSharesForCrawl(ctx, time.Now().Unix())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	files, err := s.store.AllFiles(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	events, err := s.store.PendingIndexEvents(ctx, 1_000_000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, StatusResponse{
		ShareCount:         len(shareList),
		FileCount:          len(files),
		PendingIndexEvents: len(events),
	})
}

type addShareRequest struct {
	ShareURL    string `json:"share_url"`
	ShareCode   string `json:"share_code"`
	ReceiveCode string `json:"receive_code"`
}

func (s *Server) handleShares(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	writeJSON(w, http.StatusCreated, share)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
