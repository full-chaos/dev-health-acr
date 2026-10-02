package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"
)

// credScanner finds the configured key in bytes and in every decoded form
// that could reach a persisted field (sol r1 H1): raw bytes, JSON-decoded
// strings (so \u escapes are resolved), percent-decoded strings, and
// base64 at every alignment in both alphabets.
type credScanner struct {
	key   string
	forms [][]byte
}

func newCredScanner(key string) *credScanner {
	s := &credScanner{key: key}
	if key == "" {
		return s
	}
	add := func(f string) {
		if len(f) >= 8 {
			s.forms = append(s.forms, []byte(f))
		}
	}
	add(key)
	add(url.QueryEscape(key))
	add(url.PathEscape(key))
	escaped, _ := json.Marshal(key)
	add(strings.Trim(string(escaped), `"`))
	// base64 of the key embedded at any offset: the characters fully
	// determined by the key bytes are stable across alignments.
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		for off := 0; off < 3; off++ {
			buf := append(make([]byte, off), key...)
			full := enc.EncodeToString(buf)
			start := (off*4 + 2) / 3
			end := ((off + len(key)) * 4) / 3
			if end > len(full) {
				end = len(full)
			}
			if start < end {
				add(full[start:end])
			}
		}
	}
	return s
}

func (s *credScanner) rawContains(b []byte) bool {
	for _, f := range s.forms {
		if bytes.Contains(b, f) {
			return true
		}
	}
	return false
}

func (s *credScanner) stringHit(v string) bool {
	if s.key == "" {
		return false
	}
	if s.rawContains([]byte(v)) {
		return true
	}
	if u, err := url.QueryUnescape(v); err == nil && strings.Contains(u, s.key) {
		return true
	}
	if u, err := url.PathUnescape(v); err == nil && strings.Contains(u, s.key) {
		return true
	}
	trimmed := strings.TrimSpace(v)
	if len(trimmed) >= 12 {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
			if decoded, err := enc.DecodeString(trimmed); err == nil && s.hit(decoded, 1) {
				return true
			}
		}
	}
	return false
}

// hit scans bytes: raw, then (if JSON) every decoded key and string value,
// recursively through base64 up to a small depth.
func (s *credScanner) hit(b []byte, depth int) bool {
	if s.key == "" {
		return false
	}
	if s.rawContains(b) {
		return true
	}
	if depth > 2 || !utf8.Valid(b) {
		return false
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return s.stringHit(string(b))
	}
	found := false
	var walk func(any)
	walk = func(x any) {
		if found {
			return
		}
		switch t := x.(type) {
		case string:
			found = s.stringHit(t)
		case []any:
			for _, i := range t {
				walk(i)
			}
		case map[string]any:
			for k, i := range t {
				if s.stringHit(k) {
					found = true
					return
				}
				walk(i)
			}
		}
	}
	walk(v)
	return found
}

func (s *credScanner) scan(b []byte) bool { return s.hit(b, 0) }
