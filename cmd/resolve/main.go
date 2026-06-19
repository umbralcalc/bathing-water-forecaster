// Command resolve settles a committed forecast ledger against the lab results
// that have since published, scoring calibration and refusing any sample that
// would breach the no-leakage rule.
//
//	resolve -ledger data/predictions/2025-07-01_northumberland.json
//
// For each committed site it finds the first sample taken strictly after the
// commit time, records whether it exceeded the threshold (respecting censoring),
// and writes a resolved ledger with Brier/log-loss, Brier skill score and a
// reliability curve. Sites with no later sample yet, or a censoring-indeterminate
// outcome, are reported as skipped rather than silently dropped.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
	"github.com/umbralcalc/bathing-water-forecaster/internal/forecast"
)

func main() {
	ledgerPath := flag.String("ledger", "", "path to a committed ledger JSON (required)")
	dir := flag.String("dir", "data/predictions", "directory for the resolved ledger")
	flag.Parse()
	if *ledgerPath == "" {
		log.Fatal("-ledger is required")
	}

	commit, err := forecast.ReadCommit(*ledgerPath)
	if err != nil {
		log.Fatalf("read ledger: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	bw := bwq.New()

	var resolutions []forecast.Resolution
	var skipped []string
	for _, p := range commit.Predictions {
		samples, err := bw.InSeasonSamples(ctx, p.Point)
		if err != nil {
			log.Printf("skip %s: %v", p.Point, err)
			skipped = append(skipped, p.Point)
			continue
		}
		res, ok := forecast.Resolve(p, forecast.Site{Point: p.Point, Samples: samples})
		if !ok {
			skipped = append(skipped, p.Point)
			continue
		}
		// Defence in depth: assert the no-leakage invariant explicitly.
		if !res.SampleTime.After(p.CommitTime) {
			log.Fatalf("LEAKAGE: site %s resolved against a sample at %s not after commit %s",
				p.Point, res.SampleTime, p.CommitTime)
		}
		resolutions = append(resolutions, res)
	}

	score := forecast.Aggregate(resolutions)
	resolved := forecast.ResolvedLedger{
		Region:      commit.Region,
		CommitTime:  commit.CommitTime,
		ResolvedAt:  time.Now().UTC(),
		Resolutions: resolutions,
		Skipped:     skipped,
		Score:       score,
	}

	path, err := forecast.WriteResolved(*dir, resolved)
	if err != nil {
		log.Fatalf("write resolved ledger: %v", err)
	}

	report(commit, resolved)
	fmt.Printf("\n  → %s\n", path)
}

func report(commit forecast.Commit, r forecast.ResolvedLedger) {
	if commit.Retrospective() {
		fmt.Printf("⚠ RETROSPECTIVE ledger (written %s, commit dated %s) — a backtest, not a genuine pre-commitment.\n",
			commit.GeneratedAt.Format("2006-01-02"), commit.CommitTime.Format("2006-01-02"))
	}
	fmt.Printf("Resolved %s (committed %s)\n", r.Region, r.CommitTime.Format("2006-01-02"))
	fmt.Printf("Settled %d of %d commitments (%d skipped: no later/determinate sample)\n\n",
		len(r.Resolutions), len(commit.Predictions), len(r.Skipped))

	for _, res := range r.Resolutions {
		mark := "·"
		if res.Exceeded {
			mark = "EXCEEDED"
		}
		fmt.Printf("  %s  %-26s  forecast %5.1f%%  →  %s%-6.0f  %s\n",
			res.SampleTime.Format("2006-01-02"), trunc(res.Prediction.Name, 26),
			100*res.Prediction.PExceed, res.Censoring, res.Count, mark)
	}

	s := r.Score
	if s.N == 0 {
		fmt.Println("\nNo determinate resolutions yet to score.")
		return
	}
	fmt.Printf("\nScore over %d resolved samples (base rate %.1f%% exceedance):\n", s.N, 100*s.BaseRate)
	fmt.Printf("  Brier %.4f   log-loss %.4f   Brier skill vs base rate %+.3f\n",
		s.MeanBrier, s.MeanLogLoss, s.BrierSkill)
	fmt.Println("  Reliability (forecast band → empirical exceedance rate):")
	for _, b := range s.ReliabilityBins {
		if b.N == 0 {
			continue
		}
		fmt.Printf("    %.0f–%.0f%%  mean %4.1f%%  →  %4.1f%%  (n=%d)\n",
			100*b.Lo, 100*b.Hi, 100*b.MeanP, 100*b.Empirical, b.N)
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
