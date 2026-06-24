package api115

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestClassifyHTTPStatusErrors(t *testing.T) {
	err := ClassifyHTTPError(403, errors.New("forbidden"))
	if !IsProxyFailure(err) {
		t.Fatalf("403 should be proxy failure, got %v", err)
	}

	err = ClassifyHTTPError(405, errors.New("method not allowed"))
	if !IsProxyFailure(err) {
		t.Fatalf("405 should be proxy failure, got %v", err)
	}

	err = ClassifyHTTPError(504, errors.New("gateway timeout"))
	if !IsRetryable(err) {
		t.Fatalf("504 should be retryable, got %v", err)
	}
}

func TestClassifySnapResponseErrors(t *testing.T) {
	resp := SnapResponse{
		State: false,
		Error: "receive code error",
		Errno: 4100017,
	}
	err := ClassifySnapError(resp)
	if !IsDeadShare(err) {
		t.Fatalf("receive code error should be dead share, got %v", err)
	}

	resp = SnapResponse{
		State: true,
		Data: SnapData{
			ShareState: 0,
		},
	}
	err = ClassifySnapError(resp)
	if !IsDeadShare(err) {
		t.Fatalf("share_state 0 should be dead share, got %v", err)
	}

	resp = SnapResponse{
		State: true,
		Data: SnapData{
			ShareState: 1,
			List:       nil,
			Count:      10,
		},
	}
	err = ClassifySnapError(resp)
	if !IsRetryable(err) {
		t.Fatalf("empty data with nonzero count should be retryable, got %v", err)
	}
}

// 115 returns share-lifecycle errors in Chinese. A cancelled/missing share is
// permanent, so it must be DEAD (never retried), not RETRYABLE.
func TestClassifySnapErrorMarksChineseDeadShareMessages(t *testing.T) {
	cases := []string{
		"分享已取消", // share has been cancelled (confirmed from production logs)
		"分享不存在", // share not found
		"提取码错误", // wrong receive code
		"链接已失效", // link expired/invalid (confirmed from production logs)
		"分享的文件涉嫌违规，链接已失效", // policy-violation takedown
	}
	for _, msg := range cases {
		resp := SnapResponse{State: false, Error: msg}
		err := ClassifySnapError(resp)
		if !IsDeadShare(err) {
			t.Fatalf("115 dead-share message %q should be DEAD, got %v", msg, err)
		}
	}
}

