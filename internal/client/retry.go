package client

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type RetryError struct {
	StatusCode int
	After      time.Duration
}

func (e *RetryError) Error() string {
	if e.After > 0 {
		return fmt.Sprintf("platform returned HTTP %d; retry after %s", e.StatusCode, e.After)
	}
	return fmt.Sprintf("platform returned retryable HTTP %d", e.StatusCode)
}

func retryAfter(header http.Header, now time.Time) time.Duration {
	raw := header.Get("Retry-After")
	if seconds, err := strconv.ParseUint(raw, 10, 31); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
