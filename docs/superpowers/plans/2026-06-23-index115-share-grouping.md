# index115 Share Grouping (Virtual Directories) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Group indexed 115 shares under virtual directories on the consumer homepage (e.g. enter `韩剧` to see its 600 shares), driven by a `115_groups.txt` overlay.

**Architecture:** Indexer (`five`) parses `115_groups.txt` (independent of `115_shares.txt`) and bakes `share.group_id` + a `share_group` table into the trimmed `index.db`. Consumer (`PowerList`) renders groups as synthetic root `FileItem`s with a colon-free sentinel `share_code` (`grp<N>`); all three consumers (storage driver, JSON API, WebDAV) thread `share_code` straight through to `service.Browse`, so no SPA/driver/handler change is needed — only `Browse`, the store, and a one-line WebDAV path-walk fix.

**Tech Stack:** Go (modules `five` and `github.com/OpenListTeam/OpenList/v4`), SQLite, table-driven tests.

**Spec:** `docs/superpowers/specs/2026-06-23-index115-share-grouping-design.md`

**Two repos — note the working directory per task:**
- Indexer: `/home/user/workspace/five`
- Consumer: `/home/user/GolandProjects/PowerList`

---

## File Structure

### Indexer (`five`)
- **Create** `internal/shares/groups.go` — `ParseGroups` + `parseShareCode` (overlay parser; `shares.Parse` untouched).
- **Create** `internal/shares/groups_test.go` — parser tests.
- **Modify** `internal/model/model.go` — add `ShareGroup` struct.
- **Modify** `internal/store/sqlite.go` — migrate (`share_group` + `share.group_id`), new `ApplyGroups`.
- **Modify** `internal/store/sqlite_test.go` — `ApplyGroups` + export-retention tests.
- **Modify** `cmd/115-indexer/main.go` — `-groups-file` flag + `import-groups` mode.

### Consumer (`PowerList`)
- **Modify** `internal/index115/model.go` — `ShareSummary.GroupID` + `GroupInfo`.
- **Modify** `internal/index115/store.go` — `shareMeta.GroupID`, `Store.groups`, `RefreshShares`/`ListShares` read group, `ListGroups`.
- **Modify** `internal/index115/store_test.go` — schema gains `group_id`/`share_group`; group tests.
- **Modify** `internal/index115/service.go` — sentinel handling in `Browse` + helpers.
- **Modify** `internal/index115/service_test.go` — `stubStore.ListGroups` + group browse tests.
- **Modify** `server/index115_webdav.go` — one-line cross-share fix in `resolve`.
- **Create** `server/index115_webdav_test.go` — regression test for the fix.

---

## Task 1 (indexer): Grouping overlay parser

**Repo:** `/home/user/workspace/five`

**Files:**
- Create: `internal/shares/groups.go`
- Create: `internal/shares/groups_test.go`

- [ ] **Step 1: Write the failing parser tests**

Create `internal/shares/groups_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/workspace/five && go test ./internal/shares/ -run TestParseGroups -v`
Expected: FAIL — `undefined: ParseGroups` (and `model.ShareGroup`).

- [ ] **Step 3: Add the `ShareGroup` struct to the model**

In `internal/model/model.go`, add (near the existing `Share` type):

```go
// ShareGroup is one virtual directory from the grouping overlay
// (internal/shares groups.go). ShareCodes holds the member share codes in
// file order; the store assigns group_id by slice order.
type ShareGroup struct {
	Name       string
	ShareCodes []string
}
```

- [ ] **Step 4: Implement the parser**

Create `internal/shares/groups.go`:

