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
	BathingWater struct {
		About          eldaString `json:"_about"`
		EUBWIDNotation eldaString `json:"eubwidNotation"`
		Name           eldaString `json:"name"`
	} `json:"bwq_bathingWater"`
	SamplingPoint struct {
		Notation eldaString `json:"samplePointNotation"`
	} `json:"bwq_samplingPoint"`
	SampleDateTime struct {
		Inner struct {
			Value eldaString `json:"_value"`
		} `json:"inXSDDateTime"`
	} `json:"sampleDateTime"`
	SampleWeek struct {
		Label eldaString `json:"label"`
	} `json:"sampleWeek"`
	EColiCount     eldaFloat    `json:"escherichiaColiCount"`
	EColiQualifier rawQualifier `json:"escherichiaColiQualifier"`
	EntCount       eldaFloat    `json:"intestinalEnterococciCount"`
	EntQualifier   rawQualifier `json:"intestinalEnterococciQualifier"`
	Discountable   eldaBool     `json:"discountable"`
}

type rawQualifier struct {
	Notation eldaString `json:"countQualifierNotation"`
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
