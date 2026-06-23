package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// buildPackage writes zipDestPath containing index.db (from trimmedDBPath), the
// contents of bleveSrcDir under "bleve/", and a version.txt holding the export
// timestamp (format 20060102-150405). Flat layout: extracts to
// <dir>/index.db + <dir>/bleve/ + <dir>/version.txt.
func buildPackage(trimmedDBPath, bleveSrcDir, zipDestPath string) error {
	if dir := filepath.Dir(zipDestPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	out, err := os.Create(zipDestPath)
	if err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()

	if err := addFileToZip(zw, trimmedDBPath, "index.db"); err != nil {
		return fmt.Errorf("add index.db: %w", err)
	}
	err = filepath.Walk(bleveSrcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(bleveSrcDir, path)
		if err != nil {
			return err
		}
		return addFileToZip(zw, path, filepath.ToSlash(filepath.Join("bleve", rel)))
	})
	if err != nil {
		return fmt.Errorf("walk bleve: %w", err)
	}
	if err := addVersionToZip(zw, time.Now()); err != nil {
		return err
	}
	return nil
}

// addVersionToZip writes version.txt containing the export timestamp, formatted
// as YYYYMMDD-HHMMSS (e.g. 20260623-084025).
func addVersionToZip(zw *zip.Writer, now time.Time) error {
	w, err := zw.Create("version.txt")
	if err != nil {
		return fmt.Errorf("add version.txt: %w", err)
	}
	if _, err := w.Write([]byte(now.Format("20060102-150405"))); err != nil {
		return fmt.Errorf("write version.txt: %w", err)
	}
	return nil
}

func addFileToZip(zw *zip.Writer, src, name string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// newestBleveIndex returns the newest non-building "index_%06d" dir under root,
// or "" if none.
func newestBleveIndex(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var newest string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "index_") || strings.HasSuffix(e.Name(), "_building") {
			continue
		}
		if newest == "" || e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return ""
	}
	return filepath.Join(root, newest)
}
