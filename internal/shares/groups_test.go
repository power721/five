package shares

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseGroupsAllIdentifierForms(t *testing.T) {
	input := strings.NewReader(`# 欧美剧
美剧【权力的游戏】	https://115.com/s/swaaaa11111?password=8888&#
swnsdrk3h2m?password=p783
https://115cdn.com/s/swbbbb22222?password=6666
sw68wz93ncb
`)
	groups, dups, err := ParseGroups(input)
	if err != nil {
		t.Fatalf("ParseGroups() error = %v", err)
	}
	if len(dups) != 0 {
		t.Fatalf("duplicates = %v, want none", dups)
	}
	if len(groups) != 1 || groups[0].Name != "欧美剧" {
		t.Fatalf("groups = %+v", groups)
	}
	want := []string{"swaaaa11111", "swnsdrk3h2m", "swbbbb22222", "sw68wz93ncb"}
	if !reflect.DeepEqual(groups[0].ShareCodes, want) {
		t.Fatalf("codes = %v, want %v", groups[0].ShareCodes, want)
	}
}

func TestParseGroupsMultipleGroupsKeepOrder(t *testing.T) {
	input := strings.NewReader(`# 欧美剧
sw1

# 纪录片
sw2
sw3
`)
	groups, _, err := ParseGroups(input)
	if err != nil {
		t.Fatalf("ParseGroups() error = %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if groups[0].Name != "欧美剧" || !reflect.DeepEqual(groups[0].ShareCodes, []string{"sw1"}) {
		t.Fatalf("group0 = %+v", groups[0])
	}
	if groups[1].Name != "纪录片" || !reflect.DeepEqual(groups[1].ShareCodes, []string{"sw2", "sw3"}) {
		t.Fatalf("group1 = %+v", groups[1])
	}
}

func TestParseGroupsDuplicateCodeLastWins(t *testing.T) {
	input := strings.NewReader(`# 欧美剧
sw1

# 纪录片
sw1
`)
	groups, dups, err := ParseGroups(input)
	if err != nil {
		t.Fatalf("ParseGroups() error = %v", err)
	}
	if !reflect.DeepEqual(dups, []string{"sw1"}) {
		t.Fatalf("duplicates = %v, want [sw1]", dups)
	}
	if len(groups[0].ShareCodes) != 0 {
		t.Fatalf("group0 codes = %v, want empty (moved)", groups[0].ShareCodes)
	}
	if !reflect.DeepEqual(groups[1].ShareCodes, []string{"sw1"}) {
		t.Fatalf("group1 codes = %v, want [sw1]", groups[1].ShareCodes)
	}
}

func TestParseGroupsShareBeforeHeaderErrors(t *testing.T) {
	_, _, err := ParseGroups(strings.NewReader("sw1\n"))
	if err == nil {
		t.Fatal("expected error for share before any group header")
	}
}

func TestParseGroupsEmptyGroupAllowed(t *testing.T) {
	groups, _, err := ParseGroups(strings.NewReader("# 空\n"))
	if err != nil {
		t.Fatalf("ParseGroups() error = %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "空" || len(groups[0].ShareCodes) != 0 {
		t.Fatalf("groups = %+v", groups)
	}
}

// Regression: the existing share-source parser is unchanged.
func TestParseSourceFileStillParses(t *testing.T) {
	out, err := Parse(strings.NewReader("/a sw1 0 code\n"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(out) != 1 || out[0].ShareCode != "sw1" {
		t.Fatalf("Parse() = %+v", out)
	}
}
