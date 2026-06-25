package crawler

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"five/internal/api115"
	"five/internal/model"
)

type Page struct {
	Nodes      []model.File
	HasMore    bool
	ShareTitle string
	FileSize   int64
}

type Lister interface {
	ListPage(ctx context.Context, share model.Share, cid string, offset, limit int) (Page, error)
}

type Store interface {
	UpsertFiles(ctx context.Context, files []model.File) error
	SaveCheckpoint(ctx context.Context, cp model.Checkpoint) error
	LoadCheckpoint(ctx context.Context, shareCode string) (model.Checkpoint, bool, error)
	UpdateShareMeta(ctx context.Context, shareCode, receiveCode, title string, fileSize int64) error
	FindDuplicateShare(ctx context.Context, shareCode string, fileSize, minSize int64) (string, bool, error)
}

type Config struct {
	PageSize   int
	RetryCount int
	// PauseChecker, when non-nil, is polled at the top of the page loop. If it
	// reports true, CrawlShare finishes the current page, checkpoints, and
	// returns ErrPaused instead of fetching the next page. Nil means never pause.
	PauseChecker func() bool
	// DedupeMinFileSize: on the first page, if the share's file_size is >= this
	// and another share already has the same size, the share is a duplicate and
	// is not indexed (CrawlShare returns DuplicateShareError). 0 disables dedup.
	DedupeMinFileSize int64
}

const RootCID = "0"

// ErrPaused is returned by CrawlShare when PauseChecker reports the crawler is
// paused. The in-flight page completes and is checkpointed first, so the share
// resumes cleanly from the next page on the next crawl.
var ErrPaused = errors.New("crawler paused")

// DuplicateShareError is returned by CrawlShare on the first page when the
// share's file_size matches an already-indexed share (>= DedupeMinFileSize).
// Canonical is the keeper that this share duplicates.
type DuplicateShareError struct {
	Canonical string
}

func (e *DuplicateShareError) Error() string { return "duplicate of " + e.Canonical }

// ErrDuplicateShare is the sentinel wrapped by DuplicateShareError for errors.Is.
var ErrDuplicateShare = errors.New("duplicate share")

func (e *DuplicateShareError) Unwrap() error { return ErrDuplicateShare }

type Crawler struct {
	lister Lister
	store  Store
	cfg    Config
}

func New(lister Lister, store Store, cfg Config) *Crawler {
	if cfg.PageSize <= 0 {
		cfg.PageSize = 100
	}
	if cfg.RetryCount <= 0 {
		cfg.RetryCount = 2
	}
	return &Crawler{lister: lister, store: store, cfg: cfg}
}