func TestClassifiedErrorSupportsErrorsIs(t *testing.T) {
	err := WrapError(KindProxyFailure, "proxy blocked", http.StatusForbidden, nil)
	if !errors.Is(err, ErrProxyFailure) {
		t.Fatalf("expected errors.Is proxy failure, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type proxyRecorder struct {
	acquired []string
	failed   []string
	success  []string
	queue    []ProxyRef
}

type memoryCookieStore struct {
	value string
	saves int
}

func (m *memoryCookieStore) Load() string {
	return m.value
}

func (m *memoryCookieStore) Save(cookie string) {
	m.value = cookie
	m.saves++
}

func (m *memoryCookieStore) Clear() {
	m.value = ""
}

func (p *proxyRecorder) Acquire(context.Context) (ProxyRef, bool) {
	if len(p.queue) == 0 {
		return ProxyRef{}, false
	}
	ref := p.queue[0]
	p.queue = p.queue[1:]
	p.acquired = append(p.acquired, ref.ID)
	return ref, true
}

func (p *proxyRecorder) RecordFailure(id string) {
	p.failed = append(p.failed, id)
}

func (p *proxyRecorder) RecordSuccess(id string) {
	p.success = append(p.success, id)
}

func TestClientRetriesWithNextProxyOn403(t *testing.T) {
	proxy1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer proxy1.Close()
	proxy2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"state": true,
			"error": "",
			"errno": 0,
			"data": {"shareinfo": {"share_state": 1}, "count": 0, "list": [], "share_state": 1}
		}`)
	}))
	defer proxy2.Close()

	recorder := &proxyRecorder{
		queue: []ProxyRef{
			{ID: "p1", URL: proxy1.URL},
			{ID: "p2", URL: proxy2.URL},
		},
	}

	client := &Client{
		BaseURL:    "http://example.invalid",
		HTTPClient: &http.Client{},
		ProxyPool:  recorder,
	}

	_, err := client.List(t.Context(), ListRequest{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
		CID:         "0",
		Offset:      0,
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("list with proxy retry: %v", err)
	}
	if len(recorder.failed) != 1 || recorder.failed[0] != "p1" {
		t.Fatalf("failed proxies = %#v", recorder.failed)
	}
	if len(recorder.success) != 1 || recorder.success[0] != "p2" {
		t.Fatalf("successful proxies = %#v", recorder.success)
	}
}

func TestClientMarksSuccessfulProxy(t *testing.T) {
	proxy1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"state": true,
			"error": "",
			"errno": 0,
			"data": {"shareinfo": {"share_state": 1}, "count": 0, "list": [], "share_state": 1}
		}`)
	}))
	defer proxy1.Close()

	recorder := &proxyRecorder{
		queue: []ProxyRef{
			{ID: "p1", URL: proxy1.URL},
		},
	}

	client := &Client{
		BaseURL:    "http://example.invalid",
		HTTPClient: &http.Client{},
		ProxyPool:  recorder,
	}

	_, err := client.List(t.Context(), ListRequest{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
		CID:         "0",
		Offset:      0,
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("list success with proxy: %v", err)
	}
	if len(recorder.success) != 1 || recorder.success[0] != "p1" {
		t.Fatalf("successful proxies = %#v", recorder.success)
	}
}

func TestClientUsesProxyURLForHTTPRequests(t *testing.T) {
	proxied := false
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"state": true,
			"error": "",
			"errno": 0,
			"data": {"shareinfo": {"share_state": 1}, "count": 0, "list": [], "share_state": 1}
		}`)
	}))
	defer proxyServer.Close()

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("request should have gone through proxy server")
	}))
	defer targetServer.Close()

	recorder := &proxyRecorder{
		queue: []ProxyRef{
			{ID: "p1", URL: proxyServer.URL},
		},
	}
	client := &Client{
		BaseURL:    targetServer.URL,
		HTTPClient: &http.Client{},
		ProxyPool:  recorder,
	}

	_, err := client.List(t.Context(), ListRequest{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
		CID:         "0",
		Offset:      0,
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("list through proxy: %v", err)
	}
	if !proxied {
		t.Fatal("expected proxy server to receive the request")
	}
}

func TestClientRejectsInvalidProxyURL(t *testing.T) {
	recorder := &proxyRecorder{
		queue: []ProxyRef{
			{ID: "p1", URL: "://bad proxy"},
		},
	}
	client := &Client{
		BaseURL:    "http://example.invalid",
		HTTPClient: &http.Client{},
		ProxyPool:  recorder,
	}

	_, err := client.List(t.Context(), ListRequest{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
		CID:         "0",
		Offset:      0,
		Limit:       20,
	})
	if err == nil {
		t.Fatal("expected invalid proxy URL to fail")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return
	}
	if !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("unexpected invalid proxy error: %v", err)
	}
}

func TestClientSavesSetCookieForLaterRequests(t *testing.T) {
	store := &memoryCookieStore{}
	call := 0
	client := &Client{
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				call++
				if call == 1 {
					resp := &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(`{
							"state": true,
							"error": "",
							"errno": 0,
							"data": {"shareinfo": {"share_state": 1}, "count": 0, "list": [], "share_state": 1}
						}`)),
						Header: make(http.Header),
					}
					resp.Header.Add("Set-Cookie", "sessionid=abc123; Path=/; HttpOnly")
					return resp, nil
				}
				if got := r.Header.Get("cookie"); !strings.Contains(got, "sessionid=abc123") {
					t.Fatalf("second request cookie header = %q", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"state": true,
						"error": "",
						"errno": 0,
						"data": {"shareinfo": {"share_state": 1}, "count": 0, "list": [], "share_state": 1}
					}`)),
					Header: make(http.Header),
				}, nil
			}),
		},
		CookieStore: store,
	}

	for i := 0; i < 2; i++ {
		_, err := client.List(t.Context(), ListRequest{
			ShareCode:   "swf01d43zby",
			ReceiveCode: "echo",
			CID:         "0",
			Offset:      0,
			Limit:       20,
		})
		if err != nil {
			t.Fatalf("list call %d: %v", i+1, err)
		}
	}
	if store.saves != 1 {
		t.Fatalf("cookie saves = %d, want 1", store.saves)
	}
	if store.value == "" {
		t.Fatal("expected cookie to be persisted")
	}
}