```go
package shares

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"five/internal/model"
)

// parseShareCode extracts the 115 share code from a single identifier in any of
// these forms: a bare code (sw...), a "code?password=..." token, or an
// http(s)://host/s/<code> URL (ParseURL is host-agnostic, so 115.com and
// 115cdn.com both work). Only the code is returned.
func parseShareCode(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty share identifier")
	}
	switch {
	case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"):
		share, err := ParseURL(s)
		if err != nil {
			return "", err
		}
		return share.ShareCode, nil
	case strings.Contains(s, "?"):
		i := strings.Index(s, "?")
		return strings.TrimSpace(s[:i]), nil
	default:
		return s, nil
	}
}

// ParseGroups reads the grouping overlay. Each "# name" line starts a group (in
// file order, which becomes sort order). Every other non-blank line is a share
// identifier, optionally preceded by a title column — the identifier is the
// last whitespace-separated field. A share code that appears under more than one
// group is assigned to the LAST group (last wins), removed from the earlier
// group, and returned in duplicates for the caller to warn about.
func ParseGroups(r io.Reader) (groups []model.ShareGroup, duplicates []string, err error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	owner := map[string]int{} // share code -> index into groups
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			groups = append(groups, model.ShareGroup{Name: name})
			continue
		}
		if len(groups) == 0 {
			return nil, nil, fmt.Errorf("line %d: share appears before any group header", lineNo)
		}
		fields := strings.Fields(line)
		code, perr := parseShareCode(fields[len(fields)-1])
		if perr != nil {
			return nil, nil, fmt.Errorf("line %d: %w", lineNo, perr)
		}
		idx := len(groups) - 1
		if prev, ok := owner[code]; ok {
			if prev == idx {
				continue // exact duplicate within the same group
			}
			groups[prev].ShareCodes = removeString(groups[prev].ShareCodes, code)
			duplicates = append(duplicates, code)
		}
		groups[idx].ShareCodes = append(groups[idx].ShareCodes, code)
		owner[code] = idx
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return groups, duplicates, nil
}

func removeString(slice []string, s string) []string {
	for i, v := range slice {
		if v == s {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/user/workspace/five && go test ./internal/shares/ -v`
Expected: PASS (all parser tests, old and new).

- [ ] **Step 6: Commit**

```bash
cd /home/user/workspace/five
git add internal/shares/groups.go internal/shares/groups_test.go internal/model/model.go
git commit -m "feat(indexer): add 115_groups.txt overlay parser (ParseGroups)"
```

---

## Task 2 (indexer): Schema + ApplyGroups store method

**Repo:** `/home/user/workspace/five`

**Files:**
- Modify: `internal/store/sqlite.go` (migrate ~line 147; new method after `UpsertShare` ~line 628)
- Modify: `internal/store/sqlite_test.go`

- [ ] **Step 1: Write the failing store tests**

Append to `internal/store/sqlite_test.go` (inside `package store`, alongside the existing `CREATE TABLE share (...)` test fixture at ~line 619 — reuse that helper if one exists; otherwise use the public `Open` + `ApplyGroups`):

```go
func TestApplyGroupsAssignsMembershipAndDormant(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t) // helper that opens a real Store via store.Open + migrate
	defer cleanup()

	// Seed two shares.
	for _, sc := range []string{"sw1", "sw2"} {
		if err := s.UpsertShare(ctx, model.Share{ShareCode: sc, ReceiveCode: "r", Status: "ACTIVE"}); err != nil {
			t.Fatalf("upsert %s: %v", sc, err)
		}
	}

	// sw1 grouped; sw3 grouped but absent from the index (dormant); sw2 ungrouped.
	groups := []model.ShareGroup{
		{Name: "欧美剧", ShareCodes: []string{"sw1", "sw3"}},
		{Name: "纪录片", ShareCodes: []string{}},
	}
	dormant, err := s.ApplyGroups(ctx, groups)
	if err != nil {
		t.Fatalf("ApplyGroups() error = %v", err)
	}
	if !reflect.DeepEqual(dormant, []string{"sw3"}) {
		t.Fatalf("dormant = %v, want [sw3]", dormant)
	}

	// group_id assigned by slice order; share rows updated by share_code.
	var g1, g2 int
	var sw1Group, sw2Group sql.NullInt64
	mustScan(t, s, `SELECT group_id FROM share_group WHERE name='欧美剧'`, &g1)
	mustScan(t, s, `SELECT group_id FROM share_group WHERE name='纪录片'`, &g2)
	if g1 != 1 || g2 != 2 {
		t.Fatalf("group ids = %d,%d, want 1,2", g1, g2)
	}
	mustScan(t, s, `SELECT group_id FROM share WHERE share_code='sw1'`, &sw1Group)
	mustScan(t, s, `SELECT group_id FROM share WHERE share_code='sw2'`, &sw2Group)
	if !sw1Group.Valid || sw1Group.Int64 != 1 {
		t.Fatalf("sw1 group_id = %+v, want 1", sw1Group)
	}
	if sw2Group.Valid {
		t.Fatalf("sw2 group_id = %+v, want NULL", sw2Group)
	}
}

func TestApplyGroupsReappliesAndClearsRemovedMembers(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "r", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyGroups(ctx, []model.ShareGroup{{Name: "欧美剧", ShareCodes: []string{"sw1"}}}); err != nil {
		t.Fatal(err)
	}
	// Re-apply with sw1 removed from all groups -> group_id must be NULL again.
	if _, err := s.ApplyGroups(ctx, []model.ShareGroup{{Name: "其他", ShareCodes: []string{}}}); err != nil {
		t.Fatal(err)
	}
	var g sql.NullInt64
	mustScan(t, s, `SELECT group_id FROM share WHERE share_code='sw1'`, &g)
	if g.Valid {
		t.Fatalf("sw1 group_id = %+v, want NULL after re-apply", g)
	}
}

func TestExportTrimmedRetainsShareGroup(t *testing.T) {
	ctx := context.Background()
	s, cleanup := openTestStore(t)
	defer cleanup()

	if err := s.UpsertShare(ctx, model.Share{ShareCode: "sw1", ReceiveCode: "r", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyGroups(ctx, []model.ShareGroup{{Name: "欧美剧", ShareCodes: []string{"sw1"}}}); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "trimmed.db")
	if err := s.ExportTrimmed(ctx, dest); err != nil {
		t.Fatalf("ExportTrimmed() error = %v", err)
	}
	db, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"share_group", "share", "file"} {
		var n int
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n); err != nil {
			t.Fatalf("trimmed db missing table %s: %v", table, err)
		}
	}
	var hasCol int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('share') WHERE name='group_id'`).Scan(&hasCol); err != nil {
		t.Fatal(err)
	}
	if hasCol != 1 {
		t.Fatalf("trimmed share.group_id missing")
	}
}
```

Add the helpers used above (the test is in-package `store`, so `s.db` is reachable; `Open` runs migrate which creates `share_group` + `share.group_id`):

```go
func openTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return s, func() { _ = s.Close() }
}

