package catchment

import (
	"math"
	"testing"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
)

func TestHaversineKm(t *testing.T) {
	// Spittal bathing water to the Berwick rain gauge ~3 km away.
	d := HaversineKm(55.756857, -1.988831, 55.765373, -2.031043)
	if d < 2 || d > 4 {
		t.Errorf("Spittal→Berwick distance = %.2f km, want ~3 km", d)
	}
	// Zero distance to self.
	if d0 := HaversineKm(51.5, -0.1, 51.5, -0.1); d0 > 1e-9 {
		t.Errorf("self-distance = %v, want 0", d0)
	}
	// A known ~half-degree-of-latitude span (~55.6 km).
	if d := HaversineKm(51.0, 0, 51.5, 0); math.Abs(d-55.6) > 1.0 {
		t.Errorf("0.5° latitude = %.2f km, want ~55.6 km", d)
	}
}

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestAntecedentRainfall(t *testing.T) {
	readings := []hydro.Reading{
		{Date: day("2026-05-30"), Value: 0.6, Valid: true},
		{Date: day("2026-05-31"), Value: 0.2, Valid: true},
		{Date: day("2026-06-01"), Value: 6.1, Valid: true},
		{Date: day("2026-06-02"), Value: 24.2, Valid: true},
		{Date: day("2026-06-03"), Value: 3.4, Valid: true},
	}
	sample := day("2026-06-02").Add(10 * time.Hour) // sampled mid-day

	// 1-day window = the sample day itself.
	if total, cov := AntecedentRainfall(readings, sample, 1); total != 24.2 || cov != 1 {
		t.Errorf("1-day = %.1f (cov %d), want 24.2 (1)", total, cov)
	}
	// 2-day window = sample day + the day before; must not include the day after.
	if total, cov := AntecedentRainfall(readings, sample, 2); math.Abs(total-30.3) > 1e-9 || cov != 2 {
		t.Errorf("2-day = %.1f (cov %d), want 30.3 (2)", total, cov)
	}
	// 4-day window reaches back to 2026-05-30.
	if total, cov := AntecedentRainfall(readings, sample, 4); math.Abs(total-31.1) > 1e-9 || cov != 4 {
		t.Errorf("4-day = %.1f (cov %d), want 31.1 (4)", total, cov)
	}
}

func TestRainfallBetweenLaggedWindow(t *testing.T) {
	readings := []hydro.Reading{
		{Date: day("2026-05-30"), Value: 0.6, Valid: true},
		{Date: day("2026-05-31"), Value: 0.2, Valid: true},
		{Date: day("2026-06-01"), Value: 6.1, Valid: true},
		{Date: day("2026-06-02"), Value: 24.2, Valid: true},
		{Date: day("2026-06-03"), Value: 3.4, Valid: true},
	}
	sample := day("2026-06-02").Add(10 * time.Hour)

	// "prior" lag = days 2–4 back = 2026-05-29..05-31 → only 05-30 and 05-31 present.
	if total, cov := RainfallBetween(readings, sample, 4, 2); math.Abs(total-0.8) > 1e-9 || cov != 2 {
		t.Errorf("lag[4..2] = %.1f (cov %d), want 0.8 (2)", total, cov)
	}
	// AntecedentRainfall(days=2) must equal RainfallBetween(1,0).
	a, _ := AntecedentRainfall(readings, sample, 2)
	b, _ := RainfallBetween(readings, sample, 1, 0)
	if a != b {
		t.Errorf("AntecedentRainfall(2)=%.1f should equal RainfallBetween(1,0)=%.1f", a, b)
	}
}

func TestAntecedentRainfallSkipsInvalid(t *testing.T) {
	readings := []hydro.Reading{
		{Date: day("2026-06-01"), Value: 6.1, Valid: true},
		{Date: day("2026-06-02"), Value: 99, Valid: false}, // missing/invalid — must be ignored
	}
	total, cov := AntecedentRainfall(readings, day("2026-06-02"), 2)
	if total != 6.1 || cov != 1 {
		t.Errorf("invalid reading not skipped: total=%.1f cov=%d, want 6.1/1", total, cov)
	}
}

func TestLinkRainGaugesSortsByDistance(t *testing.T) {
	stations := []hydro.Station{
		{ID: "far", Lat: 55.9, Long: -2.2, Measures: []hydro.Measure{{ID: "far-m", Parameter: "rainfall", PeriodSec: 86400}}},
		{ID: "near", Lat: 55.765, Long: -2.031, Measures: []hydro.Measure{{ID: "near-m", Parameter: "rainfall", PeriodSec: 86400}}},
		{ID: "nodaily", Lat: 55.76, Long: -2.0, Measures: []hydro.Measure{{ID: "x", Parameter: "rainfall", PeriodSec: 900}}},
	}
	gauges := LinkRainGauges(stations, 55.756857, -1.988831)
	if len(gauges) != 2 {
		t.Fatalf("expected 2 daily-rainfall gauges (nodaily skipped), got %d", len(gauges))
	}
	if gauges[0].Station.ID != "near" {
		t.Errorf("nearest should be 'near', got %q", gauges[0].Station.ID)
	}
	if gauges[0].DistanceKm > gauges[1].DistanceKm {
		t.Errorf("gauges not sorted by distance")
	}
	if gauges[0].DailyMeasureID != "near-m" {
		t.Errorf("wrong daily measure id: %q", gauges[0].DailyMeasureID)
	}
}
