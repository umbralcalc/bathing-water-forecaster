package siteload

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
)

// writeEntry plants a cache entry aged by the given duration.
func writeEntry(t *testing.T, dir, point string, age time.Duration) {
	t.Helper()
	e := cacheEntry{
		CachedAt: time.Now().UTC().Add(-age),
		Window:   2,
		Site: forecast.Site{
			Point:   point,
			Name:    "Cached Bay",
			Samples: []bwq.Sample{{SamplePoint: point, Time: time.Now().AddDate(0, 0, -30)}},
		},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, point+".json"), b, 0o644); err != nil {
		t.Fatalf("write entry: %v", err)
	}
}

// deadClients point at a server that refuses every request, standing in for an
// API outage. 404 is not retryable, so the fetch fails without any backoff.
func deadClients(t *testing.T) (*bwq.Client, *hydro.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	bw, hy := bwq.New(), hydro.New()
	bw.BaseURL, hy.BaseURL = srv.URL, srv.URL
	return bw, hy
}

// TestLoadCachedServesStaleOnFetchFailure is the guard against a throttled or
// half-offline run publishing a fraction of the sites: an expired entry is worth
// far more than dropping the site entirely.
func TestLoadCachedServesStaleOnFetchFailure(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "01234", 30*24*time.Hour) // far past any sane maxAge
	bw, hy := deadClients(t)

	site, cached, err := LoadCached(context.Background(), bw, hy, "01234", 50.1, -1.2, "", 15, 2, dir, 7*24*time.Hour, false)
	if err != nil {
		t.Fatalf("LoadCached: %v", err)
	}
	if !cached || site.Name != "Cached Bay" {
		t.Errorf("got cached=%v name=%q, want the stale entry", cached, site.Name)
	}
}

// A fresh entry must never reach the network at all.
func TestLoadCachedUsesFreshEntry(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "01234", time.Hour)
	bw, hy := deadClients(t)

	site, cached, err := LoadCached(context.Background(), bw, hy, "01234", 50.1, -1.2, "", 15, 2, dir, 7*24*time.Hour, false)
	if err != nil || !cached || site.Name != "Cached Bay" {
		t.Fatalf("got site=%q cached=%v err=%v, want the fresh entry", site.Name, cached, err)
	}
}

// With nothing cached there is nothing to fall back to, so the error must
// surface rather than yielding an empty site.
func TestLoadCachedReportsFailureWithoutCache(t *testing.T) {
	bw, hy := deadClients(t)
	if _, _, err := LoadCached(context.Background(), bw, hy, "01234", 50.1, -1.2, "", 15, 2, t.TempDir(), 7*24*time.Hour, false); err == nil {
		t.Fatal("expected an error when the fetch fails and no cache exists")
	}
}
