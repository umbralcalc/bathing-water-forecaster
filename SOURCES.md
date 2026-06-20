# SOURCES

Data sources, their licences, and the **verified** API surface for the
bathing-water-forecaster. Everything here is Environment Agency open data under
the **Open Government Licence v3.0**, no API key or registration required.

Entries marked ✅ have been smoke-tested live against the API (see the
"Verified" notes). Entries marked ⏳ are planned/expected and not yet confirmed.

## Licence

Contains public sector information from the Environment Agency licensed under the
Open Government Licence v3.0
(`http://reference.data.gov.uk/id/open-government-licence`). The in-season dataset
self-declares this licence in its metadata.

---

## 1. Resolution layer — EA Bathing Water Quality API ✅

Epimorphics linked-data API (ELDA), base `https://environment.data.gov.uk`.
Content negotiation by extension: `.json`, `.csv`, `.ttl`, `.xml`. The `/doc/...`
prefix returns rendered items; the `/data/...` prefix returns dataset/cube
descriptions (and 400s on item-list queries — use `/doc/...` for items).

### In-season SampleAssessment feed (the forecast target) ✅

```
GET /doc/bathing-water-quality/in-season/sample.json?_pageSize=N
```

- Paging: `_pageSize`, `_page`. Sorting: `_sort=-sampleDateTime`.
- Per-site filter (dotted-path property filter — the working form):
  `?bwq_samplingPoint.samplePointNotation=36071`
  (the path-selector `/sample/point/36071.json` 404s; a bare `samplePointNotation`
  param returns 0 items.)

Verified item shape (point 36071, 2025-W35):

| Field | Example | Notes |
|---|---|---|
| `bwq_bathingWater.eubwidNotation` | `ukg2200-36071` | designated water id |
| `bwq_bathingWater.name._value` | `River Severn in Shrewsbury` | |
| `bwq_samplingPoint.samplePointNotation` | `36071` | the join key |
| `sampleDateTime.inXSDDateTime._value` | `2025-08-28T10:05:00` | |
| `sampleWeek.label` | `British Week:2025-W35` | |
| `escherichiaColiCount` | `150` | colony count /100ml |
| `escherichiaColiQualifier.countQualifierNotation` | `=` | **censoring flag** |
| `intestinalEnterococciCount` | `73` | |
| `intestinalEnterococciQualifier.countQualifierNotation` | `<` | **censoring flag** |
| `discountable` | `false` | boolean (abnormal-weather discount) |

**Censoring is real and dominant (verified).** `countQualifierNotation` ∈
`{ =, <, > }` mapping to actual / lessThan / greaterThan
(`def/bathing-water-quality/{actual,lessThan,greaterThan}`). In the 300
most-recent samples: E. coli `<`×180, `=`×119, `>`×1; enterococci `<`×193,
`=`×107. **>60% of observations are left-censored** ("below reporting limit").
Treating these as point values is not an option — the censored likelihood is
load-bearing (see PLAN "Validation & honesty rules" and `cmd/censoring-ablation`).

**Republished revisions — must dedupe (verified).** The same physical sample is
published multiple times under different `recordDate` values as the lab result is
confirmed/corrected — the item URI ends `.../date/20190614/time/101000/recordDate/
20210412`. Point 03600 returns 834 records for 785 distinct samples (4 revisions
of the 2019-06-14 sample alone). The client dedupes by (samplePoint,
sampleDateTime) keeping the latest `recordDate`; skipping this double-counts every
downstream statistic and the backtest.

**History depth — DEEP (verified, corrects an earlier caveat).** Established
sites carry the full record back to **1988** — e.g. points 03600/04200/04800/
10500/20100/30100/03700 each return **650–834** in-season samples spanning
1988-05 → 2026-06. Short records (point 36071: 44 samples from 2024; point 04220:
5 from 2026) are *newly-designated* sites, not a feed limitation. The backtest
window is wide, not the constraint earlier supposed. (Note: the published-CSV
`source` for pre-2011 rows is a 2011 baseline file, so the deep history is a
loaded historical series, not contemporaneous in-season publication — fine for
backtesting, worth stating.)

