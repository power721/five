package searchindex

import (
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
