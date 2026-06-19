// Package siteload assembles a forecast.Site for one bathing water: its
// coordinates, full in-season sample history, and the daily-rainfall series of
// its nearest gauge. It is the shared I/O step behind cmd/forecast and
// cmd/exceedance-backtest, keeping the forecast package itself free of network
// concerns.
package siteload

import (
	"context"
	"fmt"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/catchment"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
)

// Load builds a site. If lat or long is zero the coordinates and name are fetched
// from the rBWD compliance feed; pass them in (e.g. from bwq.DesignatedSites) to
// skip that lookup. window is the antecedent-rainfall window in days, distKm the
// rain-gauge search radius.
func Load(ctx context.Context, bw *bwq.Client, hy *hydro.Client, point string, lat, long float64, name string, distKm float64, window int) (forecast.Site, error) {
	if lat == 0 || long == 0 {
		comp, err := bw.Compliance(ctx, bwq.RegimeRBWD, point)
		if err != nil {
			return forecast.Site{}, err
		}
		if len(comp) == 0 || comp[0].Lat == 0 {
			return forecast.Site{}, fmt.Errorf("no coordinates for point %s", point)
		}
		lat, long, name = comp[0].Lat, comp[0].Long, comp[0].BathingWaterName
	}

	samples, err := bw.InSeasonSamples(ctx, point)
	if err != nil {
		return forecast.Site{}, err
	}
	if len(samples) == 0 {
		return forecast.Site{}, fmt.Errorf("no samples for point %s", point)
	}

	stations, err := hy.NearbyStations(ctx, "rainfall", lat, long, distKm)
	if err != nil {
		return forecast.Site{}, err
	}
	gauges := catchment.LinkRainGauges(stations, lat, long)
	if len(gauges) == 0 {
		return forecast.Site{}, fmt.Errorf("no rain gauge within %g km of point %s", distKm, point)
	}
	g := gauges[0]

	oldest := samples[len(samples)-1].Time.AddDate(0, 0, -window)
	readings, err := hy.Readings(ctx, g.DailyMeasureID, oldest, samples[0].Time)
	if err != nil {
		return forecast.Site{}, err
	}

	return forecast.Site{
		Point:      point,
		Name:       name,
		Lat:        lat,
		Long:       long,
		Gauge:      g.Station.Label,
		Samples:    samples,
		Rain:       readings,
		WindowDays: window,
	}, nil
}
