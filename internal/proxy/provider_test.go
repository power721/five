package proxy

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProviderFetchBuildsHTTPProxyURL(t *testing.T) {
	var logs bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer log.SetOutput(prevWriter)
	defer log.SetFlags(prevFlags)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "183.166.18.170:11812\r\n")
	}))
	defer srv.Close()

	now := time.Date(2026, 6, 18, 19, 8, 23, 0, time.UTC)
	p := &Provider{
		BaseURL:  srv.URL,
		Key:      "ELV4RTI2",
		Password: "B29EFCFB33FA",
		Now:      func() time.Time { return now },
	}
	proxy, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch proxy: %v", err)
	}
	if proxy.ID != "183.166.18.170:11812" {
		t.Fatalf("proxy id = %q", proxy.ID)
	}
	if proxy.URL != "http://183.166.18.170:11812" {
		t.Fatalf("proxy url = %q", proxy.URL)
	}
	if want := now.Add(3 * time.Minute); !proxy.Deadline.Equal(want) {
		t.Fatalf("proxy deadline = %v, want %v", proxy.Deadline, want)
	}
	output := logs.String()
	if !strings.Contains(output, "event=proxy_fetched") || !strings.Contains(output, "proxy=183.166.18.170:11812") {
		t.Fatalf("unexpected proxy fetch log: %q", output)
	}
}