func mustScan(t *testing.T, s *Store, query string, dest ...any) {
	t.Helper()
	if err := s.db.QueryRow(query).Scan(dest...); err != nil {
		t.Fatalf("scan %q: %v", query, err)
	}
}
```

Ensure the test file imports `database/sql`, `path/filepath`, `reflect`, and `fmt` (add any that are missing).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/workspace/five && go test ./internal/store/ -run 'TestApplyGroups|TestExportTrimmedRetainsShareGroup' -v`
Expected: FAIL — `ApplyGroups undefined` and `share_group` table absent.

- [ ] **Step 3: Add the schema migration**

In `internal/store/sqlite.go` `migrate()` — add to the `stmts` slice (after the `share` table creation):

```go
`CREATE TABLE IF NOT EXISTS share_group (
    group_id   INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    sort_order INTEGER NOT NULL
);`,
```

And extend the existing `ensureColumns` call for the `share` table (~line 216) to add `group_id`:

```go
if err := s.ensureColumns(ctx, "share", []columnDef{
	{name: "share_title", ddl: "TEXT NOT NULL DEFAULT ''"},
	{name: "file_size", ddl: "INTEGER NOT NULL DEFAULT 0"},
	{name: "group_id", ddl: "INTEGER"},
}); err != nil {
	return fmt.Errorf("migrate share columns: %w", err)
}
```

- [ ] **Step 4: Implement `ApplyGroups`**

In `internal/store/sqlite.go`, add after `UpsertShare` (uses the already-imported `model` and `fmt`):

