# bathing-water-forecaster — PLAN

A seasonally-live, **calibrated bathing-water pollution-exceedance forecaster**,
published as a frozen interactive dashboard with a public proof-of-commit
calibration loop. One Go + stochadex repo, built to the same discipline as the
other forecasters, chosen as the public-good companion to the health-surveillance
flagship: open OGL data, an unimpeachably civic "is it safe to swim?" framing with
zero gambling adjacency, and a rainfall→runoff causal structure that gives a real
model rather than a seasonal mean.

This file is the design and the road to here. Methodology lives in `README.md`;
data sources and their licences in `SOURCES.md`.

## Why this project, and the competitive position (stated up front)

There IS an incumbent here, and we say so plainly rather than pretend otherwise.
The Environment Agency has, since 2013, published its own **daily short-term
pollution-risk predictions** (a `RiskPrediction` with `riskLevel` of `normal` /
`increased` / `unknown`), driven largely by rainfall, at the subset of bathing
waters enrolled in its Pollution Risk Forecasting (PRF) programme. Forecasting
"pollution risk from rainfall" as a fresh idea would walk straight into the
incumbent-forecaster trap (the weather / flood / grid pattern we ruled out
elsewhere).

So we deliberately occupy the gap *beside* the EA's flag, not on top of it:

- **Calibrated probabilities, not a triage flag.** The EA publishes an
  operational binary-ish flag. We publish a *probability* of threshold
  exceedance with a quantified uncertainty, and — the load-bearing move — a
  **public calibration record** of how well those probabilities land. The EA does
  not publish a calibration scorecard for its flag; we make calibration the
  product.
- **All designated sites, not only PRF ones.** The EA's daily forecast covers
  only PRF-enrolled beaches. We pool across *all* ~400 designated England sites,
  using the hierarchy to lend strength to sites the official forecast skips.
- **An explicit, open head-to-head.** Where the EA's `RiskPrediction` exists, we
  score our calibrated probability against their flag on the same resolved
  samples and publish who is better-calibrated — openly, both ways. This is the
  bathing-water analogue of a market-gap check: not "is the model good" but
  "exactly where, if anywhere, does a calibrated open model add information over
  the authority's operational flag."

Why the domain clears the bar the rejected ones failed: licence is the cleanest
public-sector form (OGL v3.0, no registration), the data is *live in season*, and
the framing is pure public good — nobody bets on whether a beach exceeds its
E. coli threshold, and there is no private data owner to antagonise.

## What we forecast

For each designated bathing water, for the days ahead within the bathing season,
committed before the sample is taken:

- **Headline** — `P(exceedance)`: probability that the next in-season sample at the
  site exceeds the rBWD "good/sufficient" threshold for the binding determinand
  (intestinal enterococci and/or E. coli, per the published Thresholds).
- **Shape** — predictive distribution of the log-colony-count for each determinand,
  from which threshold probabilities at any cut are read off.
- **Site-season** — an end-of-season projected *compliance class* (Excellent /
  Good / Sufficient / Poor) per site, refreshed weekly as samples land — the
  "watch the line move through the summer" hook.

We forecast distributions, simulating the exceedance process many times and
reading published quantities off the ensemble.

**Named precisely.** The target is the lab result of the EA's routine in-season
weekly sample at a designated England sampling point, as published via the
in-season SampleAssessment feed. Samples flagged with `abnormalWeatherException`,
and periods under `SuspensionOfMonitoring` or an open `PollutionIncident`, are
handled explicitly (they distort both the target and the naive base rate) rather
than silently included.

## Data

Full detail and licences in `SOURCES.md`. One platform, all OGL v3.0, no key.

- **Resolution layer — EA Bathing Water Quality API**
  (`environment.data.gov.uk/bwq`). The in-season SampleAssessment feed gives, per
  ~400 sampling points, weekly lab counts for intestinal enterococci, E. coli and
  coliforms, with `sampleDateTime`, `sampleWeek`, `abnormalWeatherException`, and
  count `Qualifier`s (moreThan / lessThan / actual — i.e. **censored values**, see
  honesty rules). Endpoints serve JSON and CSV directly. The published rBWD
  `Threshold` values define our exceedance cuts; annual `ComplianceAssessment`
  gives the season-level ground truth.
- **Incumbent baseline — EA `RiskPrediction` (stp-risk-prediction endpoints).**
  The official daily normal/increased flag, used only as a *comparison baseline*
  for the head-to-head, never as an input.
