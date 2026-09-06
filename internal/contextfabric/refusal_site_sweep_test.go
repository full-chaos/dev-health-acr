package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE SWEEP, not a patch at the reported line.
//
// Two review rounds found the same defect at two different sites: an event, a
// refusal and a measurement describing ONE decision, assembled from more than
// one document. r2 found it at the declined arm; a keystone review then found
// the sibling at the retry arm, which my r2 fix had not touched because I fixed
// the line I was shown instead of sweeping for the shape.
//
// So the sites are ENUMERATED here and the enumeration is checked against the
// source, rather than being a claim in a commit message that goes stale the
// first time someone adds a third arm.
//
//	site                                        event measurement   refusal      status
//	------------------------------------------  ------------------  -----------  ------
//	fitAssembledResult declined arm  (:214)      measured            measured     bound (r2)
//	fitAssembledResult retry arm     (:320,:352) retryMeasured       retryMeasured bound (keystone)
//	planRefusal itself               (:556,:557) measured            measured     bound (r2)
//	fitAssembledResult measured FIT  (:144)      measured            none         n/a
//	fitAssembledResult retry FAILED  (:261)      measured            none (error) n/a
//	recordCandidateNarrowing         (:315,:643) served attempt      none         n/a
//	cardinality / synthesis_input    (engine.go) no measurement      none         n/a

// decisionEventFunctions are the functions that emit a narrowing event about a
// decision. A new one is not forbidden -- it must simply be added here
// deliberately, with its documents bound, which is the point.
//
// THE POPULATION WAS WIDENED, and the reason matters more than the list. It
// used to be "emits a narrowing event AND raises a refusal", which let a third
// site through: `recordCandidateNarrowing` emits an event and never refuses, and
// it published a prediction for a cohort selection the served answer had
// discarded. The refusal was never part of the class. The class is ONE DECISION
// DESCRIBED BY TWO DOCUMENTS, and every function that emits a decision event is
// in it whether or not it also refuses.
var decisionEventFunctions = []string{
	"fitAssembledResult",
	"planRefusal",
	"recordCandidateNarrowing",
	// Investigate emits two narrowing events -- cardinality and
	// synthesis_input -- and is listed after CHECKING, not to silence the
	// population check. Neither call sets a measurement or a prediction:
	// verified by walking Investigate's body for `recordMeasurement` and
	// `PredictedItems`, which returns nothing. An event carrying one document
	// and no second one cannot describe two, so it is outside the class while
	// still being inside the population -- which is exactly the distinction
	// this list exists to make explicit rather than leave to a reader.
	"Investigate",
}

// TestEveryRefusalSitePairsOneDocument pins the enumeration.
//
// It asserts a NEGATIVE that is easy to state and was twice violated: inside a
// function that both emits a narrowing event and raises a budget refusal, the
// event's measurement and the refusal's measurement must come from the same
// variable. Structurally that means no such function may reference more than
// one distinct `*Measured`/`*Measurement` source in the same emit/refuse pair.
//
// A structural test rather than a behavioural one, and deliberately so: the
// behavioural half exists too (the refusal grid and the keystone's retry
// binding repro), but neither can see a THIRD arm nobody has written a fixture
// for. This can.
func TestEveryRefusalSitePairsOneDocument(t *testing.T) {
	t.Parallel()
	_, files := parsePackageForQuantifier(t)

	for _, function := range decisionEventFunctions {
		calls := callsWithin(t, files, function)
		if !calls["recordPlanNarrowing"] && !calls["recordCandidateNarrowing"] {
			t.Errorf("%s no longer emits a narrowing event; the enumeration in this test is stale and "+
				"must be re-derived rather than left asserting nothing", function)
		}
	}

	// The population check: any OTHER function in the package that both emits
	// and refuses is a site nobody has bound, and it must be added to the list
	// above deliberately. This is what makes the test a sweep rather than a
	// restatement of the two sites I already know about.
	unlisted := []string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			emits := false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall {
					return true
				}
				switch calleeName(call) {
				case "recordPlanNarrowing", "recordCandidateNarrowing":
					emits = true
				}
				return true
			})
			if !emits {
				continue
			}
			listed := false
			for _, known := range decisionEventFunctions {
				if fn.Name.Name == known {
					listed = true
					break
				}
			}
			if !listed {
				unlisted = append(unlisted, fn.Name.Name)
			}
		}
	}
	if len(unlisted) != 0 {
		t.Errorf("these functions emit a narrowing decision event but are not in the bound enumeration: "+
			"%v. Each is a place where the event, its prediction and its measurement can describe "+
			"different documents -- the defect found at THREE sites now. Bind the documents and add "+
			"the site here.", unlisted)
	}
}