func (c *Crawler) CrawlShare(ctx context.Context, share model.Share, crawledAt int64) error {
	log.Printf("event=crawl_share_started share=%s", share.ShareCode)
	metaPersisted := false
	dedupChecked := false
	cp, ok, err := c.store.LoadCheckpoint(ctx, share.ShareCode)
	if err != nil {
		return err
	}

	var queue []model.CrawlTask
	visited := map[string]bool{}
	if ok {
		queue = append(queue, cp.Queue...)
		visited = cp.Visited
	}
	activeCID := ""
	activeOffset := 0
	activeDepth := 0
	if ok && cp.CID != "" && !cp.Visited[cp.CID] {
		activeCID = cp.CID
		activeOffset = cp.NextOffset
		activeDepth = cp.ActiveDepth
	}
	if len(queue) == 0 && activeCID == "" {
		queue = append(queue, model.CrawlTask{
			CID:   RootCID,
			Depth: 0,
		})
	}
	if visited == nil {
		visited = map[string]bool{}
	}

	for activeCID != "" || len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		var task model.CrawlTask
		offset := 0
		if activeCID != "" {
			task = model.CrawlTask{CID: activeCID, Depth: activeDepth}
			activeCID = ""
			offset = activeOffset
			activeOffset = 0
		} else {
			task = queue[0]
			queue = queue[1:]
		}
		if visited[task.CID] {
			continue
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			if c.cfg.PauseChecker != nil && c.cfg.PauseChecker() {
				return ErrPaused
			}
			cp := model.Checkpoint{
				ShareCode:   share.ShareCode,
				CID:         task.CID,
				NextOffset:  offset,
				ActiveDepth: task.Depth,
				Queue:       append([]model.CrawlTask(nil), queue...),
				Visited:     cloneVisited(visited),
				UpdatedAt:   time.Now().Unix(),
			}
			if err := c.store.SaveCheckpoint(ctx, cp); err != nil {
				return err
			}
			var page Page
			var err error
			for attempt := 0; attempt <= c.cfg.RetryCount; attempt++ {
				page, err = c.lister.ListPage(ctx, share, task.CID, offset, c.cfg.PageSize)
				if err == nil {
					break
				}
				if !isPageRetryable(err) || attempt == c.cfg.RetryCount {
					log.Printf("event=crawl_page_failed share=%s cid=%s offset=%d error=%q", share.ShareCode, task.CID, offset, err.Error())
					return err
				}
				log.Printf("event=crawl_page_retry share=%s cid=%s offset=%d attempt=%d error=%q", share.ShareCode, task.CID, offset, attempt+1, err.Error())
			}
			// Dedup check runs once per share, on the first page, before any file
			// is indexed. A duplicate returns immediately and writes nothing.
			if !dedupChecked {
				dedupChecked = true
				if c.cfg.DedupeMinFileSize > 0 && page.FileSize >= c.cfg.DedupeMinFileSize {
					if canonical, isDup, err := c.store.FindDuplicateShare(ctx, share.ShareCode, page.FileSize, c.cfg.DedupeMinFileSize); err != nil {
						return err
					} else if isDup {
						log.Printf("event=crawl_share_duplicate share=%s canonical=%s file_size=%d", share.ShareCode, canonical, page.FileSize)
						return &DuplicateShareError{Canonical: canonical}
					}
				}
			}
			// Share metadata (title/size) is constant per share and present on
			// every snap page; persist it once on the first page we see. Only fill
			// the title when it is not already set: the scheduler re-crawls ACTIVE
			// shares, and overwriting here would undo a manual rename
			// (e.g. dedupe-share-titles). Backfill remains the force path.
			if !metaPersisted && page.ShareTitle != "" && share.ShareTitle == "" {
				if err := c.store.UpdateShareMeta(ctx, share.ShareCode, share.ReceiveCode, page.ShareTitle, page.FileSize); err != nil {
					return err
				}
				metaPersisted = true
			}
			if len(page.Nodes) == 0 && !page.HasMore {
				log.Printf("event=crawl_page_fetched share=%s cid=%s offset=%d nodes=0 indexed=0 has_more=false", share.ShareCode, task.CID, offset)
				visited[task.CID] = true
				break
			}
			for i := range page.Nodes {
				page.Nodes[i].ShareCode = share.ShareCode
				page.Nodes[i].CrawledAt = crawledAt
				page.Nodes[i].Depth = task.Depth + 1
				page.Nodes[i].ParentID = task.CID
			}
			filtered := filterIndexableFiles(page.Nodes)
			log.Printf(
				"event=crawl_page_fetched share=%s cid=%s offset=%d nodes=%d indexed=%d has_more=%t",
				share.ShareCode,
				task.CID,
				offset,
				len(page.Nodes),
				len(filtered),
				page.HasMore,
			)
			if err := c.store.UpsertFiles(ctx, filtered); err != nil {
				return err
			}
			for _, node := range page.Nodes {
				if node.IsDir {
					queue = append(queue, model.CrawlTask{
						CID:   node.FileID,
						Depth: node.Depth,
					})
				}
			}
			// Mark the cid visited before saving the end-of-page checkpoint when
			// this was its last page: the checkpoint advances NextOffset past the
			// data, so it must already record the cid as done. Otherwise an
			// interruption right after the save leaves a checkpoint pointing at an
			// empty past-end page with the cid unvisited, and the next run re-fetch
			// loops forever ("empty data with nonzero count").
			if !page.HasMore {
				visited[task.CID] = true
			}
			cp = model.Checkpoint{
				ShareCode:   share.ShareCode,
				CID:         task.CID,
				NextOffset:  offset + c.cfg.PageSize,
				ActiveDepth: task.Depth,
				Queue:       append([]model.CrawlTask(nil), queue...),
				Visited:     cloneVisited(visited),
				UpdatedAt:   time.Now().Unix(),
			}
			if err := c.store.SaveCheckpoint(ctx, cp); err != nil {
				return err
			}
			if !page.HasMore {
				break
			}
			offset += c.cfg.PageSize
		}
	}

	finalCP := model.Checkpoint{
		ShareCode:   share.ShareCode,
		CID:         RootCID,
		NextOffset:  0,
		ActiveDepth: 0,
		Queue:       nil,
		Visited:     cloneVisited(visited),
		UpdatedAt:   time.Now().Unix(),
	}
	if err := c.store.SaveCheckpoint(ctx, finalCP); err != nil {
		return err
	}
	log.Printf("event=crawl_share_finished share=%s visited=%d", share.ShareCode, len(finalCP.Visited))
	return nil
}

func cloneVisited(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func filterIndexableFiles(files []model.File) []model.File {
	out := make([]model.File, 0, len(files))
	for _, f := range files {
		if f.IsDir || isIndexableMediaExt(f.Ext) {
			out = append(out, f)
		}
	}
	return out
}

func isPageRetryable(err error) bool {
	return api115.IsRetryable(err) || api115.IsProxyFailure(err)
}

func isIndexableMediaExt(ext string) bool {
	switch strings.ToLower(ext) {
	case "mp4", "mkv", "avi", "mov", "wmv", "flv", "m4v", "ts", "m2ts", "iso",
		"mpeg", "mpg", "webm", "vob", "rm", "rmvb", "3gp", "asf", "f4v",
		"mp3", "flac", "aac", "wav", "m4a", "ape", "ogg", "wma", "ac3", "dts",
		"dsf", "dff", "wv", "tta", "tak", "mka", "mp2", "mpa", "mpc", "ofr", "ra",
		"srt", "ass", "ssa", "sub", "vtt", "ttml":
		return true
	default:
		return false
	}
}
