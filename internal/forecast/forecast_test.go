package forecast

import (
	"math"
	"testing"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
)

func count(v float64, c bwq.Censoring) bwq.Count {
	return bwq.Count{Value: v, Censoring: c, Present: true}
}

func TestExceededHonoursCensoring(t *testing.T) {
	const thr = 500.0
	cases := []struct {
		name             string
		c                bwq.Count
		wantExc, wantDet bool
	}{
		{"actual above", count(890, bwq.Actual), true, true},
		{"actual below", count(120, bwq.Actual), false, true},
		{"actual equal-not-above", count(500, bwq.Actual), false, true},
		{"lessThan well below", count(10, bwq.LessThan), false, true},        // "<10" can't exceed 500
		{"lessThan above thr", count(600, bwq.LessThan), false, false},       // "<600" indeterminate
		{"greaterThan above", count(15000, bwq.GreaterThan), true, true},     // ">15000" exceeds 500
		{"greaterThan below thr", count(100, bwq.GreaterThan), false, false}, // ">100" indeterminate
		{"absent", bwq.Count{Present: false}, false, false},
	}
	for _, tc := range cases {
		exc, det := Exceeded(tc.c, thr)
		if exc != tc.wantExc || det != tc.wantDet {
			t.Errorf("%s: Exceeded = (%v,%v), want (%v,%v)", tc.name, exc, det, tc.wantExc, tc.wantDet)
		}
	}
}

func at(s string) time.Time {
	tm, _ := time.Parse("2006-01-02", s)
	return tm
}

func TestResolveNoLeakageAndMatch(t *testing.T) {
	site := Site{
		Point: "04700",
		Samples: []bwq.Sample{
			{SamplePoint: "04700", Time: at("2025-06-25"), EColi: count(50, bwq.Actual)},  // before commit
			{SamplePoint: "04700", Time: at("2025-07-03"), EColi: count(900, bwq.Actual)}, // first after commit
			{SamplePoint: "04700", Time: at("2025-07-10"), EColi: count(20, bwq.LessThan)},
		},
	}
	p := Prediction{Point: "04700", CommitTime: at("2025-07-01"), Threshold: 500, PExceed: 0.8}

	res, ok := Resolve(p, site)
	if !ok {
		t.Fatal("expected a resolution")
	}
	// Must match the 2025-07-03 sample (strictly after commit), never the prior one.
	if !res.SampleTime.Equal(at("2025-07-03")) {
		t.Errorf("matched wrong sample: %s, want 2025-07-03", res.SampleTime.Format("2006-01-02"))
	}
	if !res.Exceeded {
		t.Error("900 > 500 should be an exceedance")
	}
	wantBrier := (0.8 - 1.0) * (0.8 - 1.0)
	if math.Abs(res.Brier-wantBrier) > 1e-12 {
		t.Errorf("Brier = %v, want %v", res.Brier, wantBrier)
	}
}

func TestResolveSkipsWhenNoLaterSample(t *testing.T) {
	site := Site{Samples: []bwq.Sample{{Time: at("2025-06-25"), EColi: count(50, bwq.Actual)}}}
	p := Prediction{CommitTime: at("2025-07-01"), Threshold: 500}
	if _, ok := Resolve(p, site); ok {
		t.Error("expected no resolution when every sample precedes the commit")
	}
}

func TestResolveSkipsIndeterminateCensoring(t *testing.T) {
	site := Site{Samples: []bwq.Sample{{Time: at("2025-07-03"), EColi: count(600, bwq.LessThan)}}}
	p := Prediction{CommitTime: at("2025-07-01"), Threshold: 500}
	if _, ok := Resolve(p, site); ok {
		t.Error("expected no resolution for censoring-indeterminate outcome")
	}
}

func TestAggregateScoring(t *testing.T) {
	// Two confident, correct forecasts and one confident, wrong one.
	rs := []Resolution{
		mkRes(0.9, true),
		mkRes(0.1, false),
		mkRes(0.9, false), // wrong
	}
	sc := Aggregate(rs)
	if sc.N != 3 || sc.Exceedances != 1 {
		t.Fatalf("N/exceedances = %d/%d, want 3/1", sc.N, sc.Exceedances)
	}
	if math.Abs(sc.BaseRate-1.0/3.0) > 1e-9 {
		t.Errorf("base rate = %v, want 1/3", sc.BaseRate)
	}
	wantBrier := ((0.9-1)*(0.9-1) + (0.1-0)*(0.1-0) + (0.9-0)*(0.9-0)) / 3
	if math.Abs(sc.MeanBrier-wantBrier) > 1e-9 {
		t.Errorf("mean Brier = %v, want %v", sc.MeanBrier, wantBrier)
	}
	// A perfectly-calibrated set should beat the base rate (positive skill).
	perfect := []Resolution{mkRes(1, true), mkRes(0, false), mkRes(0, false), mkRes(1, true)}
	if s := Aggregate(perfect); s.BrierSkill <= 0.99 {
		t.Errorf("perfect forecasts should have Brier skill ≈ 1, got %v", s.BrierSkill)
	}
}

func mkRes(p float64, exceeded bool) Resolution {
	y := 0.0
	if exceeded {
		y = 1
	}
	return Resolution{
		Prediction: Prediction{PExceed: p},
		Exceeded:   exceeded,
		Brier:      (p - y) * (p - y),
		LogLoss:    logLoss(p, exceeded),
	}
}

func TestRetrospectiveProvenance(t *testing.T) {
	live := Commit{CommitTime: at("2025-07-01"), GeneratedAt: at("2025-07-01").Add(2 * time.Hour)}
	if live.Retrospective() {
		t.Error("a ledger written hours after its commit is a genuine commitment, not retrospective")
	}
	back := Commit{CommitTime: at("2025-07-01"), GeneratedAt: at("2026-06-19")}
	if !back.Retrospective() {
		t.Error("a ledger written a year after its commit date must be flagged retrospective")
	}
}

func TestTrainingSetRespectsCutoffAndCoverage(t *testing.T) {
	site := Site{
		WindowDays: 1,
		Samples: []bwq.Sample{
			{Time: at("2025-06-20").Add(10 * time.Hour), EColi: count(100, bwq.Actual)},  // in: before cutoff, has rain
			{Time: at("2025-06-27").Add(10 * time.Hour), EColi: count(40, bwq.LessThan)}, // out: no rain reading that day
			{Time: at("2025-07-05").Add(10 * time.Hour), EColi: count(200, bwq.Actual)},  // out: after cutoff
		},
		Rain: []hydro.Reading{
			{Date: at("2025-06-20"), Value: 3.0, Valid: true},
		},
	}
	obs := site.TrainingSet(at("2025-07-01"))
	if len(obs) != 1 {
		t.Fatalf("expected 1 training obs (cutoff + coverage filtering), got %d", len(obs))
	}
	if obs[0].Covars[0] != 3.0 {
		t.Errorf("covariate = %v, want 3.0 antecedent mm", obs[0].Covars[0])
	}
}
