package bwq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func loadItems(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// TestParseInSeasonSamples checks the in-season decoder against a real captured
// page, and asserts the load-bearing property: censored counts keep their
// qualifier rather than collapsing to a point value.
func TestParseInSeasonSamples(t *testing.T) {
	items, err := decodeItems(loadItems(t, "in-season-sample.json"))
	if err != nil {
		t.Fatalf("decodeItems: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("no items in fixture")
	}

	var samples []Sample
	for _, it := range items {
		var r rawSample
		if err := json.Unmarshal(it, &r); err != nil {
			t.Fatalf("unmarshal item: %v", err)
		}
		samples = append(samples, r.toSample())
	}

	var sawCensored, sawActual bool
	for _, s := range samples {
		if s.SamplePoint == "" {
			t.Errorf("sample missing samplePoint: %+v", s)
		}
		if s.Time.IsZero() {
			t.Errorf("sample %s has zero time", s.Week)
		}
		if !s.EColi.Present {
			t.Errorf("sample %s missing E. coli count", s.Week)
		}
		switch s.EColi.Censoring {
		case LessThan, GreaterThan:
			sawCensored = true
			if s.EColi.Value <= 0 {
				t.Errorf("censored count should still carry its limit value, got %v", s.EColi.Value)
			}
		case Actual:
			sawActual = true
		}
	}
	if !sawCensored {
		t.Error("fixture expected to contain at least one censored (< or >) count")
	}
	if !sawActual {
		t.Error("fixture expected to contain at least one actual (=) count")
	}
}

func TestParseComplianceRBWD(t *testing.T) {
	items, err := decodeItems(loadItems(t, "compliance-rbwd.json"))
	if err != nil {
		t.Fatalf("decodeItems: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("no items in fixture")
	}
	var r rawCompliance
	if err := json.Unmarshal(items[0], &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c := r.toCompliance(RegimeRBWD)
	if c.Year < 2015 {
		t.Errorf("rBWD year should be >= 2015, got %d", c.Year)
	}
	if c.ClassName == "" || c.ClassCode == "" {
		t.Errorf("missing classification: %+v", c)
	}
	if c.Lat == 0 || c.Long == 0 {
		t.Errorf("expected coordinates on compliance item, got lat=%v long=%v", c.Lat, c.Long)
	}
}

func TestParseComplianceEEC(t *testing.T) {
	items, err := decodeItems(loadItems(t, "compliance-eec.json"))
	if err != nil {
		t.Fatalf("decodeItems: %v", err)
	}
	var r rawCompliance
	if err := json.Unmarshal(items[0], &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c := r.toCompliance(RegimeEEC)
	if c.Year < 1988 || c.Year > 2014 {
		t.Errorf("EEC year expected in 1988..2014, got %d", c.Year)
	}
}

func TestDedupeSamples(t *testing.T) {
	mk := func(point, day string, rec string, ec float64) Sample {
		tt, _ := time.Parse("2006-01-02", day)
		rd, _ := time.Parse("2006-01-02", rec)
		return Sample{SamplePoint: point, Time: tt, RecordDate: rd, EColi: Count{Value: ec, Present: true}}
	}
	in := []Sample{
		mk("03600", "2019-06-14", "2021-04-12", 8500), // latest revision — should win
		mk("03600", "2019-06-14", "2019-06-14", 100),
		mk("03600", "2019-06-14", "2019-09-10", 200),
		mk("03600", "2019-06-07", "2019-06-07", 50), // distinct sample
		mk("04000", "2019-06-14", "2019-06-14", 70), // distinct point
	}
	out := dedupeSamples(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 distinct samples, got %d", len(out))
	}
	for _, s := range out {
		if s.SamplePoint == "03600" && s.Time.Format("2006-01-02") == "2019-06-14" {
			if s.EColi.Value != 8500 {
				t.Errorf("dedup kept wrong revision: E.coli=%v, want 8500 (latest recordDate)", s.EColi.Value)
			}
		}
	}
}

func TestCensoringString(t *testing.T) {
	cases := map[Censoring]string{Actual: "=", LessThan: "<", GreaterThan: ">"}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("Censoring(%d).String() = %q, want %q", c, got, want)
		}
	}
}
