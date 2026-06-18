// Command ingest-bwq pulls the EA Bathing Water Quality feeds for a single
// sampling point and prints a summary — the end-to-end smoke test of the bwq
// client against the live API.
//
//	ingest-bwq -point 03600
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/umbralcalc/bathing-water-forecaster/internal/bwq"
)

func main() {
	point := flag.String("point", "03600", "sampling point notation, e.g. 03600")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := bwq.New()

	samples, err := client.InSeasonSamples(ctx, *point)
	if err != nil {
		log.Fatalf("in-season samples: %v", err)
	}
	rbwd, err := client.Compliance(ctx, bwq.RegimeRBWD, *point)
	if err != nil {
		log.Fatalf("rBWD compliance: %v", err)
	}

	if len(samples) == 0 {
		fmt.Printf("point %s: no in-season samples found\n", *point)
		os.Exit(0)
	}

	name := samples[0].BathingWaterName
	fmt.Printf("Sampling point %s — %s\n", *point, name)
	fmt.Printf("In-season samples: %d (%s … %s)\n",
		len(samples),
		samples[len(samples)-1].Time.Format("2006-01-02"),
		samples[0].Time.Format("2006-01-02"))

	// Censoring breakdown — the figure that motivates the whole likelihood.
	var ecCensored, entCensored int
	for _, s := range samples {
		if s.EColi.Censoring != bwq.Actual {
			ecCensored++
		}
		if s.Enterococci.Censoring != bwq.Actual {
			entCensored++
		}
	}
	fmt.Printf("Censored counts: E. coli %d/%d, enterococci %d/%d\n",
		ecCensored, len(samples), entCensored, len(samples))

	fmt.Println("Most recent samples:")
	for _, s := range samples[:min(5, len(samples))] {
		fmt.Printf("  %s  E.coli %s%-6.0f  ent %s%-6.0f%s\n",
			s.Time.Format("2006-01-02"),
			s.EColi.Censoring, s.EColi.Value,
			s.Enterococci.Censoring, s.Enterococci.Value,
			discountNote(s.Discountable))
	}

	if len(rbwd) > 0 {
		fmt.Println("rBWD annual classifications:")
		for _, c := range rbwd[:min(5, len(rbwd))] {
			fmt.Printf("  %d  %s\n", c.Year, c.ClassName)
		}
		fmt.Printf("Location: %.5f, %.5f\n", rbwd[0].Lat, rbwd[0].Long)
	}
}

func discountNote(d bool) string {
	if d {
		return "  [discountable]"
	}
	return ""
}
