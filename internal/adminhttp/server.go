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
	ShareFileStats(ctx context.Context, shareCode string) (int, int64, error)
	GetShare(ctx context.Context, shareCode string) (model.Share, bool, error)
	LoadCheckpoint(ctx context.Context, shareCode string) (model.Checkpoint, bool, error)
	ReactivateShare(ctx context.Context, shareCode string) (bool, error)
	DeleteShare(ctx context.Context, shareCode string) (bool, error)
}

type StatusResponse struct {
	ShareCount int `json:"share_count"`
	FileCount  int `json:"file_count"`
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

// ShareDetailResponse is the GET /shares/{code} payload: the crawl progress in
// ShareProgress plus the canonical share link and per-share indexed file totals.
type ShareDetailResponse struct {
	ShareProgress
	Link          string `json:"link"`
	FileCount     int    `json:"fileCount"`
	TotalFileSize int64  `json:"totalFileSize"`
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
	writeJSON(w, http.StatusOK, StatusResponse{
		ShareCount: shareCount,
		FileCount:  fileCount,
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
	rest := r.URL.Path[len("/shares/"):]
	if code, ok := strings.CutSuffix(rest, "/reactivate"); ok {
		s.handleShareReactivate(w, r, code)
		return
	}
	if r.Method == http.MethodDelete {
		s.handleShareDelete(w, r, rest)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	shareCode := rest
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
	fileCount, totalSize, err := s.store.ShareFileStats(r.Context(), shareCode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, ShareDetailResponse{
		ShareProgress: progress,
		Link:          shares.ShareURL(share.ShareCode, share.ReceiveCode),
		FileCount:     fileCount,
		TotalFileSize: totalSize,
	})
}

func (s *Server) handleShareReactivate(w http.ResponseWriter, r *http.Request, shareCode string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if shareCode == "" {
		http.Error(w, "share code required", http.StatusBadRequest)
		return
	}
	ok, err := s.store.ReactivateShare(r.Context(), shareCode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.logger.Printf("event=share_reactivated share=%s", shareCode)
	writeJSON(w, http.StatusOK, map[string]string{"share_code": shareCode, "status": "ACTIVE"})
}

// handleShareDelete implements DELETE /shares/{code}[?force=true]. A share with
// no indexed files is deleted outright; a share that still has files is refused
// (409) unless force=true is passed, in which case the share and all of its
// files/checkpoint are removed.
func (s *Server) handleShareDelete(w http.ResponseWriter, r *http.Request, shareCode string) {
	if shareCode == "" {
		http.Error(w, "share code required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	fileCount, _, err := s.store.ShareFileStats(ctx, shareCode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if fileCount > 0 && !force {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     fmt.Sprintf("share has %d files; pass force=true to delete", fileCount),
			"share_code": shareCode,
			"file_count": fileCount,
		})
		return
	}
	ok, err := s.store.DeleteShare(ctx, shareCode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.logger.Printf("event=share_deleted share=%s files=%d force=%v", shareCode, fileCount, force)
	writeJSON(w, http.StatusOK, map[string]any{
		"share_code": shareCode,
		"deleted":    true,
		"file_count": fileCount,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
