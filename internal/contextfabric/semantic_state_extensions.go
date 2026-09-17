package contextfabric

// The extension member value contract. One predicate decides what a member
// value may be, and it runs on both sides of the store: a snapshot whose
// members fail it is refused at capture (never written, never rewritten) and
// reads back malformed if one is ever found stored. Equality between two
// members is semantic, so a store that re-renders JSON (PostgreSQL jsonb
// reorders object keys and drops insignificant whitespace) still replays as
// the same snapshot.
//
// A member value is admitted when it is valid UTF-8 and valid JSON, and:
//   - nests at most SemanticStateExtensionMaxDepth arrays/objects deep;
//   - repeats no key inside any one object;
//   - carries no NUL character and no lone UTF-16 surrogate escape;
//   - writes every number as a plain decimal -- an optional minus sign, an
//     integer part without leading zeros, an optional fraction, no exponent,
//     never negative zero -- with at most
//     SemanticStateExtensionMaxNumberDigits digits. A store renders such a
//     number back unchanged, and two numbers are equal when their decimal
//     values are equal.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// SemanticStateExtensionMaxDepth bounds array/object nesting in a member.
	SemanticStateExtensionMaxDepth = 32
	// SemanticStateExtensionMaxNumberDigits bounds the digits of one number.
	SemanticStateExtensionMaxNumberDigits = 64
)

// validateSemanticStateExtensions applies the member contract to every
// member, in name order.
func validateSemanticStateExtensions(extensions SemanticStateExtensions) error {
	names := make([]string, 0, len(extensions))
	for name := range extensions {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
			return fmt.Errorf("extension member %d has a name that is not valid UTF-8 or carries a NUL character", i)
		}
		if err := validateSemanticStateExtensionValue(extensions[name]); err != nil {
			return fmt.Errorf("extension member %q %w", name, err)
		}
	}
	return nil
}

var (
	errExtensionNotUTF8        = errors.New("is not valid UTF-8")
	errExtensionNotJSON        = errors.New("is not one valid JSON value")
	errExtensionTooDeep        = fmt.Errorf("nests deeper than %d", SemanticStateExtensionMaxDepth)
	errExtensionRepeatedKey    = errors.New("repeats a key inside one object")
	errExtensionNUL            = errors.New("carries a NUL character")
	errExtensionLoneSurrogate  = errors.New("carries a lone UTF-16 surrogate escape")
	errExtensionNumberForm     = errors.New("carries a number that is not a plain decimal")
	errExtensionNumberTooLarge = fmt.Errorf("carries a number with more than %d digits", SemanticStateExtensionMaxNumberDigits)
)

// validateSemanticStateExtensionValue is the member value contract.
func validateSemanticStateExtensionValue(raw json.RawMessage) error {
	if !utf8.Valid(raw) {
		return errExtensionNotUTF8
	}
	if !json.Valid(raw) {
		return errExtensionNotJSON
	}
	if err := checkSurrogateEscapes(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	type frame struct {
		object    bool
		expectKey bool
		keys      map[string]bool
	}
	var stack []*frame
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errExtensionNotJSON
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		isKey := top != nil && top.object && top.expectKey
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{', '[':
				if len(stack) >= SemanticStateExtensionMaxDepth {
					return errExtensionTooDeep
				}
				if top != nil && top.object {
					top.expectKey = true
				}
				stack = append(stack, &frame{object: value == '{', expectKey: value == '{', keys: map[string]bool{}})
			default:
				stack = stack[:len(stack)-1]
			}
			continue
		case string:
			if strings.ContainsRune(value, 0) {
				return errExtensionNUL
			}
			if isKey {
				if top.keys[value] {
					return errExtensionRepeatedKey
				}
				top.keys[value] = true
				top.expectKey = false
				continue
			}
		case json.Number:
			if err := checkPlainDecimal(string(value)); err != nil {
				return err
			}
		}
		if top != nil && top.object {
			top.expectKey = true
		}
	}
}

