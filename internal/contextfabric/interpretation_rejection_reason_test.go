package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// rejectedQuestion is an interpretation Validate() refuses for a BUSINESS
// RULE -- clarification requested with no reason. Chosen deliberately: it
// is the shape DiagnoseContextFabricInterpretedQuestionBound cannot name
// (no registered model-facing bound is involved), so it exercises the case
// this vocabulary exists for rather than one the bound registry already
// covered.
func rejectedQuestion() InterpretedQuestion {
	return InterpretedQuestion{
		Shape:               contractsv1.ContextFabricShapeOpen,
		RequestedJudgment:   "status_and_drivers",
		TimeContext:         TimeContext{Axis: TemporalCurrent},
		ClarificationNeeded: true,
	}
}

// TestClassifyInterpretationRejectionPreservesEverySentinel is the
// non-regression guard for the wrapping. Attaching a reason must not change
// what any existing caller sees: the route classifies on
// ErrInterpretationRejected, the receipt path on ErrModelOutput, and
// message-text assertions on the rendered string. Wrapping OUTWARD keeps
// all three, and this test is what proves it rather than asserting it in a
// comment.
func TestClassifyInterpretationRejectionPreservesEverySentinel(t *testing.T) {
	t.Parallel()
	cause := errors.New("clarification_needed requires a reason")
	err := ClassifyInterpretationRejection(rejectedQuestion(), cause)

	if !errors.Is(err, ErrInterpretationRejected) {
		t.Fatalf("errors.Is(err, ErrInterpretationRejected) = false -- the route's 422 classification would break")
	}
	if !errors.Is(err, ErrModelOutput) {
		t.Fatalf("errors.Is(err, ErrModelOutput) = false -- receiptOutcomeForError would stop reporting invalid_output")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(err, cause) = false -- the original cause must stay reachable")
	}
	if !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("err.Error() = %q, want it to still contain the cause text %q", err.Error(), cause.Error())
	}
}

// TestClassifyInterpretationRejectionCarriesTheRule is the positive half:
// the reason is attached, and it is the rule that actually rejected.
func TestClassifyInterpretationRejectionCarriesTheRule(t *testing.T) {
	t.Parallel()
	err := ClassifyInterpretationRejection(rejectedQuestion(), errors.New("clarification_needed requires a reason"))
	got := InterpretationRejectionReasonOf(err)
	want := contractsv1.ContextFabricInterpretationRejectionClarificationReasonMissing
	if got != want {
		t.Fatalf("InterpretationRejectionReasonOf() = %q, want %q", got, want)
	}
}

// TestClassifyInterpretationRejectionStillAttachesTheBoundViolation guards
// the "outward, not instead of" decision. The bound and the reason answer
// different questions and a rejection can carry both; wrapping the reason
// around the classified error must leave ModelBoundViolation reachable by
// errors.As.
func TestClassifyInterpretationRejectionStillAttachesTheBoundViolation(t *testing.T) {
	t.Parallel()
	q := rejectedQuestion()
	q.ClarificationNeeded = false
	q.RequestedJudgment = strings.Repeat("j", contractsv1.ContextFabricRequestedJudgmentMaxLength+1)

	err := ClassifyInterpretationRejection(q, errors.New("interpreted question violates v1 bounds"))

	var violation *ModelBoundViolation
	if !errors.As(err, &violation) {
		t.Fatalf("errors.As(err, &ModelBoundViolation{}) = false -- wrapping the reason outward must not hide the bound")
	}
	if got := InterpretationRejectionReasonOf(err); got != contractsv1.ContextFabricInterpretationRejectionRequestedJudgmentInvalid {
		t.Fatalf("reason = %q, want the requested_judgment rule alongside the bound", got)
	}
}

