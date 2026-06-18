package searchindex

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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
				Path:      "/Avatar.2009.2160p.mkv",
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

type eventProvider struct {
	files  map[string]model.File
	events []model.IndexEvent
}

func (e *eventProvider) AllFiles(_ context.Context) ([]model.File, error) {
	out := make([]model.File, 0, len(e.files))
	for _, f := range e.files {
		out = append(out, f)
	}
	return out, nil
}

func (e *eventProvider) PendingIndexEvents(_ context.Context, limit int) ([]model.IndexEvent, error) {
	if len(e.events) < limit {
		limit = len(e.events)
	}
	return append([]model.IndexEvent(nil), e.events[:limit]...), nil
}

func (e *eventProvider) MarkIndexEventsProcessed(_ context.Context, ids []int64) error {
	keep := e.events[:0]
	for _, ev := range e.events {
		matched := false
		for _, id := range ids {
			if ev.ID == id {
				matched = true
				break
			}
		}
		if !matched {
			keep = append(keep, ev)
		}
	}
	e.events = keep
	return nil
}

func (e *eventProvider) FileByID(_ context.Context, fileID string) (model.File, bool, error) {
	f, ok := e.files[fileID]
	return f, ok, nil
}

func TestApplyPendingEventsIndexesNewFiles(t *testing.T) {
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := &eventProvider{
		files: map[string]model.File{
			"f1": {
				FileID:    "f1",
				ShareCode: "swf01d43zby",
				Name:      "Avatar.2009.2160p.mkv",
				Path:      "/Avatar.2009.2160p.mkv",
				Ext:       "mkv",
			},
		},
	}
	manifest, err := builder.Rebuild(context.Background(), provider, 1, 1)
	if err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}

	provider.files["f2"] = model.File{
		FileID:    "f2",
		ShareCode: "swf01d43zby",
		Name:      "Born.with.Luck.S01E01.mp4",
		Path:      "/Born.with.Luck.S01E01.mp4",
		Ext:       "mp4",
	}
	provider.events = []model.IndexEvent{
		{ID: 1, FileID: "f2", Op: "upsert"},
	}

	if err := builder.ApplyPendingEvents(context.Background(), manifest.IndexPath, provider, 100); err != nil {
		t.Fatalf("apply pending events: %v", err)
	}

	index, err := bleve.Open(manifest.IndexPath)
	if err != nil {
		t.Fatalf("open bleve: %v", err)
	}
	defer index.Close()

	docCount, err := index.DocCount()
	if err != nil {
		t.Fatalf("doc count: %v", err)
	}
	if docCount != 2 {
		t.Fatalf("doc count after events = %d, want 2", docCount)
	}

	query := bleve.NewMatchQuery("Born.with.Luck.S01E01.mp4")
	query.SetField("name")
	req := bleve.NewSearchRequest(query)
	req.Fields = []string{"name", "path", "ext"}
	res, err := index.Search(req)
	if err != nil {
		t.Fatalf("search bleve: %v", err)
	}
	doc, err := index.Document("f2")
	if err != nil {
		t.Fatalf("load f2 document: %v", err)
	}
	if doc == nil {
		t.Fatal("expected f2 document to exist")
	}
	if res.Total != 1 {
		t.Fatalf("search total after events = %d, want 1", res.Total)
	}
	if len(provider.events) != 0 {
		t.Fatalf("pending events = %d, want 0", len(provider.events))
	}
}

type loopingEventProvider struct {
	eventProvider
	calls *atomic.Int32
}

func (l *loopingEventProvider) PendingIndexEvents(ctx context.Context, limit int) ([]model.IndexEvent, error) {
	l.calls.Add(1)
	return l.eventProvider.PendingIndexEvents(ctx, limit)
}

func TestRunEventLoopStopsWithContext(t *testing.T) {
	dir := t.TempDir()
	builder := New(filepath.Join(dir, "bleve"))
	provider := &loopingEventProvider{
		eventProvider: eventProvider{
			files: map[string]model.File{
				"f1": {
					FileID:    "f1",
					ShareCode: "swf01d43zby",
					Name:      "Avatar.2009.2160p.mkv",
					Path:      "/Avatar.2009.2160p.mkv",
					Ext:       "mkv",
				},
			},
		},
		calls: &atomic.Int32{},
	}
	manifest, err := builder.Rebuild(context.Background(), provider, 1, 1)
	if err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- builder.RunEventLoop(ctx, manifest.IndexPath, provider, 10, 10*time.Millisecond)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run event loop err: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("event loop did not stop on context cancel")
	}
	if provider.calls.Load() == 0 {
		t.Fatal("expected event loop to poll pending events at least once")
	}
}