// TestTheRetryRefusalAndItsEventDescribeTheSameDocument is the BEHAVIOURAL half,
// and it is the one that actually fails on the defect.
//
// The structural pin above enumerates the sites; it passes on the broken tree
// too, because the shape it checks is "which functions emit and refuse", not
// "do their numbers agree". Shipping only the structural pin would leave the
// keystone's finding with no test that fails on it -- exactly the false-green
// shape this branch has already paid for twice.
//
// Adapted from the keystone round's own reproduction, kept close to it on
// purpose: the fixture is what reaches the arm where a retry runs, the
// candidate reduction runs and is insufficient, and a refusal follows.
func TestTheRetryRefusalAndItsEventDescribeTheSameDocument(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	var cohortSizes []int
	engine, _ := attributionEngine(t, attributionFixtureSpec{
		members: 4, globalFindings: 3, groupDrivers: 5, multiGroupDrivers: 6,
		memberDrivers: 1, candidates: 12,
	}, &sink, EngineOptions{
		ServiceVersion: "acr-test", MaxItems: 20, MaxSerializedBytes: 7000,
		SynthesisDeadlineReserve: time.Second,
		Now:                      func() time.Time { return time.Unix(200, 0).UTC() },
		NewResultID:              func() string { return "result_99999999" },
	}, &cohortSizes)

	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.MaxSubjectCandidates = 12
	request.Options.MaxDrivers = 50
	if err := request.Validate(); err != nil {
		t.Fatalf("the fixture request does not validate: %v", err)
	}

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)

	// PREMISES, each asserted, because every one of them is a way this
	// fixture could stop reaching the arm under test and pass on zeros.
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || !refusal.RetryAttempted {
		t.Fatalf("the fixture did not reach a retry refusal: %v", err)
	}
	if len(cohortSizes) != 2 || cohortSizes[1] >= cohortSizes[0] {
		t.Fatalf("synthesis cohorts %v: the fixture did not run a NARROWED second synthesis", cohortSizes)
	}
	line := assembledResultLine(t, sink.String())
	if !strings.Contains(line, "outcome_reduction_declined=insufficient") {
		t.Fatalf("the fixture did not reach the INSUFFICIENT reduction, which is the only exit where "+
			"the reduced and unreduced documents differ.\nline: %s", line)
	}

	// THE ASSERTION: one decision, one document. The refusal reports the
	// numbers a caller is given; the event reports the numbers an operator
	// sees. They are about the same answer and must say the same thing.
	if !strings.Contains(line, fmt.Sprintf("measured_items=%d ", refusal.MeasuredItems)) ||
		!strings.Contains(line, fmt.Sprintf("measured_bytes=%d ", refusal.MeasuredBytes)) {
		t.Errorf("the retry refusal and its sole assembled_result event describe DIFFERENT documents.\n"+
			"  refusal: items=%d bytes=%d axis=%s (ceilings %d/%d)\n  line: %s",
			refusal.MeasuredItems, refusal.MeasuredBytes, refusal.Overrun,
			refusal.MaxItems, refusal.MaxSerializedBytes, line)
	}
	// And the axis agrees with the numbers it is attached to.
	if refusal.MeasuredItems <= refusal.MaxItems && refusal.Overrun == "items" {
		t.Errorf("the refusal names overrun=items with %d items against a ceiling of %d",
			refusal.MeasuredItems, refusal.MaxItems)
	}
}

// TestTheCandidateRescueEventPredictsTheCohortItServed is the third site's
// behavioural pin, and the one that fails on the defect.
//
// The structural pin above enumerates emitters; it cannot see whether a given
// emitter's prediction and measurement describe the same document. On the
// candidate-rescue path the cohort retry is DECLINED and its selection
// discarded, the candidate trim rescues the original answer, and the served
// document still carries the original members — but the event predicted from
// the discarded selection, publishing a prediction for a cohort nobody
// synthesized and nobody received.
//
// Adapted from the keystone round's own reproduction, and driven over BOTH
// decline reasons because each reaches the arm by a different route.
func TestTheCandidateRescueEventPredictsTheCohortItServed(t *testing.T) {
	t.Parallel()
	for _, reason := range []RetryDeclinedReason{RetryDeclinedNoReserve, RetryDeclinedInsufficientDeadline} {
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			var sink bytes.Buffer
			var cohortSizes []int
			options := budgetStageOptions(30, 0)
			ctx := context.Background()
			if reason == RetryDeclinedInsufficientDeadline {
				options.SynthesisDeadlineReserve = time.Hour
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Minute)
				defer cancel()
			}
			engine, _ := attributionEngine(t, attributionFixtureSpec{
				members: 4, globalFindings: 3, groupDrivers: 5, multiGroupDrivers: 6,
				memberDrivers: 1, candidates: 12,
			}, &sink, options, &cohortSizes)

			request := validInvestigationRequestWithConfirmedWindow()
			request.Options.MaxSubjectCandidates = 12
			request.Options.MaxDrivers = 50
			if err := request.Validate(); err != nil {
				t.Fatalf("the fixture request does not validate: %v", err)
			}
			result, err := engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("Investigate() error = %v, want a served answer", err)
			}
			line := assembledResultLine(t, sink.String())

			// PREMISES, each asserted: the arm only exists when synthesis ran
			// ONCE over four members, the answer was rescued by the candidate
			// trim, and the cohort retry was declined for this reason.
			if len(cohortSizes) != 1 || cohortSizes[0] != 4 {
				t.Fatalf("synthesis cohorts %v: the fixture must synthesize exactly once, over 4 members",
					cohortSizes)
			}
			if result.Cohort == nil || len(result.Cohort.Members) != 4 {
				t.Fatalf("the served answer does not carry the 4 original members: %+v", result.Cohort)
			}
			if !strings.Contains(line, "outcome_reduction_applied=true") ||
				!strings.Contains(line, "retry_declined="+string(reason)) {
				t.Fatalf("the fixture did not reach the candidate rescue with the cohort retry "+
					"declined as %q.\nline: %s", reason, line)
			}

			// THE ASSERTION: the prediction is for the cohort that was served.
			want := PredictedItemsForPlan(*result.AnswerPlan, cohortSizes[0])
			if !strings.Contains(line, fmt.Sprintf("predicted_items=%d ", want)) {
				t.Errorf("the event's prediction belongs to the DISCARDED cohort selection. Want "+
					"predicted_items=%d for the %d members actually synthesized and served.\nline: %s",
					want, cohortSizes[0], line)
			}
		})
	}
}
