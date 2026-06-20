package bwq

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

const pathRiskPrediction = "/doc/bathing-water-quality/stp-risk-prediction.json"

// RiskLevel is the Environment Agency's operational short-term pollution-risk
// flag — the incumbent forecast the calibrated model is scored against.
type RiskLevel int

const (
	RiskNormal RiskLevel = iota
	RiskIncreased
	RiskUnknown
)

func (r RiskLevel) String() string {
	switch r {
	case RiskIncreased:
		return "increased"
	case RiskUnknown:
		return "unknown"
	default:
		return "normal"
	}
}

// RiskPrediction is one daily EA RiskPrediction for a sampling point. It is used
// only as a comparison baseline for the head-to-head, never as a model input.
type RiskPrediction struct {
	SamplePoint string
	Date        time.Time // predictedOn — the day the flag applies to
	Level       RiskLevel
	PublishedAt time.Time // disambiguates multiple issues for one date
	PRFType     string    // "PRF_PROVIDED", "NON_PRF_SITE", or "" (historical rows)
}

// RiskPredictions returns the EA risk-prediction history for one sampling point.
func (c *Client) RiskPredictions(ctx context.Context, samplePoint string) ([]RiskPrediction, error) {
	q := url.Values{}
	q.Set(stpSamplePointFilter, samplePoint)

	var out []RiskPrediction
	err := c.paginate(ctx, pathRiskPrediction, q, func(item json.RawMessage) error {
		var r rawRisk
		if err := json.Unmarshal(item, &r); err != nil {
			return err
		}
		out = append(out, r.toRiskPrediction())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// stpSamplePointFilter is the dotted-path filter that isolates one site's
// predictions (mirrors samplePointFilter on the sample feed).
const stpSamplePointFilter = "stp_samplingPoint.samplePointNotation"

type rawRisk struct {
	SamplingPoint eldaString `json:"stp_samplingPoint"`
	RiskLevel     eldaString `json:"riskLevel"`
	PredictedOn   struct {
		Value eldaString `json:"_value"`
	} `json:"predictedOn"`
	PublishedAt struct {
		Value eldaString `json:"_value"`
	} `json:"publishedAt"`
	PRFOriginType eldaString `json:"prfOriginType"`
}

func (r rawRisk) toRiskPrediction() RiskPrediction {
	date, _ := time.Parse("2006-01-02", string(r.PredictedOn.Value))
	pub, _ := time.Parse("2006-01-02T15:04:05", string(r.PublishedAt.Value))
	return RiskPrediction{
		SamplePoint: pointFromURI(string(r.SamplingPoint)),
		Date:        date,
		Level:       parseRiskLevel(string(r.RiskLevel)),
		PublishedAt: pub,
		PRFType:     string(r.PRFOriginType),
	}
}

func parseRiskLevel(uri string) RiskLevel {
	switch lastSegment(uri) {
	case "increased":
		return RiskIncreased
	case "unknown":
		return RiskUnknown
	default:
		return RiskNormal
	}
}

// pointFromURI extracts the trailing sampling-point notation from a sampling-
// point URI such as ".../bwsp.eaew/04700".
func pointFromURI(uri string) string { return lastSegment(uri) }

// RegionOf returns the UK NUTS1 region code embedded in a bathing-water
// eubwidNotation (e.g. "ukc2102-03600" → "ukc"), used as the grouping key for
// regional pooling. It returns "" when the notation is too short to classify.
func RegionOf(eubwid string) string {
	if len(eubwid) >= 3 && eubwid[:2] == "uk" {
		return eubwid[:3]
	}
	return ""
}

func lastSegment(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[i+1:]
		}
	}
	return s
}
