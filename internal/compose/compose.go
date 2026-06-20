// Package compose expresses the exceedance model as a composition of stochadex
// partitions rather than one monolithic regression. Each additive term of the
// latent log-concentration — baseline, season, rainfall — is its own partition,
// and a downstream "concentration" partition combines them into μ and the
// exceedance probability. Partitions are wired with stochadex upstream parameter
// passing, so each component's contribution is computed by the engine, delivered
// same-step to the concentration partition, and stored separately — a forecast
// decomposes into attributable component series for free.
//
// This is the reproduce milestone of the stochadex port: the graph reproduces the
// fitted [exceedance.Regression] exactly and is validated against it. The
// distinctive next step — one partition's state driving another across time — is
// the shared regional "wet-week" anomaly (an Ornstein–Uhlenbeck partition feeding
// the concentration); the concentration partition is already shaped to receive an
// extra additive upstream term.
package compose

import (
	"math"

	"github.com/umbralcalc/stochadex/pkg/general"
	"github.com/umbralcalc/stochadex/pkg/simulator"
)

// Coeffs are the fitted coefficients of the additive log-concentration model,
// μ = Beta0 + BetaRain·rain + BetaSin·sin(doy) + BetaCos·cos(doy), with Gaussian
// scale Sigma and the log of the exceedance threshold.
type Coeffs struct {
	Beta0, BetaRain, BetaSin, BetaCos float64
	Sigma, LogThreshold               float64
}

// DayInput is one day's covariate vector fed to the simulation.
type DayInput struct{ Rain, Sin, Cos float64 }

// Decomposition holds the per-day component series read back from the run. Each
// slice has one entry per input day; Baseline+Season+Rainfall == Mu, and PExceed
// is Φ((Mu−LogThreshold)/Sigma).
type Decomposition struct {
	Baseline []float64
	Season   []float64
	Rainfall []float64
	Mu       []float64
	PExceed  []float64
}

// Run builds the partition DAG, simulates it across the input days, and reads the
// component series back out of the resulting state storage.
func Run(c Coeffs, days []DayInput) Decomposition {
	if len(days) == 0 {
		return Decomposition{}
	}

	// inputs: replay [rain, sin, cos] per day. FromStorageIteration emits
	// Data[stepNumber]; Data[0] is the (unused) initial row, so day i is served
	// at step i+1.
	data := make([][]float64, len(days)+1)
	data[0] = []float64{0, 0, 0}
	for i, d := range days {
		data[i+1] = []float64{d.Rain, d.Sin, d.Cos}
	}
	inputs := &simulator.PartitionConfig{
		Name:              "inputs",
		Iteration:         &general.FromStorageIteration{Data: data},
		InitStateValues:   []float64{0, 0, 0},
		StateHistoryDepth: 1,
	}

	// baseline: a constant β0, no upstream.
	baseline := valuesPartition("baseline", 1, func(p *simulator.Params) []float64 {
		return []float64{c.Beta0}
	})

	// season and rainfall read the day's covariates from the inputs partition.
	season := valuesPartition("season", 1, func(p *simulator.Params) []float64 {
		in := p.Get("inputs")
		return []float64{c.BetaSin*in[1] + c.BetaCos*in[2]}
	})
	season.ParamsFromUpstream = map[string]simulator.NamedUpstreamConfig{
		"inputs": {Upstream: "inputs"},
	}
	rainfall := valuesPartition("rainfall", 1, func(p *simulator.Params) []float64 {
		in := p.Get("inputs")
		return []float64{c.BetaRain * in[0]}
	})
	rainfall.ParamsFromUpstream = map[string]simulator.NamedUpstreamConfig{
		"inputs": {Upstream: "inputs"},
	}

	// concentration combines the three additive components into μ and P(exceed).
	concentration := valuesPartition("concentration", 2, func(p *simulator.Params) []float64 {
		mu := p.Get("baseline")[0] + p.Get("season")[0] + p.Get("rainfall")[0]
		return []float64{mu, normalCDF((mu - c.LogThreshold) / c.Sigma)}
	})
	concentration.ParamsFromUpstream = map[string]simulator.NamedUpstreamConfig{
		"baseline": {Upstream: "baseline"},
		"season":   {Upstream: "season"},
		"rainfall": {Upstream: "rainfall"},
	}

	partitions := []*simulator.PartitionConfig{inputs, baseline, season, rainfall, concentration}
	for _, p := range partitions {
		p.Init()
	}

	storage := runToStorage(partitions, len(days))
	// The storage's first row is the initial condition (step 0); step t (t≥1)
	// serves day t−1, so the trailing len(days) rows align to the inputs.
	get := func(name string) [][]float64 {
		rows := storage.GetValues(name)
		if len(rows) > len(days) {
			rows = rows[len(rows)-len(days):]
		}
		return rows
	}
	conc := get("concentration")
	return Decomposition{
		Baseline: col(get("baseline"), 0),
		Season:   col(get("season"), 0),
		Rainfall: col(get("rainfall"), 0),
		Mu:       col(conc, 0),
		PExceed:  col(conc, 1),
	}
}

// runToStorage configures and runs the simulation, collecting every partition's
// state at each step into a StateTimeStorage (the same wiring the analysis helper
// uses, but without its plotting/dataframe dependencies).
func runToStorage(partitions []*simulator.PartitionConfig, steps int) *simulator.StateTimeStorage {
	generator := simulator.NewConfigGenerator()
	storage := simulator.NewStateTimeStorage()
	generator.SetSimulation(&simulator.SimulationConfig{
		OutputCondition:      &simulator.EveryStepOutputCondition{},
		OutputFunction:       &simulator.StateTimeStorageOutputFunction{Store: storage},
		TerminationCondition: &simulator.NumberOfStepsTerminationCondition{MaxNumberOfSteps: steps},
		TimestepFunction:     &simulator.ConstantTimestepFunction{Stepsize: 1.0},
		InitTimeValue:        0.0,
	})
	for _, p := range partitions {
		generator.SetPartition(p)
	}
	coordinator := simulator.NewPartitionCoordinator(generator.GenerateConfigs())
	coordinator.Run()
	return storage
}

// valuesPartition wraps a pure function of the (upstream-delivered) params as a
// stochadex partition of the given state width.
func valuesPartition(name string, width int, f func(*simulator.Params) []float64) *simulator.PartitionConfig {
	return &simulator.PartitionConfig{
		Name: name,
		Iteration: &general.ValuesFunctionIteration{
			Function: func(params *simulator.Params, _ int, _ []*simulator.StateHistory, _ *simulator.CumulativeTimestepsHistory) []float64 {
				return f(params)
			},
		},
		InitStateValues:   make([]float64, width),
		StateHistoryDepth: 1,
	}
}

// col extracts column j from a row-major series.
func col(rows [][]float64, j int) []float64 {
	out := make([]float64, len(rows))
	for i, r := range rows {
		if j < len(r) {
			out[i] = r[j]
		}
	}
	return out
}

const sqrt2 = 1.4142135623730951

func normalCDF(x float64) float64 { return 0.5 * math.Erfc(-x/sqrt2) }
