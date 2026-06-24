package searchindex

import (
	"context"
	"fmt"
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

type Builder struct {
	rootDir string
}

// rebuildBatchSize caps how many documents accumulate in a bleve batch before
// Rebuild flushes it. Rebuild formerly indexed the entire corpus into a single
// in-memory batch and committed once; at ~1.2M files that peaked at multiple GB
// and the process was OOM-killed ("signal: killed"). Flushing every N docs keeps
// peak memory bounded to one chunk. Package-level var so tests can shrink it.
var rebuildBatchSize = 2000

type searchDoc struct {
	Name string `json:"name"`
}

func (d searchDoc) Type() string {
	return "doc"
}

func New(rootDir string) *Builder {
	return &Builder{rootDir: rootDir}
}

// docID is the bleve document id for a file: "shareCode-fileId". The 115 cid is
// NOT globally unique — the same folder linked by several shares reuses one cid
// — so keying the bleve doc on the bare cid would let one share's doc overwrite
// another's (the search analogue of the "stolen root" SQLite bug). Scoping by
// share_code, mirroring the file table's composite primary key, makes every doc
// id unique. Share codes ("sw"+alphanumerics) and numeric cids never contain
// '-', so the first '-' splits the id unambiguously; the consumer (PowerList)
// parses it back with parseCompositeFileID.
func docID(shareCode, fileID string) string {
	return shareCode + "-" + fileID
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
			Name: f.Name,
		}
		if err := batch.Index(docID(f.ShareCode, f.FileID), doc); err != nil {
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

func buildMapping() mapping.IndexMapping {
	indexMapping := bleve.NewIndexMapping()
	docMapping := bleve.NewDocumentMapping()
	nameField := bleve.NewTextFieldMapping()
	nameField.Store = true
	docMapping.AddFieldMappingsAt("name", nameField)
	indexMapping.AddDocumentMapping("doc", docMapping)
	indexMapping.DefaultType = "doc"
	return indexMapping
}

func NewNameQuery(term string) query.Query {
	q := bleve.NewMatchQuery(term)
	q.SetField("name")
	return q
}
