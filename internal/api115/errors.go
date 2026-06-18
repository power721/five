package api115

import (
	"errors"
	"fmt"
	"net/http"
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

func ClassifyHTTPError(statusCode int, cause error) error {
	switch statusCode {
	case http.StatusForbidden:
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

func ClassifySnapError(resp SnapResponse) error {
	if !resp.State {
		msg := strings.ToLower(resp.Error)
		switch {
		case strings.Contains(msg, "receive code"), strings.Contains(msg, "share not found"), strings.Contains(msg, "share has been cancelled"):
			return WrapError(KindDeadShare, resp.Error, 0, nil)
		default:
			return WrapError(KindRetryable, resp.Error, 0, nil)
		}
	}
	if !resp.ValidShare() {
		return WrapError(KindDeadShare, "share state invalid", 0, nil)
	}
	if resp.Data.Count > 0 && len(resp.Data.List) == 0 {
		return WrapError(KindRetryable, "empty data with nonzero count", 0, nil)
	}
	return nil
}
