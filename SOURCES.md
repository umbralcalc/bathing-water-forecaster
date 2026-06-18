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

## 2. Incumbent baseline — EA RiskPrediction (PRF) ⏳

The official daily `normal`/`increased`/`unknown` short-term pollution-risk flag,
PRF-enrolled sites only. Used **only** as a head-to-head comparison baseline
(`cmd/vs-ea-prediction`), never as a model input. ⏳ Confirm the stp-risk-
prediction endpoint path, site coverage, and cadence (PLAN gating check #5).

---

## 3. Covariates — EA Flood-Monitoring + Hydrology + Rainfall ⏳

Same platform, same OGL. ~15-minute rainfall and river-level/flow readings from
~8,000 stations. Linked to each bathing water via its published **zone of
influence** and profile outfall features (ESO/SWO/TSO). ⏳ Endpoints and the
catchment linkage are PLAN gating check #2.

- Flood-Monitoring: `https://environment.data.gov.uk/flood-monitoring/...`
- Hydrology: `https://environment.data.gov.uk/hydrology/...`

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
