package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4735 — THESE TESTS CARRY ACCEPTANCE CRITERION 1. The source sweep
// (chaos4735_family_language_sweep_test.go) is a defence-in-depth heuristic
// over known shapes and is explicitly NOT a nonexistence proof; the
// mechanical claim lives here, on the wire.
//
// That allocation was not a preference, it was measured. Three adversarial
// review rounds each defeated the source sweep with a new construction. TWO OF
// THE THREE were caught here anyway and could not reach a caller: R2 tried to
// route family-keyed text through `error.message` and the family-invariance
// test below rejected it; R3 tried a new `details` key and the closed key set
// rejected it. The source sweep is the early-warning layer; this is the layer
// that has actually held.
//
// THE LIMIT OF THAT CLAIM, stated so it is not overread: these tests cover
// **the API investigation route's 413 body only** -- its `details` object and
// its `error.message`. They say nothing about MCP's projection surface, about
// other endpoints, or about any future served field. MCP in particular returns
// `ContextFabricAnswerProjection` through a separate field-by-field projector,
// so a family-keyed string reaching THAT surface would be invisible to
// everything in this file. A closed-key check for the MCP surface is ticketed
// separately.
//
// There was no handler-level test over this body before this change at all.
// The planned refusal shipped with its shape pinned only at the engine, so the
// English sentence reached the wire without any test observing it -- which is
// most of why it survived to be found by an outside reviewer.

// closedToken is the shape every string in the refusal's details must have:
// one lower-case token. A sentence has spaces; a token does not.
//
// NOT SUFFICIENT ON ITS OWN, and codex round 1 proved it: `ask_about_one_subject`
// is a snake_case phrase that satisfies this regex perfectly. A reviewer
// constructed a `narrower_hint` field carrying exactly that and this test
// passed. The regex is retained as a cheap first filter, but the assertion
// that actually holds the line is `allowedDetailKeys` below -- a CLOSED key
// set. Shape checks can be satisfied by a determined author; an allowlist of
// keys cannot be satisfied by adding a key.
var closedToken = regexp.MustCompile(`^[a-z0-9_]+$`)

// allowedDetailKeys is the COMPLETE set of keys the 413 details object may
// carry. Anything else fails, whatever it contains.
//
// This is the fix for codex round 1's P1. The earlier version of this test
// checked the SHAPE of values and never asked which keys existed, so a new
// key smuggling family-keyed advice through was invisible to it. A budget
// refusal's details is a small fixed object -- there is no reason for it to
// be open, and closing it turns "someone added a field" from something a
// reviewer might notice into something CI reports.
var allowedDetailKeys = map[string]bool{
	"overrun":               true,
	"measured_items":        true,
	"measured_bytes":        true,
	"max_items":             true,
	"max_serialized_bytes":  true,
	"question_family":       true,
	"retry_attempted":       true,
	"narrower_continuation": true,
}

// allowedContinuationKeys is the same discipline one level down. The
// continuation object is where a "hint" or "suggestion" string would most
// naturally be added later.
var allowedContinuationKeys = map[string]bool{"family": true, "axis": true}

func TestChaos4735BudgetRefusal413CarriesNoServerAuthoredProse(t *testing.T) {
	t.Parallel()

	// Every family, not just the one the review happened to demonstrate: the
	// deleted switch had five arms and a default, so a per-family table is
	// what shows the whole surface is clean rather than one sampled row.
	for _, family := range contractsv1.ContextFabricQuestionFamilyVocabulary() {
		t.Run(string(family), func(t *testing.T) {
			t.Parallel()
			definition, found := contextfabric.LookupQuestionFamily(family)
			if !found {
				t.Fatalf("family %q has no registry row", family)
			}
			refusal := contextfabric.AnswerBudgetRefusal{
				Overrun:                  contractsv1.ContextFabricBudgetOverrunItems,
				MeasuredItems:            41,
				MaxItems:                 30,
				MaxSerializedBytes:       1 << 20,
				Family:                   family,
				NarrowerContinuationAxis: definition.NarrowerContinuationAxis,
				RetryAttempted:           true,
			}
			app, token, _ := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				return contextfabric.InvestigationResult{}, refusal
			}))
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, investigationRequest(t, token))

			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413; body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Error struct {
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			details := body.Error.Details

			// 1. The deleted field is GONE, by name. A renamed sentence would
			//    pass every other assertion here.
			if _, present := details["narrower_question"]; present {
				t.Errorf("413 details still carry narrower_question: %v", details["narrower_question"])
			}

			// 1b. The key set is CLOSED. Codex round 1 defeated the
			//     shape-only version of this test with a `narrower_hint`
			//     key whose snake_case value passed every value check. A
			//     value assertion cannot catch a new field; a key
			//     allowlist catches it by construction.
			for key := range details {
				if !allowedDetailKeys[key] {
					t.Errorf("413 details carry an unexpected key %q = %v. The refusal's details is a closed object; a new key needs a ruling, not a line in the allowlist -- and family-keyed advice is exactly what arrives this way", key, details[key])
				}
			}

			// 2. Nothing in details is prose. This is the assertion that
			//    survives a rename: it does not care what the field is
			//    called, only that no value in the object is a sentence.
			//
			//    Scoped to `details` on purpose. error.v1's `message` is a
			//    required, fixed, family-INDEPENDENT string ("The Context
			//    Fabric answer did not fit the response budget") that the
			//    consumer deliberately never reads; this ticket is about
			//    language keyed on a vocabulary value, not about the error
			//    envelope having a message field at all.
			assertNoProse(t, details, "details")

			// 2b. The two string-valued scalars are VOCABULARY MEMBERS, not
			//     merely token-shaped. This is the same lesson as 1b: shape
			//     is a weaker claim than membership, and the gap between
			//     them is where invented text lives.
			if overrun, _ := details["overrun"].(string); !contractsv1.ValidContextFabricBudgetOverrun(contractsv1.ContextFabricBudgetOverrun(overrun)) {
				t.Errorf("overrun = %q, not a member of the closed overrun vocabulary", overrun)
			}
			if got, _ := details["question_family"].(string); got != string(family) {
				t.Errorf("question_family = %q, want %q", got, family)
			}

			// 3. The continuation is present exactly when an axis exists,
			//    and absent -- not "none" -- when it does not.
			continuation, present := details["narrower_continuation"]
			if definition.NarrowerContinuationAxis == contextfabric.NarrowingContinuationNone {
				if present {
					t.Errorf("family %q declares no narrowing axis but the body still carries a continuation: %v", family, continuation)
				}
				return
			}
			if !present {
				t.Fatalf("family %q declares axis %q but the body carries no continuation", family, definition.NarrowerContinuationAxis)
			}
			fields, ok := continuation.(map[string]any)
			if !ok {
				t.Fatalf("narrower_continuation = %T, want an object", continuation)
			}
			for key := range fields {
				if !allowedContinuationKeys[key] {
					t.Errorf("narrower_continuation carries an unexpected key %q = %v", key, fields[key])
				}
			}
			if got := fields["family"]; got != string(family) {
				t.Errorf("continuation family = %v, want %q", got, family)
			}
			axis, _ := fields["axis"].(string)
			if !contextfabric.ValidNarrowingContinuationAxis(contextfabric.NarrowingContinuationAxis(axis)) {
				t.Errorf("continuation axis = %q, not a member of the closed vocabulary", axis)
			}
			if axis != string(definition.NarrowerContinuationAxis) {
				t.Errorf("continuation axis = %q, want the registry's declared %q -- the route must SERVE the declaration, not re-derive it", axis, definition.NarrowerContinuationAxis)
			}
		})
	}
}

