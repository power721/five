package api115

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type ErrorKind string

const (
	KindRetryable    ErrorKind = "RETRYABLE"
	KindProxyFailure ErrorKind = "PROXY_FAILURE"
	KindDeadShare    ErrorKind = "DEAD_SHARE"
	KindUnknown      ErrorKind = "UNKNOWN"
)

var (
	ErrRetryable    = errors.New("retryable 115 error")
	ErrProxyFailure = errors.New("proxy failure")
	ErrDeadShare    = errors.New("dead share")
)

type ClassifiedError struct {
	Kind       ErrorKind
	Message    string
	StatusCode int
	Cause      error
}

func (e *ClassifiedError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *ClassifiedError) Unwrap() error {
	switch e.Kind {
	case KindRetryable:
		return ErrRetryable
	case KindProxyFailure:
		return ErrProxyFailure
	case KindDeadShare:
		return ErrDeadShare
	default:
		return e.Cause
	}
}

func WrapError(kind ErrorKind, message string, statusCode int, cause error) error {
	return &ClassifiedError{
		Kind:       kind,
		Message:    message,
		StatusCode: statusCode,
		Cause:      cause,
	}
}

func IsRetryable(err error) bool {
	return errors.Is(err, ErrRetryable)
}

func IsProxyFailure(err error) bool {
	return errors.Is(err, ErrProxyFailure)
}

func IsDeadShare(err error) bool {
	return errors.Is(err, ErrDeadShare)
}

// IsEmptyDataError reports whether err is the "empty data with nonzero count"
// snap error: 115 reported a positive file count but returned no list. It is
// classified retryable because a stale cookie can cause it (callers refresh the
// cookie and retry), but for some shares 115 never returns data, so callers cap
// retries on it instead of retrying forever.
func IsEmptyDataError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "empty data with nonzero count")
}

func ClassifyHTTPError(statusCode int, cause error) error {
	switch statusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusForbidden:
		return WrapError(KindProxyFailure, "115 forbidden", statusCode, cause)
	case http.StatusGatewayTimeout, http.StatusBadGateway, http.StatusTooManyRequests:
		return WrapError(KindRetryable, "115 upstream unavailable", statusCode, cause)
	default:
		if statusCode >= 500 {
			return WrapError(KindRetryable, "115 server error", statusCode, cause)
		}
		return WrapError(KindUnknown, "115 http error", statusCode, cause)
	}
}

func ClassifyRequestError(cause error) error {
	if cause == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(cause, &urlErr) {
		if urlErr.Timeout() {
			return WrapError(KindProxyFailure, "proxy timeout", 0, cause)
		}
	}
	var netErr net.Error
	if errors.As(cause, &netErr) && netErr.Timeout() {
		return WrapError(KindProxyFailure, "proxy timeout", 0, cause)
	}
	return WrapError(KindRetryable, "115 request failed", 0, cause)
}

// isDeadShareMessage reports whether a 115 snap error text describes a share
// that is permanently unusable (cancelled, deleted, or needs a receive code we
// do not have). Such shares must never be retried. 115 returns these messages
// in Chinese; the English phrases are retained as a fallback.
func isDeadShareMessage(msg string) bool {
	switch {
	case strings.Contains(msg, "receive code"),
		strings.Contains(msg, "share not found"),
		strings.Contains(msg, "share has been cancelled"),
		strings.Contains(msg, "已取消"),
		strings.Contains(msg, "不存在"),
		strings.Contains(msg, "提取码"):
		return true
	}
	return false
}

func ClassifySnapError(resp SnapResponse) error {
	if !resp.State {
		msg := strings.ToLower(resp.Error)
		if isDeadShareMessage(msg) {
			return WrapError(KindDeadShare, resp.Error, 0, nil)
		}
		return WrapError(KindRetryable, resp.Error, 0, nil)
	}
	if !resp.ValidShare() {
		return WrapError(KindDeadShare, "share state invalid", 0, nil)
	}
	if resp.Data.Count > 0 && len(resp.Data.List) == 0 {
		return WrapError(KindRetryable, "empty data with nonzero count", 0, nil)
	}
	return nil
}
