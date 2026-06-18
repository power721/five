package crawler

import (
	"context"
	"testing"

	"five/internal/model"
)

type fakeLister struct {
	pages map[string][]Page
	errs  map[string]map[int]error
	calls []string
}

func (f *fakeLister) ListPage(_ context.Context, share model.Share, cid string, offset, limit int) (Page, error) {
	f.calls = append(f.calls, cid+":"+string(rune('0'+offset)))
	if errPages, ok := f.errs[cid]; ok {
		if err, ok := errPages[offset]; ok {
			return Page{}, err
		}
	}
	list := f.pages[cid]
	index := offset / limit
	if index >= len(list) {
		return Page{}, nil
	}
	return list[index], nil
}

type memoryStore struct {
	files          []model.File
	checkpoint     model.Checkpoint
	checkpointSeen bool
	upsertBatches  [][]string
}

func (m *memoryStore) UpsertFiles(_ context.Context, files []model.File) error {
	batch := make([]string, 0, len(files))
	for _, f := range files {
		batch = append(batch, f.FileID)
	}
	m.upsertBatches = append(m.upsertBatches, batch)
	m.files = append(m.files, files...)
	return nil
}

func (m *memoryStore) SaveCheckpoint(_ context.Context, cp model.Checkpoint) error {
	m.checkpoint = cp
	m.checkpointSeen = true
	return nil
}

func (m *memoryStore) LoadCheckpoint(_ context.Context, _ string) (model.Checkpoint, bool, error) {
	if !m.checkpointSeen {
		return model.Checkpoint{}, false, nil
	}
	return m.checkpoint, true, nil
}

func TestCrawlerCrawlShareBFSAndCheckpoint(t *testing.T) {
	c := New(&fakeLister{
		pages: map[string][]Page{
			"0": {
				{
					Nodes: []model.File{
						{FileID: "d1", ShareCode: "swf01d43zby", ParentID: "0", Name: "Season 01", Path: "/Season 01", IsDir: true, Depth: 1},
						{FileID: "f1", ShareCode: "swf01d43zby", ParentID: "0", Name: "Avatar.mkv", Path: "/Avatar.mkv", Ext: "mkv", Depth: 1},
					},
					HasMore: false,
				},
			},
			"d1": {
				{
					Nodes: []model.File{
						{FileID: "f2", ShareCode: "swf01d43zby", ParentID: "d1", Name: "Episode 1.mkv", Path: "/Season 01/Episode 1.mkv", Ext: "mkv", Depth: 2},
					},
					HasMore: false,
				},
			},
		},
	}, &memoryStore{}, Config{PageSize: 100})

	store := c.store.(*memoryStore)
	share := model.Share{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
	}

	if err := c.CrawlShare(context.Background(), share, 100); err != nil {
		t.Fatalf("crawl share: %v", err)
	}

	if len(store.files) != 3 {
		t.Fatalf("stored files = %d, want 3", len(store.files))
	}
	if !store.checkpointSeen {
		t.Fatal("expected checkpoint to be saved")
	}
	if len(store.checkpoint.Queue) != 0 {
		t.Fatalf("checkpoint queue length = %d, want 0", len(store.checkpoint.Queue))
	}
	if !store.checkpoint.Visited["0"] || !store.checkpoint.Visited["d1"] {
		t.Fatalf("visited map = %#v", store.checkpoint.Visited)
	}
}