// TestChaos4735BudgetRefusalMessageIsFamilyIndependent closes the OTHER route
// to served, family-keyed text: `error.message`.
//
// FOUND BY ADVERSARIAL REVIEW (codex round 2, P1). The body test above
// deliberately scopes itself to `details`, and the earlier version of this
// change justified that in a comment: `error.message` is "fixed,
// family-independent, schema-required, and never read by the consumer". The
// reviewer's observation is sharp and correct -- **the exemption holds only
// BECAUSE the message is family-independent, and nothing was enforcing that.**
// A one-line change at the `writeContextFabricFailure` call site could pass a
// family-keyed message and every test here would still be green.
//
// A justification in a comment is not a control. This turns it into one, and
// it does so without banning the field: the message may be any sentence it
// likes, as long as it is the SAME sentence for every family. That is exactly
// the property the exemption assumed.
func TestChaos4735BudgetRefusalMessageIsFamilyIndependent(t *testing.T) {
	t.Parallel()

	messages := map[string][]string{}
	for _, family := range contractsv1.ContextFabricQuestionFamilyVocabulary() {
		definition, found := contextfabric.LookupQuestionFamily(family)
		if !found {
			t.Fatalf("family %q has no registry row", family)
		}
		refusal := contextfabric.AnswerBudgetRefusal{
			Overrun: contractsv1.ContextFabricBudgetOverrunItems, MeasuredItems: 41, MaxItems: 30,
			MaxSerializedBytes: 1 << 20, Family: family,
			NarrowerContinuationAxis: definition.NarrowerContinuationAxis, RetryAttempted: true,
		}
		app, token, _ := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
			return contextfabric.InvestigationResult{}, refusal
		}))
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, investigationRequest(t, token))

		var body struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode for %q: %v", family, err)
		}
		if body.Error.Message == "" {
			t.Fatalf("family %q produced an empty error.message; error.v1 requires one", family)
		}
		messages[body.Error.Message] = append(messages[body.Error.Message], string(family))
	}

	if len(messages) != 1 {
		t.Errorf("error.message varies by question family across %d distinct messages: %v\nThe engine does not author user language keyed on a vocabulary value (chris rulings 2026-08-31 13:35/13:40). The refusal's message is exempt from the prose rules ONLY because it is the same sentence for every family; the moment it varies, it is the phrase table again, one field over.", len(messages), messages)
	}
}

// assertNoProse walks a decoded JSON value and fails on any string that is
// not a single closed token. It recurses, so a sentence hidden one level
// down inside a nested object is caught too -- which matters here, because
// the continuation IS a nested object and is the obvious place a future
// change would put a "hint" or "suggestion" string.
func assertNoProse(t *testing.T, value any, path string) {
	t.Helper()
	switch typed := value.(type) {
	case string:
		if !closedToken.MatchString(typed) {
			t.Errorf("%s = %q: the 413 body carries server-authored text where a closed token belongs (chris rulings 2026-08-31 13:35/13:40)", path, typed)
		}
	case map[string]any:
		for key, nested := range typed {
			assertNoProse(t, nested, path+"."+key)
		}
	case []any:
		for index, nested := range typed {
			assertNoProse(t, nested, path+"["+itoa(index)+"]")
		}
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
