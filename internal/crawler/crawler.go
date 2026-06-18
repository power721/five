package crawler

import (
	"context"
	"log"
	"path"
	"strings"
	"time"

	"five/internal/api115"
	"five/internal/model"
)

type Page struct {
	Nodes   []model.File
	HasMore bool
}

type Lister interface {
	ListPage(ctx context.Context, share model.Share, cid string, offset, limit int) (Page, error)
}

type Store interface {
	UpsertFiles(ctx context.Context, files []model.File) error
	SaveCheckpoint(ctx context.Context, cp model.Checkpoint) error
	LoadCheckpoint(ctx context.Context, shareCode string) (model.Checkpoint, bool, error)
}

type Config struct {
	PageSize   int
	RetryCount int
}

const RootCID = "0"

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
	activePath := ""
	activeDepth := 0
	if ok && cp.CID != "" && !cp.Visited[cp.CID] {
		activeCID = cp.CID
		activeOffset = cp.NextOffset
		activePath = cp.ActivePath
		activeDepth = cp.ActiveDepth
	}
	if len(queue) == 0 && activeCID == "" {
		queue = append(queue, model.CrawlTask{
			CID:   RootCID,
			Path:  "",
			Depth: 0,
		})
	}
	if visited == nil {
		visited = map[string]bool{}
	}

	for activeCID != "" || len(queue) > 0 {
		var task model.CrawlTask
		offset := 0
		if activeCID != "" {
			task = model.CrawlTask{CID: activeCID, Path: activePath, Depth: activeDepth}
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
			cp := model.Checkpoint{
				ShareCode:   share.ShareCode,
				CID:         task.CID,
				NextOffset:  offset,
				ActivePath:  task.Path,
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
				if !api115.IsRetryable(err) || attempt == c.cfg.RetryCount {
					log.Printf("event=crawl_page_failed share=%s cid=%s offset=%d error=%q", share.ShareCode, task.CID, offset, err.Error())
					return err
				}
				log.Printf("event=crawl_page_retry share=%s cid=%s offset=%d attempt=%d error=%q", share.ShareCode, task.CID, offset, attempt+1, err.Error())
			}
			if len(page.Nodes) == 0 && !page.HasMore {
				log.Printf("event=crawl_page_fetched share=%s cid=%s offset=%d nodes=0 indexed=0 has_more=false", share.ShareCode, task.CID, offset)
				break
			}
			for i := range page.Nodes {
				if page.Nodes[i].Path == "" {
					page.Nodes[i].Path = path.Join(task.Path, page.Nodes[i].Name)
				}
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
						Path:  node.Path,
						Depth: node.Depth,
					})
				}
			}
			cp = model.Checkpoint{
				ShareCode:   share.ShareCode,
				CID:         task.CID,
				NextOffset:  offset + c.cfg.PageSize,
				ActivePath:  task.Path,
				ActiveDepth: task.Depth,
				Queue:       append([]model.CrawlTask(nil), queue...),
				Visited:     cloneVisited(visited),
				UpdatedAt:   time.Now().Unix(),
			}
			if err := c.store.SaveCheckpoint(ctx, cp); err != nil {
				return err
			}
			if !page.HasMore {
				visited[task.CID] = true
				break
			}
			offset += c.cfg.PageSize
		}
	}

	finalCP := model.Checkpoint{
		ShareCode:   share.ShareCode,
		CID:         RootCID,
		NextOffset:  0,
		ActivePath:  "",
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
