package directread

import (
	"strings"
	"testing"
)

// Codex r1 P1: a free-JSON output leaf (investmentBreakdown:
// evidenceQualityDistribution, evidenceQualityStats.bandCounts) was passed
// through unchanged, so a person field nested in it left acr. Both are flat
// maps band -> number in ops (investment/response.go:34,46). A JSON leaf now
// passes only as a flat object of numbers with non-person keys; anything
// else is removed and reported.
func TestJSONLeafOnlyPassesAFlatNumberMap(t *testing.T) {
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	op, refusal := cat.Lookup("investmentBreakdown")
	if refusal != nil {
		t.Fatalf("lookup: %v", refusal)
	}
	cases := []struct {
		name, dist, bands string
		removed           bool
	}{
		{"flat numbers pass", `{"high":3,"low":1.5}`, `{"a":1,"b":2}`, false},
		{"empty maps pass", `{}`, `{}`, false},
		{"null passes (both are nullable)", `null`, `null`, false},
		{"nested person object", `{"employee":{"email":"person@example.test"}}`, `{"a":1}`, true},
		{"person-named key with a number", `{"email":1}`, `{"a":1}`, true},
		{"string value", `{"high":"person@example.test"}`, `{"a":1}`, true},
		{"array value", `{"high":[1,2]}`, `{"a":1}`, true},
		{"nested in bandCounts", `{"high":1}`, `{"employee":{"email":"person@example.test"}}`, true},
		{"bandCounts string", `{"high":1}`, `{"a":"x"}`, true},
		{"top-level array", `[1,2]`, `{"a":1}`, true},
		{"top-level string", `"person@example.test"`, `{"a":1}`, true},
	}
	for _, c := range cases {
		body := `{"analytics":{"breakdowns":[],"evidenceQualityDistribution":` + c.dist + `,"evidenceQualityStats":{"bandCounts":` + c.bands + `,"mean":0.1,"stddev":0,"total":1}}}`
		res, err := FilterResponse(op, []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		leaked := strings.Contains(string(res.Data), "person@example.test") || strings.Contains(string(res.Data), `"email"`)
		if c.removed {
			if leaked || res.RemovedValues == 0 {
				t.Errorf("%s: leaked=%v removed=%d data=%s", c.name, leaked, res.RemovedValues, res.Data)
			}
		} else if res.RemovedValues != 0 || !strings.Contains(string(res.Data), `"evidenceQualityDistribution"`) {
			t.Errorf("%s: a clean JSON leaf was removed: %s (removed %d)", c.name, res.Data, res.RemovedValues)
		}
	}
}