```go
// ApplyGroups reconciles the grouping overlay in a single transaction: it
// replaces share_group with the given groups (slice order = group_id/sort_order,
// 1-based) and reassigns each share's group_id by share_code match. group_id is
// cleared for every share first, so shares absent from the overlay end up NULL.
// A code that matches no share row is returned in dormant for the caller to warn
// (it takes effect once that share is imported and ApplyGroups runs again).
func (s *Store) ApplyGroups(ctx context.Context, groups []model.ShareGroup) (dormant []string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM share_group;`); err != nil {
		return nil, fmt.Errorf("clear share_group: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE share SET group_id = NULL;`); err != nil {
		return nil, fmt.Errorf("clear share.group_id: %w", err)
	}

	for i, g := range groups {
		groupID := int64(i + 1)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO share_group(group_id, name, sort_order) VALUES (?, ?, ?);`,
			groupID, g.Name, groupID); err != nil {
			return nil, fmt.Errorf("insert share_group %q: %w", g.Name, err)
		}
		for _, code := range g.ShareCodes {
			res, err := tx.ExecContext(ctx, `UPDATE share SET group_id = ? WHERE share_code = ?;`, groupID, code)
			if err != nil {
				return nil, fmt.Errorf("set group_id for %s: %w", code, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				dormant = append(dormant, code)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dormant, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/user/workspace/five && go test ./internal/store/ -v`
Expected: PASS (new tests + existing store tests unaffected).

- [ ] **Step 6: Commit**

```bash
cd /home/user/workspace/five
git add internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "feat(indexer): add share_group + share.group_id schema and ApplyGroups"
```

---

## Task 3 (indexer): `import-groups` CLI mode

**Repo:** `/home/user/workspace/five`

**Files:**
- Modify: `cmd/115-indexer/main.go` (flag block ~line 33; switch ~line 114)

- [ ] **Step 1: Add the `-groups-file` flag**

In `cmd/115-indexer/main.go`, add to the `var (...)` flag block (e.g. after `sharesFile` at line 33):

```go
groupsFile = flag.String("groups-file", "115_groups.txt", "groups overlay file path for import-groups mode")
```

- [ ] **Step 2: Add the `import-groups` mode**

In the `switch *mode { ... }` block, add a new case (e.g. after `import-shares`, before `backfill-share-meta`):

```go
case "import-groups":
	f, err := os.Open(*groupsFile)
	if err != nil {
		log.Fatalf("open groups file: %v", err)
	}
	defer f.Close()
	groups, dups, err := shares.ParseGroups(f)
	if err != nil {
		log.Fatalf("parse groups file: %v", err)
	}
	for _, code := range dups {
		log.Printf("warn: share %s appears in more than one group; last assignment wins", code)
	}
	dormant, err := s.ApplyGroups(ctx, groups)
	if err != nil {
		log.Fatalf("apply groups: %v", err)
	}
	for _, code := range dormant {
		log.Printf("warn: share %s is grouped but not present in the index (dormant)", code)
	}
	fmt.Fprintf(os.Stdout, "applied %d groups\n", len(groups))
```

- [ ] **Step 3: Build and smoke-test**

Run:
```bash
cd /home/user/workspace/five
go build ./cmd/115-indexer/
printf '# 欧美剧\nsw68wz93ncb\nswnsdrk3h2m?password=p783\nhttps://115cdn.com/s/swsfnan3hjs?password=6666\n' > /tmp/115_groups.txt
./115-indexer -mode import-groups -groups-file /tmp/115_groups.txt -db /tmp/grouptest.db
```
Expected output: `applied 1 groups` and a `warn: share ... is grouped but not present ... (dormant)` line per code (the temp DB has no shares yet — that proves the dormant path).

- [ ] **Step 4: Verify end-to-end with a real share**

```bash
cd /home/user/workspace/five
./115-indexer -mode register-share -share-code sw68wz93ncb -receive-code r -db /tmp/grouptest.db
./115-indexer -mode import-groups -groups-file /tmp/115_groups.txt -db /tmp/grouptest.db
sqlite3 /tmp/grouptest.db "SELECT share_code, group_id FROM share; SELECT group_id, name FROM share_group;"
```
Expected: `sw68wz93ncb|1` and `1|欧美剧`.

- [ ] **Step 5: Commit**

```bash
cd /home/user/workspace/five
git add cmd/115-indexer/main.go
git commit -m "feat(indexer): add import-groups mode and -groups-file flag"
```

---

## Task 4 (consumer): Model + store group plumbing

**Repo:** `/home/user/GolandProjects/PowerList`

**Files:**
- Modify: `internal/index115/model.go`
- Modify: `internal/index115/store.go`
- Modify: `internal/index115/store_test.go`

- [ ] **Step 1: Write the failing store tests**

In `internal/index115/store_test.go`, first extend the `openTestStore` schema (lines ~175-197): in the `share` CREATE TABLE add a `group_id INTEGER` column, and append a `share_group` table to the `stmts` slice:

```go
// in CREATE TABLE share, add:
//   group_id INTEGER,
// after last_crawled_at

// append to stmts:
`CREATE TABLE share_group (
    group_id   INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    sort_order INTEGER NOT NULL
);`,
```

Then append this test:

```go
func TestStoreListGroupsAndGroupMembership(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := openTestStore(t, dbPath)

	insertTestShare(t, store.db, testShareRow{ShareCode: "sw1", ShareTitle: "Grouped", Status: "ACTIVE"})
	insertTestShare(t, store.db, testShareRow{ShareCode: "sw2", ShareTitle: "Loose", Status: "ACTIVE"})
	insertTestFile(t, store.db, testFileRow{FileID: "f1", ShareCode: "sw1", ParentID: "0", Name: "a.mkv", UpdatedAt: 1})
	insertTestFile(t, store.db, testFileRow{FileID: "f2", ShareCode: "sw2", ParentID: "0", Name: "b.mkv", UpdatedAt: 1})
	if _, err := store.db.Exec(`INSERT INTO share_group(group_id, name, sort_order) VALUES (1, '欧美剧', 1), (2, '纪录片', 2);`); err != nil {
		t.Fatalf("insert groups: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE share SET group_id = 1 WHERE share_code = 'sw1';`); err != nil {
		t.Fatalf("set group_id: %v", err)
	}

	if err := store.RefreshShares(context.Background()); err != nil {
		t.Fatalf("RefreshShares() error = %v", err)
	}

	groups, err := store.ListGroups(context.Background())
	if err != nil {
		t.Fatalf("ListGroups() error = %v", err)
	}
	if len(groups) != 2 || groups[0].ID != 1 || groups[0].Name != "欧美剧" || groups[1].Name != "纪录片" {
		t.Fatalf("groups = %+v", groups)
	}

	items, err := store.ListShares(context.Background())
	if err != nil {
		t.Fatalf("ListShares() error = %v", err)
	}
	byCode := map[string]int64{}
	for _, it := range items {
		byCode[it.ShareCode] = it.GroupID
	}
	if byCode["sw1"] != 1 {
		t.Fatalf("sw1 GroupID = %d, want 1", byCode["sw1"])
	}
	if byCode["sw2"] != 0 {
		t.Fatalf("sw2 GroupID = %d, want 0 (loose)", byCode["sw2"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -run TestStoreListGroupsAndGroupMembership -v`
Expected: FAIL — `items[it].GroupID` undefined and `ListGroups` undefined.

- [ ] **Step 3: Update the model types**

In `internal/index115/model.go`, add `GroupID` to `ShareSummary` and define `GroupInfo`:

```go
type ShareSummary struct {
	ShareCode   string
	ReceiveCode string
	ShareTitle  string
	Path        string
	IsDir       bool
	GroupID     int64
	FileCount   int64
	DirCount    int64
	UpdatedAt   int64
}

// GroupInfo is one virtual directory rendered on the homepage. ID maps to the
// grp<ID> sentinel share_code used to drill into the group.
type GroupInfo struct {
	ID   int64
	Name string
}
```

- [ ] **Step 4: Update the store**

In `internal/index115/store.go`:

(a) Add `GroupID int64` to the unexported `shareMeta` struct (after `ID`):

```go
type shareMeta struct {
	ShareCode     string
	ReceiveCode   string
	ShareTitle    string
	RootFolderID  string
	GroupID       int64
	Status        string
	LastCrawledAt int64
	ID            int64
}
```

(b) Add a `groups []GroupInfo` field to the `Store` struct:

```go
type Store struct {
	db     *sql.DB
	shares map[string]shareMeta
	groups []GroupInfo
}
```

(c) In `RefreshShares`, extend the share SELECT to read `group_id`, scan it, and load `share_group`. Change the first query/scan (lines ~34-52) to:

```go
rows, err := s.db.QueryContext(ctx, `
	SELECT id, share_code, COALESCE(receive_code, ''), COALESCE(share_title, ''),
	       status, COALESCE(last_crawled_at, 0), COALESCE(group_id, 0)
	FROM share`)
if err != nil {
	return err
}
defer rows.Close()

shares := map[string]shareMeta{}
for rows.Next() {
	var meta shareMeta
	if err := rows.Scan(&meta.ID, &meta.ShareCode, &meta.ReceiveCode, &meta.ShareTitle,
		&meta.Status, &meta.LastCrawledAt, &meta.GroupID); err != nil {
		return err
	}
	current, ok := shares[meta.ShareCode]
	if !ok || preferShareMeta(meta, current) {
		shares[meta.ShareCode] = meta
	}
}
if err := rows.Err(); err != nil {
	return err
}
```

Then, just before `s.shares = shares` at the end of `RefreshShares`, load the groups:

```go
groupRows, err := s.db.QueryContext(ctx, `SELECT group_id, name FROM share_group ORDER BY sort_order ASC`)
if err != nil {
	return err
}
defer groupRows.Close()
var gs []GroupInfo
for groupRows.Next() {
	var g GroupInfo
	if err := groupRows.Scan(&g.ID, &g.Name); err != nil {
		return err
	}
	gs = append(gs, g)
}
if err := groupRows.Err(); err != nil {
	return err
}

s.shares = shares
s.groups = gs
return nil
```

(d) In `ListShares`, set `GroupID` on each summary — after `meta := s.shares[item.ShareCode]` (line ~133) add:

```go
item.GroupID = meta.GroupID
```

(e) Add the `ListGroups` method (e.g. after `ListShares`):

```go
func (s *Store) ListGroups(ctx context.Context) ([]GroupInfo, error) {
	return s.groups, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -v`
Expected: PASS (new test + all existing store/service tests; the service tests won't compile yet only if StoreReader changed — it does not here).

- [ ] **Step 6: Commit**

```bash
cd /home/user/GolandProjects/PowerList
git add internal/index115/model.go internal/index115/store.go internal/index115/store_test.go
git commit -m "feat(index115): load group_id + share_group in store, add ListGroups"
```

---

## Task 5 (consumer): Sentinel groups in `service.Browse`

**Repo:** `/home/user/GolandProjects/PowerList`

**Files:**
- Modify: `internal/index115/service.go`
- Modify: `internal/index115/service_test.go`

- [ ] **Step 1: Extend the test stub and write failing browse tests**

In `internal/index115/service_test.go`, add a `groups` field and `ListGroups` method to `stubStore` (lines ~63-85):

```go
type stubStore struct {
	shares []ShareSummary
	groups []GroupInfo
	items  []FileItem
	file   FileItem
	ok     bool
	err    error
}

func (s stubStore) ListGroups(ctx context.Context) ([]GroupInfo, error) {
	return s.groups, s.err
}
```

Keep the existing `ListShares`/`ListChildren`/`FileByID`/`FileWithFullPath` methods as-is.

Append these tests:

```go
func TestServiceBrowseRootListsGroupsThenLooseShares(t *testing.T) {
	svc := &Service{
		store: stubStore{
			groups: []GroupInfo{{ID: 1, Name: "欧美剧"}, {ID: 2, Name: "纪录片"}},
			shares: []ShareSummary{
				{ShareCode: "swG", ShareTitle: "Grouped", GroupID: 1},
				{ShareCode: "swL", ShareTitle: "Loose", GroupID: 0},
			},
		},
	}

	items, err := svc.Browse(context.Background(), BrowseRequest{})
	if err != nil {
		t.Fatalf("Browse() error = %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 root items (2 groups + 1 loose), got %d: %+v", len(items), items)
	}
	if items[0].ShareCode != "grp1" || items[0].Name != "欧美剧" || !items[0].IsDir {
		t.Fatalf("group item 0 = %+v", items[0])
	}
	if items[1].ShareCode != "grp2" || items[1].Name != "纪录片" {
		t.Fatalf("group item 1 = %+v", items[1])
	}
	if items[2].ShareCode != "swL" || items[2].Name != "Loose" {
		t.Fatalf("loose item = %+v (grouped swG must NOT appear at root)", items[2])
	}
}

func TestServiceBrowseGroupSentinelListsMembersOnly(t *testing.T) {
	svc := &Service{
		store: stubStore{
			shares: []ShareSummary{
				{ShareCode: "swG1", ShareTitle: "M1", GroupID: 1},
				{ShareCode: "swG2", ShareTitle: "M2", GroupID: 1},
				{ShareCode: "swO", ShareTitle: "Other", GroupID: 2},
			},
		},
	}

	items, err := svc.Browse(context.Background(), BrowseRequest{ShareCode: "grp1"})
	if err != nil {
		t.Fatalf("Browse() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 members, got %d: %+v", len(items), items)
	}
	codes := []string{items[0].ShareCode, items[1].ShareCode}
	if codes[0] != "swG1" || codes[1] != "swG2" {
		t.Fatalf("members = %v, want [swG1 swG2]", codes)
	}
}

func TestServiceBrowseUnknownGroupIsEmpty(t *testing.T) {
	svc := &Service{store: stubStore{}}
	items, err := svc.Browse(context.Background(), BrowseRequest{ShareCode: "grp99"})
	if err != nil {
		t.Fatalf("Browse() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty, got %+v", items)
	}
}
```

The existing `TestServiceBrowseRootReturnsShares` must still pass: its stub has no `groups` and one loose share (GroupID zero-value), so `listRoot` returns that one share.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -run TestServiceBrowse -v`
Expected: FAIL — root still returns the grouped share too; `grp1` returns children-of-share path.

- [ ] **Step 3: Implement the sentinel logic in `Browse`**

In `internal/index115/service.go`:

(a) Add `"strconv"` to the import block.

(b) Add `ListGroups` to the `StoreReader` interface (so the `Service` can call it through the interface — `*Store` already implements it from Task 4):

```go
type StoreReader interface {
	ListShares(ctx context.Context) ([]ShareSummary, error)
	ListGroups(ctx context.Context) ([]GroupInfo, error)
	ListChildren(ctx context.Context, shareCode, parentID string) ([]FileItem, error)
	FileByID(ctx context.Context, fileID string) (FileItem, bool, error)
	FileWithFullPath(ctx context.Context, fileID string) (FileItem, bool, error)
}
```

(c) Replace the top of `Browse` (the `ShareCode == ""` branch, lines ~53-82) and add helpers. The full replacement for `Browse` plus new helpers:

```go
const groupSentinelPrefix = "grp"

func (s *Service) Browse(ctx context.Context, req BrowseRequest) ([]FileItem, error) {
	if s == nil {
		return nil, errors.New("browse service is nil")
	}
	if s.store == nil {
		return nil, errors.New("browse store is nil")
	}

	if gid, ok := groupSentinelID(req.ShareCode); ok {
		return s.listGroupMembers(ctx, gid)
	}
	if req.ShareCode == "" {
		return s.listRoot(ctx)
	}

	parentID := req.ParentID
	if parentID == "" || parentID == "/" {
		parentID = "0"
	}
	items, err := s.store.ListChildren(ctx, req.ShareCode, parentID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	return items, nil
}

// listRoot renders the homepage: one virtual dir per group (share_group order),
// followed by loose (ungrouped) shares. Grouped shares are reachable only via
// their group sentinel.
func (s *Service) listRoot(ctx context.Context) ([]FileItem, error) {
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	shares, err := s.store.ListShares(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	items := make([]FileItem, 0, len(groups)+len(shares))
	for _, g := range groups {
		items = append(items, FileItem{
			ShareCode: groupSentinel(g.ID),
			Name:      g.Name,
			Path:      "/" + g.Name,
			IsDir:     true,
		})
	}
	items = append(items, looseShareItems(shares)...)
	return items, nil
}

func (s *Service) listGroupMembers(ctx context.Context, gid int64) ([]FileItem, error) {
	shares, err := s.store.ListShares(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	items := make([]FileItem, 0)
	for _, share := range shares {
		if share.GroupID != gid {
			continue
		}
		items = append(items, newShareDirItem(share))
	}
	return items, nil
}

func looseShareItems(shares []ShareSummary) []FileItem {
	var items []FileItem
	for _, share := range shares {
		if share.GroupID != 0 {
			continue
		}
		items = append(items, newShareDirItem(share))
	}
	return items
}

func newShareDirItem(share ShareSummary) FileItem {
	name := share.ShareTitle
	if name == "" {
		name = share.ShareCode
	}
	return FileItem{
		ShareCode:   share.ShareCode,
		ReceiveCode: share.ReceiveCode,
		ShareTitle:  share.ShareTitle,
		Name:        name,
		Path:        "/" + name,
		IsDir:       true,
		UpdatedAt:   share.UpdatedAt,
	}
}

func groupSentinel(id int64) string {
	return groupSentinelPrefix + strconv.FormatInt(id, 10)
}

// groupSentinelID decodes a "grp<N>" sentinel share_code. Real share codes start
// with "sw", so there is no collision.
func groupSentinelID(code string) (int64, bool) {
	if !strings.HasPrefix(code, groupSentinelPrefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(code, groupSentinelPrefix), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/GolandProjects/PowerList && go test ./internal/index115/ -v`
Expected: PASS (all service + store tests).

- [ ] **Step 5: Commit**

```bash
cd /home/user/GolandProjects/PowerList
git add internal/index115/service.go internal/index115/service_test.go
git commit -m "feat(index115): render group virtual dirs via grp<N> sentinel in Browse"
```

---

## Task 6 (consumer): WebDAV cross-share drilling fix

**Repo:** `/home/user/GolandProjects/PowerList`

**Files:**
- Modify: `server/index115_webdav.go` (in `resolve`, ~line 224)
- Create: `server/index115_webdav_test.go`

- [ ] **Step 1: Write the failing regression test**

Create `server/index115_webdav_test.go`:

```go
package server

import (
	"context"
	"errors"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/index115"
)

// stubGroupBrowseProvider returns canned children for the root, a group
// sentinel, and a real member share, exercising the group -> member crossing.
type stubGroupBrowseProvider struct{}

func (stubGroupBrowseProvider) Browse(ctx context.Context, req index115.BrowseRequest) ([]index115.FileItem, error) {
	switch {
	case req.ShareCode == "":
		return []index115.FileItem{{ShareCode: "grp1", ShareTitle: "欧美剧", Name: "欧美剧", IsDir: true}}, nil
	case req.ShareCode == "grp1":
		return []index115.FileItem{{ShareCode: "swM", ShareTitle: "Member", Name: "Member", IsDir: true}}, nil
	case req.ShareCode == "swM":
		return []index115.FileItem{{FileID: "f1", ShareCode: "swM", Name: "movie.mkv", IsDir: false}}, nil
	}
	return nil, errors.New("unexpected browse")
}

func TestWebDAVResolveDrillsGroupIntoMember(t *testing.T) {
	prev := index115BrowseService
	index115BrowseService = stubGroupBrowseProvider{}
	t.Cleanup(func() { index115BrowseService = prev })

	fs := &index115WebDAVFS{}
	entry, err := fs.resolve(context.Background(), "/欧美剧/Member")
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	// Without the fix, childInfos would re-browse the grp1 sentinel and return
	// the Member node again instead of the actual file under swM.
	if len(entry.children) != 1 {
		t.Fatalf("children = %d, want 1 (movie.mkv): %+v", len(entry.children), entry.children)
	}
	if entry.children[0].Name() != "movie.mkv" {
		t.Fatalf("child = %q, want movie.mkv", entry.children[0].Name())
	}
}

func TestWebDAVResolveGroupListsMembers(t *testing.T) {
	prev := index115BrowseService
	index115BrowseService = stubGroupBrowseProvider{}
	t.Cleanup(func() { index115BrowseService = prev })

	fs := &index115WebDAVFS{}
	entry, err := fs.resolve(context.Background(), "/欧美剧")
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if len(entry.children) != 1 || entry.children[0].Name() != "Member" {
		t.Fatalf("children = %+v, want [Member]", entry.children)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/GolandProjects/PowerList && go test ./server/ -run TestWebDAVResolve -v`
Expected: `TestWebDAVResolveDrillsGroupIntoMember` FAILs — it re-browses `grp1` and returns `Member` (the group member node) instead of `movie.mkv`. The second test may pass already.

- [ ] **Step 3: Apply the one-line fix**

In `server/index115_webdav.go`, in `resolve`'s path-walk loop, immediately after the line `currentInfo = newIndex115FileInfo(match, idx == 1)` (line ~224) and before `if idx == len(parts)-1 {`, add:

```go
// Crossing from a group node (sentinel share_code like "grp1") into one
// of its member shares: switch the active share for deeper drilling.
// Normal intra-share paths are unaffected (match.ShareCode == current.ShareCode).
if match.ShareCode != "" && match.ShareCode != current.ShareCode {
	current = match
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/GolandProjects/PowerList && go test ./server/ -run TestWebDAVResolve -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/GolandProjects/PowerList
git add server/index115_webdav.go server/index115_webdav_test.go
git commit -m "fix(index115): webdav switch active share when drilling group -> member"
```

---

## Task 7: Whole-repo builds and full test suites

**Repos:** both.

- [ ] **Step 1: Build + test the indexer**

```bash
cd /home/user/workspace/five
go build ./... && go test ./...
```
Expected: build OK, all tests PASS.

- [ ] **Step 2: Build + test the consumer**

```bash
cd /home/user/GolandProjects/PowerList
go build ./... && go test ./internal/index115/ ./server/
```
Expected: build OK, all tests PASS.

- [ ] **Step 3 (end-to-end, manual): produce a grouped index and verify**

```bash
cd /home/user/workspace/five
# 1. register/crawl at least one share that exists in 115_groups.txt
# 2. apply groups + export
./115-indexer -mode import-groups -groups-file 115_groups.txt -db data/index.db
./115-indexer -mode export-db -out 115.index.zip   # (path per existing publish flow)
# 3. on the consumer, after the index swaps in, hit browse:
curl -s 'http://<powerlist>/index115/browse' | head        # -> group dirs (grp1..) + any loose shares
curl -s 'http://<powerlist>/index115/browse?share_code=grp1' | head  # -> that group's member shares
```
Expected: root shows the 4 group directories; `grp1` lists its member shares; a member share drills to files as before; WebDAV `/dav/index115/<group>/<member>/` resolves.

- [ ] **Step 4: Update memory**

Record the new `import-groups` mode and the `grp<N>` sentinel contract in `.claude/projects/-home-user-workspace-five/memory/` (link from `[[index115-two-project-layout]]`).

---

## Notes for the implementer

- **No SPA change.** The compiled frontend (`public/dist`) is untouched; group nodes flow through `share_code` like any share.
- **`share.group_id` is read-only on the consumer.** The consumer never creates the column — it only reads what the indexer's trimmed `index.db` ships. A consumer pointed at an old index (pre-`group_id`) will error in `RefreshShares`; re-export after deploying the indexer changes.
- **Sentinel safety:** real 115 share codes start with `sw`; `grp<N>` cannot collide.
- **`ApplyGroups` is idempotent** — a full re-apply clears and rebuilds, so removed members correctly revert to loose.
