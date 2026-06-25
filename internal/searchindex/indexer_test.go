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

// TestRebuildMergesMovieAcrossNamesAndMatchesEither guards movie content-dedup:
// two large, no-marker files with the same (sha1,size) but different names merge
// into ONE doc that carries both names, so a search for either still hits.
func TestRebuildMergesMovieAcrossNamesAndMatchesEither(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{FileID: "9", ShareCode: "swz", Name: "Avatar.2009.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
			{FileID: "1", ShareCode: "swa", Name: "阿凡达.2009.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
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
	if count != 1 {
		t.Fatalf("doc count = %d, want 1 (movie merged across names)", count)
	}
	for _, term := range []string{"Avatar", "阿凡达"} {
		req := bleve.NewSearchRequest(bleve.NewMatchQuery(term))
		res, err := index.Search(req)
		if err != nil {
			t.Fatalf("search %q: %v", term, err)
		}
		if res.Total != 1 {
			t.Errorf("search %q total = %d, want 1 (merged movie must match either name)", term, res.Total)
		}
	}
}

// TestRebuildKeepsDifferentlyNamedEpisodesSeparate guards episode dedup: two
// small files with the same (sha1,size) but different names stay as two docs.
func TestRebuildKeepsDifferentlyNamedEpisodesSeparate(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{FileID: "1", ShareCode: "swa", Name: "Show - S01E09 - 第9集.mkv", SHA1: "AAA", Size: 2 * gb},
			{FileID: "2", ShareCode: "swb", Name: "Show.S01E09.mkv", SHA1: "AAA", Size: 2 * gb},
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
		t.Fatalf("doc count = %d, want 2 (episodes with different names stay separate)", count)
	}
}

// TestRebuildRollsUpEpisodesIntoContainerFolder guards the folder rollup end to
// end: a season folder with >=5 marker episodes becomes ONE doc carrying the
// episode stems; the episodes are not separate docs, and an episode-code search
// hits the folder (not a file).
func TestRebuildRollsUpEpisodesIntoContainerFolder(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "剧", IsDir: true},
			{FileID: "d2", ShareCode: "sw1", ParentID: "d1", Name: "第一季", IsDir: true},
			{FileID: "e1", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E01.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
			{FileID: "e2", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E02.mkv", Ext: "mkv", SHA1: "H2", Size: 2 * gb},
			{FileID: "e3", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E03.mkv", Ext: "mkv", SHA1: "H3", Size: 2 * gb},
			{FileID: "e4", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E04.mkv", Ext: "mkv", SHA1: "H4", Size: 2 * gb},
			{FileID: "e5", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E05.mkv", Ext: "mkv", SHA1: "H5", Size: 2 * gb},
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
		t.Fatalf("doc count = %d, want 2 (root + season; 5 episodes rolled into season)", count)
	}

	// Searching the show name hits the season folder (its absorbed episode
	// names), not a file.
	req := bleve.NewSearchRequest(bleve.NewMatchQuery("Show"))
	res, err := index.Search(req)
	if err != nil {
		t.Fatalf("search Show: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("search Show total = %d, want 1 (the season folder)", res.Total)
	}
	if res.Hits[0].ID != "sw1-d2" {
		t.Errorf("hit id = %q, want sw1-d2 (season folder)", res.Hits[0].ID)
	}
}

// TestRebuildDoesNotRollUpMovieCollection guards the converse: a folder of large
// movies (no markers, <5 files) is NOT a container, so the movies stay as
// separate docs and the folder is just a passthrough entry.
func TestRebuildDoesNotRollUpMovieCollection(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := staticProvider{
		files: []model.File{
			{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "合集", IsDir: true},
			{FileID: "m1", ShareCode: "sw1", ParentID: "d1", Name: "AvatarA.2160p.mkv", Ext: "mkv", SHA1: "H1", Size: 40 * gb},
			{FileID: "m2", ShareCode: "sw1", ParentID: "d1", Name: "AvatarB.2160p.mkv", Ext: "mkv", SHA1: "H2", Size: 40 * gb},
			{FileID: "m3", ShareCode: "sw1", ParentID: "d1", Name: "AvatarC.2160p.mkv", Ext: "mkv", SHA1: "H3", Size: 40 * gb},
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
	if count != 4 {
		t.Fatalf("doc count = %d, want 4 (folder passthrough + 3 movies)", count)
	}
	req := bleve.NewSearchRequest(bleve.NewMatchQuery("AvatarB"))
	res, err := index.Search(req)
	if err != nil {
		t.Fatalf("search AvatarB: %v", err)
	}
	if res.Total != 1 {
		t.Errorf("search AvatarB total = %d, want 1 (the movie file)", res.Total)
	}
}
