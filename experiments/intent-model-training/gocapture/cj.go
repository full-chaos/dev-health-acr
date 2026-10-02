package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonical JSON (spec §5): object keys sorted, no whitespace, integers as
// exact decimal text, no floats, strings escaped as Python's
// json.dumps(ensure_ascii=True). Go and Python produce identical bytes.

func cj(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return cjRaw(raw)
}

func cjRaw(raw []byte) ([]byte, error) {
	// Refuse invalid UTF-8: encoding/json would silently replace it, and
	// Python refuses it (sol r1 M10).
	if !utf8.Valid(raw) {
		return nil, errors.New("canonical JSON: invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("trailing JSON")
	}
	var out bytes.Buffer
	if err := writeCJ(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCJ(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case json.Number:
		text := v.String()
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			if _, uerr := strconv.ParseUint(text, 10, 64); uerr != nil {
				return fmt.Errorf("canonical JSON admits integers only, got %q", text)
			}
		}
		out.WriteString(text)
	case string:
		if err := writeASCIIString(out, v); err != nil {
			return err
		}
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCJ(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeASCIIString(out, k); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeCJ(out, v[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("canonical JSON: unsupported %T", value)
	}
	return nil
}

// writeASCIIString writes a JSON string exactly as Python's
// json.dumps(ensure_ascii=True) does: non-ASCII as lowercase \uxxxx,
// astral code points as UTF-16 surrogate pairs. Invalid UTF-8 is refused.
func writeASCIIString(out *bytes.Buffer, s string) error {
	out.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x80 {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size <= 1 {
				return errors.New("canonical JSON: invalid UTF-8")
			}
			if r > 0xFFFF {
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(out, `\u%04x\u%04x`, hi, lo)
			} else {
				fmt.Fprintf(out, `\u%04x`, r)
			}
			i += size
			continue
		}
		i++
		switch c {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		default:
			// Python escapes everything outside ' '..'~', DEL included.
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(out, `\u%04x`, c)
			} else {
				out.WriteByte(c)
			}
		}
	}
	out.WriteByte('"')
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cjSHA(v any) (string, error) {
	data, err := cj(v)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}
