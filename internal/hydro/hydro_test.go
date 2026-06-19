package hydro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return b
}

func TestParseStations(t *testing.T) {
	var resp struct {
		Items []rawStation `json:"items"`
	}
	if err := json.Unmarshal(read(t, "stations-near.json"), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Items) == 0 {
		t.Fatal("no stations in fixture")
	}
	s := resp.Items[0].toStation()
	if s.ID == "" || s.Label == "" {
		t.Errorf("station missing id/label: %+v", s)
	}
	if s.Lat == 0 || s.Long == 0 {
		t.Errorf("station missing coordinates: %+v", s)
	}
	m, ok := s.DailyRainfall()
	if !ok {
		t.Fatal("expected a daily (86400s) rainfall measure")
	}
	if m.PeriodSec != 86400 || m.Parameter != "rainfall" {
		t.Errorf("wrong measure resolved: %+v", m)
	}
}

func TestParseReadings(t *testing.T) {
	var resp struct {
		Items []rawReading `json:"items"`
	}
	if err := json.Unmarshal(read(t, "readings-daily.json"), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Items) == 0 {
		t.Fatal("no readings in fixture")
	}
	var validCount int
	for _, raw := range resp.Items {
		r := raw.toReading()
		if r.Date.IsZero() {
			t.Errorf("reading has zero date: %+v", raw)
		}
		if r.Valid {
			validCount++
			if r.Value < 0 {
				t.Errorf("negative rainfall: %v", r.Value)
			}
		}
	}
	if validCount == 0 {
		t.Error("expected at least one valid reading")
	}
}

func TestMeasurePath(t *testing.T) {
	full := "http://environment.data.gov.uk/hydrology/id/measures/abc-rainfall-t-86400-mm-qualified"
	if got, want := measurePath(full), "/id/measures/abc-rainfall-t-86400-mm-qualified"; got != want {
		t.Errorf("measurePath(full) = %q, want %q", got, want)
	}
	if got, want := measurePath("abc-123"), "/id/measures/abc-123"; got != want {
		t.Errorf("measurePath(bare) = %q, want %q", got, want)
	}
}
