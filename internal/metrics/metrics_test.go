package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistryServesPlainTextSnapshot(t *testing.T) {
	r := New()
	r.SetGauge("proxy_available", 2)
	r.IncCounter("crawl_runs_total", 1)

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "proxy_available 2") {
		t.Fatalf("metrics missing proxy gauge: %q", text)
	}
	if !strings.Contains(text, "crawl_runs_total 1") {
		t.Fatalf("metrics missing crawl counter: %q", text)
	}
}
