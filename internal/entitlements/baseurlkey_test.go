package entitlements

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
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

// A key under eight bytes is matched in its raw form only, as in ops.
func TestAShortKeyIsMatchedRawOnly(t *testing.T) {
	if got := keyLeakForms("abcdefg"); len(got) != 1 || got[0] != "abcdefg" {
		t.Fatalf("forms of a 7-byte key = %v, want the raw form only", got)
	}
	if baseURLRefusal("https://p.example.invalid/"+hex.EncodeToString([]byte("abcdefg")), "abcdefg") != "" {
		t.Fatal("the hex form of a short key is not a leak form")
	}
	if got := keyLeakForms(""); got != nil {
		t.Fatalf("forms of an empty key = %v, want none", got)
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
