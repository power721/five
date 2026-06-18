package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Provider struct {
	BaseURL    string
	HTTPClient *http.Client
	Key        string
	Password   string
	TTL        time.Duration
	Now        func() time.Time
}

func (p *Provider) Fetch(ctx context.Context) (Proxy, error) {
	base := p.BaseURL
	if base == "" {
		base = "https://share.proxy.qg.net/get"
	}
	u, err := url.Parse(base)
	if err != nil {
		return Proxy{}, err
	}
	q := u.Query()
	q.Set("key", p.Key)
	q.Set("pwd", p.Password)
	q.Set("num", "1")
	q.Set("area", "")
	q.Set("isp", "")
	q.Set("format", "txt")
	q.Set("seq", "\r\n")
	q.Set("distinct", "true")
	u.RawQuery = q.Encode()

	httpClient := p.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Proxy{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Proxy{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Proxy{}, fmt.Errorf("proxy provider status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Proxy{}, err
	}
	server := strings.TrimSpace(string(body))
	if server == "" {
		return Proxy{}, fmt.Errorf("proxy provider returned no proxy")
	}
	nowFn := p.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 3 * time.Minute
	}
	return Proxy{
		ID:       server,
		URL:      "http://" + server,
		State:    StateActive,
		Deadline: nowFn().Add(ttl),
	}, nil
}