**Abnormal-weather flag — it's `discountable`, NOT `abnormalWeatherException`
(verified, corrects PLAN naming).** No `abnormalWeatherException` field exists on
items; the only relevant key is the `discountable` boolean (a sample eligible to
be discounted from the annual classification under abnormal-situation rules,
`def/.../AbnormalSituation`). It is uniformly `false` across the 1000 most-recent
samples — discounting is rare. ⏳ Find a `discountable=true` example in historical
data to confirm semantics; update PLAN's "abnormalWeatherException" references.
`SuspensionOfMonitoring` / `PollutionIncident` (PLAN) are not surfaced on the
in-season sample item — ⏳ locate them (likely separate concepts/endpoints).

### Thresholds — statutory constants, NOT an endpoint ⏳

All `/{def,doc}/.../threshold` paths 404. The rBWD exceedance cuts are fixed
statutory values that differ by water type (coastal/transitional vs inland
freshwater) and determinand. They must be **encoded from the Bathing Water
Regulations 2013 schedule**, not fetched. ⏳ Transcribe and cite the exact
single-sample/percentile cuts before wiring exceedance probabilities.

### Annual ComplianceAssessment — TWO cubes, two regimes (verified) ✅

The annual ground truth splits across two datasets by classification regime.
Both filter by `?bwq_samplingPoint.samplePointNotation=NNNNN` and carry the
sampling point's **lat/long/easting/northing** (directly useful for catchment
linkage, check #2).

- **Historic EEC-directive** — `GET /doc/bathing-water-quality/compliance.json`.
  Years **1988–2014**. Codes (`def/bathing-water-quality/{I,G,F,C}`):
  `I` Minimum (Imperative), `G` Higher (Guideline), `F` Fail, `C` Closed.
  *Not* the rBWD 4-class system — do not use for the season-class projection
  target; useful only as long-run context.
- **Modern rBWD 4-class** — `GET /doc/bathing-water-quality/compliance-rBWD.json`.
  Years **2015–2025** (latest = 2025, ~400 sites). Codes
  (`def/bwq-cc-2015/{1,2,3,4,11}`): **`1` Excellent, `2` Good, `3` Sufficient,
  `4` Poor**, `11` Closed. **This is the season-class projection ground truth.**
  2025 distribution (~400 sites): Excellent 287, Good 80, Poor 18, Sufficient 13,
  Closed 2 — i.e. exceedance/Poor is a *rare* outcome at the season level too,
  reinforcing the rare-event framing.

Sort with the nested key `_sort=-sampleYear.ordinalYear` (a bare `_sort=-sampleYear`
mis-sorts; year filters like `?sampleYear.ordinalYear=2024` return 0 — filter by
point or page, not by year).

---

## 2. Incumbent baseline — EA RiskPrediction (PRF) ✅

The official daily short-term pollution-risk flag. Used **only** as a head-to-head
comparison baseline (`cmd/vs-ea-prediction`), never as a model input.

`GET /doc/bathing-water-quality/stp-risk-prediction.json` (ELDA, same platform).
Per-site filter `?stp_samplingPoint.samplePointNotation=04700` (dotted-path, as on
the sample feed). Item fields: `stp_samplingPoint` (flat URI, point notation in the
trailing segment), `riskLevel` (`def/bwq-stp/{normal,increased,unknown}`),
`predictedOn` (the forecast day), `publishedAt` (disambiguates re-issues for a day),
`prfOriginType` (`PRF_PROVIDED`/`NON_PRF_SITE`/null).

