package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestGetRetriesThrottle is the behaviour the whole-catalogue export depends on:
// the EA gateway answers a burst with 403, and a client that gives up there
// loses the site. The retry must clear it.
func TestGetRetriesThrottle(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.Header().Set("Retry-After", "0") // keep the test quick
			http.Error(w, "throttled", http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	body, err := Get(context.Background(), srv.Client(), "test", srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", body)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

// A status that will never fix itself must fail on the first try rather than
// burning the backoff budget.
func TestGetDoesNotRetryClientError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := Get(context.Background(), srv.Client(), "test", srv.URL); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// A throttle that never lifts must give up rather than hang the export.
func TestGetGivesUpAfterMaxAttempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, "throttled", http.StatusForbidden)
	}))
	defer srv.Close()

	if _, err := Get(context.Background(), srv.Client(), "test", srv.URL); err == nil {
		t.Fatal("expected an error")
	}
	if int(calls) != MaxAttempts {
		t.Errorf("calls = %d, want %d", calls, MaxAttempts)
	}
}

// A cancelled context must abort the backoff rather than sleep through it.
func TestGetHonoursContextDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "throttled", http.StatusForbidden)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Get(ctx, srv.Client(), "test", srv.URL); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > baseDelay {
		t.Errorf("waited %v, want the backoff cut short by the context", elapsed)
	}
}
