package bwq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DefaultBaseURL is the live EA linked-data API. Item lists are served under the
// /doc prefix (the /data prefix returns dataset descriptions and 400s on item
// queries).
const DefaultBaseURL = "https://environment.data.gov.uk"

// Endpoint paths, all verified live. The two compliance cubes are distinct
// resources with disjoint code vocabularies and year ranges.
const (
	pathInSeasonSample = "/doc/bathing-water-quality/in-season/sample.json"
	pathComplianceEEC  = "/doc/bathing-water-quality/compliance.json"
	pathComplianceRBWD = "/doc/bathing-water-quality/compliance-rBWD.json"

	// samplePointFilter is the working per-site filter: a dotted property path.
	// The path-selector (/sample/point/NNNNN.json) 404s and a bare
	// samplePointNotation param returns nothing.
	samplePointFilter = "bwq_samplingPoint.samplePointNotation"

	// maxPageSize is the API's effective per-request item cap.
	maxPageSize = 1000
)

// Client talks to the EA Bathing Water Quality API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New returns a Client pointed at the live API with a sane timeout.
func New() *Client {
	return &Client{
		BaseURL:    DefaultBaseURL,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// InSeasonSamples returns every in-season SampleAssessment for one sampling
// point (by samplePointNotation, e.g. "03600"), newest first, following
// pagination to exhaustion.
func (c *Client) InSeasonSamples(ctx context.Context, samplePoint string) ([]Sample, error) {
	q := url.Values{}
	q.Set(samplePointFilter, samplePoint)
	q.Set("_sort", "-sampleDateTime")

	var out []Sample
	err := c.paginate(ctx, pathInSeasonSample, q, func(item json.RawMessage) error {
		var r rawSample
		if err := json.Unmarshal(item, &r); err != nil {
			return err
		}
		out = append(out, r.toSample())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dedupeSamples(out), nil
}

// dedupeSamples collapses the multiple published revisions of a single physical
// sample (same point and sampleDateTime) to its latest recordDate, preserving
// the input's newest-first order. Without this every downstream count and the
// backtest would double-count republished samples.
func dedupeSamples(samples []Sample) []Sample {
	type key struct {
		point string
		t     time.Time
	}
	latest := make(map[key]int, len(samples))
	for i, s := range samples {
		k := key{s.SamplePoint, s.Time}
		if j, ok := latest[k]; ok && !samples[i].RecordDate.After(samples[j].RecordDate) {
			continue
		}
		latest[k] = i
	}
	out := make([]Sample, 0, len(latest))
	for i, s := range samples {
		if latest[key{s.SamplePoint, s.Time}] == i {
			out = append(out, s)
		}
	}
	return out
}

// Compliance returns the annual classifications for one sampling point under the
// given regime, newest year first.
func (c *Client) Compliance(ctx context.Context, regime ComplianceRegime, samplePoint string) ([]Compliance, error) {
	path := pathComplianceRBWD
	if regime == RegimeEEC {
		path = pathComplianceEEC
	}
	q := url.Values{}
	q.Set(samplePointFilter, samplePoint)
	q.Set("_sort", "-sampleYear.ordinalYear") // nested key; bare -sampleYear mis-sorts

	var out []Compliance
	err := c.paginate(ctx, path, q, func(item json.RawMessage) error {
		var r rawCompliance
		if err := json.Unmarshal(item, &r); err != nil {
			return err
		}
		out = append(out, r.toCompliance(regime))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SamplingPoint identifies a designated bathing-water sampling point and its
// location — the unit the forecaster iterates over to cover the whole country.
type SamplingPoint struct {
	Notation       string // samplePointNotation, e.g. "03600"
	BathingWaterID string // eubwidNotation
	Name           string
	Lat            float64
	Long           float64
	Year           int // the rBWD classification year this record came from
}

// DesignatedSites returns every designated England site classified in the most
// recent rBWD year, with coordinates — the canonical "all ~400 sites" list. It
// reads the rBWD compliance cube newest-year first, keeps only the latest year,
// and dedupes by sampling point. Coordinates come free with each record, so no
// per-site location lookup is needed downstream.
func (c *Client) DesignatedSites(ctx context.Context) ([]SamplingPoint, error) {
	q := url.Values{}
	q.Set("_sort", "-sampleYear.ordinalYear")

	var sites []SamplingPoint
	seen := make(map[string]bool)
	latestYear := 0
	stop := false

	err := c.paginate(ctx, pathComplianceRBWD, q, func(item json.RawMessage) error {
		if stop {
			return nil
		}
		var r rawCompliance
		if err := json.Unmarshal(item, &r); err != nil {
			return err
		}
		cm := r.toCompliance(RegimeRBWD)
		if latestYear == 0 {
			latestYear = cm.Year
		}
		if cm.Year < latestYear {
			stop = true // sorted desc by year: nothing newer remains
			return nil
		}
		if cm.SamplePoint == "" || seen[cm.SamplePoint] {
			return nil
		}
		seen[cm.SamplePoint] = true
		sites = append(sites, SamplingPoint{
			Notation:       cm.SamplePoint,
			BathingWaterID: cm.BathingWaterID,
			Name:           cm.BathingWaterName,
			Lat:            cm.Lat,
			Long:           cm.Long,
			Year:           cm.Year,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sites, nil
}

// paginate walks _page=0,1,... at maxPageSize, invoking fn for each item, until a
// short page signals exhaustion.
func (c *Client) paginate(ctx context.Context, path string, q url.Values, fn func(json.RawMessage) error) error {
	q.Set("_pageSize", fmt.Sprintf("%d", maxPageSize))
	for page := 0; ; page++ {
		q.Set("_page", fmt.Sprintf("%d", page))
		items, err := c.getItems(ctx, path, q)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := fn(item); err != nil {
				return err
			}
		}
		if len(items) < maxPageSize {
			return nil
		}
	}
}

// getItems performs one request and returns its decoded item envelope.
func (c *Client) getItems(ctx context.Context, path string, q url.Values) ([]json.RawMessage, error) {
	u := c.BaseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bwq: GET %s: status %d: %s", u, resp.StatusCode, snippet(body))
	}
	return decodeItems(body)
}

// decodeItems unwraps the ELDA result envelope to its raw items.
func decodeItems(body []byte) ([]json.RawMessage, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("bwq: decoding envelope: %w", err)
	}
	return env.Result.Items, nil
}

func snippet(b []byte) string {
	const n = 200
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
