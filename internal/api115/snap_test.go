package api115

import (
	"encoding/json"
	"testing"
)

const sampleSnap = `{
  "state": true,
  "error": "",
  "errno": 0,
  "data": {
    "shareinfo": {
      "share_state": 1,
      "receive_code": "echo"
    },
    "count": 24,
    "list": [
      {
        "fid": "3427894426982797760",
        "cid": 3427894426395595300,
        "n": "Born with Luck.S01E24.2160p.DV.H.265.DDP 5.1.mp4",
        "s": 4190151605,
        "d": 0,
        "ico": "mp4",
        "sha": "D1EE1E6D4E5F4CEB793EB5E0C73DA7EF4C3C3E3E"
      },
      {
        "fid": "",
        "cid": 3427894426395595401,
        "n": "Season 01",
        "s": 0,
        "d": 1,
        "ico": "folder",
        "sha": ""
      }
    ],
    "share_state": 1
  }
}`

func TestSnapResponsePreservesIDsAndMapsNodes(t *testing.T) {
	var resp SnapResponse
	if err := json.Unmarshal([]byte(sampleSnap), &resp); err != nil {
		t.Fatalf("unmarshal snap: %v", err)
	}

	if !resp.State {
		t.Fatal("expected response state to be true")
	}
	if resp.Data.Count != 24 {
		t.Fatalf("count = %d, want 24", resp.Data.Count)
	}

	file := resp.Data.List[0].ToFile("swf01d43zby", "3427894426395595175", "/Born with Luck.S01E24.2160p.DV.H.265.DDP 5.1.mp4", 1, 123)
	if file.FileID != "3427894426982797760" {
		t.Fatalf("file id = %q", file.FileID)
	}
	if file.ParentID != "3427894426395595175" {
		t.Fatalf("parent id = %q", file.ParentID)
	}
	if file.Name != "Born with Luck.S01E24.2160p.DV.H.265.DDP 5.1.mp4" {
		t.Fatalf("name = %q", file.Name)
	}
	if file.Ext != "mp4" {
		t.Fatalf("ext = %q", file.Ext)
	}
	if file.Size != 4190151605 {
		t.Fatalf("size = %d", file.Size)
	}
	if file.IsDir {
		t.Fatal("file should not be a directory")
	}

	dir := resp.Data.List[1].ToFile("swf01d43zby", "3427894426395595175", "/Season 01", 1, 123)
	if dir.FileID != "3427894426395595401" {
		t.Fatalf("directory id = %q", dir.FileID)
	}
	if !dir.IsDir {
		t.Fatal("directory should be marked as dir")
	}
}

func TestSnapResponseInvalidShareState(t *testing.T) {
	resp := SnapResponse{State: true}
	resp.Data.ShareInfo.ShareState = 0

	if resp.ValidShare() {
		t.Fatal("share_state 0 should not be valid")
	}
}
