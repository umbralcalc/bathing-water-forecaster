package compose

import (
	"github.com/umbralcalc/stochadex/pkg/continuous"
	"github.com/umbralcalc/stochadex/pkg/simulator"
)

// OUParams configures the shared regional "wet-week" anomaly — an
// Ornstein–Uhlenbeck process z(t) that mean-reverts to Mu at speed Theta with
// volatility Sigma. One such process is shared by every site in a region.
type OUParams struct {
	Theta float64
	Sigma float64
	Mu    float64
	Init  float64
	Seed  uint64
}

// RegionalSite is one site's contribution to a coupled regional simulation: a
// deterministic mean BaseMu (the site's baseline+season+rain for the scenario),
// a noise scale Sigma, and Lambda — how strongly the shared anomaly loads onto
// this site. LogThreshold is log of the exceedance cut.
type RegionalSite struct {
	Name         string
	BaseMu       float64
	Sigma        float64
	Lambda       float64
	LogThreshold float64
}

// RegionalRun holds the per-step shared anomaly and each site's response.
type RegionalRun struct {
	Anomaly        []float64            // z(t)
	PExceed        map[string][]float64 // per-site P(exceed) over steps
	AnomalyContrib map[string][]float64 // per-site λ·z(t), the anomaly's push on μ
}

// RunRegional simulates one shared Ornstein–Uhlenbeck anomaly partition coupled
// to many site concentration partitions. The anomaly carries temporal state
// across steps (mean reversion), and every site's concentration reads the same
// anomaly value each step via upstream parameter passing — so when the regional
// anomaly rises, every site's exceedance probability rises coherently. This is
// the structure the per-site model cannot express: one latent process driving a
// whole coastline.
func RunRegional(sites []RegionalSite, ou OUParams, steps int) RegionalRun {
	anomaly := &simulator.PartitionConfig{
		Name:              "anomaly",
		Iteration:         &continuous.OrnsteinUhlenbeckIteration{},
		Params:            simulator.NewParams(map[string][]float64{"thetas": {ou.Theta}, "mus": {ou.Mu}, "sigmas": {ou.Sigma}}),
		InitStateValues:   []float64{ou.Init},
		StateHistoryDepth: 1,
		Seed:              ou.Seed,
	}

	partitions := []*simulator.PartitionConfig{anomaly}
	for _, s := range sites {
		s := s // capture
		conc := valuesPartition("conc_"+s.Name, 3, func(p *simulator.Params) []float64 {
			z := p.Get("anomaly")[0]
			contrib := s.Lambda * z
			mu := s.BaseMu + contrib
			return []float64{mu, normalCDF((mu - s.LogThreshold) / s.Sigma), contrib}
		})
		conc.ParamsFromUpstream = map[string]simulator.NamedUpstreamConfig{"anomaly": {Upstream: "anomaly"}}
		partitions = append(partitions, conc)
	}
	for _, p := range partitions {
		p.Init()
	}

	storage := runToStorage(partitions, steps)
	trim := func(name string) [][]float64 {
		rows := storage.GetValues(name)
		if len(rows) > steps {
			rows = rows[len(rows)-steps:]
		}
		return rows
	}

	run := RegionalRun{
		Anomaly:        col(trim("anomaly"), 0),
		PExceed:        make(map[string][]float64, len(sites)),
		AnomalyContrib: make(map[string][]float64, len(sites)),
	}
	for _, s := range sites {
		c := trim("conc_" + s.Name)
		run.PExceed[s.Name] = col(c, 1)
		run.AnomalyContrib[s.Name] = col(c, 2)
	}
	return run
}
