package store

import (
	"testing"

	"five/internal/model"
)

func TestPlanShareRenames(t *testing.T) {
	t.Run("no duplicates yields no renames", func(t *testing.T) {
		got := planShareRenames([]model.Share{
			{ShareCode: "a", ShareTitle: "X"},
			{ShareCode: "b", ShareTitle: "Y"},
		})
		if len(got) != 0 {
			t.Fatalf("got %v, want no renames", got)
		}
	})

	t.Run("lowest id keeps bare title, rest get numeric suffix", func(t *testing.T) {
		// Input order is id-ASC (as ListShares returns).
		got := planShareRenames([]model.Share{
			{ShareCode: "id1", ShareTitle: "原盘精选"},
			{ShareCode: "id2", ShareTitle: "原盘精选"},
			{ShareCode: "id3", ShareTitle: "原盘精选"},
		})
		want := []model.ShareRename{
			{ShareCode: "id2", From: "原盘精选", To: "原盘精选1"},
			{ShareCode: "id3", From: "原盘精选", To: "原盘精选2"},
		}
		assertRenames(t, got, want)
	})

	t.Run("suffix skips titles already used by other shares (global uniqueness)", func(t *testing.T) {
		// "原盘精选1" is already a real title on a different share, so id2
		// cannot reuse it and must take "原盘精选2".
		got := planShareRenames([]model.Share{
			{ShareCode: "id1", ShareTitle: "原盘精选"},
			{ShareCode: "id2", ShareTitle: "原盘精选"},
			{ShareCode: "id3", ShareTitle: "原盘精选1"},
		})
		want := []model.ShareRename{
			{ShareCode: "id2", From: "原盘精选", To: "原盘精选2"},
		}
		assertRenames(t, got, want)
	})

	t.Run("whitespace trimmed before grouping", func(t *testing.T) {
		got := planShareRenames([]model.Share{
			{ShareCode: "id1", ShareTitle: "  X "},
			{ShareCode: "id2", ShareTitle: "X"},
		})
		want := []model.ShareRename{
			{ShareCode: "id2", From: "X", To: "X1"},
		}
		assertRenames(t, got, want)
	})

	t.Run("empty titles are skipped", func(t *testing.T) {
		got := planShareRenames([]model.Share{
			{ShareCode: "id1", ShareTitle: ""},
			{ShareCode: "id2", ShareTitle: "   "},
		})
		if len(got) != 0 {
			t.Fatalf("got %v, want no renames for empty titles", got)
		}
	})
}

func assertRenames(t *testing.T, got, want []model.ShareRename) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("renames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("renames[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
