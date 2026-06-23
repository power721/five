package searchindex

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"five/internal/model"
)

type FileProvider interface {
	AllFiles(ctx context.Context) ([]model.File, error)
}

type EventProvider interface {
	FileProvider
	PendingIndexEvents(ctx context.Context, limit int) ([]model.IndexEvent, error)
	MarkIndexEventsProcessed(ctx context.Context, ids []int64) error
	FileByID(ctx context.Context, fileID string) (model.File, bool, error)
}

type Builder struct {
	rootDir string
}

// eventRetryBackoff / eventRetryMaxBackoff bound the retry delay when the event
// loop hits a transient error (e.g. a momentarily locked DB). Package-level vars
// (not consts) so tests can shrink them.
var (
	eventRetryBackoff    = time.Second
	eventRetryMaxBackoff = time.Minute
)

// rebuildBatchSize caps how many documents accumulate in a bleve batch before
// Rebuild flushes it. Rebuild formerly indexed the entire corpus into a single
// in-memory batch and committed once; at ~1.2M files that peaked at multiple GB
// and the process was OOM-killed ("signal: killed"). Flushing every N docs keeps
// peak memory bounded to one chunk. Package-level var so tests can shrink it.
var rebuildBatchSize = 2000

type searchDoc struct {
	FileID    string `json:"file_id"`
	ShareCode string `json:"share_code"`
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Depth     int    `json:"depth"`
}

func (d searchDoc) Type() string {
	return "doc"
}

func New(rootDir string) *Builder {
	return &Builder{rootDir: rootDir}
}

func (b *Builder) Rebuild(ctx context.Context, provider FileProvider, version int64, builtAt int64) (model.IndexManifest, error) {
	if builtAt == 0 {
		builtAt = time.Now().Unix()
	}
	if err := os.MkdirAll(b.rootDir, 0o755); err != nil {
		return model.IndexManifest{}, err
	}
	buildingPath := filepath.Join(b.rootDir, fmt.Sprintf("index_%06d_building", version))
	readyPath := filepath.Join(b.rootDir, fmt.Sprintf("index_%06d", version))
	_ = os.RemoveAll(buildingPath)
	_ = os.RemoveAll(readyPath)

	index, err := bleve.New(buildingPath, buildMapping())
	if err != nil {
		return model.IndexManifest{}, err
	}

	files, err := provider.AllFiles(ctx)
	if err != nil {
		index.Close()
		return model.IndexManifest{}, err
	}

	// Index in bounded batches instead of one giant in-memory batch. The whole
	// corpus formerly accumulated in a single bleve.Batch before a single commit,
	// which peaked at multiple GB and got OOM-killed on large indexes. Flushing
	// every rebuildBatchSize docs caps peak memory at one chunk; the trailing
	// partial batch is flushed after the loop.
	batch := index.NewBatch()
	var pending int
	flush := func() error {
		if pending == 0 {
			return nil
		}
		if err := index.Batch(batch); err != nil {
			return err
		}
		batch.Reset()
		pending = 0
		return nil
	}
	for _, f := range files {
		doc := searchDoc{
			FileID:    f.FileID,
			ShareCode: f.ShareCode,
			Name:      f.Name,
			IsDir:     f.IsDir,
			Depth:     f.Depth,
		}
		if err := batch.Index(f.FileID, doc); err != nil {
			index.Close()
			return model.IndexManifest{}, err
		}
		pending++
		if pending >= rebuildBatchSize {
			if err := flush(); err != nil {
				index.Close()
				return model.IndexManifest{}, err
			}
		}
	}
	if err := flush(); err != nil {
		index.Close()
		return model.IndexManifest{}, err
	}
	if err := index.Close(); err != nil {
		return model.IndexManifest{}, err
	}

	if err := os.Rename(buildingPath, readyPath); err != nil {
		return model.IndexManifest{}, err
	}
	return model.IndexManifest{
		Version:   version,
		IndexPath: readyPath,
		Status:    "READY",
		BuiltAt:   builtAt,
		FileCount: int64(len(files)),
	}, nil
}

func (b *Builder) ApplyPendingEvents(ctx context.Context, indexPath string, provider EventProvider, limit int) error {
	index, err := bleve.Open(indexPath)
	if err != nil {
		return err
	}
	defer index.Close()
	_, err = b.applyInto(ctx, index, provider, limit)
	return err
}

// applyInto applies up to limit pending events into the already-open index and
// marks them processed. It intentionally does NOT open or close the index: the
// caller owns the handle so it can be reused across batches. Closing scorch on
// every batch kills its background segment merger, so segments accumulate until
// each Open/Batch grinds to a halt — which is what stalled the consumer.
func (b *Builder) applyInto(ctx context.Context, index bleve.Index, provider EventProvider, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	events, err := provider.PendingIndexEvents(ctx, limit)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, nil
	}

	batch := index.NewBatch()
	var processed []int64
	for _, event := range events {
		switch event.Op {
		case "upsert":
			file, ok, err := provider.FileByID(ctx, event.FileID)
			if err != nil {
				return 0, err
			}
			if !ok {
				continue
			}
			if err := batch.Index(file.FileID, searchDoc{
				FileID:    file.FileID,
				ShareCode: file.ShareCode,
				Name:      file.Name,
				IsDir:     file.IsDir,
				Depth:     file.Depth,
			}); err != nil {
				return 0, err
			}
			processed = append(processed, event.ID)
		}
	}
	if err := index.Batch(batch); err != nil {
		return 0, err
	}
	if len(processed) > 0 {
		if err := provider.MarkIndexEventsProcessed(ctx, processed); err != nil {
			return 0, err
		}
	}
	return len(processed), nil
}

func buildMapping() mapping.IndexMapping {
	indexMapping := bleve.NewIndexMapping()
	docMapping := bleve.NewDocumentMapping()
	nameField := bleve.NewTextFieldMapping()
	nameField.Store = true
	shareField := bleve.NewKeywordFieldMapping()
	shareField.Store = true
	docMapping.AddFieldMappingsAt("name", nameField)
	docMapping.AddFieldMappingsAt("share_code", shareField)
	indexMapping.AddDocumentMapping("doc", docMapping)
	indexMapping.DefaultType = "doc"
	return indexMapping
}

func NewNameQuery(term string) query.Query {
	q := bleve.NewMatchQuery(term)
	q.SetField("name")
	return q
}

func (b *Builder) RunEventLoop(ctx context.Context, indexPath string, provider EventProvider, limit int, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Minute
	}
	// Open the index ONCE and reuse it across batches. Opening/closing per batch
	// terminates scorch's background segment merger every cycle, so segments pile
	// up and every subsequent Open/Batch gets slower until throughput collapses.
	index, err := bleve.Open(indexPath)
	if err != nil {
		return err
	}
	defer index.Close()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	backoff := eventRetryBackoff
	for {
		processed, err := b.applyInto(ctx, index, provider, limit)
		if err != nil {
			// A transient failure (e.g. a momentarily locked DB) must not take
			// the whole daemon down via the runDaemon error path. Log, back off,
			// and retry; only a cancelled context ends the loop.
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("event=index_events_error err=%q backoff=%s", err.Error(), backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			if backoff < eventRetryMaxBackoff {
				backoff *= 2
				if backoff > eventRetryMaxBackoff {
					backoff = eventRetryMaxBackoff
				}
			}
			continue
		}
		backoff = eventRetryBackoff
		if processed > 0 {
			log.Printf("event=index_events_applied count=%d", processed)
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
