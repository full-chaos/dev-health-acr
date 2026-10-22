package entitlements

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	storedTestKey = "sk-stored-" + "0123456789"
	cleanBaseURL  = "https://p.example.invalid/v1"
)

func percentEscapeAll(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		out.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{s[i]})))
	}
	return out.String()
}

// The oracle was produced by running the real dev-health-ops functions
// (CarriesKey over KeyLeakForms, and the base url validator's userinfo refusal)
// on synthetic keys; see the file's own header for the recipe. Every row is
// one case: this port must agree with ops on the key rule row by row, refuse
// every url ops refuses for userinfo, and accept every url that has neither.
func TestBaseURLKeyRulesMatchTheOpsOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/baseurl_key_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		OpsCommit string   `json:"ops_commit"`
		Keys      []string `json:"keys"`
		Rows      [][]any  `json:"rows"`
	}
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	if len(oracle.OpsCommit) != 40 || len(oracle.Keys) < 9 || len(oracle.Rows) < 500 {
		t.Fatalf("oracle is not the recorded one: commit %q, %d keys, %d rows", oracle.OpsCommit, len(oracle.Keys), len(oracle.Rows))
	}
	var hits, misses, userinfoRefusals int
	for index, row := range oracle.Rows {
		key := oracle.Keys[int(row[0].(float64))]
		baseURL, opsHit, opsUserinfo := row[1].(string), row[2].(bool), row[3].(bool)
		if got := carriesKey(keyLeakForms(key), baseURL); got != opsHit {
			t.Fatalf("row %d: key rule = %t, ops = %t (key %d bytes, url %d bytes)", index, got, opsHit, len(key), len(baseURL))
		}
		refusal := baseURLRefusal(baseURL, key)
		if opsHit && refusal == "" {
			t.Fatalf("row %d: ops refuses the url for the key and this port accepted it", index)
		}
		if opsUserinfo && !baseURLHasUserinfo(baseURL) {
			t.Fatalf("row %d: ops refuses the url for userinfo and this port did not", index)
		}
		if !opsHit && !opsUserinfo && refusal != "" && !strings.Contains(authorityOf(baseURL), "@") {
			t.Fatalf("row %d: this port refused a url with neither the key nor userinfo", index)
		}
		switch {
		case opsHit:
			hits++
		default:
			misses++
		}
		if opsUserinfo {
			userinfoRefusals++
		}
	}
	if hits < 200 || misses < 50 || userinfoRefusals < 40 {
		t.Fatalf("oracle shape: %d key hits, %d misses, %d userinfo refusals; the rows that prove each side are missing", hits, misses, userinfoRefusals)
	}
}

// authorityOf is the text between the scheme separator and the first of / ? #.
func authorityOf(baseURL string) string {
	_, rest, found := strings.Cut(baseURL, "://")
	if !found {
		return ""
	}
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		return rest[:end]
	}
	return rest
}

func TestBaseURLCarriesKeyAtWriteTime(t *testing.T) {
	if baseURLRefusal("https://p.example.invalid/v1/sk-live-0123456789", "sk-live-0123456789") != baseURLRefusalKey {
		t.Fatal("a base url holding the key must be refused")
	}
	if baseURLRefusal(cleanBaseURL, "sk-live-0123456789") != "" || baseURLRefusal(cleanBaseURL, "") != "" {
		t.Fatal("a clean base url must pass")
	}
}