- **Covariates — EA Flood-Monitoring + Hydrology + Rainfall APIs.** Same platform,
  same OGL, ~15-minute rainfall and river-level/flow readings from ~8,000
  stations. Each bathing water's **zone of influence** (a published spatial
  entity: "region where rainfall may significantly affect water quality") and its
  profile's storm-overflow / outfall features (ESO/SWO/TSO) tell us which
  upstream rain and flow gauges to attach. This catchment linkage is the causal
  backbone.
- **Static site context — Bathing Water Profiles.** Sediment type, sampling
  frequency, `waterQualityImpactedByHeavyRain` boolean, and named outfall features
  per site — priors and pooling structure.

## The model

A **hierarchical censored log-count exceedance model**, driven by catchment
rainfall/flow, simulated to exceedance probabilities.

For site *i*, day *t*, determinand *d*, the latent log-concentration:

```
log c_idt = base_id + season_doy(t) + Σ_k rain_k(i,t)·β_id,k + flow(i,t)·γ_id + tide/temp terms + ε
P(exceed)_idt = P( c_idt > threshold_d )      (integrated over ε and the shared anomaly)
```

- **base_id** — site×determinand baseline, partially pooled site ⊂ region ⊂
  national, so sparse and PRF-skipped sites borrow strength;
- **season** — within-season day-of-year term (early/late season differ);
- **rain_k** — antecedent rainfall over the site's zone-of-influence gauges at
  several lags `k` (the dominant, causally-motivated driver — runoff and combined-
  sewer-overflow spill after rain); lag structure is the interesting part;
- **flow / tide / temp** — river flow into the bathing water, tidal state for
  coastal sites, water temperature where available;
- **shared anomaly** — a regional wet-week latent factor coupling nearby sites, so
  a storm raises exceedance risk coherently across a coastline (the stochadex
  latent factor).

Given the latent distribution and the published thresholds, exceedance
probabilities and season-compliance projections are ensemble statistics. Engine in
`internal/exceedance`. Fit by **simulation-based inference**, handling censoring
(below).

## The modelling unit (settled empirically)

A sweep (`cmd/pool-sweep`) over pooling depth (site vs site⊂region vs
site⊂region⊂national) and rainfall-lag window length/decay. Hypothesis to test,
not assume: heavy pooling wins for low-frequency-sampled and PRF-skipped sites,
while well-sampled problem beaches carry their own signal; the rain-lag window is
catchment-size dependent. Pick on out-of-sample exceedance log-loss, not intuition.

## Validation & honesty rules

- **Censoring is first-class, not ignored.** Lab counts arrive with moreThan /
  lessThan qualifiers — they are **interval-censored**. The likelihood must treat
  "> X" and "< X" as censored observations, not point values. Quietly substituting
  the cap would bias every threshold probability. `internal/exceedance` models the
  censoring explicitly; a `cmd/censoring-ablation` quantifies the calibration cost
  of naively ignoring it (an honest figure and a post in its own right).
- **The sampling-frequency skill ceiling — the load-bearing honesty figure.**
  Resolution data is ~weekly per site (and *less* than weekly at consistently-good
  sites, by design). The covariates are 15-minute. So the forecast skill is hard-
  capped by how sparsely the truth is observed, and exceedances are *rare events*
  at most sites. `cmd/skill-ceiling` decomposes achievable skill: how much of
  exceedance variance is predictable from antecedent rain/flow versus irreducible
  given weekly, censored, rare-event sampling. We state the ceiling up front rather
  than overselling.
- **Backtest.** Expanding-window, no-leakage, in-season only (`cmd/exceedance-
  backtest`), scoring against (a) a per-site seasonal base rate and (b) a simple
  "did it rain in the last 24/48h" rule — the naive rainfall heuristic the public
  already half-knows. Beating *that* cleanly is the bar; if we can't, we say so.
- **Head-to-head vs the EA flag (`cmd/vs-ea-prediction`).** On PRF sites where the
  EA `RiskPrediction` exists, score our calibrated `P(exceed)` against their
  normal/increased flag on the same resolved samples. Publish reliability for both.
  The honest headline is likely "comparable on the flag, with added calibration and
  coverage the official forecast doesn't provide" — and if the EA flag is simply
  better, we report that too.
