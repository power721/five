package shares

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"five/internal/model"
)

// shareURLRe finds a 115 share URL anywhere in a line, e.g. the
// "<title>\t<url>" rows in shares.txt/movies.txt where the title holds spaces.
// The host is left open (115.com, 115cdn.com, ...) because ParseURL only cares
// about the "/s/<code>" path.
var shareURLRe = regexp.MustCompile(`https?://[^/\s]+/s/[^\s]+`)

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
		// "<title>\t<url>" rows: the title may contain spaces, so find the URL
		// anywhere in the line rather than relying on whitespace field counts.
		if m := shareURLRe.FindString(line); m != "" {
			share, err := ParseURL(m)
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

// ShareURL builds the canonical 115 share link for a code and its receive
// (password) code. It is the inverse of ParseURL: the password query string is
// omitted when receiveCode is empty.
func ShareURL(shareCode, receiveCode string) string {
	if receiveCode == "" {
		return "https://115.com/s/" + shareCode
	}
	return "https://115.com/s/" + shareCode + "?password=" + receiveCode
}