// TestInterpretationRejectionReasonOfIsNeverEmpty pins the "unclassified,
// never empty string" contract at the seam telemetry actually reads.
func TestInterpretationRejectionReasonOfIsNeverEmpty(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "a plain error carrying no rejection", err: errors.New("something else")},
		{name: "a nil error", err: nil},
		{name: "a rejection sentinel with no reason attached", err: fmt.Errorf("%w: legacy path", ErrInterpretationRejected)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := InterpretationRejectionReasonOf(testCase.err); got != InterpretationRejectionUnclassified {
				t.Fatalf("InterpretationRejectionReasonOf() = %q, want %q -- never the empty string", got, InterpretationRejectionUnclassified)
			}
		})
	}
}

// TestNewInterpretationRejectionCanonicalizesANonMember is the fail-closed
// assertion for the out-of-package constructor. A caller in another package
// cannot be trusted to pass a member, and a non-member must surface as
// "unclassified" rather than reach a log field verbatim.
func TestNewInterpretationRejectionCanonicalizesANonMember(t *testing.T) {
	t.Parallel()
	const nonMember = "MARKER_NOT_A_MEMBER_af31"
	err := NewInterpretationRejection(nonMember, errors.New("boom"))

	// Assert the STORED field, not only what the reader returns. There are
	// two canonicalization points -- the constructor and
	// InterpretationRejectionReasonOf -- and an assertion that only goes
	// through the reader stays green when the constructor's own
	// canonicalization is removed. Found by mutating exactly that
	// (mutation M3), which is why this assertion exists in this shape: the
	// Reason field is EXPORTED, so a caller reading it directly gets
	// whatever the constructor stored, with no reader to launder it.
	var rejection *InterpretationRejection
	if !errors.As(err, &rejection) {
		t.Fatalf("errors.As(err, &InterpretationRejection{}) = false")
	}
	if rejection.Reason == nonMember {
		t.Fatalf("InterpretationRejection.Reason = %q -- the constructor stored a NON-MEMBER verbatim; a caller reading the exported field directly would put model-derived text on a telemetry surface", rejection.Reason)
	}
	if rejection.Reason != InterpretationRejectionUnclassified {
		t.Fatalf("InterpretationRejection.Reason = %q, want %q", rejection.Reason, InterpretationRejectionUnclassified)
	}
	if got := InterpretationRejectionReasonOf(err); got != InterpretationRejectionUnclassified {
		t.Fatalf("InterpretationRejectionReasonOf() = %q, want %q -- a non-member must never survive to a telemetry field", got, InterpretationRejectionUnclassified)
	}
}

// TestFactCapabilityParameterRejectionNamesItsRuleOnBothRealSites pins the
// two later-stage rejection sites by DRIVING THEM, not by constructing the
// error the way they do. A test that builds its own error proves only that
// the constructor works; it would stay green if the production call site
// were reverted to a bare fmt.Errorf, which is exactly the regression worth
// catching.
//
// Both sites are covered because both exist: ReadFacts' pre-pass, and
// buildFactQuery's own defense-in-depth check that becomes the reachable
// one if the pre-pass is ever narrowed. A sweep of every ErrInterpretationRejected
// producer is what found the second; naming only the first would have left
// half the class unnamed.
func TestFactCapabilityParameterRejectionNamesItsRuleOnBothRealSites(t *testing.T) {
	t.Parallel()
	want := contractsv1.ContextFabricInterpretationRejectionFactCapabilityParameterNotAllowed

	newRegistry := func(t *testing.T) (*FactCapabilityRegistry, CanonicalFactRequest, SubjectRef) {
		t.Helper()
		project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
		provider := &factProviderStub{capability: FactCapability{
			Kind: FactMetrics, Name: "ops-metrics", Version: "metrics-v1",
			SupportedSubjectKinds: []SubjectKind{SubjectProject},
			AllowedParameters:     []string{"window_days"},
			Dimension:             HealthDimensionExecutionCompletion,
			SubjectRoles:          []FactRole{FactRoleSubject},
		}}
		registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
		if err != nil {
			t.Fatal(err)
		}
		request := canonicalFactRequest(project, FactMetrics)
		request.Requirements[0].Parameters = map[string]string{"sql": "select *"}
		return registry, request, project
	}

	t.Run("ReadFacts pre-pass", func(t *testing.T) {
		t.Parallel()
		registry, request, _ := newRegistry(t)
		_, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request)
		if err == nil {
			t.Fatal("ReadFacts() = nil error, want a rejection")
		}
		if !errors.Is(err, ErrInterpretationRejected) {
			t.Fatalf("the sentinel must survive the wrapping, or the route falls through to an unclassified 500: %v", err)
		}
		if got := InterpretationRejectionReasonOf(err); got != want {
			t.Fatalf("InterpretationRejectionReasonOf() = %q, want %q -- the rejection this validator cannot see must still name its own rule", got, want)
		}
	})

	t.Run("buildFactQuery defense in depth", func(t *testing.T) {
		t.Parallel()
		registry, request, project := newRegistry(t)
		var capability FactCapability
		for _, candidate := range registry.Capabilities() {
			if candidate.Kind == FactMetrics {
				capability = candidate
			}
		}
		// investigationScopeSubjectSet, not a hand-built map: the
		// subject-scope check runs BEFORE the parameter check, so a
		// hand-built allowed-set rejects on scope first and the test would
		// pass for the wrong reason -- observed while writing this.
		allowed := investigationScopeSubjectSet(request)
		_, err := buildFactQuery(request, request.Requirements[0], capability, allowed, []SubjectRef{project})
		if err == nil {
			t.Fatal("buildFactQuery() = nil error, want a rejection")
		}
		if !errors.Is(err, ErrInterpretationRejected) {
			t.Fatalf("the sentinel must survive the wrapping: %v", err)
		}
		if got := InterpretationRejectionReasonOf(err); got != want {
			t.Fatalf("InterpretationRejectionReasonOf() = %q, want %q", got, want)
		}
	})
}

