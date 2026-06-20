// Package catchment links a bathing-water sampling point to the upstream rain
// (and, later, flow) gauges whose antecedent readings drive its exceedance risk.
//
// This first cut links by distance: the nearest rainfall gauges to the site's
// coordinates. The PLAN's fuller linkage uses each water's published zone of
// influence and named storm-overflow/outfall features to choose hydrologically
// upstream gauges rather than merely near ones; that refinement layers on top of
// the same types and the same antecedent-rainfall machinery established here.
package catchment

import (
	"math"
	"sort"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
)

// RainGauge is a rainfall station linked to a site, with its distance and the
// daily-rainfall measure to read.
type RainGauge struct {
	Station        hydro.Station
	DistanceKm     float64
	DailyMeasureID string
}

// LinkRainGauges returns the rainfall gauges reporting a daily total within
// distKm of (lat, long), nearest first. Gauges without a daily-rainfall measure
// are skipped.
func LinkRainGauges(stations []hydro.Station, lat, long float64) []RainGauge {
	var gauges []RainGauge
	for _, s := range stations {
		m, ok := s.DailyRainfall()
		if !ok {
			continue
		}
		gauges = append(gauges, RainGauge{
			Station:        s,
			DistanceKm:     HaversineKm(lat, long, s.Lat, s.Long),
			DailyMeasureID: m.ID,
		})
	}
	sort.Slice(gauges, func(i, j int) bool { return gauges[i].DistanceKm < gauges[j].DistanceKm })
	return gauges
}

// AntecedentRainfall sums valid daily rainfall over the window ending on the
// sample day: the days in [sampleDay − (days−1), sampleDay] inclusive, so days=1
// is the sample day itself and days=2 is "today plus yesterday". It reports the
// total and how many of the window's days had a valid reading, so callers can
// reject windows with gaps.
func AntecedentRainfall(readings []hydro.Reading, sampleTime time.Time, days int) (total float64, covered int) {
	if days < 1 {
		return 0, 0
	}
	return RainfallBetween(readings, sampleTime, days-1, 0)
}

// RainfallBetween sums valid daily rainfall over a lagged window: the days in
// [sampleDay − startDaysBack, sampleDay − endDaysBack] inclusive, with
// startDaysBack ≥ endDaysBack ≥ 0. It is the building block for several rain-lag
// covariates — e.g. RainfallBetween(.,.,1,0) is the 2-day antecedent total, while
// RainfallBetween(.,.,6,2) is the "prior-week" lag (days 2–6 before the sample),
// letting a model separate fresh runoff from earlier catchment wetting.
func RainfallBetween(readings []hydro.Reading, sampleTime time.Time, startDaysBack, endDaysBack int) (total float64, covered int) {
	if startDaysBack < endDaysBack || endDaysBack < 0 {
		return 0, 0
	}
	sampleDay := truncateDay(sampleTime)
	lo := sampleDay.AddDate(0, 0, -startDaysBack)
	hi := sampleDay.AddDate(0, 0, -endDaysBack)
	for _, r := range readings {
		if !r.Valid {
			continue
		}
		d := truncateDay(r.Date)
		if !d.Before(lo) && !d.After(hi) {
			total += r.Value
			covered++
		}
	}
	return total, covered
}

// HaversineKm returns the great-circle distance in kilometres between two
// WGS84 points.
func HaversineKm(lat1, long1, lat2, long2 float64) float64 {
	const earthKm = 6371.0088
	φ1, φ2 := rad(lat1), rad(lat2)
	dφ := rad(lat2 - lat1)
	dλ := rad(long2 - long1)
	a := math.Sin(dφ/2)*math.Sin(dφ/2) +
		math.Cos(φ1)*math.Cos(φ2)*math.Sin(dλ/2)*math.Sin(dλ/2)
	return earthKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func rad(deg float64) float64 { return deg * math.Pi / 180 }

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
