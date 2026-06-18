package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPValidatorDoesNotRetrySameProxyAfterProxyFailure(t *testing.T) {
	var requests int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requests, 1)
		if count == 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"state": true,
			"error": "",
			"errno": 0,
			"data": {"shareinfo": {"share_state": 1}, "count": 0, "list": [], "share_state": 1}
		}`)
	}))
	defer proxyServer.Close()

	validator := &HTTPValidator{
		BaseURL: "http://example.invalid",
		Timeout: 100 * time.Millisecond,
	}

	if validator.Validate(t.Context(), Proxy{ID: "p1", URL: proxyServer.URL}) {
		t.Fatal("expected proxy failure to fail validation")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("healthcheck requests = %d, want 1", got)
	}
}
