// Package forecast implements the proof-of-commit loop: it freezes a calibrated
// P(exceedance) for each site before the weekly sample is taken, then settles
// those commitments against the lab result once it publishes, with explicit
// no-leakage discipline and honest scoring.
//
// The loop's credibility rests on two properties this package enforces:
//
//   - Training and covariates are bounded strictly before the commit time, so a
//     forecast can never see its own outcome.
//   - Resolution matches a commitment only to a sample taken strictly after the
//     commit time, and reports exceedance in a way that respects censoring (a
//     "< 10" reading cannot be an exceedance of a higher threshold; a
//     "> 15000" reading certainly is).
package forecast

import (
	"math"
	"sort"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/catchment"
	"github.com/umbralcalc/bathing-water-forecaster/internal/exceedance"
	"github.com/umbralcalc/bathing-water-forecaster/internal/hydro"
)

// ModelParams records the fitted single-site model behind a prediction, so a
// commitment is fully reproducible from the ledger.
type ModelParams struct {
	Beta  []float64 `json:"beta"` // Beta[0] intercept, Beta[1] rain coefficient
	Sigma float64   `json:"sigma"`
}

// Prediction is one committed forecast for the next weekly sample at a site.
type Prediction struct {
	Point            string      `json:"point"`
	Name             string      `json:"name"`
	CommitTime       time.Time   `json:"commitTime"`
	Determinand      string      `json:"determinand"`
	Threshold        float64     `json:"threshold"`
	PExceed          float64     `json:"pExceed"`
	WindowDays       int         `json:"windowDays"`
	AntecedentRainMM float64     `json:"antecedentRainMM"`
	Gauge            string      `json:"gauge"`
	TrainN           int         `json:"trainN"`
	Model            ModelParams `json:"model"`
}

// Commit is a full forecasting run: every site's commitment for one region at one
// commit time — the unit written to data/predictions/.
//
// GeneratedAt is the wall-clock time the ledger was actually written. It is the
// anti-backdating guard of the whole scheme: a credible proof-of-commit ledger
// has GeneratedAt at or before CommitTime, whereas a retrospective backtest
// (commit date set in the past via -as-of) is self-evidently marked by a
// GeneratedAt far later than CommitTime. The provenance travels with the ledger
// so a forecast can never be silently presented as something it is not.
type Commit struct {
	Region      string       `json:"region"`
	CommitTime  time.Time    `json:"commitTime"`
	GeneratedAt time.Time    `json:"generatedAt"`
	Predictions []Prediction `json:"predictions"`
}

// Retrospective reports whether the ledger was written materially after its claimed
// commit time — i.e. it is a backtest, not a genuine forward commitment.
func (c Commit) Retrospective() bool {
	return c.GeneratedAt.Sub(c.CommitTime) > 24*time.Hour
}

// Site bundles everything needed to fit one site's model and form a commitment.
type Site struct {
	Point      string
	Name       string
	Lat, Long  float64
	Gauge      string
	Samples    []bwq.Sample    // any order
	Rain       []hydro.Reading // daily rainfall from the linked gauge
	WindowDays int
}

// TrainingSet builds censored observations from samples taken strictly before
// cutoff that have full antecedent-rainfall coverage — the leakage-free training
// data for a commitment at cutoff.
func (s Site) TrainingSet(cutoff time.Time) []exceedance.CovObservation {
	var obs []exceedance.CovObservation
	for _, sm := range s.Samples {
		if !sm.Time.Before(cutoff) {
			continue
		}
		o, ok := exceedance.ObservationFromCount(sm.EColi)
		if !ok {
			continue
		}
		rain, cov := catchment.AntecedentRainfall(s.Rain, sm.Time, s.WindowDays)
		if cov != s.WindowDays {
			continue
		}
		obs = append(obs, exceedance.CovObservation{
			LogValue:  o.LogValue,
			Censoring: o.Censoring,
			Covars:    []float64{rain},
		})
	}
	return obs
}

// AntecedentAsOf returns the antecedent rainfall over the window ending at t,
// using only rainfall observed up to t — the covariate frozen into a commitment.
func (s Site) AntecedentAsOf(t time.Time) (float64, bool) {
	rain, cov := catchment.AntecedentRainfall(s.Rain, t, s.WindowDays)
	return rain, cov == s.WindowDays
}

// NextSampleAfter returns the earliest sample taken strictly after t.
func (s Site) NextSampleAfter(t time.Time) (bwq.Sample, bool) {
	ordered := append([]bwq.Sample(nil), s.Samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })
	for _, sm := range ordered {
		if sm.Time.After(t) {
			return sm, true
		}
	}
	return bwq.Sample{}, false
}

// Resolution settles one commitment against the sample that followed it.
type Resolution struct {
	Prediction Prediction `json:"prediction"`
	SampleTime time.Time  `json:"sampleTime"`
	Count      float64    `json:"count"`
	Censoring  string     `json:"censoring"`
	Exceeded   bool       `json:"exceeded"`
	Brier      float64    `json:"brier"`
	LogLoss    float64    `json:"logLoss"`
}

