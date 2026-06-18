package proxy

import (
	"context"
	"net/http"
	"time"

	"five/internal/api115"
)

type HTTPValidator struct {
	BaseURL   string
	UserAgent string
	Cookie    string
	Timeout   time.Duration
}

func (v *HTTPValidator) Validate(ctx context.Context, proxy Proxy) bool {
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &api115.Client{
		BaseURL: v.BaseURL,
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
		UserAgent: v.UserAgent,
		Cookie:    v.Cookie,
	}
	_, err := client.ListOnceWithProxy(ctx, api115.ListRequest{
		ShareCode:   "healthcheck",
		ReceiveCode: "healthcheck",
		CID:         "0",
		Offset:      0,
		Limit:       1,
	}, proxy.URL)
	return err == nil || !api115.IsProxyFailure(err)
}
