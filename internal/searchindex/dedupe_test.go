package searchindex

import (
	"fmt"
	"reflect"
	"testing"

	"five/internal/model"
)

func TestIsEpisodeFile(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	cases := []struct {
		label string
		f     model.File
		want  bool
	}{
		{"small no marker", model.File{Name: "movie.mkv", Size: 2 * gb}, true},
		{"small with marker", model.File{Name: "S01E09.mkv", Size: 2 * gb}, true},
		{"big no marker", model.File{Name: "Movie.2160p.mkv", Size: 40 * gb}, false},
		{"big S02E18", model.File{Name: "Series.S02E18.mkv", Size: 40 * gb}, true},
		{"big EP09", model.File{Name: "Show.EP09.mkv", Size: 40 * gb}, true},
		{"big 第18集", model.File{Name: "剧.第18集.mkv", Size: 40 * gb}, true},
		{"big bare E18", model.File{Name: "Show.E18.mkv", Size: 40 * gb}, true},
		{"big year 2018", model.File{Name: "Movie.2018.2160p.mkv", Size: 40 * gb}, false},
		{"big E2015 not episode", model.File{Name: "Something.E2015.mkv", Size: 40 * gb}, false},
	}
	for _, c := range cases {
		if got := isEpisodeFile(c.f); got != c.want {
			t.Errorf("%s: isEpisodeFile=%v, want %v", c.label, got, c.want)
		}
	}
}

func TestPlanDocsMovieMergesAcrossNames(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "9", ShareCode: "swz", Name: "Avatar.2009.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
		{FileID: "1", ShareCode: "swa", Name: "阿凡达.2009.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
	}
	got := planDocs(files)
	if len(got) != 1 {
		t.Fatalf("got %d docs, want 1 (movie merges across names)", len(got))
	}
	if got[0].docID != "swa-1" {
		t.Errorf("docID = %q, want swa-1 (lexicographically smallest)", got[0].docID)
	}
	want := []string{"Avatar.2009.2160p.mkv", "阿凡达.2009.2160p.mkv"}
	if !reflect.DeepEqual(got[0].names, want) {
		t.Errorf("names = %v, want %v (sorted)", got[0].names, want)
	}
}

func TestPlanDocsEpisodeBySizeKeepsNamesSeparate(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "1", ShareCode: "swa", Name: "Show - S01E09 - 第9集.mkv", SHA1: "AAA", Size: 2 * gb},
		{FileID: "2", ShareCode: "swb", Name: "Show.S01E09.mkv", SHA1: "AAA", Size: 2 * gb},
	}
	if got := planDocs(files); len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (episode keeps different names separate)", len(got))
	}
}

func TestPlanDocsEpisodeMarkerOverridesSize(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "1", ShareCode: "swa", Name: "Series.S02E18.2160p.mkv", SHA1: "AAA", Size: 40 * gb},
		{FileID: "2", ShareCode: "swb", Name: "Series - S02E18.mkv", SHA1: "AAA", Size: 40 * gb},
	}
	if got := planDocs(files); len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (episode marker overrides size>threshold)", len(got))
	}
}

func TestPlanDocsSameNameAndContentCollapses(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "9", ShareCode: "swz", Name: "回到未来3.mkv", SHA1: "AAA", Size: 13 * gb},
		{FileID: "1", ShareCode: "swa", Name: "回到未来3.mkv", SHA1: "AAA", Size: 13 * gb},
	}
	got := planDocs(files)
	if len(got) != 1 {
		t.Fatalf("got %d docs, want 1 (same name+content collapses)", len(got))
	}
	if got[0].docID != "swa-1" {
		t.Errorf("docID = %q, want swa-1", got[0].docID)
	}
}