// TestReceiptValidateRejectsANonVocabularyRejectionReason pins the
// closed-vocabulary guarantee at the receipt's own PERSISTENCE boundary,
// where WindowClass's identical guard already lives.
//
// Found by an adversarial review round, and it was a real hole rather than a
// hypothetical one: the field is exported on an exported struct, Validate()
// did not check it, and the receipt sink forwards what it is given. So a
// caller assigning a raw model string landed corpus text -- newlines
// included -- verbatim in a durable artifact. Canonicalizing in the error
// constructors protects the error path only; that is a property of those
// functions, never of this struct.
func TestReceiptValidateRejectsANonVocabularyRejectionReason(t *testing.T) {
	t.Parallel()
	base := func() ModelExecutionReceipt {
		started := time.Now().UTC()
		return ModelExecutionReceipt{
			Operation: ModelOperationInterpret, Provider: "openai", Model: "m", ModelVersion: "v1",
			PromptVersion: "p1", SchemaVersion: "s1", EvaluatorVersion: "e1",
			InputDigest: strings.Repeat("a", 64), Outcome: "invalid_output",
			StartedAt: started, CompletedAt: started.Add(time.Second), Attempts: 1,
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("the fixture must be valid before the field is set: %v", err)
	}

	t.Run("a non-member is refused", func(t *testing.T) {
		t.Parallel()
		r := base()
		r.InterpretationRejectionReason = "MODEL_OUTPUT_MARKER_4821\nlog-shaped-content"
		if err := r.Validate(); err == nil {
			t.Fatal("Validate() = nil for a receipt carrying non-vocabulary text; corpus content would reach the durable sink verbatim")
		}
	})
	t.Run("a member is accepted", func(t *testing.T) {
		t.Parallel()
		r := base()
		r.InterpretationRejectionReason = contractsv1.ContextFabricInterpretationRejectionShapeInvalid
		if err := r.Validate(); err != nil {
			t.Fatalf("Validate() = %v for a legitimate vocabulary member", err)
		}
	})
	t.Run("empty stays legal", func(t *testing.T) {
		t.Parallel()
		r := base()
		r.InterpretationRejectionReason = ""
		if err := r.Validate(); err != nil {
			t.Fatalf("Validate() = %v for a receipt with no rejection to describe", err)
		}
	})
}
