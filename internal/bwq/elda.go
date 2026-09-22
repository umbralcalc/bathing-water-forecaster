package bwq

import (
	"encoding/json"
	"strconv"
	"time"
)

// The EA API is an Epimorphics linked-data API (ELDA). Its JSON encodes the same
// logical value in several shapes depending on cardinality and datatype: a plain
// string, an object {"_value": ...}, or an array of either. The decoders below
// normalise those shapes so the rest of the package sees ordinary Go values.

// envelope is the common ELDA response wrapper.
type envelope struct {
	Result struct {
		Items []json.RawMessage `json:"items"`
	} `json:"result"`
}

// eldaString decodes ELDA's polymorphic string forms: "x", {"_value":"x"},
// [{"_value":"x"}, ...]. Unknown shapes decode to the empty string rather than
// erroring, so one odd item never fails a whole page.
type eldaString string

func (s *eldaString) UnmarshalJSON(b []byte) error {
	var str string
	if json.Unmarshal(b, &str) == nil {
		*s = eldaString(str)
		return nil
	}
	var obj struct {
		Value string `json:"_value"`
	}
	if json.Unmarshal(b, &obj) == nil && obj.Value != "" {
		*s = eldaString(obj.Value)
		return nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(b, &arr) == nil && len(arr) > 0 {
		return s.UnmarshalJSON(arr[0])
	}
	return nil
}

// unmarshalFirst decodes an ELDA nested resource, which the schema implies is a
// single object but which arrives as an array of them whenever the item carries
// more than one value for the property (a republished qualifier, say). Taking
// the first keeps one odd item from failing a whole page, matching eldaString.
// dst must point at a method-free alias of the target type, or this recurses.
func unmarshalFirst(b []byte, dst any) error {
	if json.Unmarshal(b, dst) == nil {
		return nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(b, &arr) == nil && len(arr) > 0 {
		return unmarshalFirst(arr[0], dst)
	}
	return nil // unknown shape: leave the zero value rather than fail the page
}

// eldaLabelled is a resource reference carrying a human-readable label.
type eldaLabelled struct {
	Label eldaString `json:"label"`
}

func (l *eldaLabelled) UnmarshalJSON(b []byte) error {
	type plain eldaLabelled
	return unmarshalFirst(b, (*plain)(l))
}

// eldaBool decodes {"_value":"true","_datatype":"boolean"} as well as a bare
// JSON boolean.
type eldaBool bool

func (v *eldaBool) UnmarshalJSON(b []byte) error {
	var raw bool
	if json.Unmarshal(b, &raw) == nil {
		*v = eldaBool(raw)
		return nil
	}
	var s eldaString
	if err := s.UnmarshalJSON(b); err != nil {
		return err
	}
	*v = eldaBool(string(s) == "true")
	return nil
}

// eldaFloat decodes a bare JSON number or {"_value": 1.2} / {"_value": "1.2"}.
type eldaFloat struct {
	Value   float64
	Present bool
}

func (f *eldaFloat) UnmarshalJSON(b []byte) error {
	var num float64
	if json.Unmarshal(b, &num) == nil {
		f.Value, f.Present = num, true
		return nil
	}
	var obj struct {
		Value json.RawMessage `json:"_value"`
	}
	if json.Unmarshal(b, &obj) == nil && len(obj.Value) > 0 {
		if json.Unmarshal(obj.Value, &num) == nil {
			f.Value, f.Present = num, true
			return nil
		}
		var str string
		if json.Unmarshal(obj.Value, &str) == nil {
			if parsed, err := strconv.ParseFloat(str, 64); err == nil {
				f.Value, f.Present = parsed, true
			}
		}
	}
	return nil
}

// rawSample mirrors the in-season sample item fields this package consumes.
type rawSample struct {
	About          eldaString      `json:"_about"`
	BathingWater   rawBathingWater `json:"bwq_bathingWater"`
	SamplingPoint  rawPoint        `json:"bwq_samplingPoint"`
	SampleDateTime rawDateTime     `json:"sampleDateTime"`
	SampleWeek     eldaLabelled    `json:"sampleWeek"`
	EColiCount     eldaFloat       `json:"escherichiaColiCount"`
	EColiQualifier rawQualifier    `json:"escherichiaColiQualifier"`
	EntCount       eldaFloat       `json:"intestinalEnterococciCount"`
	EntQualifier   rawQualifier    `json:"intestinalEnterococciQualifier"`
	Discountable   eldaBool        `json:"discountable"`
}

type rawBathingWater struct {
	About          eldaString `json:"_about"`
	EUBWIDNotation eldaString `json:"eubwidNotation"`
	Name           eldaString `json:"name"`
}

func (v *rawBathingWater) UnmarshalJSON(b []byte) error {
	type plain rawBathingWater
	return unmarshalFirst(b, (*plain)(v))
}

type rawPoint struct {
	Notation eldaString `json:"samplePointNotation"`
}

func (v *rawPoint) UnmarshalJSON(b []byte) error {
	type plain rawPoint
	return unmarshalFirst(b, (*plain)(v))
}

type rawDateTime struct {
	Inner struct {
		Value eldaString `json:"_value"`
	} `json:"inXSDDateTime"`
}

func (v *rawDateTime) UnmarshalJSON(b []byte) error {
	type plain rawDateTime
	return unmarshalFirst(b, (*plain)(v))
}

type rawQualifier struct {
	Notation eldaString `json:"countQualifierNotation"`
}

func (v *rawQualifier) UnmarshalJSON(b []byte) error {
	type plain rawQualifier
	return unmarshalFirst(b, (*plain)(v))
}

func (q rawQualifier) censoring() Censoring {
	switch string(q.Notation) {
	case "<":
		return LessThan
	case ">":
		return GreaterThan
	default:
		return Actual
	}
}

// toSample maps a decoded raw item to the clean domain type.
func (r rawSample) toSample() Sample {
	t, _ := parseSampleTime(string(r.SampleDateTime.Inner.Value))
	return Sample{
		BathingWaterID:   string(r.BathingWater.EUBWIDNotation),
		BathingWaterName: string(r.BathingWater.Name),
		SamplePoint:      string(r.SamplingPoint.Notation),
		Time:             t,
		RecordDate:       recordDateFromAbout(string(r.About)),
		Week:             weekFromLabel(string(r.SampleWeek.Label)),
		EColi: Count{
			Value:     r.EColiCount.Value,
			Censoring: r.EColiQualifier.censoring(),
			Present:   r.EColiCount.Present,
		},
		Enterococci: Count{
			Value:     r.EntCount.Value,
			Censoring: r.EntQualifier.censoring(),
			Present:   r.EntCount.Present,
		},
		Discountable: bool(r.Discountable),
	}
}

// parseSampleTime parses the API's "2025-08-28T10:05:00" local-time stamps.
func parseSampleTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02T15:04:05", s)
}

// recordDateFromAbout extracts the publication revision date from an item URI of
// the form ".../recordDate/20210412". A sample is republished under several
// recordDates as the lab result is confirmed/corrected; the latest is canonical.
func recordDateFromAbout(about string) time.Time {
	const marker = "/recordDate/"
	i := indexOf(about, marker)
	if i < 0 {
		return time.Time{}
	}
	s := about[i+len(marker):]
	if j := indexByte(s, '/'); j >= 0 {
		s = s[:j]
	}
	t, _ := time.Parse("20060102", s)
	return t
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// weekFromLabel reduces "British Week:2025-W35" to "2025-W35".
func weekFromLabel(label string) string {
	if i := lastColon(label); i >= 0 {
		return label[i+1:]
	}
	return label
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

// rawCompliance mirrors the annual compliance item fields (shared shape across
// both the EEC and rBWD cubes).
type rawCompliance struct {
	BathingWater struct {
		EUBWIDNotation eldaString `json:"eubwidNotation"`
		Name           eldaString `json:"name"`
	} `json:"bwq_bathingWater"`
	SamplingPoint struct {
		Notation eldaString `json:"samplePointNotation"`
		Lat      eldaFloat  `json:"lat"`
		Long     eldaFloat  `json:"long"`
		Easting  eldaFloat  `json:"easting"`
		Northing eldaFloat  `json:"northing"`
	} `json:"bwq_samplingPoint"`
	Classification struct {
		Notation eldaString `json:"complianceCodeNotation"`
		Name     eldaString `json:"name"`
	} `json:"complianceClassification"`
	SampleYear struct {
		Ordinal eldaFloat `json:"ordinalYear"`
	} `json:"sampleYear"`
}

func (r rawCompliance) toCompliance(regime ComplianceRegime) Compliance {
	return Compliance{
		SamplePoint:      string(r.SamplingPoint.Notation),
		BathingWaterID:   string(r.BathingWater.EUBWIDNotation),
		BathingWaterName: string(r.BathingWater.Name),
		Year:             int(r.SampleYear.Ordinal.Value),
		Regime:           regime,
		ClassCode:        string(r.Classification.Notation),
		ClassName:        string(r.Classification.Name),
		Lat:              r.SamplingPoint.Lat.Value,
		Long:             r.SamplingPoint.Long.Value,
		Easting:          r.SamplingPoint.Easting.Value,
		Northing:         r.SamplingPoint.Northing.Value,
	}
}