func TestPlanDocsPassthroughNeverMerged(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "1", ShareCode: "swa", Name: "folder", IsDir: true},
		{FileID: "2", ShareCode: "swb", Name: "folder", IsDir: true},
		{FileID: "3", ShareCode: "swc", Name: "a.bin", SHA1: "", Size: 5 * gb},
		{FileID: "4", ShareCode: "swd", Name: "a.bin", SHA1: "", Size: 5 * gb},
		{FileID: "5", ShareCode: "swe", Name: "empty", SHA1: emptyStringHash, Size: 0},
		{FileID: "6", ShareCode: "swf", Name: "empty", SHA1: emptyStringHash, Size: 0},
	}
	if got := planDocs(files); len(got) != 6 {
		t.Fatalf("got %d docs, want 6 (dirs/unhashed/sentinel never deduped)", len(got))
	}
}

func TestPlanDocsDeterministicAcrossInputOrder(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	mk := func(share, fid, name string) model.File {
		return model.File{FileID: fid, ShareCode: share, Name: name, SHA1: "AAA", Size: 40 * gb}
	}
	a := planDocs([]model.File{mk("swz", "9", "Avatar.mkv"), mk("swa", "1", "阿凡达.mkv")})
	b := planDocs([]model.File{mk("swa", "1", "阿凡达.mkv"), mk("swz", "9", "Avatar.mkv")})
	if len(a) != 1 || len(b) != 1 || a[0].docID != b[0].docID || !reflect.DeepEqual(a[0].names, b[0].names) {
		t.Fatalf("not deterministic across input order: a=%v b=%v", a, b)
	}
}

func TestStem(t *testing.T) {
	cases := []struct {
		label string
		f     model.File
		want  string
	}{
		{"file strips ext", model.File{Name: "S01E18.mkv", Ext: "mkv"}, "S01E18"},
		{"file keeps dotpack", model.File{Name: "Show.S01E01.1080p.mkv", Ext: "mkv"}, "Show.S01E01.1080p"},
		{"file no ext unchanged", model.File{Name: "README"}, "README"},
		{"file empty ext unchanged", model.File{Name: "movie.mkv", Ext: ""}, "movie.mkv"},
		{"dir verbatim", model.File{Name: "2024合集", IsDir: true}, "2024合集"},
		{"dir with dot verbatim", model.File{Name: "v2.0", IsDir: true, Ext: "0"}, "v2.0"},
	}
	for _, c := range cases {
		if got := stem(c.f); got != c.want {
			t.Errorf("%s: stem=%q, want %q", c.label, got, c.want)
		}
	}
}

func TestIsContainer(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	mk := func(name string, size int64) model.File { return model.File{Name: name, Size: size} }
	// 5 marker episodes
	marker5 := []model.File{mk("S01E01.mkv", 2*gb), mk("S01E02.mkv", 2*gb), mk("S01E03.mkv", 2*gb), mk("S01E04.mkv", 2*gb), mk("S01E05.mkv", 2*gb)}
	// 5 small no-marker files (>=60% small)
	small5 := []model.File{mk("01.mkv", 1*gb), mk("02.mkv", 1*gb), mk("03.mkv", 1*gb), mk("04.mkv", 1*gb), mk("05.mkv", 1*gb)}
	// 4 markers (below floor)
	marker4 := marker5[:4]
	// 3 large movies (collection)
	large3 := []model.File{mk("A.2160p.mkv", 40*gb), mk("B.2160p.mkv", 40*gb), mk("C.2160p.mkv", 40*gb)}
	// mixed: 5 files but 3 large, 2 small (<60% small)
	mixed := []model.File{mk("x.mkv", 1*gb), mk("y.mkv", 1*gb), mk("a.2160p.mkv", 40*gb), mk("b.2160p.mkv", 40*gb), mk("c.2160p.mkv", 40*gb)}
	cases := []struct {
		label string
		kids  []model.File
		want  bool
	}{
		{"5 markers", marker5, true},
		{"5 small no markers", small5, true},
		{"4 markers below floor", marker4, false},
		{"empty", nil, false},
		{"3 large collection", large3, false},
		{"5 files but 3 large", mixed, false},
	}
	for _, c := range cases {
		if got := isContainer(c.kids); got != c.want {
			t.Errorf("%s: isContainer=%v, want %v", c.label, got, c.want)
		}
	}
}

