package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPackageZipLayout(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	if err := os.WriteFile(dbPath, []byte("DB"), 0o644); err != nil {
		t.Fatal(err)
	}
	bleveDir := filepath.Join(dir, "bleve")
	os.MkdirAll(filepath.Join(bleveDir, "store"), 0o755)
	os.WriteFile(filepath.Join(bleveDir, "index_meta.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(bleveDir, "store", "seg"), []byte("seg"), 0o644)

	zipPath := filepath.Join(dir, "out", "115.index.zip")
	if err := buildPackage(dbPath, bleveDir, zipPath); err != nil {
		t.Fatalf("buildPackage: %v", err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	want := map[string]bool{"index.db": false, "bleve/index_meta.json": false, "bleve/store/seg": false}
	for _, f := range zr.File {
		if _, ok := want[f.Name]; !ok {
			t.Errorf("unexpected entry %q", f.Name)
			continue
		}
		want[f.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing entry %q", name)
		}
	}
}

func TestNewestBleveIndex(t *testing.T) {
	root := t.TempDir()
	if got := newestBleveIndex(root); got != "" {
		t.Fatalf("empty root = %q, want \"\"", got)
	}
	os.MkdirAll(filepath.Join(root, "index_000001_building"), 0o755)
	os.MkdirAll(filepath.Join(root, "index_000007"), 0o755)
	os.MkdirAll(filepath.Join(root, "index_000042"), 0o755)
	got := newestBleveIndex(root)
	want := filepath.Join(root, "index_000042")
	if got != want {
		t.Fatalf("newest = %q, want %q", got, want)
	}
}