func TestClientMarksTimedOutProxyAsFailure(t *testing.T) {
	timeoutProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer timeoutProxy.Close()

	recorder := &proxyRecorder{
		queue: []ProxyRef{
			{ID: "p1", URL: timeoutProxy.URL},
		},
	}
	client := &Client{
		BaseURL:    "http://example.invalid",
		HTTPClient: &http.Client{Timeout: 10 * time.Millisecond},
		ProxyPool:  recorder,
	}

	_, err := client.List(t.Context(), ListRequest{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
		CID:         "0",
		Offset:      0,
		Limit:       20,
	})
	if err == nil {
		t.Fatal("expected timeout through proxy to fail")
	}
	if len(recorder.failed) != 1 || recorder.failed[0] != "p1" {
		t.Fatalf("failed proxies = %#v", recorder.failed)
	}
	if !IsProxyFailure(err) {
		t.Fatalf("timeout via proxy should be classified as proxy failure, got %v", err)
	}
}

func TestClientDoesNotFallBackToDirectWhenProxyPoolIsExhausted(t *testing.T) {
	proxy1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer proxy1.Close()

	recorder := &proxyRecorder{
		queue: []ProxyRef{
			{ID: "p1", URL: proxy1.URL},
		},
	}
	client := &Client{
		BaseURL:    "http://example.invalid",
		HTTPClient: &http.Client{},
		ProxyPool:  recorder,
	}

	_, err := client.List(t.Context(), ListRequest{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
		CID:         "0",
		Offset:      0,
		Limit:       20,
	})
	if err == nil {
		t.Fatal("expected exhausted proxy pool to fail")
	}
	if !IsProxyFailure(err) {
		t.Fatalf("expected proxy failure when proxy pool exhausted, got %v", err)
	}
}

func TestClientLogsProxyUsage(t *testing.T) {
	var logs bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer log.SetOutput(prevWriter)
	defer log.SetFlags(prevFlags)

	proxy1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"state": true,
			"error": "",
			"errno": 0,
			"data": {"shareinfo": {"share_state": 1}, "count": 0, "list": [], "share_state": 1}
		}`)
	}))
	defer proxy1.Close()

	recorder := &proxyRecorder{
		queue: []ProxyRef{
			{ID: "183.166.18.170:11812", URL: proxy1.URL},
		},
	}

	client := &Client{
		BaseURL:    "http://example.invalid",
		HTTPClient: &http.Client{},
		ProxyPool:  recorder,
	}

	_, err := client.List(t.Context(), ListRequest{
		ShareCode:   "swf01d43zby",
		ReceiveCode: "echo",
		CID:         "0",
		Offset:      0,
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("list with proxy logging: %v", err)
	}
	output := logs.String()
	if !strings.Contains(output, "event=proxy_request") || !strings.Contains(output, "proxy=183.166.18.170:11812") {
		t.Fatalf("missing proxy request log: %q", output)
	}
}