func TestFolderNames(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	t.Run("folder_name_plus_distinct_child_stems_sorted", func(t *testing.T) {
		d := model.File{Name: "第一季", ShareCode: "sw1", FileID: "d1", IsDir: true}
		kids := []model.File{
			{FileID: "e3", ShareCode: "sw1", Name: "Show.S01E03.mkv", Ext: "mkv", Size: 2 * gb},
			{FileID: "e1", ShareCode: "sw1", Name: "Show.S01E01.mkv", Ext: "mkv", Size: 2 * gb},
			{FileID: "e1b", ShareCode: "sw1", Name: "Show.S01E01.avi", Ext: "avi", Size: 2 * gb}, // dup stem
		}
		got := folderNames(d, kids)
		want := []string{"Show.S01E01", "Show.S01E03", "第一季"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("folderNames=%v, want %v", got, want)
		}
	})
	t.Run("caps_at_maxFolderNames_keeps_folder_name", func(t *testing.T) {
		d := model.File{Name: "zzz_root", ShareCode: "sw1", FileID: "d1", IsDir: true}
		kids := make([]model.File, 300)
		for i := range kids {
			kids[i] = model.File{FileID: "e", ShareCode: "sw1", Name: "S01E01.mkv", Ext: "mkv"} // all same stem
		}
		// all identical -> 1 distinct child stem + folder name = 2 names
		got := folderNames(d, kids)
		if len(got) != 2 {
			t.Fatalf("deduped names = %d, want 2 (folder name + 1 distinct stem)", len(got))
		}
	})
	t.Run("many_distinct_stems_capped_folder_name_always_present", func(t *testing.T) {
		d := model.File{Name: "zfolder", ShareCode: "sw1", FileID: "d1", IsDir: true}
		kids := make([]model.File, 300)
		for i := range kids {
			kids[i] = model.File{ShareCode: "sw1", Name: "Show.S01E01.mkv", Ext: "mkv"}
			kids[i].Name = "Show.S01E" + string(rune('A'+i%26)) + fmt.Sprintf("%02d", i) + ".mkv"
		}
		got := folderNames(d, kids)
		if len(got) > maxFolderNames {
			t.Fatalf("names = %d, want ≤ %d", len(got), maxFolderNames)
		}
		found := false
		for _, n := range got {
			if n == "zfolder" {
				found = true
			}
		}
		if !found {
			t.Errorf("folder name %q not retained after cap", d.Name)
		}
	})
}

func TestPlanDocsRollsUpMarkerContainer(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "剧", IsDir: true},
		{FileID: "d2", ShareCode: "sw1", ParentID: "d1", Name: "第一季", IsDir: true},
		{FileID: "e1", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E01.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
		{FileID: "e2", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E02.mkv", Ext: "mkv", SHA1: "H2", Size: 2 * gb},
		{FileID: "e3", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E03.mkv", Ext: "mkv", SHA1: "H3", Size: 2 * gb},
		{FileID: "e4", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E04.mkv", Ext: "mkv", SHA1: "H4", Size: 2 * gb},
		{FileID: "e5", ShareCode: "sw1", ParentID: "d2", Name: "Show.S01E05.mkv", Ext: "mkv", SHA1: "H5", Size: 2 * gb},
	}
	got := planDocs(files)
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (root + season; episodes rolled into season)", len(got))
	}
	want := map[string][]string{
		"sw1-d1": {"剧"},
		"sw1-d2": {"Show.S01E01", "Show.S01E02", "Show.S01E03", "Show.S01E04", "Show.S01E05", "第一季"},
	}
	gotMap := map[string][]string{}
	for _, d := range got {
		gotMap[d.docID] = d.names
	}
	for id, names := range want {
		if !reflect.DeepEqual(gotMap[id], names) {
			t.Errorf("doc %s names = %v, want %v", id, gotMap[id], names)
		}
	}
}

func TestPlanDocsRollsUpMarkerLessContainer(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "剧", IsDir: true},
		{FileID: "c1", ShareCode: "sw1", ParentID: "d1", Name: "01.mkv", Ext: "mkv", SHA1: "H1", Size: 1 * gb},
		{FileID: "c2", ShareCode: "sw1", ParentID: "d1", Name: "02.mkv", Ext: "mkv", SHA1: "H2", Size: 1 * gb},
		{FileID: "c3", ShareCode: "sw1", ParentID: "d1", Name: "03.mkv", Ext: "mkv", SHA1: "H3", Size: 1 * gb},
		{FileID: "c4", ShareCode: "sw1", ParentID: "d1", Name: "04.mkv", Ext: "mkv", SHA1: "H4", Size: 1 * gb},
		{FileID: "c5", ShareCode: "sw1", ParentID: "d1", Name: "05.mkv", Ext: "mkv", SHA1: "H5", Size: 1 * gb},
	}
	got := planDocs(files)
	// d1 is a container (5 small files); c1..c5 suppressed.
	if len(got) != 1 {
		t.Fatalf("got %d docs, want 1 (folder absorbed 5 marker-less episodes)", len(got))
	}
	if got[0].docID != "sw1-d1" {
		t.Errorf("docID = %q, want sw1-d1", got[0].docID)
	}
	want := []string{"01", "02", "03", "04", "05", "剧"}
	if !reflect.DeepEqual(got[0].names, want) {
		t.Errorf("names = %v, want %v", got[0].names, want)
	}
}