// A key written with percent-escapes on characters URI syntax leaves bare (the
// letter s, a hyphen, a digit) is the same key once a server decodes the url.
func TestBaseURLCarriesKeyThroughPercentEscapes(t *testing.T) {
	for name, text := range map[string]string{
		"one letter escaped":          "https://p.example.invalid/%73k-stored-0123456789",
		"hyphen escaped":              "https://p.example.invalid/sk%2Dstored-0123456789",
		"digit escaped, upper hex":    "https://p.example.invalid/sk-stored-%30123456789",
		"every byte escaped":          "https://p.example.invalid/" + percentEscapeAll(storedTestKey),
		"double escaped":              "https://p.example.invalid/%2573k-stored-0123456789",
		"four layers":                 "https://p.example.invalid/" + percentEscapeAll(percentEscapeAll(percentEscapeAll(percentEscapeAll(storedTestKey)))),
		"bad escape beside the key":   "https://p.example.invalid/%zz/%73k-stored-0123456789",
		"escaped in the host":         "https://%73k-stored-0123456789.example.invalid/v1",
		"escaped in the query string": "https://p.example.invalid/v1?k=%73k-stored-0123456789",
		"upper case":                  "https://p.example.invalid/SK-STORED-0123456789",
	} {
		if baseURLRefusal(text, storedTestKey) != baseURLRefusalKey {
			t.Fatalf("%s: %q must be a key hit", name, text)
		}
	}
	five := percentEscapeAll(percentEscapeAll(percentEscapeAll(percentEscapeAll(percentEscapeAll(storedTestKey)))))
	for name, text := range map[string]string{
		"clean":                          cleanBaseURL,
		"api-version query":              "https://p.example.invalid/openai/deployments/d?api-version=2024-02-01",
		"escapes, not the key":           "https://p.example.invalid/%73k-other-0123456789",
		"half the key escaped":           "https://p.example.invalid/%73k-stored",
		"percent sign, no pairs":         "https://p.example.invalid/100%",
		"five layers (ops decodes four)": "https://p.example.invalid/" + five,
	} {
		if got := baseURLRefusal(text, storedTestKey); got != "" {
			t.Fatalf("%s: %q must not be refused, got %q", name, text, got)
		}
	}
}

// A derived form (escaped, hex, base64) of a key of any length counts when it
// is at least eight characters, as in ops: a 4-byte key is caught in hex (8
// characters), a 3-byte key has no derived form that long, and an empty key
// has no form at all.
func TestAShortKeysDerivedFormsOfEightCharsAreMatched(t *testing.T) {
	for _, tc := range []struct {
		key      string
		baseURL  string
		wantHit  bool
		describe string
	}{
		{"abcd", "https://p.example.invalid/" + hex.EncodeToString([]byte("abcd")), true, "4-byte key in hex (8 chars)"},
		{"abcd", "https://p.example.invalid/" + strings.ToUpper(hex.EncodeToString([]byte("abcd"))), true, "4-byte key in upper-case hex"},
		{"abcd", "https://p.example.invalid/" + percentEscapeAll(hex.EncodeToString([]byte("abcd"))), true, "4-byte key in hex, percent-escaped"},
		{"abcd", "https://p.example.invalid/abcd", true, "4-byte key raw"},
		{"abc", "https://p.example.invalid/" + hex.EncodeToString([]byte("abc")), false, "3-byte key in hex (6 chars) is not a form"},
		{"abcd", "https://p.example.invalid/" + hex.EncodeToString([]byte("abc")), false, "another key's hex is not a leak"},
		{"abcdefg", "https://p.example.invalid/" + hex.EncodeToString([]byte("abcdefg")), true, "7-byte key in hex (14 chars)"},
		{"abcdefg", "https://p.example.invalid/YWJjZGVmZw", true, "7-byte key in base64 (10 chars)"},
		{"a b ", "https://p.example.invalid/a+b+", false, "4-byte key query-escaped to 4 chars is not a form"},
		{"a b ", "https://p.example.invalid/a b ", true, "4-byte key raw with spaces"},
	} {
		if got := baseURLRefusal(tc.baseURL, tc.key) == baseURLRefusalKey; got != tc.wantHit {
			t.Errorf("%s: refused = %v, want %v (%s)", tc.describe, got, tc.wantHit, tc.baseURL)
		}
	}
	if got := keyLeakForms("abc"); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("forms of a 3-byte key = %v, want the raw form only", got)
	}
	if got := keyLeakForms(""); got != nil {
		t.Fatalf("forms of an empty key = %v, want none", got)
	}
}

// oldKeyLeakForms is keyLeakForms before the short-key rule: a key under eight
// bytes had its raw form only, every other key all its forms. A key of eight
// bytes or more must keep exactly those forms.
func oldKeyLeakForms(apiKey string) []string {
	forms := []string{strings.ToLower(apiKey)}
	if len(apiKey) < 8 {
		return forms
	}
	derived := []string{url.QueryEscape(apiKey), url.PathEscape(apiKey), hex.EncodeToString([]byte(apiKey))}
	for shift := 0; shift < 3; shift++ {
		raw := append(make([]byte, shift), apiKey...)
		for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			text := enc.EncodeToString(raw)
			start := []int{0, 2, 3}[shift]
			if len(text) > start+2 {
				derived = append(derived, text[start:len(text)-2])
			}
		}
	}
	for _, form := range derived {
		forms = append(forms, strings.ToLower(form))
	}
	return forms
}

