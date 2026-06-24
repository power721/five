package main

import (
	"bytes"
	"context"
	"testing"

	"five/internal/model"
)

type fakeDedupeStore struct {
	renames []model.ShareRename
	dryRun  bool
}

func (f *fakeDedupeStore) DedupeShareTitles(_ context.Context, dryRun bool) ([]model.ShareRename, error) {
	f.dryRun = dryRun
	return f.renames, nil
}

func TestRunDedupeShareTitles(t *testing.T) {
	t.Run("dry run prints plan without applying", func(t *testing.T) {
		store := &fakeDedupeStore{renames: []model.ShareRename{
			{ShareCode: "sw1", From: "原盘精选", To: "原盘精选1"},
		}}
		var out bytes.Buffer
		if err := runDedupeShareTitles(context.Background(), store, false, &out); err != nil {
			t.Fatalf("run: %v", err)
		}
		if store.dryRun != true {
			t.Fatal("expected dryRun=true for non-apply run")
		}
		got := out.String()
		if !bytes.Contains([]byte(got), []byte(`share sw1: "原盘精选" -> "原盘精选1"`)) {
			t.Fatalf("missing rename line: %q", got)
		}
		if !bytes.Contains([]byte(got), []byte("would rename 1 shares; re-run with -apply to commit")) {
			t.Fatalf("missing dry-run summary: %q", got)
		}
	})

	t.Run("apply prints renamed summary", func(t *testing.T) {
		store := &fakeDedupeStore{renames: []model.ShareRename{
			{ShareCode: "sw1", From: "X", To: "X1"},
		}}
		var out bytes.Buffer
		if err := runDedupeShareTitles(context.Background(), store, true, &out); err != nil {
			t.Fatalf("run: %v", err)
		}
		if store.dryRun != false {
			t.Fatal("expected dryRun=false for apply run")
		}
		if !bytes.Contains([]byte(out.String()), []byte("renamed 1 shares")) {
			t.Fatalf("missing apply summary: %q", out.String())
		}
	})

	t.Run("no duplicates prints nothing-found", func(t *testing.T) {
		store := &fakeDedupeStore{}
		var out bytes.Buffer
		if err := runDedupeShareTitles(context.Background(), store, false, &out); err != nil {
			t.Fatalf("run: %v", err)
		}
		if !bytes.Contains([]byte(out.String()), []byte("no duplicate titles found")) {
			t.Fatalf("missing empty summary: %q", out.String())
		}
	})
}
