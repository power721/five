package searchindex

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/blevesearch/bleve/v2"

	"five/internal/model"
)

type staticProvider struct {
	files []model.File
}

func (s staticProvider) AllFiles(_ context.Context) ([]model.File, error) {
	return s.files, nil
}

func TestRebuildCreatesSearchableIndexAndManifest(t *testing.T) {
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{
				FileID:    "f1",
				ShareCode: "swf01d43zby",
				ParentID:  "0",
				Name:      "Avatar.2009.2160p.mkv",
				Ext:       "mkv",
			},
		},
	}

	manifest, err := builder.Rebuild(context.Background(), provider, 1, 1)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if manifest.Status != "READY" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}

	index, err := bleve.Open(manifest.IndexPath)
	if err != nil {
		t.Fatalf("open bleve: %v", err)
	}
	defer index.Close()

	req := bleve.NewSearchRequest(bleve.NewQueryStringQuery("Avatar"))
	res, err := index.Search(req)
	if err != nil {
		t.Fatalf("search bleve: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("search total = %d, want 1", res.Total)
	}
}

// TestRebuildFlushesBoundedBatchesAcrossRemainder guards the chunked-flush path
// in Rebuild: with a small batch size and a doc count that is NOT a multiple of
// it (7 + 7 + 3), every document must still land in the index. A naive "flush
// only when full" implementation drops the trailing partial batch and fails
// here. (Rebuild formerly held the whole corpus in one batch and got
// OOM-killed at ~1.2M files; this test pins the bounded-batch refactor.)
func TestRebuildFlushesBoundedBatchesAcrossRemainder(t *testing.T) {
	prev := rebuildBatchSize
	rebuildBatchSize = 7
	defer func() { rebuildBatchSize = prev }()

	const n = 17
	files := make([]model.File, 0, n)
	for i := 0; i < n; i++ {
		files = append(files, model.File{
			FileID:    fmt.Sprintf("f%d", i),
			ShareCode: "sw1",
			Name:      fmt.Sprintf("movie-%d.mkv", i),
			Ext:       "mkv",
		})
	}

	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	manifest, err := builder.Rebuild(context.Background(), staticProvider{files: files}, 1, 1)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	index, err := bleve.Open(manifest.IndexPath)
	if err != nil {
		t.Fatalf("open bleve: %v", err)
	}
	defer index.Close()

	count, err := index.DocCount()
	if err != nil {
		t.Fatalf("doc count: %v", err)
	}
	if count != uint64(n) {
		t.Fatalf("doc count = %d, want %d (trailing partial batch must be flushed)", count, n)
	}
}

// TestRebuildKeysDocsByShareCodeAndFileID guards the composite bleve doc id. The
// 115 cid is NOT globally unique — one folder linked by several shares reuses a
// single cid — so keying docs on the bare cid let one share's doc overwrite the
// other's. With doc id "shareCode-fileId" both rows survive and each is
// reachable under its own id; the consumer splits it back with
// parseCompositeFileID.
func TestRebuildKeysDocsByShareCodeAndFileID(t *testing.T) {
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{FileID: "shared", ShareCode: "swA", Name: "fromA.mkv"},
			{FileID: "shared", ShareCode: "swB", Name: "fromB.mkv"},
		},
	}

	manifest, err := builder.Rebuild(context.Background(), provider, 1, 1)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	index, err := bleve.Open(manifest.IndexPath)
	if err != nil {
		t.Fatalf("open bleve: %v", err)
	}
	defer index.Close()

	count, err := index.DocCount()
	if err != nil {
		t.Fatalf("doc count: %v", err)
	}
	if count != 2 {
		t.Fatalf("doc count = %d, want 2 (shared cid must not collide)", count)
	}
	for _, id := range []string{"swA-shared", "swB-shared"} {
		doc, err := index.Document(id)
		if err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if doc == nil {
			t.Fatalf("expected document %q to exist (composite doc id)", id)
		}
	}
}
