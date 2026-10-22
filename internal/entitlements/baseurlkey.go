package entitlements

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
)

// The ops llm-config route serves a stored base_url as it was saved. A base_url
// that carries the org's own api key (or userinfo, which a client sends as Basic
// credentials) would hand that key to whatever host the url names, so such a
// config is refused before it becomes an OrgLLMConfig and before any client dials.

const (
	baseURLRefusalKey      = "base_url_carries_key"
	baseURLRefusalUserinfo = "base_url_carries_userinfo"
)

// BaseURLRefusedError is a served config acr will not use because of its
// base_url. Reason is a fixed word; the url and the key are never part of it.
type BaseURLRefusedError struct{ Reason string }

func (e *BaseURLRefusedError) Error() string {
	return "Dev Health BYO LLM base_url refused: " + e.Reason
}

// Unwrap makes a refused base_url an unusable configuration: the org's turn
// fails closed and never falls back to the platform model.
func (e *BaseURLRefusedError) Unwrap() error { return ErrLLMConfigUnusable }

// baseURLRefusal is the fixed reason a base_url is refused for, or "". The
// key rule is the one ops applies when it saves and serves a base_url (the
// same leak forms over the url as written and after percent-decoding); userinfo
// is refused whatever it holds, as the ops base url validator refuses it.
func baseURLRefusal(baseURL, apiKey string) string {
	if carriesKey(keyLeakForms(apiKey), baseURL) {
		return baseURLRefusalKey
	}
	if baseURLHasUserinfo(baseURL) {
		return baseURLRefusalUserinfo
	}
	return ""
}

// baseURLHasUserinfo is whether the authority of baseURL (the text after the
// // that follows the scheme, or that starts a protocol-relative url, up to the
// first of / ? #) holds an @. It does not parse the url, so a url net/url would
// not parse is judged the same way.
func baseURLHasUserinfo(baseURL string) bool {
	rest := strings.TrimSpace(baseURL)
	if !strings.HasPrefix(rest, "//") {
		_, after, found := strings.Cut(rest, ":")
		if !found || !strings.HasPrefix(after, "//") {
			return false
		}
		rest = after
	}
	authority := rest[2:]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	return strings.Contains(authority, "@")
}

// keyLeakForms are the lower-cased forms in which the saved key could be
// echoed back by a provider: raw, percent-escaped, hex, and base64 at each of
// the three byte alignments (std and url alphabets, no padding). A key under
// eight bytes has its raw form only.
func keyLeakForms(apiKey string) []string {
	if apiKey == "" {
		return nil
	}
	forms := []string{strings.ToLower(apiKey)}
	if len(apiKey) < 8 {
		return forms
	}
	forms = append(forms, url.QueryEscape(apiKey), url.PathEscape(apiKey), hex.EncodeToString([]byte(apiKey)))
	for shift := 0; shift < 3; shift++ {
		raw := append(make([]byte, shift), apiKey...)
		for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			text := enc.EncodeToString(raw)
			start := []int{0, 2, 3}[shift]
			if len(text) > start+2 {
				forms = append(forms, text[start:len(text)-2])
			}
		}
	}
	for index, form := range forms {
		forms[index] = strings.ToLower(form)
	}
	return forms
}

// carriesKey is whether text holds any of the lower-cased key forms, as written
// or once its percent-escapes are decoded (a server reads %73k as sk).
func carriesKey(forms []string, text string) bool {
	for _, candidate := range percentDecodings(text) {
		lowered := strings.ToLower(candidate)
		for _, form := range forms {
			if form != "" && strings.Contains(lowered, form) {
				return true
			}
		}
	}
	return false
}

// maxPercentDecodings bounds how many layers of escaping are peeled off.
const maxPercentDecodings = 4

// percentDecodings is text, then text with every well-formed %XX replaced by its
// byte, repeated until nothing changes: a malformed escape elsewhere in the
// string never stops the decoding of the rest.
func percentDecodings(text string) []string {
	out := []string{text}
	for len(out) <= maxPercentDecodings {
		next := decodePercentPairs(out[len(out)-1])
		if next == out[len(out)-1] {
			break
		}
		out = append(out, next)
	}
	return out
}

func decodePercentPairs(text string) string {
	if !strings.Contains(text, "%") {
		return text
	}
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '%' && i+2 < len(text) && isHexDigit(text[i+1]) && isHexDigit(text[i+2]) {
			high, _ := strconv.ParseUint(text[i+1:i+3], 16, 8)
			out.WriteByte(byte(high))
			i += 2
			continue
		}
		out.WriteByte(text[i])
	}
	return out.String()
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
