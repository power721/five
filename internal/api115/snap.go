package api115

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"five/internal/model"
)

type NumericString string

func (n *NumericString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*n = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*n = NumericString(s)
		return nil
	}
	*n = NumericString(string(data))
	return nil
}

func (n NumericString) String() string {
	return string(n)
}

type SnapResponse struct {
	State bool     `json:"state"`
	Error string   `json:"error"`
	Errno int      `json:"errno"`
	Data  SnapData `json:"data"`
}

type SnapData struct {
	ShareInfo  SnapShareInfo `json:"shareinfo"`
	Count      int           `json:"count"`
	List       []SnapNode    `json:"list"`
	ShareState int           `json:"share_state"`
}

type SnapShareInfo struct {
	ShareState  int    `json:"share_state"`
	ReceiveCode string `json:"receive_code"`
	ShareTitle  string `json:"share_title"`
	FileSize    int64  `json:"file_size"`
}

type SnapNode struct {
	FID  NumericString `json:"fid"`
	CID  NumericString `json:"cid"`
	Name string        `json:"n"`
	Size int64         `json:"s"`
	Dir  int           `json:"d"`
	ICO  string        `json:"ico"`
	SHA1 string        `json:"sha"`
	Time NumericString `json:"t"`
}

func (r SnapResponse) ValidShare() bool {
	if !r.State {
		return false
	}
	if r.Data.ShareInfo.ShareState != 0 {
		return r.Data.ShareInfo.ShareState == 1
	}
	return r.Data.ShareState == 1
}

func (n SnapNode) NodeID() string {
	if n.FID.String() != "" {
		return n.FID.String()
	}
	return n.CID.String()
}

func (n SnapNode) IsDir() bool {
	if n.FID.String() != "" {
		return false
	}
	if n.CID.String() != "" {
		return true
	}
	return n.Dir == 1
}

func (n SnapNode) ToFile(shareCode, parentID, filePath string, depth int, crawledAt int64) model.File {
	ext := ""
	if !n.IsDir() {
		ext = strings.TrimPrefix(path.Ext(n.Name), ".")
		if ext == "" {
			ext = n.ICO
		}
	}
	updatedAt, _ := strconv.ParseInt(n.Time.String(), 10, 64)
	return model.File{
		FileID:    n.NodeID(),
		ShareCode: shareCode,
		ParentID:  parentID,
		Name:      n.Name,
		Path:      filePath,
		Ext:       ext,
		Size:      n.Size,
		IsDir:     n.IsDir(),
		Depth:     depth,
		SHA1:      n.SHA1,
		UpdatedAt: updatedAt,
		CrawledAt: crawledAt,
	}
}

type Client struct {
	BaseURL     string
	HTTPClient  *http.Client
	Cookie      string
	CookieStore CookieStore
	UserAgent   string
	ProxyPool   ProxyPool
}

type CookieStore interface {
	Load() string
	Save(cookie string)
}

type ProxyRef struct {
	ID  string
	URL string
}

type ProxyPool interface {
	Acquire() (ProxyRef, bool)
	RecordFailure(id string)
	RecordSuccess(id string)
}

type ListRequest struct {
	ShareCode   string
	ReceiveCode string
	CID         string
	Offset      int
	Limit       int
}

func (c *Client) List(ctx context.Context, req ListRequest) (SnapResponse, error) {
	if c.ProxyPool == nil {
		return c.listOnce(ctx, req, "")
	}
	var lastErr error
	attemptedProxy := false
	for {
		proxyID := ""
		proxyURL := ""
		ref, ok := c.ProxyPool.Acquire()
		if !ok {
			if attemptedProxy {
				if lastErr == nil {
					lastErr = WrapError(KindProxyFailure, "proxy pool exhausted", 0, nil)
				}
				return SnapResponse{}, lastErr
			}
			return SnapResponse{}, WrapError(KindProxyFailure, "proxy pool exhausted", 0, nil)
		}
		attemptedProxy = true
		proxyID = ref.ID
		proxyURL = ref.URL
		log.Printf("event=proxy_request proxy=%s share=%s cid=%s offset=%d limit=%d", proxyID, req.ShareCode, req.CID, req.Offset, req.Limit)
		resp, err := c.listOnce(ctx, req, proxyURL)
		if err == nil {
			c.ProxyPool.RecordSuccess(proxyID)
			return resp, nil
		}
		lastErr = err
		if proxyID != "" && IsProxyFailure(err) {
			c.ProxyPool.RecordFailure(proxyID)
			continue
		}
		return SnapResponse{}, err
	}
}

func (c *Client) ListOnceWithProxy(ctx context.Context, req ListRequest, proxyURL string) (SnapResponse, error) {
	return c.listOnce(ctx, req, proxyURL)
}

func (c *Client) listOnce(ctx context.Context, req ListRequest, proxyURL string) (SnapResponse, error) {
	base := c.BaseURL
	if base == "" {
		base = "https://115cdn.com/webapi/share/snap"
	}
	u, err := url.Parse(base)
	if err != nil {
		return SnapResponse{}, err
	}
	q := u.Query()
	q.Set("share_code", req.ShareCode)
	q.Set("receive_code", req.ReceiveCode)
	q.Set("cid", req.CID)
	q.Set("offset", strconv.Itoa(req.Offset))
	q.Set("limit", strconv.Itoa(req.Limit))
	q.Set("asc", "0")
	q.Set("o", "file_name")
	q.Set("format", "json")
	u.RawQuery = q.Encode()

	httpClient, err := c.httpClientForProxy(proxyURL)
	if err != nil {
		return SnapResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SnapResponse{}, err
	}
	httpReq.Header.Set("accept", "*/*")
	if c.UserAgent != "" {
		httpReq.Header.Set("user-agent", c.UserAgent)
	}
	cookie := c.Cookie
	if c.CookieStore != nil {
		if loaded := c.CookieStore.Load(); loaded != "" {
			cookie = loaded
		}
	}
	if cookie != "" {
		httpReq.Header.Set("cookie", cookie)
	}
	httpReq.Header.Set("referer", fmt.Sprintf("https://115cdn.com/s/%s?password=%s", req.ShareCode, req.ReceiveCode))

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return SnapResponse{}, ClassifyRequestError(err)
	}
	defer resp.Body.Close()
	if c.CookieStore != nil {
		if setCookie := resp.Header.Values("Set-Cookie"); len(setCookie) > 0 {
			c.CookieStore.Save(joinCookiePairs(setCookie))
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return SnapResponse{}, ClassifyHTTPError(resp.StatusCode, fmt.Errorf("115 snap status %d", resp.StatusCode))
	}
	var out SnapResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SnapResponse{}, err
	}
	if err := ClassifySnapError(out); err != nil {
		return SnapResponse{}, err
	}
	return out, nil
}

func (c *Client) httpClientForProxy(proxyURL string) (*http.Client, error) {
	base := c.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	if proxyURL == "" {
		return base, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(parsed)
	return &http.Client{
		Timeout:   base.Timeout,
		Transport: transport,
	}, nil
}

func joinCookiePairs(values []string) string {
	pairs := make([]string, 0, len(values))
	for _, v := range values {
		part := strings.SplitN(v, ";", 2)[0]
		part = strings.TrimSpace(part)
		if part != "" {
			pairs = append(pairs, part)
		}
	}
	return strings.Join(pairs, "; ")
}
