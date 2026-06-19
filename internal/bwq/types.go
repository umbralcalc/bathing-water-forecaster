// Package bwq is a client for the Environment Agency Bathing Water Quality API
// (https://environment.data.gov.uk/bwq), an Epimorphics linked-data API serving
// in-season sample assessments and annual compliance classifications for the
// designated bathing waters of England under the Open Government Licence v3.0.
//
// The single design decision this package exists to get right is censoring.
// Lab colony counts arrive with a qualifier — "=", "<" or ">" — and in recent
// data the majority of counts are left-censored ("< reporting limit"). Those are
// interval observations, not point values, and the downstream likelihood must
// treat them as such. We therefore never collapse a count to a bare float: every
// measurement carries its [Censoring] alongside the reported value.
package bwq

import "time"

// Censoring records how a reported lab count relates to the true concentration,
// decoded from the API's countQualifierNotation field.
type Censoring int

const (
	// Actual ("=") — the reported value is the measured count.
	Actual Censoring = iota
	// LessThan ("<") — left-censored: the true count is below the reported
	// value (the reporting/detection limit).
	LessThan
	// GreaterThan (">") — right-censored: the true count exceeds the reported
	// value (the assay's upper countable bound).
	GreaterThan
)

func (c Censoring) String() string {
	switch c {
	case Actual:
		return "="
	case LessThan:
		return "<"
	case GreaterThan:
		return ">"
	default:
		return "?"
	}
}

// Count is a single determinand measurement: a reported colony count per 100 ml
// together with the censoring that qualifies it. Present is false when the API
// item omits the determinand entirely (distinct from a reported zero).
type Count struct {
	Value     float64
	Censoring Censoring
	Present   bool
}

// Sample is one in-season SampleAssessment: the weekly lab result at a designated
// England sampling point that the forecaster commits a probability against.
type Sample struct {
	BathingWaterID   string    // eubwidNotation, e.g. "ukc2102-03600"
	BathingWaterName string    // e.g. "Spittal"
	SamplePoint      string    // samplePointNotation, e.g. "03600" — the join key
	Time             time.Time // sampleDateTime
	RecordDate       time.Time // publication revision; latest wins on dedup
	Week             string    // sampleWeek label, e.g. "2025-W35"
	EColi            Count     // Escherichia coli
	Enterococci      Count     // intestinal enterococci
	// Discountable marks a sample eligible to be discounted from the annual
	// classification under abnormal-situation rules. It is the API's actual
	// mechanism — there is no "abnormalWeatherException" field.
	Discountable bool
}

// ComplianceRegime distinguishes the two annual-classification cubes the API
// serves, which use disjoint code vocabularies across disjoint year ranges.
type ComplianceRegime int

const (
	// RegimeRBWD is the revised Bathing Water Directive 4-class system
	// (Excellent/Good/Sufficient/Poor), years 2015 onward — the season-class
	// projection ground truth.
	RegimeRBWD ComplianceRegime = iota
	// RegimeEEC is the historic EEC-directive system (Imperative/Guideline/
	// Fail/Closed), years 1988–2014 — long-run context only.
	RegimeEEC
)

// Compliance is one annual ComplianceAssessment for a sampling point. The point's
// coordinates are carried because the compliance feed is the most convenient
// published source of per-site location for the catchment linkage.
type Compliance struct {
	SamplePoint      string
	BathingWaterID   string
	BathingWaterName string
	Year             int
	Regime           ComplianceRegime
	ClassCode        string  // "1".."4","11" (rBWD) or "I","G","F","C" (EEC)
	ClassName        string  // e.g. "Excellent", "Fail"
	Lat              float64 // WGS84; 0 if absent
	Long             float64
	Easting          float64 // OSGB36; 0 if absent
	Northing         float64
}
