package hydro

import (
	"encoding/json"
	"time"
)

// rawStation mirrors the Hydrology API station item.
type rawStation struct {
	Notation string       `json:"notation"`
	Guid     string       `json:"stationGuid"`
	Label    flexString   `json:"label"`
	Lat      float64      `json:"lat"`
	Long     float64      `json:"long"`
	Measures []rawMeasure `json:"measures"`
}

type rawMeasure struct {
	ID        string `json:"@id"`
	Parameter string `json:"parameter"`
	Period    int    `json:"period"`
}

func (r rawStation) toStation() Station {
	id := r.Notation
	if id == "" {
		id = r.Guid
	}
	measures := make([]Measure, 0, len(r.Measures))
	for _, m := range r.Measures {
		measures = append(measures, Measure{ID: m.ID, Parameter: m.Parameter, PeriodSec: m.Period})
	}
	return Station{
		ID:       id,
		Label:    string(r.Label),
		Lat:      r.Lat,
		Long:     r.Long,
		Measures: measures,
	}
}

// rawReading mirrors a Hydrology API readings item. value is a pointer so a
// missing/null measurement is distinguishable from a true zero; the invalid and
// missing flags arrive as strings ("0"/"1"), not JSON booleans.
type rawReading struct {
	Date     string   `json:"date"`
	DateTime string   `json:"dateTime"`
	Value    *float64 `json:"value"`
	Quality  string   `json:"quality"`
	Invalid  flexBool `json:"invalid"`
	Missing  flexBool `json:"missing"`
}

func (r rawReading) toReading() Reading {
	t := parseDay(r.Date)
	if t.IsZero() {
		t = parseInstant(r.DateTime)
	}
	valid := r.Value != nil && !bool(r.Invalid) && !bool(r.Missing)
	var v float64
	if r.Value != nil {
		v = *r.Value
	}
	return Reading{Date: t, Value: v, Valid: valid}
}

func parseDay(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func parseInstant(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// flexBool decodes the API's boolean-ish fields, which appear as JSON booleans
// or as the strings "0"/"1"/"true"/"false".
type flexBool bool

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	var native bool
	if json.Unmarshal(raw, &native) == nil {
		*b = flexBool(native)
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		*b = flexBool(s == "1" || s == "true")
		return nil
	}
	return nil
}

// flexString decodes a JSON string or the first element of a JSON string array
// (the API uses either for station labels).
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	var str string
	if json.Unmarshal(b, &str) == nil {
		*s = flexString(str)
		return nil
	}
	var arr []string
	if json.Unmarshal(b, &arr) == nil && len(arr) > 0 {
		*s = flexString(arr[0])
		return nil
	}
	return nil
}
