// Package hydro is a client for the EA Hydrology API
// (https://environment.data.gov.uk/hydrology), the long-record source of
// rainfall, river-flow and level readings used as covariates for the exceedance
// model.
//
// The Hydrology API serves quality-controlled historical series (years, not the
// ~28-day live window of the sibling Flood-Monitoring API), which is what the
// rain→count association check and the expanding-window backtest need. A
// lower-latency Flood-Monitoring path for live, in-season forecasting can be
// added behind the same types later.
package hydro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DefaultBaseURL is the live Hydrology API root.
const DefaultBaseURL = "https://environment.data.gov.uk/hydrology"

// Client talks to the EA Hydrology API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New returns a Client pointed at the live API.
func New() *Client {
	return &Client{
		BaseURL:    DefaultBaseURL,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Station is a hydrology monitoring station and the measures it reports.
type Station struct {
	ID       string // notation / stationGuid
	Label    string
	Lat      float64
	Long     float64
	Measures []Measure
}

// Measure is one observed series at a station (e.g. daily rainfall total).
type Measure struct {
	ID        string // full measure URI, used as the readings key
	Parameter string // "rainfall", "flow", "level"
	PeriodSec int    // 86400 = daily, 900 = 15-minute
}

// Reading is one observation. Date is the calendar day (for daily series) or the
// start instant of the interval (for sub-daily); Value is in the measure's unit.
type Reading struct {
	Date  time.Time
	Value float64
	Valid bool // false when the API marks the reading missing/invalid
}

// DailyRainfall returns the station's daily (86400s) rainfall measure, if any.
func (s Station) DailyRainfall() (Measure, bool) {
	return s.measure("rainfall", 86400)
}

func (s Station) measure(parameter string, periodSec int) (Measure, bool) {
	for _, m := range s.Measures {
		if m.Parameter == parameter && m.PeriodSec == periodSec {
			return m, true
		}
	}
	return Measure{}, false
}

// NearbyStations returns stations reporting observedProperty (e.g. "rainfall")
// within distKm of the given point, as served by the API's spatial filter.
func (c *Client) NearbyStations(ctx context.Context, observedProperty string, lat, long, distKm float64) ([]Station, error) {
	q := url.Values{}
	q.Set("observedProperty", observedProperty)
	q.Set("lat", trimFloat(lat))
	q.Set("long", trimFloat(long))
	q.Set("dist", trimFloat(distKm))

	body, err := c.get(ctx, "/id/stations", q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Items []rawStation `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("hydro: decoding stations: %w", err)
	}
	out := make([]Station, 0, len(resp.Items))
	for _, r := range resp.Items {
		out = append(out, r.toStation())
	}
	return out, nil
}

// Readings returns the readings of a single measure between start and end
// (inclusive), oldest first.
func (c *Client) Readings(ctx context.Context, measureID string, start, end time.Time) ([]Reading, error) {
	q := url.Values{}
	q.Set("mineq-date", start.Format("2006-01-02"))
	q.Set("max-date", end.Format("2006-01-02"))
	q.Set("_limit", "2000000")

	path := measurePath(measureID) + "/readings"
	body, err := c.get(ctx, path, q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Items []rawReading `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("hydro: decoding readings: %w", err)
	}
	out := make([]Reading, 0, len(resp.Items))
	for _, r := range resp.Items {
		out = append(out, r.toReading())
	}
	return out, nil
}

// measurePath reduces a full measure URI to the API path the client appends to
// BaseURL, tolerating a bare measure id as well.
func measurePath(measureID string) string {
	const marker = "/id/measures/"
	if i := indexOf(measureID, marker); i >= 0 {
		return measureID[i:]
	}
	return "/id/measures/" + measureID
}

func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
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
		return nil, fmt.Errorf("hydro: GET %s: status %d", u, resp.StatusCode)
	}
	return body, nil
}

func trimFloat(f float64) string {
	return fmt.Sprintf("%g", f)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