// Exceeded reports whether a count exceeds threshold, honouring censoring. The
// second return is false when censoring makes the outcome indeterminate (e.g.
// "< 600" against a threshold of 500), in which case the sample cannot score.
func Exceeded(c bwq.Count, threshold float64) (exceeded, determinate bool) {
	if !c.Present {
		return false, false
	}
	switch c.Censoring {
	case bwq.LessThan:
		// True value is below c.Value. Determinate only if the whole interval
		// sits at or below the threshold.
		if c.Value <= threshold {
			return false, true
		}
		return false, false
	case bwq.GreaterThan:
		// True value is above c.Value. Determinate only if the whole interval
		// sits at or above the threshold.
		if c.Value >= threshold {
			return true, true
		}
		return false, false
	default:
		return c.Value > threshold, true
	}
}

// Resolve matches a prediction to the next sample after its commit time and
// scores it. ok is false when there is no later sample yet, or when the outcome
// is censoring-indeterminate. It panics-free; the no-leakage check (sample strictly
// after commit) is intrinsic to NextSampleAfter.
func Resolve(p Prediction, s Site) (Resolution, bool) {
	sample, found := s.NextSampleAfter(p.CommitTime)
	if !found {
		return Resolution{}, false
	}
	exceeded, determinate := Exceeded(sample.EColi, p.Threshold)
	if !determinate {
		return Resolution{}, false
	}
	y := boolToFloat(exceeded)
	return Resolution{
		Prediction: p,
		SampleTime: sample.Time,
		Count:      sample.EColi.Value,
		Censoring:  sample.EColi.Censoring.String(),
		Exceeded:   exceeded,
		Brier:      (p.PExceed - y) * (p.PExceed - y),
		LogLoss:    logLoss(p.PExceed, exceeded),
	}, true
}

// Score aggregates resolved commitments into the headline calibration metrics.
type Score struct {
	N               int     `json:"n"`
	Exceedances     int     `json:"exceedances"`
	BaseRate        float64 `json:"baseRate"`
	MeanBrier       float64 `json:"meanBrier"`
	MeanLogLoss     float64 `json:"meanLogLoss"`
	BrierSkill      float64 `json:"brierSkillScore"` // vs the base-rate forecast
	ReliabilityBins []Bin   `json:"reliabilityBins"`
}

// Bin is one point of the reliability curve.
type Bin struct {
	Lo        float64 `json:"lo"`
	Hi        float64 `json:"hi"`
	N         int     `json:"n"`
	MeanP     float64 `json:"meanForecast"`
	Empirical float64 `json:"empiricalRate"`
}

// Aggregate computes the score over a set of resolutions.
func Aggregate(rs []Resolution) Score {
	var sc Score
	sc.N = len(rs)
	if sc.N == 0 {
		return sc
	}
	var sumBrier, sumLog float64
	for _, r := range rs {
		if r.Exceeded {
			sc.Exceedances++
		}
		sumBrier += r.Brier
		sumLog += r.LogLoss
	}
	sc.BaseRate = float64(sc.Exceedances) / float64(sc.N)
	sc.MeanBrier = sumBrier / float64(sc.N)
	sc.MeanLogLoss = sumLog / float64(sc.N)

	// Brier skill score against the constant base-rate forecast.
	baseBrier := sc.BaseRate * (1 - sc.BaseRate)
	if baseBrier > 0 {
		sc.BrierSkill = 1 - sc.MeanBrier/baseBrier
	}
	sc.ReliabilityBins = reliability(rs, 5)
	return sc
}

func reliability(rs []Resolution, nbins int) []Bin {
	bins := make([]Bin, nbins)
	for i := range bins {
		bins[i].Lo = float64(i) / float64(nbins)
		bins[i].Hi = float64(i+1) / float64(nbins)
	}
	sumP := make([]float64, nbins)
	exc := make([]int, nbins)
	for _, r := range rs {
		b := int(r.Prediction.PExceed * float64(nbins))
		if b >= nbins {
			b = nbins - 1
		}
		bins[b].N++
		sumP[b] += r.Prediction.PExceed
		if r.Exceeded {
			exc[b]++
		}
	}
	for i := range bins {
		if bins[i].N > 0 {
			bins[i].MeanP = sumP[i] / float64(bins[i].N)
			bins[i].Empirical = float64(exc[i]) / float64(bins[i].N)
		}
	}
	return bins
}

// Brier is the squared error of a probabilistic forecast against a binary outcome.
func Brier(p float64, y bool) float64 {
	d := p - boolToFloat(y)
	return d * d
}

// LogLoss is the (clipped) logarithmic score of a probabilistic forecast.
func LogLoss(p float64, y bool) float64 { return logLoss(p, y) }

func logLoss(p float64, y bool) float64 {
	const eps = 1e-6
	p = math.Min(math.Max(p, eps), 1-eps)
	if y {
		return -math.Log(p)
	}
	return -math.Log(1 - p)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
