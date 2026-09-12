package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// rejectedClauseDraft builds the SMALLEST draft/input pair that reaches a
// model-minted struct's own Validate() inside
// SynthesisDraft.ValidateAgainst, so this test certifies the log line
// against an error the REAL validator produced rather than a hand-built
// one: status degraded (so no direct judgment is required), a deterministic
// answer, no top-level evidence refs, no claims, and one driver carrying
// whatever the caller wants rejected.
//
// The driver's own Validate() runs strictly BEFORE every subject/path/
// evidence membership check in ValidateAgainst, which is why an empty graph
// is enough and why no allow-set has to be populated to reach it.
func rejectedClauseDraft(mutate func(contextfabric.DriverJudgment) contextfabric.DriverJudgment) (contextfabric.SynthesisDraft, contextfabric.SynthesisInput) {
	driver := contextfabric.DriverJudgment{
		DriverID: "driver_12345678", Standing: contextfabric.DriverPrincipal,
		Category: string(contractsv1.ContextFabricDriverCategoryRelationship),
		Title:    "Title", Summary: "Summary",
		Derivation: contextfabric.DerivationRuleInferred, EpistemicStatus: contextfabric.EpistemicInferred,
		Confidence: 0.5,
		// AffectedSubjects must be non-empty or the driver's own
		// below-minimum clause fires FIRST and every case below reports it
		// instead of the clause it was built to reach. The subject need not
		// be in any allow-set: driver.Validate() runs strictly before
		// ValidateAgainst's membership checks, and every case here makes
		// Validate() itself reject.
		AffectedSubjects: []contextfabric.SubjectRef{{
			Kind: contextfabric.SubjectProject, CanonicalID: "project_1", Label: "Project",
		}},
		EvidenceRefIDs: []string{"evidence_12345678"},
	}
	draft := contextfabric.SynthesisDraft{
		Status:              contextfabric.InvestigationDegraded,
		DeterministicAnswer: "No answer was asserted.",
		Drivers:             []contextfabric.DriverJudgment{mutate(driver)},
	}
	return draft, contextfabric.SynthesisInput{}
}

// TestContextFabricInvestigationFailuresLogRejectedClause certifies the
// EMITTED line through the real handler at production level.
//
// The value on the line is the point: rejection_reason says driver_invalid
// for every one of ContextFabricDriverJudgment.validate's twenty clauses,
// so before this the line could not distinguish a category outside the
// closed vocabulary from a title two characters too long from a driver that
// cited no evidence at all -- three defects with three different owners.
// The two rejecting cases below are chosen to differ in exactly that way,
// which also proves the field is not a constant.
//
// It also pins the complementarity with violated_bound documented on the
// vocabulary: the length case carries BOTH keys, and the two business-rule
// cases carry the clause where violated_bound is structurally absent --
// which is the whole reason a second field was needed.
func TestContextFabricInvestigationFailuresLogRejectedClause(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantClause any
		wantReason any
		wantBound  any
	}{
		{
			name: "a driver category outside the vocabulary names its clause and no bound",
			err: func() error {
				draft, input := rejectedClauseDraft(func(d contextfabric.DriverJudgment) contextfabric.DriverJudgment {
					d.Category = "not_a_category"
					return d
				})
				return contextfabric.ClassifySynthesisRejection(draft, input, draft.ValidateAgainst(input))
			}(),
			wantClause: "driver.category_out_of_vocabulary",
			wantReason: "driver_invalid",
			wantBound:  nil,
		},
		{
			name: "an overlong driver title names its clause AND its registered bound",
			err: func() error {
				draft, input := rejectedClauseDraft(func(d contextfabric.DriverJudgment) contextfabric.DriverJudgment {
					d.Title = strings.Repeat("a", contractsv1.ContextFabricDriverTitleMaxLength+1)
					return d
				})
				return contextfabric.ClassifySynthesisRejection(draft, input, draft.ValidateAgainst(input))
			}(),
			wantClause: "driver.title_length",
			wantReason: "driver_invalid",
			wantBound:  "synthesis.driver.title.max_length",
		},
		{
			name: "a withheld driver with no qualification names its clause and no bound",
			err: func() error {
				draft, input := rejectedClauseDraft(func(d contextfabric.DriverJudgment) contextfabric.DriverJudgment {
					d.Standing, d.Qualification = contextfabric.DriverWithheld, ""
					return d
				})
				return contextfabric.ClassifySynthesisRejection(draft, input, draft.ValidateAgainst(input))
			}(),
			wantClause: "driver.withheld_requires_qualification",
			wantReason: "driver_invalid",
			wantBound:  nil,
		},
		{
			name: "a rejection that is not a struct validation omits the key entirely",
			err: func() error {
				draft, input := rejectedClauseDraft(func(d contextfabric.DriverJudgment) contextfabric.DriverJudgment { return d })
				draft.Status = "not_a_status"
				return contextfabric.ClassifySynthesisRejection(draft, input, draft.ValidateAgainst(input))
			}(),
			// entry[...] on an absent key returns the untyped nil interface
			// value -- distinct from a present JSON null, which this must
			// never emit either: omitted entirely, not present-but-empty.
			wantClause: nil,
			wantReason: "status_invalid",
			wantBound:  nil,
		},
		{
			name:       "a failure carrying no rejection at all omits the key entirely",
			err:        contextfabric.ErrSynthesisRejected,
			wantClause: nil,
			wantReason: nil,
			wantBound:  nil,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				return contextfabric.InvestigationResult{}, testCase.err
			}))
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, investigationRequest(t, token))

			entry := decodeFailureLog(t, logs.String())
			if got, want := entry["rejected_clause"], testCase.wantClause; got != want {
				t.Fatalf("rejected_clause = %#v, want %#v (body=%s)", got, want, response.Body.String())
			}
			if got, want := entry["rejection_reason"], testCase.wantReason; got != want {
				t.Fatalf("rejection_reason = %#v, want %#v", got, want)
			}
			if got, want := entry["violated_bound"], testCase.wantBound; got != want {
				t.Fatalf("violated_bound = %#v, want %#v", got, want)
			}
		})
	}
}

// TestRejectedClauseOnTheLineIsAlwaysAVocabularyMember pins the
// log-injection guarantee at the SINK, not only at the accessor: whatever
// reaches the line must be a member of the closed vocabulary, so no field
// contents, subject label, or other model-derived text can ever appear
// there.
func TestRejectedClauseOnTheLineIsAlwaysAVocabularyMember(t *testing.T) {
	draft, input := rejectedClauseDraft(func(d contextfabric.DriverJudgment) contextfabric.DriverJudgment {
		// A category made of log-injection bait: if the clause were derived
		// from the field's CONTENTS rather than chosen at the clause, this
		// is what would land on the line.
		d.Category = `not_a_category" rejection_reason="success`
		return d
	})
	failure := contextfabric.ClassifySynthesisRejection(draft, input, draft.ValidateAgainst(input))

	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, failure
	}))
	app.Handler().ServeHTTP(httptest.NewRecorder(), investigationRequest(t, token))

	entry := decodeFailureLog(t, logs.String())
	clause, isString := entry["rejected_clause"].(string)
	if !isString {
		t.Fatalf("rejected_clause = %#v, want a string", entry["rejected_clause"])
	}
	if !contractsv1.ValidContextFabricRejectedClause(contractsv1.ContextFabricRejectedClause(clause)) {
		t.Fatalf("rejected_clause = %q, which is not a vocabulary member", clause)
	}
	if strings.Contains(logs.String(), "rejection_reason=\"success") {
		t.Fatalf("model-supplied text reached the log line: %s", logs.String())
	}
}
