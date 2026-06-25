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

// maxFolderNames caps how many distinct names a rolled-up folder doc carries
// (the folder's own name plus child stems). Bounds giant folders — the corpus
// has episode containers up to ~18k files. The folder's own name is always kept.
const maxFolderNames = 256

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

// isContainer reports whether a folder should roll its direct child files up
// into its own search doc — i.e. it is an episode container. It needs ≥5
// episode-like children: ≥5 marker children (S01E01/EP09/第N集), or ≥5 files
// that are ≥60% small (0 < size ≤ movieSizeThreshold) with <2 large. Small 2–4
// episode folders don't flood search, so they stay as individual files. kids are
// the folder's DIRECT child files only; subfolders are classified independently.
// 0.6 is compared as small*5 >= files*3 to avoid floating point.
func isContainer(kids []model.File) bool {
	var markers, files, small, large int
	for _, k := range kids {
		files++
		if episodeMarker.MatchString(k.Name) {
			markers++
		}
		if k.Size > movieSizeThreshold {
			large++
		} else if k.Size > 0 {
			small++
		}
	}
	return markers >= 5 || (files >= 5 && small*5 >= files*3 && large < 2)
}

// folderNames returns the distinct names to index for a rolled-up folder: the
// folder's own name (always kept, verbatim) plus its direct child stems, sorted
// and capped at maxFolderNames total. Child stems equal to the folder name are
// deduped; when capped, the smallest child stems by sort are kept alongside the
// folder name.
func folderNames(d model.File, kids []model.File) []string {
	seen := map[string]struct{}{d.Name: {}}
	var childStems []string
	for _, k := range kids {
		s := stem(k)
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			childStems = append(childStems, s)
		}
	}
	sort.Strings(childStems)
	names := make([]string, 0, len(childStems)+1)
	for _, s := range childStems {
		if len(names) >= maxFolderNames-1 {
			break
		}
		names = append(names, s)
	}
	names = append(names, d.Name)
	sort.Strings(names)
	return names
}

// planDocs groups files into the bleve documents Rebuild should index.
//
// Folder rollup: a folder classified as an episode container (isContainer) gets
// one doc carrying its own name plus its direct child file stems (folderNames),
// and its direct child files are suppressed — so a series search returns the
// folder, not dozens of episodes. Non-container folders are passthrough (name
// only). Remaining (non-suppressed) real-hash files are content-deduped as
// before: episodes (isEpisodeFile) key on (name, sha1, size) so each distinct
// filename is its own doc; movies key on (sha1, size) so differently-named copies
// merge into one doc carrying every name. Within a group the representative is
// the lexicographically smallest (share_code, file_id) row; its composite id is
// the doc id. Unhashed files and the empty-string-hash sentinel are passthrough.
// Every indexed name uses the file stem (stem). Output is deterministic for a
// given input.
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

	// Partition rows into dirs and files; index each file under its parent folder
	// (parent_id is share-scoped) so dirs can be classified as containers.
	var dirs []model.File
	childrenOf := map[[2]string][]model.File{} // (share_code, parent_id) -> direct child files
	for _, f := range files {
		if f.IsDir {
			dirs = append(dirs, f)
			continue
		}
		key := [2]string{f.ShareCode, f.ParentID}
		childrenOf[key] = append(childrenOf[key], f)
	}

	out := make([]indexedDoc, 0, len(files))

	// Folder docs first. Containers absorb their direct child file stems and
	// suppress those files; non-containers are passthrough (stem of a dir is its
	// verbatim name).
	suppressed := map[[2]string]bool{} // (share_code, file_id) rolled into a folder
	for _, d := range dirs {
		kids := childrenOf[[2]string{d.ShareCode, d.FileID}]
		if isContainer(kids) {
			out = append(out, indexedDoc{
				docID: docID(d.ShareCode, d.FileID),
				names: folderNames(d, kids),
			})
			for _, k := range kids {
				suppressed[[2]string{k.ShareCode, k.FileID}] = true
			}
		} else {
			out = append(out, indexedDoc{
				docID: docID(d.ShareCode, d.FileID),
				names: []string{stem(d)},
			})
		}
	}

	// File docs: existing content-dedup over non-suppressed real-hash files;
	// unhashed / sentinel rows are passthrough. Indexed names use the stem.
	groups := map[groupKey]*group{}
	var order []groupKey
	for _, f := range files {
		if f.IsDir {
			continue
		}
		if suppressed[[2]string{f.ShareCode, f.FileID}] {
			continue
		}
		if f.SHA1 == "" || f.SHA1 == emptyStringHash {
			out = append(out, indexedDoc{
				docID: docID(f.ShareCode, f.FileID),
				names: []string{stem(f)},
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
		g.names[stem(f)] = struct{}{}
		if f.ShareCode < g.rep.ShareCode || (f.ShareCode == g.rep.ShareCode && f.FileID < g.rep.FileID) {
			g.rep = f
		}
	}
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
