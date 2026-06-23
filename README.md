# bathing-water-forecaster

A calibrated forecaster for bathing-water pollution exceedances at the ~451
designated bathing waters of England, built on Environment Agency open data and
the [stochadex](https://github.com/umbralcalc/stochadex) simulation engine. For
each site it estimates the probability that the next routine in-season sample will
exceed the statutory *E. coli* threshold, commits that probability before the
sample is taken, and scores itself openly against the right baselines — including
the EA's own operational forecast.

The point of the project is **calibration, not accuracy**: a probability you can
trust, for every designated site, with the honest result stated up front.

## The honest headline

1. **The per-site forecast is at its attainable skill ceiling.** Exceedances are
   rare (~2–3% of samples) and the truth is observed only weekly and heavily
   censored, so the predictable signal is small and mostly captured by one
   covariate — 2-day antecedent rainfall. A cross-validated "fair oracle" shows
   the apparent gap to a perfect-hindsight model is mostly *unattainable
   over-fitting*; more covariates, recency weighting and coefficient pooling each
   recover little or nothing.
2. **One thing genuinely adds out-of-sample skill: a shared regional anomaly,
   inferred by sequential Monte Carlo.** Knowing how the rest of the coast is doing
   *this week* — information a per-site model cannot see — improves a held-out
   site's forecast. It is the only lever here that beats the per-site ceiling.

## The model

The latent log-concentration is Gaussian (log-normal counts — the family the rBWD
classification itself uses):

```
log c = baseline + season(day-of-year) + β·antecedent-rain  (+ λ·regional-anomaly)
P(exceed) = P(log c > log threshold)
```

- **Censoring is first-class.** Most counts are `< reporting limit`; the likelihood
  treats them as interval observations (density / CDF / survival terms), proven by
  synthetic parameter recovery. Ignoring it is invisible to accuracy but wrecks
  calibration (see results).
- Fit per site by maximum likelihood; baselines and slopes **partially pooled**
  toward regional/national means by empirical Bayes.
- Also expressed as a **stochadex partition graph** (`internal/compose`) that
  reproduces the regression to machine precision and makes each component a
  separately-attributable series; the regional anomaly is an Ornstein–Uhlenbeck
  partition inferred by a **particle filter** (`internal/anomaly`).

Data sources and the verified EA API surface are in [SOURCES.md](SOURCES.md).

## Results

All scores are out-of-sample, over ~150–220 sites unless noted.

| finding | result |
|---|---|
| **Censoring matters, invisibly** | ignoring it (~67% censored) barely moves Brier but worsens **log-loss +22.6%** |
| **Beats the naive baselines** | backtest log-loss **0.098** vs base-rate 0.121 vs "did it rain" 0.135 |
| **vs the EA flag** | comparable on Brier, **better on log-loss (0.135 vs 0.197)**; 13 of 22 exceedances landed on days the EA called `normal`; 4 of 7 sites had *no* EA forecast at all |
| **Pooling** | neutral for data-rich sites, helps the sparse ones (log-loss 0.380 → 0.374) |
| **Skill ceiling** | a cross-validated fair oracle scores *worse than climatology* — ~250% of the apparent gap is over-fitting; the model already sits at the attainable frontier |
| **Regional anomaly (SMC)** | over **48,157 leave-one-out forecasts**, conditioning on the inferred anomaly improves log-loss **−4.1%** — the only method that beats the per-site model |

The particle filter recovers a synthetic latent trajectory at correlation 0.86
from partly-censored data.

## Dashboard

`index.html` at the repo root is a self-contained interactive page (styled for the
blog, served as-is by GitHub Pages): an SVG map of every designated site over a
grey Great Britain, coloured by `P(exceed)`, plus a rainfall→exceedance explorer
that decomposes a chosen site's forecast into baseline + rain + season pushes — all
computed client-side from the fitted coefficients shipped in `data.js`.

Regenerate it with the latest data and fitted predictions in one command (cached
per site under `data/raw/`, so re-runs are fast and offline — only sites whose
cache is stale, or genuinely-new weekly samples, hit the network):

```bash
make site          # regenerates data.js (~24s warm cache); then open index.html
```

`make site-refresh` forces a full refetch; `make help` lists every target.

## Using it

```bash
go build ./...
go test ./...
```

Every analysis command takes `-points a,b,c` or `-all -limit N`:

```bash
go run ./cmd/fit-site        -point 04700   # fit one site, print the model
go run ./cmd/explain         -point 04700   # decompose a forecast (Shapley)
go run ./cmd/exceedance-backtest -all       # expanding-window backtest vs baselines
go run ./cmd/vs-ea-prediction    -all       # head-to-head vs the EA flag
go run ./cmd/censoring-ablation  -all       # cost of ignoring censoring
go run ./cmd/skill-ceiling       -all       # predictable vs irreducible skill
go run ./cmd/close-gap           -all       # fair-oracle decomposition
go run ./cmd/anomaly-smc         -all       # the cross-site SMC result
```

The proof-of-commit loop (`forecast` → `resolve`) commits each site's `P(exceed)`
to `data/predictions/` before the sample and settles it after, with no-leakage
checks; pass `-as-of` a past date to exercise it on history.

## Repo structure

```
cmd/                 one binary per analysis / pipeline step
internal/
  bwq/               EA Bathing Water Quality + RiskPrediction client (censoring, dedup, sites)
  hydro/             EA Hydrology rainfall client
  catchment/         site → gauge linkage, antecedent / lagged rainfall
  exceedance/        censored log-count likelihood, regression, attribution
  pooling/           empirical-Bayes partial pooling
  forecast/          proof-of-commit ledger, censoring-aware resolution, scoring
  compose/           stochadex partition composition (incl. the OU anomaly)
  anomaly/           particle filter for the shared regional factor
  siteload/          site assembly + on-disk cache
index.html           static interactive dashboard (GitHub Pages entry point)
data.js              generated site coefficients the dashboard loads
data/
  raw/               cached pulls (gitignored)
  predictions/       committed forecasts (the proof-of-commit ledger)
SOURCES.md  README.md
```

## Limitations

Sampling-limited — skill is capped by weekly, censored, rare-event observation,
and the model over-predicts in its rare high-probability forecasts. A single
determinand and threshold (*E. coli* > 500) so far. The anomaly is inferred with
fixed, lightly-calibrated process parameters. Proof-of-commit is demonstrated
retrospectively rather than from a live accumulating ledger.

## Attribution

Contains public sector information from the Environment Agency (Bathing Water
Quality, Hydrology, Flood-Monitoring and RiskPrediction APIs) licensed under the
Open Government Licence v3.0. This is a non-commercial public-interest
methodological exercise, is not affiliated with or endorsed by the Environment
Agency, and is **not an official source of advice on bathing-water safety** — for
that, consult the EA's
[Swimfo](https://environment.data.gov.uk/bwq/profiles/) service.
