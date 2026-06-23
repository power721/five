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