- **Proof of commit.** `cmd/forecast` commits each site's `P(exceed)` and season-
  class projection *before* the weekly sample, to `data/predictions/`. `cmd/resolve`
  settles against the later SampleAssessment publication, refusing un-committed
  sites and asserting no leakage. The seasonal cadence gives a real weekly in-
  season loop (May–Sep) and a natural off-season pause.

## Scoring

- **Brier and log-loss** on `P(exceed)` per site-sample, plus reliability curves
  stratified by site type (coastal / transitional / lake / inland).
- **CRPS** on the predictive log-count distribution.
- **Rare-event aware metrics** (e.g. Brier skill score vs base rate; precision at
  the "advise against bathing" operating point) since exceedances are infrequent
  and raw accuracy is misleading.
- **Calibration:** a running in-season reliability curve, the same plot each week,
  gaining points through the summer. Noise until it isn't — and explicitly capped
  by sampling frequency.

## Repo structure

```
cmd/
  ingest-bwq/         pull in-season SampleAssessments + thresholds + compliance
  ingest-covariates/  rainfall/flow/tide from flood-monitoring + hydrology APIs
  ingest-ea-pred/     EA RiskPrediction flags (comparison baseline only)
  link-catchment/     attach zone-of-influence gauges + outfall features per site
  pool-sweep/          empirical pooling-depth / rain-lag selection
  forecast/            commit P(exceed) + season-class before each weekly sample
  resolve/             settle against later SampleAssessment, assert no leakage
  exceedance-backtest/ expanding-window, in-season, vs base-rate + rain-rule
  vs-ea-prediction/    head-to-head calibration vs the official EA flag
  censoring-ablation/  cost of ignoring moreThan/lessThan censoring
  skill-ceiling/       predictable vs irreducible under weekly censored sampling
internal/
  exceedance/          hierarchical censored log-count model + ensemble
  catchment/           zone-of-influence ↔ rain/flow gauge linkage
  bwq/                 EA BWQ API client (LDA endpoints, JSON/CSV, censoring)
  hydro/               flood-monitoring + hydrology covariate client
data/
  raw/                 cached pulls (gitignored beyond samples)
  predictions/         committed forecasts (the proof-of-commit ledger)
SOURCES.md
README.md
PLAN.md
```

## Dashboard & distribution

- Static-baked via the dexetera pattern: BWQ + covariates pulled and posteriors
  precomputed server-side, the exceedance simulator compiled to WASM so the reader
  runs site ensembles client-side (inline driver, R2 binary).
- Hero widgets: (1) a **map of designated beaches** coloured by current
  `P(exceed)`, click a site for its predictive distribution and the rain/flow that
  drives it; (2) a **rainfall→exceedance explorer** — drag antecedent-rainfall
  sliders and watch the exceedance probability and the implied "advice against
  bathing" call shift, with real resolved samples overlaid; (3) the **season-class
  projection** per site, updating weekly.
- Landing-page hook: a rotating "beach of the week" exceedance forecast as an SVG
  thumbnail drop; the weekly resolve feeds the public calibration curve and the
  EA head-to-head scorecard.
- Audience: a summer-seasonal public-interest hook (swimmers, wild-swimming
  communities, the very live sewage-spill debate) with civic, non-bettable framing
  that sits naturally beside the health-surveillance flagship.

## Status

Draft / not started. Gating checks before build:
1. Smoke-test the BWQ in-season endpoint (e.g. latest SampleAssessments) and
   confirm the censoring qualifiers and threshold URIs parse cleanly; pull one
   site's full history end-to-end.
2. Build the catchment linkage for a handful of known problem beaches (map zone-
   of-influence + named outfalls to nearby rainfall/flow gauges) and sanity-check
   the rain→count association before committing the model.
3. Implement the censored likelihood and unit-test it (recover known thresholds
   from synthetic censored data) before any real inference — censoring correctness
   is load-bearing.
4. One end-to-end committed weekly forecast for one region before scaling to all
   sites and the season-class projection.
5. Confirm the EA `RiskPrediction` coverage (which sites, what cadence) so the
   head-to-head's scope and its stated coverage limit are honest.

## Attribution (draft)

Contains public sector information from the Environment Agency (Bathing Water
Quality, Flood-Monitoring, Hydrology and Rainfall APIs) licensed under the Open
Government Licence v3.0. This project is a non-commercial public-interest
methodological exercise, is not affiliated with or endorsed by the Environment
Agency, and is not an official source of advice on bathing-water safety — for
that, consult the Environment Agency's own Swimfo service.