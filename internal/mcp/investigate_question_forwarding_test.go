package mcp

import (
	"os"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestInvestigateQuestion_EveryRequestFieldIsForwarded closes a defect CLASS
// rather than the instance that prompted it.
//
// A field can sit on the tool schema, on MCPInvestigateQuestionRequest, and in
// validation, and still never reach the engine -- because nothing structurally
// connects "the type has a field" to "the conversion copies it". This has now
// happened twice: codex caught prior_candidate_receipts dropped this way in
// August, it was fixed per-instance, and parent_result_id was dropped the same
// way here. A per-instance fix is what let it recur.
//
// The contract-parity gate cannot catch this. It compares published schemas to
// Go wire types; a mapping function is neither. So the check has to be this
// one: enumerate the request struct by reflection and require every field to
// be mentioned in the conversion.
//
// The mechanism is deliberately crude -- it greps the conversion source for
// `input.<Field>`. A crude check that runs on every field beats a precise one
// that only ever runs on the fields someone remembered. If a future field is
// genuinely surface-only and must NOT be forwarded, add it to
// deliberatelyNotForwarded with the reason; making that an explicit, reviewed
// decision is the point.
func TestInvestigateQuestion_EveryRequestFieldIsForwarded(t *testing.T) {
	t.Parallel()

	// Fields the MCP surface owns and deliberately does not pass through.
	deliberatelyNotForwarded := map[string]string{
		"IncludeFullResult": "response-shaping for this surface only; never part of the investigation request",
		"Budget":            "translated into InvestigationOptions rather than copied, see the conversion",
		"Scope":             "translated into RequestedScope rather than copied, see the conversion",
	}

	source, err := readConversionSource()
	if err != nil {
		t.Fatalf("reading the conversion source: %v", err)
	}
	// Salted positive: if the file could not be read, or was read empty, every
	// field would look unmapped and this test would fail loudly rather than
	// vacuously pass. Assert the opposite direction too -- a field known to be
	// forwarded must be found, or the matching itself is broken.
	if !strings.Contains(source, "input.Question") {
		t.Fatal("the conversion source does not mention input.Question -- the source lookup or the match is broken, so every result below is meaningless")
	}

	requestType := reflect.TypeOf(contractsv1.MCPInvestigateQuestionRequest{})
	checked := 0
	for i := 0; i < requestType.NumField(); i++ {
		field := requestType.Field(i)
		if reason, exempt := deliberatelyNotForwarded[field.Name]; exempt {
			t.Logf("exempt: %s (%s)", field.Name, reason)
			continue
		}
		checked++
		if !strings.Contains(source, "input."+field.Name) {
			t.Errorf("MCPInvestigateQuestionRequest.%s is never read in the MCP-to-engine conversion: a caller can send it, it can validate, and it is silently dropped before the engine ever sees it", field.Name)
		}
	}
	if checked == 0 {
		t.Fatal("no fields were checked -- the reflection walk found nothing, so this test proves nothing")
	}
	t.Logf("checked %d forwarded fields", checked)
}

// readConversionSource returns the source of the file holding the
// MCP-to-engine request conversion. Read from disk rather than embedded so the
// check cannot drift from the code it is checking.
func readConversionSource() (string, error) {
	raw, err := os.ReadFile("investigate_question.go")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
