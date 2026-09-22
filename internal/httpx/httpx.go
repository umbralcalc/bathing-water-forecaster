// Package httpx holds the shared GET used by the EA API clients. Both the
// bathing-water and hydrology endpoints sit behind the same gateway, which
// throttles a burst of concurrent requests with a 403 (not the conventional
// 429) and recovers within seconds. Without a backoff a whole-catalogue export
// loses most of its sites to throttling, so every request retries here rather
// than in each caller.
package httpx

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// MaxAttempts is the number of tries a single GET makes before giving up, and
// baseDelay the first backoff step; each further attempt doubles it.
const (
	MaxAttempts = 5
	baseDelay   = 2 * time.Second
)

// userAgent identifies the exporter to the EA gateway, which is friendlier to a
// named client than to a bare Go default.
const userAgent = "bathing-water-forecaster (+https://github.com/umbralcalc/bathing-water-forecaster)"

// hint says whether a failed attempt is worth repeating and, when the server
// asked for a specific pause, how long to wait before it.
type hint struct {
	retry bool
	after time.Duration
	given bool // the server sent a Retry-After; otherwise back off
}

// Get fetches u as JSON, retrying throttles (403/429), server errors and
// connection failures with an exponential backoff. It returns a body only for a
// 200; label names the API in the error, e.g. "bwq".
func Get(ctx context.Context, hc *http.Client, label, u string) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		body, h, err := getOnce(ctx, hc, label, u)
		if err == nil {
			return body, nil
		}
		if !h.retry || attempt == MaxAttempts {
			return nil, err
		}
		wait := h.after
		if !h.given {
			wait = backoff(attempt)
		}
		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// getOnce performs a single request, reporting whether its failure is worth
// another attempt.
func getOnce(ctx context.Context, hc *http.Client, label, u string) ([]byte, hint, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, hint{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, hint{}, err
		}
		return nil, hint{retry: true}, fmt.Errorf("%s: GET %s: %w", label, u, err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		if readErr != nil {
			return nil, hint{retry: true}, fmt.Errorf("%s: GET %s: reading body: %w", label, u, readErr)
		}
		return body, hint{}, nil
	}
	statusErr := fmt.Errorf("%s: GET %s: status %d: %s", label, u, resp.StatusCode, snippet(body))
	if !retryable(resp.StatusCode) {
		return nil, hint{}, statusErr
	}
	after, given := retryAfter(resp.Header)
	return nil, hint{retry: true, after: after, given: given}, statusErr
}

// retryable reports whether status is worth another attempt: the gateway's
// throttle (403), the conventional rate limit (429), and any server-side error.
func retryable(status int) bool {
	return status == http.StatusForbidden || status == http.StatusTooManyRequests || status >= 500
}

// backoff doubles per attempt and adds jitter so concurrent workers, which are
// throttled together, do not all come back at the same instant.
func backoff(attempt int) time.Duration {
	d := baseDelay << (attempt - 1)
	return d + time.Duration(rand.Int63n(int64(d/2)))
}

func retryAfter(h http.Header) (time.Duration, bool) {
	v := h.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
	}
	return 0, false
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func snippet(b []byte) string {
	const n = 200
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