// checkPlainDecimal admits -?(0|[1-9][0-9]*)(\.[0-9]+)? with a bounded digit
// count, and refuses negative zero.
func checkPlainDecimal(literal string) error {
	digits := strings.TrimPrefix(literal, "-")
	integer, fraction, hasFraction := strings.Cut(digits, ".")
	allDigits := func(s string) bool {
		if s == "" {
			return false
		}
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	}
	if !allDigits(integer) || (len(integer) > 1 && integer[0] == '0') || (hasFraction && !allDigits(fraction)) {
		return errExtensionNumberForm
	}
	if len(integer)+len(fraction) > SemanticStateExtensionMaxNumberDigits {
		return errExtensionNumberTooLarge
	}
	if strings.HasPrefix(literal, "-") && strings.Trim(integer+fraction, "0") == "" {
		return errExtensionNumberForm
	}
	return nil
}

// checkSurrogateEscapes refuses a \uD800-\uDFFF escape that is not a high
// surrogate immediately followed by a low one: the JSON decoder would
// silently read it as U+FFFD.
func checkSurrogateEscapes(raw []byte) error {
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			continue
		}
		switch c {
		case '"':
			inString = false
		case '\\':
			if i+1 >= len(raw) {
				return errExtensionNotJSON
			}
			if raw[i+1] != 'u' {
				i++
				continue
			}
			code, ok := hexEscape(raw, i)
			if !ok {
				return errExtensionNotJSON
			}
			i += 5
			switch {
			case code >= 0xDC00 && code <= 0xDFFF:
				return errExtensionLoneSurrogate
			case code >= 0xD800 && code <= 0xDBFF:
				low, ok := hexEscape(raw, i+1)
				if !ok || low < 0xDC00 || low > 0xDFFF {
					return errExtensionLoneSurrogate
				}
				i += 6
			}
		}
	}
	return nil
}

// hexEscape reads the \uXXXX escape starting at raw[at].
func hexEscape(raw []byte, at int) (rune, bool) {
	if at+6 > len(raw) || raw[at] != '\\' || raw[at+1] != 'u' {
		return 0, false
	}
	var code rune
	for _, h := range raw[at+2 : at+6] {
		code <<= 4
		switch {
		case h >= '0' && h <= '9':
			code |= rune(h - '0')
		case h >= 'a' && h <= 'f':
			code |= rune(h-'a') + 10
		case h >= 'A' && h <= 'F':
			code |= rune(h-'A') + 10
		default:
			return 0, false
		}
	}
	return code, true
}

// semanticStateExtensionsEqual is member equality: the same names, and
// values equal as JSON -- object key order and whitespace ignored, numbers
// compared by decimal value.
func semanticStateExtensionsEqual(a, b SemanticStateExtensions) bool {
	if len(a) != len(b) {
		return false
	}
	for name, av := range a {
		bv, ok := b[name]
		if !ok || !semanticStateExtensionValuesEqual(av, bv) {
			return false
		}
	}
	return true
}

func semanticStateExtensionValuesEqual(a, b json.RawMessage) bool {
	decode := func(raw json.RawMessage) (any, bool) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		return value, true
	}
	av, aok := decode(a)
	bv, bok := decode(b)
	return aok && bok && jsonValuesEqual(av, bv)
}

func jsonValuesEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for key, value := range av {
			other, ok := bv[key]
			if !ok || !jsonValuesEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonValuesEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case json.Number:
		bv, ok := b.(json.Number)
		if !ok {
			return false
		}
		if av == bv {
			return true
		}
		if checkPlainDecimal(string(av)) != nil || checkPlainDecimal(string(bv)) != nil {
			return false
		}
		ar, aok := new(big.Rat).SetString(string(av))
		br, bok := new(big.Rat).SetString(string(bv))
		return aok && bok && ar.Cmp(br) == 0
	default:
		return a == b
	}
}