func TestKeysOfEightBytesOrMoreKeepEveryFormTheyHad(t *testing.T) {
	alphabet := "sk-ant-ABCdef0123456789_+/=%. \u00e9"
	for length := 8; length <= 64; length++ {
		key := strings.Repeat(alphabet, length)[:length]
		for !utf8.ValidString(key) {
			key = key[:len(key)-1]
		}
		got, want := map[string]bool{}, map[string]bool{}
		for _, form := range keyLeakForms(key) {
			got[form] = true
		}
		for _, form := range oldKeyLeakForms(key) {
			want[form] = true
		}
		if len(got) != len(want) {
			t.Fatalf("key of %d bytes: %d forms, was %d", len(key), len(got), len(want))
		}
		for form := range want {
			if !got[form] {
				t.Fatalf("key of %d bytes lost the form %q", len(key), form)
			}
		}
	}
}

func TestUserinfoIsRefusedWhateverItHolds(t *testing.T) {
	for _, baseURL := range []string{
		"https://user:pass@p.example.invalid/v1",
		"https://u@p.example.invalid/v1",
		"https://:@p.example.invalid/v1",
		"https://a@b@p.example.invalid/v1",
		"//user:pass@p.example.invalid/v1",
		"//u@p.example.invalid/v1",
		"  //u@p.example.invalid/v1",
	} {
		if got := baseURLRefusal(baseURL, ""); got != baseURLRefusalUserinfo {
			t.Fatalf("%q: refusal = %q, want %q", baseURL, got, baseURLRefusalUserinfo)
		}
	}
	for _, baseURL := range []string{
		cleanBaseURL,
		"https://p.example.invalid/a@b",
		"https://p.example.invalid/v1?next=a@b",
		"https://p.example.invalid/v1#a@b",
		"//p.example.invalid/v1",
		"//p.example.invalid/v1?next=a@b",
		"",
	} {
		if got := baseURLRefusal(baseURL, "sk-live-0123456789"); got != "" {
			t.Fatalf("%q: refusal = %q, want none (an @ outside the authority is not userinfo)", baseURL, got)
		}
	}
}

func TestARefusalNamesNeitherTheURLNorTheKey(t *testing.T) {
	err := error(&BaseURLRefusedError{Reason: baseURLRefusalKey})
	if !errors.Is(err, ErrLLMConfigUnusable) {
		t.Fatal("a refused base_url must be an unusable configuration")
	}
	for _, text := range []string{err.Error(), strings.ToLower(err.Error())} {
		if strings.Contains(text, "http") || strings.Contains(text, "sk-") {
			t.Fatalf("refusal text %q holds a url or key shape", text)
		}
	}
}

// referenceDerivedForms are the hex and base64 spellings of a key, derived here
// from the encodings themselves and not from keyLeakForms. (A percent-escaped
// spelling is the raw key once the url is decoded, so it is always a hit.)
func referenceDerivedForms(key string) []string {
	forms := []string{hex.EncodeToString([]byte(key))}
	for shift, start := range []int{0, 2, 3} {
		raw := append(make([]byte, shift), key...)
		for _, text := range []string{base64.RawStdEncoding.EncodeToString(raw), base64.RawURLEncoding.EncodeToString(raw)} {
			if len(text) > start+2 {
				forms = append(forms, text[start:len(text)-2])
			}
		}
	}
	return forms
}

// For a key of any length 1 to 12, a url that holds exactly one hex or base64
// spelling of the key is refused if and only if that spelling is at least
// eight characters (or holds the raw key anyway). Keys with sextets 62 and 63
// make the base64 url alphabet differ from the std one.
func TestDerivedFormsOfEveryKeyLengthFollowTheEightCharRule(t *testing.T) {
	for _, source := range []string{"Qz9xK2mPvL3a", "?>?>?>?>?>?>"} {
		for length := 1; length <= len(source); length++ {
			key := source[:length]
			for _, form := range referenceDerivedForms(key) {
				if form == "" {
					continue
				}
				baseURL := "https://p.example.invalid/" + form
				want := len(form) >= 8 || strings.Contains(strings.ToLower(form), strings.ToLower(key))
				if got := baseURLRefusal(baseURL, key) == baseURLRefusalKey; got != want {
					t.Errorf("key %q (%d bytes), spelling %q (%d chars): refused = %v, want %v", key, length, form, len(form), got, want)
				}
			}
		}
	}
}
