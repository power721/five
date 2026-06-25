package searchindex

import (
	"regexp"
	"sort"
	"strings"

	"five/internal/model"
)

// emptyStringHash is the SHA-1 of the empty string. 115 sometimes returns it as
// a placeholder for files it could not hash. Rows carrying it are never
// content-deduped — they only ever merge junk — so each is indexed on its own
// like an unhashed file.
const emptyStringHash = "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709"

// movieSizeThreshold is the size above which a file with no episode marker is
// treated as a movie / single-play unit (4K movies, concert ISOs): its
// differently-named copies merge into one search result. Files at or below this
// size are episodes (each filename kept separate) unless their name matches
// episodeMarker. The corpus is bimodal — ~54% of same-content groups are <=4GB
// (episodes), ~41% >15GB (concerts/movies) — so the value in the 4–15GB valley
// rarely matters; 10 GiB is a conservative split.
const movieSizeThreshold int64 = 10 * 1024 * 1024 * 1024

// episodeMarker matches filename patterns that unambiguously mark a TV episode.
// A match means the file is an episode regardless of size, so a large 4K episode
// is not merged as a movie. Bare "E" + digits uses a word boundary and 2–3
// digits to avoid matching years such as "E2015".
var episodeMarker = regexp.MustCompile(`(?i)(S\d{1,2}E\d{1,3})|(EP\d{1,3})|(第\d{1,3}[集话話])|(\bE\d{2,3}\b)`)

// stem returns the name to index for a row: the filename minus its extension
// (S01E18.mkv -> S01E18), so episode codes match cleanly and a search for an
// extension does not surface every file of that format. Directory names are
// returned verbatim (a folder like "2024合集" has no meaningful extension).
// Ext is extracted by the crawler and stored on the row; when absent the full
// name is indexed unchanged.
func stem(f model.File) string {
	if !f.IsDir && f.Ext != "" {
		return strings.TrimSuffix(f.Name, "."+f.Ext)
	}
	return f.Name
}

// indexedDoc is one bleve document Rebuild should emit.
type indexedDoc struct {
	docID string   // composite "shareCode-fileId" of the representative row
	names []string // distinct names to index for matching
}

// isEpisodeFile reports whether f should be deduped per-name (episode) rather
// than per-content (movie). Dirs and unhashed/sentinel rows are never deduped
// (handled by planDocs), so this only classifies real-hash files.
func isEpisodeFile(f model.File) bool {
	return episodeMarker.MatchString(f.Name) || f.Size <= movieSizeThreshold
}

// planDocs groups files into the bleve documents Rebuild should index.
//
// Real-hash files are grouped into episodes or movies. Episodes (isEpisodeFile)
// key on (name, sha1, size) so each distinct filename is its own doc. Movies key
// on (sha1, size) so differently-named copies merge into a single doc carrying
// every name (recall). Within a group the representative is the
// lexicographically smallest (share_code, file_id) row; its composite id is the
// doc id. Directories, unhashed files, and the empty-string-hash sentinel are
// passthrough — one doc each. Output is deterministic for a given input:
// passthrough rows in input order, then groups in first-seen order, names sorted.
func planDocs(files []model.File) []indexedDoc {
	type groupKey struct {
		name string // "" for movies (merge across names)
		sha1 string
		size int64
	}
	type group struct {
		rep   model.File
		names map[string]struct{}
	}
	groups := map[groupKey]*group{}
	var order []groupKey
	passthrough := make([]indexedDoc, 0)

	for _, f := range files {
		if f.IsDir || f.SHA1 == "" || f.SHA1 == emptyStringHash {
			passthrough = append(passthrough, indexedDoc{
				docID: docID(f.ShareCode, f.FileID),
				names: []string{f.Name},
			})
			continue
		}
		key := groupKey{sha1: f.SHA1, size: f.Size}
		if isEpisodeFile(f) {
			key.name = f.Name
		}
		g, ok := groups[key]
		if !ok {
			g = &group{rep: f, names: map[string]struct{}{}}
			groups[key] = g
			order = append(order, key)
		}
		g.names[f.Name] = struct{}{}
		if f.ShareCode < g.rep.ShareCode || (f.ShareCode == g.rep.ShareCode && f.FileID < g.rep.FileID) {
			g.rep = f
		}
	}

	out := make([]indexedDoc, 0, len(passthrough)+len(order))
	out = append(out, passthrough...)
	for _, key := range order {
		g := groups[key]
		names := make([]string, 0, len(g.names))
		for n := range g.names {
			names = append(names, n)
		}
		sort.Strings(names)
		out = append(out, indexedDoc{
			docID: docID(g.rep.ShareCode, g.rep.FileID),
			names: names,
		})
	}
	return out
}