**Coverage (verified, PLAN check #5).** Cadence is **daily, 2013→2025**. Across a
1000-row recent scan: ~5% `increased`, the rest `normal`. `prfOriginType` is
**unreliable historically** (null on most older rows), so the dependable signal for
"is this site actually EA-forecast" is whether it has any `increased` flags. In a
7-site cluster only **3 were EA-forecast** (Seaton Sluice, Blyth, Spittal); 4
(Whitley Bay, Amble, Bamburgh, Great Yarmouth) had **no** `increased` flag ever —
concrete evidence for the "all designated sites, not only PRF ones" gap the project
occupies. Notably the EA forecasts Spittal heavily (181 `increased`) despite its
weak local rain association, while it does **not** forecast Whitley Bay, which has a
strong one.

**Head-to-head result (3 EA-forecast sites, 670 matched samples, 3.3% exceedance):**
the EA flag discriminates well (27% exceedance on `increased` days vs 2% on `normal`).
Scored out-of-sample, the calibrated model is **slightly worse on Brier**
(−0.07 skill; the EA flag's sharp binary discrimination is hard to beat there) but
**clearly better on log-loss** (0.135 vs 0.197), because 13 of 22 exceedances
occurred on days the EA called `normal` — within-`normal` variation the continuous
rainfall model captures and a binary flag cannot. The honest headline is exactly the
PLAN's: comparable on the flag, with added calibration and coverage the official
forecast doesn't provide.

---

## 3. Covariates — EA Hydrology + Flood-Monitoring ✅

Same platform, same OGL. Rainfall (and river flow/level) gauges, queried
spatially and read as dated series. Two sibling APIs with a latency/history
trade-off — `internal/hydro` wraps the Hydrology one; the Flood-Monitoring one is
a later live-latency addition behind the same types.

**Hydrology API — long records (used for backtest + association)** ✅
`https://environment.data.gov.uk/hydrology`

- Spatial station search: `GET /id/stations?observedProperty=rainfall&lat=&long=&dist=`
  (dist in km). Items carry `notation`/`stationGuid`, `lat`/`long`, and `measures[]`.
- Each measure id encodes period: `…-rainfall-t-86400-mm-qualified` (daily total)
  and `…-rainfall-t-900-mm-qualified` (15-minute). `period` is 86400 / 900 seconds.
- Readings: `GET /id/measures/{measureID}/readings?mineq-date=YYYY-MM-DD&max-date=
  YYYY-MM-DD&_limit=N`. Items have `date` (daily) or `dateTime` (sub-daily),
  `value`, `quality` ("Unchecked"/"Good"/…), and **string-typed `invalid`/`missing`
  flags ("0"/"1")** — decoded to a validity bool. Berwick gauge: continuous daily
  back to at least 2019.

**Flood-Monitoring API — live, ~28-day window** (not yet wired)
`https://environment.data.gov.uk/flood-monitoring`. Same spatial form
(`/id/stations?parameter=rainfall&lat=&long=&dist=`), 15-min readings, but only a
rolling recent window — right for in-season live forecasting, wrong for history.

**Linkage & association — verified (PLAN check #2).** `internal/catchment` links a
site (coordinates from the compliance feed) to its nearest daily-rainfall gauge by
haversine distance and sums antecedent rainfall over a day-window before each
sample. `cmd/link-catchment` runs the rain→count sanity check. Across 12 sites
(2-day window, single nearest gauge — the crudest possible linkage), the
association is **present where expected and flat where expected**:

| Site | Pearson r(rain, log10 E.coli) | wet≥5mm vs dry elevated rate |
|---|---|---|
| Seaton Sluice | +0.38 | 12% vs 1% |
| Blyth South Beach | +0.38 | 5% vs 1% |
| Whitley Bay | +0.38 | 4% vs 1% |
| Bamburgh Castle | +0.39 | 2% vs 0% |
| Spittal (Tweed estuary) | +0.13 | 12% vs 9% |
| Great Yarmouth | +0.14 | 0% vs 1% |
| Amble Links | +0.02 | 2% vs 3% |

The causal backbone is real (wet-day exceedance up to ~12× dry-day at urban
Northumberland beaches) but **site-dependent** — estuary/large-catchment and very-
clean sites show no nearest-gauge signal, motivating the PLAN's zone-of-influence
linkage, rain-lag selection, and hierarchical pooling rather than a universal rain
rule. Note also that at clean sites most counts are left-censored (`<10`), so the
uncensored-only Pearson is thin there — the censored model (`internal/exceedance`)
is what recovers signal those rows hold.

⏳ Still to do for the fuller linkage: the published **zone of influence** spatial
entity and profile storm-overflow/outfall features (ESO/SWO/TSO), and river
flow/level gauges for river-mouth sites.

---

## 4. Static site context — Bathing Water Profiles ⏳

Sediment type, sampling frequency, `waterQualityImpactedByHeavyRain`, named
outfall features per site. Priors and pooling structure. ⏳ Locate the profile
endpoint / document source.

---

## Attribution

Contains public sector information from the Environment Agency (Bathing Water
Quality, Flood-Monitoring, Hydrology and Rainfall APIs) licensed under the Open
Government Licence v3.0. This project is a non-commercial public-interest
methodological exercise, is not affiliated with or endorsed by the Environment
Agency, and is not an official source of advice on bathing-water safety.
