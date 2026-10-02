package factoracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Leaf is one compared value with its type. Two leaves are equal only when
// both the type and the value text are equal, so an integer and an integral
// float, or a time and a string that looks like one, never match by accident.
type Leaf struct {
	T string `json:"t"`
	V string `json:"v"`
}

// Leaf types.
const (
	LeafNull   = "null"
	LeafBool   = "bool"
	LeafInt    = "int"
	LeafFloat  = "float"
	LeafString = "string"
	LeafTime   = "time"
	LeafDate   = "date"
)

func (l Leaf) String() string { return l.T + ":" + l.V }

// decodeJSON decodes with json.Number so no number passes through float64
// before it is typed.
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after the JSON value")
	}
	return out, nil
}

// sdlNamedType strips list and non-null marks: "[Float!]!" -> "Float".
func sdlNamedType(sdlType string) string {
	return strings.NewReplacer("[", "", "]", "", "!", "").Replace(strings.TrimSpace(sdlType))
}

// leafKindOfSDL maps an SDL named type to a leaf type. Enums and custom
// scalars that are not listed are strings.
func leafKindOfSDL(sdlType string) string {
	switch sdlNamedType(sdlType) {
	case "Int":
		return LeafInt
	case "Float":
		return LeafFloat
	case "Boolean":
		return LeafBool
	case "Date":
		return LeafDate
	case "DateTime":
		return LeafTime
	default:
		return LeafString
	}
}

// typedLeaf types a decoded JSON value as kind. A value whose JSON form does
// not fit the kind is an error: it is a type difference, never coerced.
func typedLeaf(kind string, value any) (Leaf, error) {
	if value == nil {
		return Leaf{T: LeafNull}, nil
	}
	switch kind {
	case LeafBool:
		b, ok := value.(bool)
		if !ok {
			return Leaf{}, fmt.Errorf("want a boolean, got %T", value)
		}
		return Leaf{T: LeafBool, V: strconv.FormatBool(b)}, nil
	case LeafInt:
		n, ok := value.(json.Number)
		if !ok {
			return Leaf{}, fmt.Errorf("want an integer, got %T", value)
		}
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return Leaf{}, fmt.Errorf("want an integer, got %s", n.String())
		}
		return Leaf{T: LeafInt, V: strconv.FormatInt(i, 10)}, nil
	case LeafFloat:
		n, ok := value.(json.Number)
		if !ok {
			return Leaf{}, fmt.Errorf("want a number, got %T", value)
		}
		f, err := strconv.ParseFloat(n.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return Leaf{}, fmt.Errorf("want a finite number, got %s", n.String())
		}
		return Leaf{T: LeafFloat, V: formatFloat(f)}, nil
	case LeafString:
		s, ok := value.(string)
		if !ok {
			return Leaf{}, fmt.Errorf("want a string, got %T", value)
		}
		return Leaf{T: LeafString, V: s}, nil
	case LeafDate:
		s, ok := value.(string)
		if !ok {
			return Leaf{}, fmt.Errorf("want a date string, got %T", value)
		}
		t, err := parseInstant(s)
		if err != nil {
			return Leaf{}, err
		}
		return Leaf{T: LeafDate, V: t.Format("2006-01-02")}, nil
	case LeafTime:
		s, ok := value.(string)
		if !ok {
			return Leaf{}, fmt.Errorf("want a time string, got %T", value)
		}
		t, err := parseInstant(s)
		if err != nil {
			return Leaf{}, err
		}
		return Leaf{T: LeafTime, V: t.UTC().Format(time.RFC3339Nano)}, nil
	default:
		return Leaf{}, fmt.Errorf("leaf kind %q is not known", kind)
	}
}

func formatFloat(f float64) string {
	if f == 0 {
		return "0"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// instantLayouts are the time forms the two planes print: GraphQL DateTime
// (RFC 3339), ClickHouse toString(DateTime[64]) and a plain date.
var instantLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02",
}

func parseInstant(s string) (time.Time, error) {
	for _, layout := range instantLayouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date or a time", s)
}

func leafFloat(l Leaf) (float64, bool) {
	if l.T != LeafFloat && l.T != LeafInt {
		return 0, false
	}
	f, err := strconv.ParseFloat(l.V, 64)
	return f, err == nil
}

// closeEnough is the float rule of a field that declares a tolerance: the
// relative difference is at most rel. rel 0 means exact.
func closeEnough(a, b, rel float64) bool {
	if a == b {
		return true
	}
	if rel <= 0 {
		return false
	}
	scale := math.Max(math.Abs(a), math.Abs(b))
	return math.Abs(a-b) <= rel*scale
}
