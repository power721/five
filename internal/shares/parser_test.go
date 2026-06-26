package shares

import (
	"strings"
	"testing"
)

func TestParseFileIgnoresMountPathAndFolderID(t *testing.T) {
	input := strings.NewReader(`# mount_path share_id folder_id code
/电影/SGNB特效_563部  sw313rp3zx1  1941670173914479227  w146
/纪录片  sw68md23w8m  root  q353
`)

	out, err := Parse(input)
	if err != nil {
		t.Fatalf("parse shares: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("shares = %d, want 2", len(out))
	}
	if out[0].ShareCode != "sw313rp3zx1" || out[0].ReceiveCode != "w146" {
		t.Fatalf("first share = %#v", out[0])
	}
}

func TestParseDeduplicatesShareAndCode(t *testing.T) {
	input := strings.NewReader(`/a sw1 0 code
/b sw1 123 code
`)

	out, err := Parse(input)
	if err != nil {
		t.Fatalf("parse shares: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("shares = %d, want 1", len(out))
	}
}

func TestParseShareURL(t *testing.T) {
	share, err := ParseURL("https://115.com/s/swf01d43zby?password=echo")
	if err != nil {
		t.Fatalf("parse share url: %v", err)
	}
	if share.ShareCode != "swf01d43zby" {
		t.Fatalf("share code = %q", share.ShareCode)
	}
	if share.ReceiveCode != "echo" {
		t.Fatalf("receive code = %q", share.ReceiveCode)
	}
	if share.Status != "ACTIVE" {
		t.Fatalf("status = %q", share.Status)
	}
}

func TestParseShareURLAllowsMissingPassword(t *testing.T) {
	share, err := ParseURL("https://115.com/s/swf01d43zby")
	if err != nil {
		t.Fatalf("parse share url without password: %v", err)
	}
	if share.ShareCode != "swf01d43zby" {
		t.Fatalf("share code = %q", share.ShareCode)
	}
	if share.ReceiveCode != "" {
		t.Fatalf("receive code = %q, want empty", share.ReceiveCode)
	}
}

func TestParseTokenFormatWithPasswordAndName(t *testing.T) {
	input := strings.NewReader("swznmd03nc7?password=p897 原盘|动漫原盘_40.49T\n")

	out, err := Parse(input)
	if err != nil {
		t.Fatalf("parse token shares: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("shares = %d, want 1", len(out))
	}
	if out[0].ShareCode != "swznmd03nc7" || out[0].ReceiveCode != "p897" {
		t.Fatalf("token share = %#v", out[0])
	}
	if out[0].Status != "ACTIVE" {
		t.Fatalf("status = %q", out[0].Status)
	}
}

func TestShareURLWithPassword(t *testing.T) {
	got := ShareURL("swf01d43zby", "echo")
	want := "https://115.com/s/swf01d43zby?password=echo"
	if got != want {
		t.Fatalf("ShareURL = %q, want %q", got, want)
	}
}

func TestShareURLWithoutPassword(t *testing.T) {
	got := ShareURL("swf01d43zby", "")
	want := "https://115.com/s/swf01d43zby"
	if got != want {
		t.Fatalf("ShareURL = %q, want %q", got, want)
	}
}

func TestParseAcceptsTitleAndShareURLColumn(t *testing.T) {
	// Format used by shares.txt / movies.txt: "<title>\t<url>". The title may
	// contain spaces, and the URL may carry a trailing "#" / "&#" fragment.
	input := strings.NewReader("美剧【权力的游戏】1-8季全 4K中字杜比视界【1.85T】\thttps://115.com/s/sw6uoem3fwo?password=8888#\n")

	out, err := Parse(input)
	if err != nil {
		t.Fatalf("parse title+url share: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("shares = %d, want 1", len(out))
	}
	if out[0].ShareCode != "sw6uoem3fwo" || out[0].ReceiveCode != "8888" {
		t.Fatalf("share = %#v", out[0])
	}
	if out[0].Status != "ACTIVE" {
		t.Fatalf("status = %q", out[0].Status)
	}
}

func TestParseAcceptsTitleAndShareURLColumnAlternateHost(t *testing.T) {
	// 115 also serves shares from 115cdn.com; ParseURL is host-agnostic, so the
	// line parser must recognize alternate hosts, not just 115.com.
	input := strings.NewReader("美剧【黑客军团】11季 1080P【1.27TB】\thttps://115cdn.com/s/swztlnh33xj?password=f3h5\n")

	out, err := Parse(input)
	if err != nil {
		t.Fatalf("parse title+url share: %v", err)
	}
	if len(out) != 1 || out[0].ShareCode != "swztlnh33xj" || out[0].ReceiveCode != "f3h5" {
		t.Fatalf("shares = %#v", out)
	}
}

func TestParseAcceptsShareURLsInFile(t *testing.T) {
	input := strings.NewReader(`# either 4-column format or share URL
https://115.com/s/swf01d43zby?password=echo
/电影 sw313rp3zx1 0 w146
`)

	out, err := Parse(input)
	if err != nil {
		t.Fatalf("parse mixed shares: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("shares = %d, want 2", len(out))
	}
	if out[0].ShareCode != "swf01d43zby" || out[0].ReceiveCode != "echo" {
		t.Fatalf("url share = %#v", out[0])
	}
}
