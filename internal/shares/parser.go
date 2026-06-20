package shares

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"strings"

	"five/internal/model"
)

func Parse(r io.Reader) ([]model.Share, error) {
	scanner := bufio.NewScanner(r)
	seen := map[string]bool{}
	var out []model.Share
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			share, err := ParseURL(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			key := share.ShareCode + "\x00" + share.ReceiveCode
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, share)
			continue
		}
		fields := strings.Fields(line)
		// Token format: "<code>?password=<receive>[ <name>...]" where the first
		// field carries the share code and an optional password query string.
		if len(fields) >= 1 && strings.Contains(fields[0], "?") {
			share, err := parseToken(fields[0])
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			key := share.ShareCode + "\x00" + share.ReceiveCode
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, share)
			continue
		}
		if len(fields) < 4 {
			return nil, fmt.Errorf("line %d: expected at least 4 columns", lineNo)
		}
		shareCode := fields[1]
		receiveCode := fields[3]
		key := shareCode + "\x00" + receiveCode
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, model.Share{
			ShareCode:   shareCode,
			ReceiveCode: receiveCode,
			Status:      "ACTIVE",
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseToken handles the "<code>?password=<receive>" share token used in some
// share lists, e.g. "swznmd03nc7?password=p897".
func parseToken(token string) (model.Share, error) {
	code, query, _ := strings.Cut(token, "?")
	if code == "" {
		return model.Share{}, fmt.Errorf("missing share code")
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return model.Share{}, err
	}
	return model.Share{
		ShareCode:   code,
		ReceiveCode: q.Get("password"),
		Status:      "ACTIVE",
	}, nil
}

func ParseURL(raw string) (model.Share, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return model.Share{}, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "s" || parts[1] == "" {
		return model.Share{}, fmt.Errorf("invalid 115 share URL path")
	}
	return model.Share{
		ShareCode:   parts[1],
		ReceiveCode: u.Query().Get("password"),
		Status:      "ACTIVE",
	}, nil
}
