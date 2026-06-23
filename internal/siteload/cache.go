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
	if !refresh && cacheDir != "" {
		if b, err := os.ReadFile(path); err == nil {
			var e cacheEntry
			if json.Unmarshal(b, &e) == nil && e.Window == window && len(e.Site.Samples) > 0 &&
				(maxAge <= 0 || time.Since(e.CachedAt) < maxAge) {
				return e.Site, true, nil // cache hit
			}
		}
	}

	// Network fetch, with a couple of retries: under concurrency the EA API
	// occasionally drops a request, and a transient miss would otherwise leave a
	// gap in the export.
	var site forecast.Site
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return site, false, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		site, err = Load(ctx, bw, hy, point, lat, long, name, distKm, window)
		if err == nil {
			break
		}
	}
	if err != nil {
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