func TestPlanDocsDoesNotRollUpCollection(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "合集", IsDir: true},
		{FileID: "m1", ShareCode: "sw1", ParentID: "d1", Name: "A.2160p.mkv", Ext: "mkv", SHA1: "H1", Size: 40 * gb},
		{FileID: "m2", ShareCode: "sw1", ParentID: "d1", Name: "B.2160p.mkv", Ext: "mkv", SHA1: "H2", Size: 40 * gb},
		{FileID: "m3", ShareCode: "sw1", ParentID: "d1", Name: "C.2160p.mkv", Ext: "mkv", SHA1: "H3", Size: 40 * gb},
	}
	got := planDocs(files)
	// d1 not a container (3 large, <5 files) -> passthrough dir + 3 movie docs
	if len(got) != 4 {
		t.Fatalf("got %d docs, want 4 (folder passthrough + 3 movies, not rolled up)", len(got))
	}
}

func TestPlanDocsDoesNotRollUpSingleMovie(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "阿凡达3", IsDir: true},
		{FileID: "f1", ShareCode: "sw1", ParentID: "d1", Name: "阿凡达3.2025.mkv", Ext: "mkv", SHA1: "H1", Size: 40 * gb},
	}
	got := planDocs(files)
	// single movie: dir passthrough + 1 movie doc (NOT rolled up)
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (folder passthrough + movie file)", len(got))
	}
}

func TestPlanDocsSuppressesFilesExcludedFromDedup(t *testing.T) {
	gb := int64(1024 * 1024 * 1024)
	// The same content (sha1 H1) appears as a container child (suppressed) and as
	// a loose root-level episode. The loose copy is still indexed; the suppressed
	// copy is NOT double-emitted.
	files := []model.File{
		{FileID: "d1", ShareCode: "sw1", ParentID: "0", Name: "剧", IsDir: true},
		{FileID: "e1", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E01.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
		{FileID: "e2", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E02.mkv", Ext: "mkv", SHA1: "H2", Size: 2 * gb},
		{FileID: "e3", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E03.mkv", Ext: "mkv", SHA1: "H3", Size: 2 * gb},
		{FileID: "e4", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E04.mkv", Ext: "mkv", SHA1: "H4", Size: 2 * gb},
		{FileID: "e5", ShareCode: "sw1", ParentID: "d1", Name: "Show.S01E05.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
		{FileID: "loose", ShareCode: "sw1", ParentID: "0", Name: "Show.S01E01.mkv", Ext: "mkv", SHA1: "H1", Size: 2 * gb},
	}
	got := planDocs(files)
	// Expect exactly: 1 container folder doc (d1) + 1 loose episode doc.
	if len(got) != 2 {
		t.Fatalf("got %d docs, want 2 (container folder + loose copy; suppressed copy not double-emitted)", len(got))
	}
}
