package siteload

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
)

type cacheEntry struct {
	CachedAt time.Time     `json:"cachedAt"`
	Window   int           `json:"window"`
	Site     forecast.Site `json:"site"`
}

// LoadCached behaves like Load but persists each fetched site to cacheDir and
// reuses it on subsequent runs. The historical bulk of a site's record — decades
// of samples and rainfall — never changes, so refetching it every run is wasted
// network time; only the most recent in-season sample is ever new. A cached pull
// is reused until it is older than maxAge (maxAge <= 0 never expires) or refresh
// forces a re-fetch. The model is always re-fitted by the caller from the cached
// samples, so only the network fetch is skipped — never the fit.
//
// When the refresh fetch fails, an expired entry is served rather than dropped:
// a site slightly behind on samples is a far better export than no site at all
// (a throttled run would otherwise silently publish a fraction of the map).
func LoadCached(
	ctx context.Context,
	bw *bwq.Client,
	hy *hydro.Client,
	point string,
	lat, long float64,
	name string,
	distKm float64,
	window int,
	cacheDir string,
	maxAge time.Duration,
	refresh bool,
) (forecast.Site, bool, error) {
	path := filepath.Join(cacheDir, point+".json")
	var stale forecast.Site
	var haveStale bool
	if cacheDir != "" {
		if e, ok := readEntry(path, window); ok {
			if !refresh && (maxAge <= 0 || time.Since(e.CachedAt) < maxAge) {
				return e.Site, true, nil // cache hit
			}
			stale, haveStale = e.Site, true // expired: refetch, but keep as a fallback
		}
	}

	// The clients already back off through throttles, so a failure here is a
	// genuine one (a dead point, or an outage) rather than a dropped request.
	site, err := Load(ctx, bw, hy, point, lat, long, name, distKm, window)
	if err != nil {
		if haveStale {
			return stale, true, nil
		}
		return site, false, err
	}
	if cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o755); err == nil {
			if b, err := json.Marshal(cacheEntry{CachedAt: time.Now().UTC(), Window: window, Site: site}); err == nil {
				_ = os.WriteFile(path, b, 0o644)
			}
		}
	}
	return site, false, nil
}

// readEntry loads a usable cache entry for the given window, if one is there.
func readEntry(path string, window int) (cacheEntry, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if json.Unmarshal(b, &e) != nil || e.Window != window || len(e.Site.Samples) == 0 {
		return cacheEntry{}, false
	}
	return e, true
}
