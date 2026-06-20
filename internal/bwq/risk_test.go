package bwq

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseRiskPredictions(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "risk-prediction.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	items, err := decodeItems(b)
	if err != nil {
		t.Fatalf("decodeItems: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("no risk predictions in fixture")
	}
	var r rawRisk
	if err := json.Unmarshal(items[0], &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	p := r.toRiskPrediction()
	if p.SamplePoint != "04700" {
		t.Errorf("samplePoint = %q, want 04700", p.SamplePoint)
	}
	if p.Date.IsZero() {
		t.Error("predictedOn date should parse")
	}
	if p.Level != RiskNormal && p.Level != RiskIncreased && p.Level != RiskUnknown {
		t.Errorf("unexpected risk level %v", p.Level)
	}
}

func TestParseRiskLevel(t *testing.T) {
	cases := map[string]RiskLevel{
		"http://environment.data.gov.uk/def/bwq-stp/normal":    RiskNormal,
		"http://environment.data.gov.uk/def/bwq-stp/increased": RiskIncreased,
		"http://environment.data.gov.uk/def/bwq-stp/unknown":   RiskUnknown,
	}
	for uri, want := range cases {
		if got := parseRiskLevel(uri); got != want {
			t.Errorf("parseRiskLevel(%q) = %v, want %v", uri, got, want)
		}
	}
}