func TestCrawlerFiltersToMediaAndSubtitleFiles(t *testing.T) {
	c := New(&fakeLister{
		pages: map[string][]Page{
			"0": {
				{
					Nodes: []model.File{
						{FileID: "v1", ShareCode: "swf01d43zby", ParentID: "0", Name: "Movie.mkv", Path: "/Movie.mkv", Ext: "mkv", Depth: 1},
						{FileID: "s1", ShareCode: "swf01d43zby", ParentID: "0", Name: "Movie.ass", Path: "/Movie.ass", Ext: "ass", Depth: 1},
						{FileID: "o1", ShareCode: "swf01d43zby", ParentID: "0", Name: "LegacyMovie.rmvb", Path: "/LegacyMovie.rmvb", Ext: "rmvb", Depth: 1},
						{FileID: "o2", ShareCode: "swf01d43zby", ParentID: "0", Name: "Archive.asf", Path: "/Archive.asf", Ext: "asf", Depth: 1},
						{FileID: "o3", ShareCode: "swf01d43zby", ParentID: "0", Name: "HiRes.dsf", Path: "/HiRes.dsf", Ext: "dsf", Depth: 1},
						{FileID: "o4", ShareCode: "swf01d43zby", ParentID: "0", Name: "Movie.ttml", Path: "/Movie.ttml", Ext: "ttml", Depth: 1},
						{FileID: "n1", ShareCode: "swf01d43zby", ParentID: "0", Name: "Movie.nfo", Path: "/Movie.nfo", Ext: "nfo", Depth: 1},
						{FileID: "t1", ShareCode: "swf01d43zby", ParentID: "0", Name: "notes.txt", Path: "/notes.txt", Ext: "txt", Depth: 1},
						{FileID: "x1", ShareCode: "swf01d43zby", ParentID: "0", Name: "playlist.m3u8", Path: "/playlist.m3u8", Ext: "m3u8", Depth: 1},
						{FileID: "x2", ShareCode: "swf01d43zby", ParentID: "0", Name: "disc.cda", Path: "/disc.cda", Ext: "cda", Depth: 1},
						{FileID: "x3", ShareCode: "swf01d43zby", ParentID: "0", Name: "stream.ram", Path: "/stream.ram", Ext: "ram", Depth: 1},
						{FileID: "x4", ShareCode: "swf01d43zby", ParentID: "0", Name: "clip.swf", Path: "/clip.swf", Ext: "swf", Depth: 1},
						{FileID: "x5", ShareCode: "swf01d43zby", ParentID: "0", Name: "tone.mid", Path: "/tone.mid", Ext: "mid", Depth: 1},
					},
					HasMore: false,
				},
			},
		},
	}, &memoryStore{}, Config{PageSize: 100})

	store := c.store.(*memoryStore)
	share := model.Share{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
	}

	if err := c.CrawlShare(context.Background(), share, 100); err != nil {
		t.Fatalf("crawl share: %v", err)
	}

	if len(store.files) != 6 {
		t.Fatalf("stored files = %d, want 6", len(store.files))
	}
	gotExts := []string{
		store.files[0].Ext,
		store.files[1].Ext,
		store.files[2].Ext,
		store.files[3].Ext,
		store.files[4].Ext,
		store.files[5].Ext,
	}
	wantExts := []string{"mkv", "ass", "rmvb", "asf", "dsf", "ttml"}
	for i := range wantExts {
		if gotExts[i] != wantExts[i] {
			t.Fatalf("stored exts = %#v, want %#v", gotExts, wantExts)
		}
	}
}

func TestCrawlerResumesFromNextPageAfterFailure(t *testing.T) {
	lister := &fakeLister{
		pages: map[string][]Page{
			"0": {
				{
					Nodes: []model.File{
						{FileID: "f1", ShareCode: "swf01d43zby", ParentID: "0", Name: "A.mkv", Path: "/A.mkv", Ext: "mkv"},
					},
					HasMore: true,
				},
				{
					Nodes: []model.File{
						{FileID: "f2", ShareCode: "swf01d43zby", ParentID: "0", Name: "B.mkv", Path: "/B.mkv", Ext: "mkv"},
					},
					HasMore: false,
				},
			},
		},
		errs: map[string]map[int]error{
			"0": {
				1: context.DeadlineExceeded,
			},
		},
	}
	store := &memoryStore{}
	c := New(lister, store, Config{PageSize: 1})
	share := model.Share{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
	}

	if err := c.CrawlShare(context.Background(), share, 100); err == nil {
		t.Fatal("expected first crawl to fail on second page")
	}
	if len(store.files) != 1 {
		t.Fatalf("stored files after first run = %d, want 1", len(store.files))
	}
	t.Logf("checkpoint after first run: cid=%q next_offset=%d queue=%#v visited=%#v", store.checkpoint.CID, store.checkpoint.NextOffset, store.checkpoint.Queue, store.checkpoint.Visited)

	delete(lister.errs["0"], 1)
	if err := c.CrawlShare(context.Background(), share, 101); err != nil {
		t.Fatalf("resume crawl share: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range store.files {
		seen[f.FileID] = true
	}
	if !seen["f1"] || !seen["f2"] {
		t.Fatalf("resumed files = %#v, want f1 and f2; calls=%#v upserts=%#v", seen, lister.calls, store.upsertBatches)
	}
}
