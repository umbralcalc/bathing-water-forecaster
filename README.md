# bathing-water-forecaster

A calibrated forecaster for bathing-water pollution exceedances at the ~451
designated bathing waters of England, built on Environment Agency open data and
the [stochadex](https://github.com/umbralcalc/stochadex) simulation engine. It
forecasts the probability that the next routine in-season sample at a site will
exceed its statutory *E. coli* threshold, commits that probability before the
sample is taken, and scores itself openly against the right baselines — including
the EA's own operational forecast.

This README is the methodology and the findings. The design rationale and
competitive positioning live in [PLAN.md](PLAN.md); the data sources and their
verified API surface in [SOURCES.md](SOURCES.md).

---

## The headline, stated plainly

The load-bearing claim of this project is **calibration, not accuracy**, and the
honest result is twofold:

1. **The per-site forecast is at its attainable skill ceiling.** Exceedances are
   rare (~2–3% of samples) and the truth is observed only ~weekly and heavily
   censored, so the predictable signal is small and mostly captured by a single
   covariate (2-day antecedent rainfall). We demonstrate — with a cross-validated
   "fair oracle" — that the apparent gap to a perfect-hindsight model is **mostly
   unattainable over-fitting**, not recoverable skill. More covariates, recency
   weighting, and coefficient pooling each recover little or nothing.

2. **One thing genuinely adds out-of-sample skill: a shared regional anomaly,
   inferred by sequential Monte Carlo.** Knowing how the *rest of the coast* is
   doing this week — information a per-site model structurally cannot see —
   improves a held-out site's forecast by ~4% on log-loss. It is the single lever
   that beats the per-site ceiling, precisely because it is new information rather
   than better estimation of old information.

Everything below is built to make those two statements measurable and honest.

---

## What it forecasts

For each designated bathing water, before the next in-season weekly sample:

- **`P(exceedance)`** — the probability the next sample's *E. coli* count exceeds
  a threshold (default 500/100 ml), read off a censored log-normal predictive
  distribution.
- **A decomposition** of that probability into named, attributable components —
  baseline, season, rainfall, and (optionally) the shared regional anomaly.
- **A committed ledger** — forecasts are frozen to `data/predictions/` *before*
  the sample, then settled against the later lab result with explicit no-leakage
  checks, building a public calibration record.

The target is the lab *E. coli* count of the EA's routine in-season sample,
published via the Bathing Water Quality `SampleAssessment` feed.

---

## Data

All Environment Agency open data, Open Government Licence v3.0, no key. Verified
endpoints and quirks are documented in [SOURCES.md](SOURCES.md). In brief:

- **Resolution** — BWQ in-season `SampleAssessment` (weekly *E. coli* /
  enterococci counts with censoring qualifiers; deep history to 1988 for
  established sites) and the two annual compliance cubes (`compliance` 1988–2014,
  `compliance-rBWD` 2015– for the Excellent/Good/Sufficient/Poor classes, which
  also supply per-site coordinates and the all-sites list).
- **Incumbent baseline** — the EA daily `RiskPrediction` `normal`/`increased`
  flag, used *only* for the head-to-head, never as a model input.
- **Covariates** — the EA Hydrology API for long-record daily rainfall, linked to
  each site by nearest-gauge distance.

Two correctness details the client handles and that materially affect results:
samples are **republished under multiple `recordDate` revisions** (deduped to the
latest), and most counts are **left-censored** (`< reporting limit`).

---

## The model

The latent log-concentration of the binding determinand is modelled as Gaussian
(log-normal counts — the same family the rBWD classification itself uses):

```
log c = baseline + season(day-of-year) + β·antecedent-rain  (+ λ·regional-anomaly)
P(exceed) = P(log c > log threshold),  integrating the Gaussian scale σ
```

- **Censoring is first-class.** A `<10` reading is an interval observation, not a
  point; the likelihood contributes a density term for actual counts, a CDF term
  for `<`, and a survival term for `>`. This is the project's load-bearing
  correctness property, proven by synthetic recovery of known parameters from
  heavily-censored data (`internal/exceedance`).
- **Fit per site by maximum likelihood** (`FitGaussian`, `FitRegression`), with a
  dependency-free Nelder–Mead optimiser and covariate standardisation.
- **Partial pooling** of the site baseline (and rain slope) toward regional and
  national means by empirical-Bayes normal–normal shrinkage (`internal/pooling`),
  with per-coefficient uncertainty from the censored likelihood's Hessian.
- **Composition** — the model is also expressed as a [stochadex](https://github.com/umbralcalc/stochadex)
  partition graph (`internal/compose`): baseline, season, and rainfall partitions
  feed a concentration partition via upstream parameter passing, reproducing the
  regression to machine precision and making each component a separately-stored,
  attributable series.
- **The shared regional anomaly** is an Ornstein–Uhlenbeck partition coupling all
  sites in a region, inferred from data by a **bootstrap particle filter** on the
  censored likelihood (`internal/anomaly`).

---

## Findings

All scores are out-of-sample. Headline numbers from runs over ~150–220 sites
(`-all -limit N`); see each command to reproduce.

### Censoring is load-bearing — and invisible to accuracy
With ~67% of counts censored, ignoring censoring (substituting the cap as if
exact) barely changes Brier but worsens **log-loss by +22.6%** — it silently
wrecks calibration exactly on the rare exceedance events that matter
(`censoring-ablation`).

### Rainfall is the signal, and it is site-dependent
Antecedent rainfall predicts exceedance strongly at urban beaches (wet-day
exceedance up to ~12× the dry-day rate; Pearson r ≈ +0.38) and not at all at
estuary or consistently-clean sites — motivating pooling rather than a universal
rain rule (`link-catchment`, `fit-site`).

### The model beats the naive baselines, modestly
Expanding-window backtest, 8-site cluster, 1,699 samples (2.2% exceedance):

| method | Brier | log-loss |
|---|---|---|
| **rain-model** | **0.0221** | **0.0983** |
| base rate | 0.0228 | 0.1208 |
| "did it rain" rule | 0.0233 | 0.1347 |

Cleanly better on log-loss (the calibration-sensitive score); Brier gains are
small because rare events make Brier insensitive (`exceedance-backtest`).

### Head-to-head vs the EA flag: comparable, better-calibrated, broader
On 3 EA-forecast sites (670 samples): the EA flag discriminates well (27%
exceedance when `increased` vs 2% when `normal`), and edges the model on **Brier**;
the model wins on **log-loss** (0.135 vs 0.197) because **13 of 22 exceedances
landed on days the EA called `normal`** — within-`normal` variation a binary flag
cannot express. And 4 of the 7 sites had *no* EA forecast at all — the coverage
gap the project occupies (`vs-ea-prediction`).

### Pooling helps the sparse sites, is neutral elsewhere
Across 158 sites it neither helps nor hurts the data-rich majority, but on sparse
sites (<80 training samples) national pooling improves log-loss (0.3796 → 0.3735)
and Brier — exactly the borrowing-strength hypothesis (`pool-sweep`, `pool-slope`).

### The skill ceiling: estimation-limited, not covariate-limited
The decomposition (`skill-ceiling`) shows an in-sample "achievable" gain of ~15%
of climatology log-loss, but the **realised** gain is ~0% — the entire gap is
estimation noise. The `close-gap` experiment then shows *why*: a **cross-validated
fair oracle** (test-era training, no peeking) scores **worse than climatology**,
so ~250% of the "gap" to the in-sample oracle is unattainable over-fitting. The
realised model already beats the fair ceiling. Recency weighting recovers nothing;
pooling recovers ~12%. **The forecaster sits at its attainable frontier.**

### The one lever that beats the ceiling: the SMC regional anomaly
The shared wet-week effect is real but weak (~+0.05 excess same-week residual
correlation; `regional-anomaly`). Yet inferring it by particle filter from the
*other* sites sampled in a region-week and conditioning the held-out site's
forecast on it improves out-of-sample skill — over **48,157 leave-one-out
forecasts across 1,910 region-weeks**:

| forecast | log-loss | Brier |
|---|---|---|
| no anomaly (per-site) | 0.0847 | 0.0192 |
| **+ inferred anomaly** | **0.0812** | **0.0189** |
| Δ | **−4.1%** | −1.6% |

This is the only method here that beats the per-site model, because it adds
information the per-site model cannot access — the state of the coastline this
week (`anomaly-smc`). The filter itself recovers a synthetic latent trajectory at
correlation 0.86 from partly-censored data.

---

## Explainability

Two complementary decompositions turn `P(exceed)` from a number into a "why":

- **Shapley attribution** (`explain`) splits the probability — through the
  nonlinear link, order-independently, summing exactly to the forecast — into
  baseline + rain + season pushes. *"44.9% = 3.7% baseline + 57.6% rain − 16.5%
  season."*
- **Partition composition** (`compose-explain`) produces the same decomposition
  from the stochadex simulation, each term computed by its own partition; and
  `anomaly-sim` shows one shared anomaly driving a whole coastline coherently.

Notably, adding season visibly *restructures the explanation* without improving
skill — explainability and accuracy are orthogonal axes, and this project treats
them as such.

---

## The toolkit

Ingestion & linkage: `ingest-bwq`, `link-catchment`.
Single-site model & explanation: `fit-site`, `explain`, `compose-explain`.
Proof-of-commit loop: `forecast`, `resolve`.
Evaluation & honesty: `exceedance-backtest`, `vs-ea-prediction`,
`censoring-ablation`, `skill-ceiling`, `close-gap`, `pool-sweep`, `pool-slope`.
Regional anomaly: `regional-anomaly`, `anomaly-sim`, `anomaly-smc`.

Each takes `-points a,b,c` or `-all -limit N`. Build and test:

```bash
go build ./...
go test ./...
go run ./cmd/anomaly-smc -all -limit 220     # the headline cross-site result
```

---

## Repo structure

```
cmd/                 one binary per analysis / pipeline step (see above)
internal/
  bwq/               EA Bathing Water Quality + RiskPrediction client (censoring, dedup, sites)
  hydro/             EA Hydrology rainfall client
  catchment/         site → gauge linkage, antecedent / lagged rainfall
  exceedance/        censored log-count likelihood, regression, attribution
  pooling/           empirical-Bayes partial pooling
  forecast/          proof-of-commit ledger, censoring-aware resolution, scoring
  compose/           stochadex partition composition (incl. the OU anomaly)
  anomaly/           particle filter for the shared regional factor
  siteload/          assembles a site (samples + linked rainfall)
data/
  raw/               cached pulls (gitignored)
  predictions/       committed forecasts (the proof-of-commit ledger)
PLAN.md  SOURCES.md  README.md
```

---

## Limitations, honestly

- **Sampling-limited.** Skill is hard-capped by weekly, censored, rare-event
  sampling; we state the ceiling rather than oversell. The model's value is
  calibration and coverage, not a large accuracy edge.
- **High-end over-confidence.** Reliability curves show the model over-predicts in
  its rare high-probability forecasts (small-N bands) — a known estimation
  artifact of rare events.
- **Single determinand and a single threshold** (*E. coli* > 500) so far;
  enterococci and the full statutory per-water-type thresholds are not yet wired.
- **The anomaly is inferred with fixed, lightly-calibrated process parameters**
  (shared fraction ρ, persistence φ); jointly inferring them by SMC is the natural
  next step.
- **Proof-of-commit is demonstrated retrospectively** (`-as-of` past dates); no
  live weekly ledger is accumulating yet.

---

## Attribution

Contains public sector information from the Environment Agency (Bathing Water
Quality, Hydrology, Flood-Monitoring and RiskPrediction APIs) licensed under the
Open Government Licence v3.0. This project is a non-commercial public-interest
methodological exercise, is not affiliated with or endorsed by the Environment
Agency, and is **not an official source of advice on bathing-water safety** — for
that, consult the Environment Agency's own Swimfo service.
