package crawler

import (
	"context"
	"path"
	"strings"
	"time"

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
	PageSize int
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
	return &Crawler{lister: lister, store: store, cfg: cfg}
}

func (c *Crawler) CrawlShare(ctx context.Context, share model.Share, crawledAt int64) error {
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
	if len(queue) == 0 {
		queue = append(queue, model.CrawlTask{
			CID:   RootCID,
			Path:  "",
			Depth: 0,
		})
	}
	if visited == nil {
		visited = map[string]bool{}
	}

	for len(queue) > 0 {
		task := queue[0]
		queue = queue[1:]
		if visited[task.CID] {
			continue
		}
		visited[task.CID] = true

		offset := 0
		for {
			page, err := c.lister.ListPage(ctx, share, task.CID, offset, c.cfg.PageSize)
			if err != nil {
				return err
			}
			if len(page.Nodes) == 0 && !page.HasMore {
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
			cp := model.Checkpoint{
				ShareCode: share.ShareCode,
				CID:       task.CID,
				Queue:     append([]model.CrawlTask(nil), queue...),
				Visited:   cloneVisited(visited),
				UpdatedAt: time.Now().Unix(),
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
		ShareCode: share.ShareCode,
		CID:       RootCID,
		Queue:     nil,
		Visited:   cloneVisited(visited),
		UpdatedAt: time.Now().Unix(),
	}
	return c.store.SaveCheckpoint(ctx, finalCP)
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
