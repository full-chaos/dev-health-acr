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

// refusalSiteFunctions are the functions that may pair a narrowing event with a
// budget refusal about the same decision. A new one is not forbidden -- it must
// simply be added here deliberately, with its documents bound, which is the
// point.
var refusalSiteFunctions = []string{"fitAssembledResult", "planRefusal"}

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

	for _, function := range refusalSiteFunctions {
		calls := callsWithin(t, files, function)
		emits := calls["recordPlanNarrowing"] || calls["recordCandidateNarrowing"]
		refuses := calls["refusalFrom"] || calls["planRefusal"]
		if !emits {
			t.Errorf("%s no longer emits a narrowing event; the enumeration in this test is stale and "+
				"must be re-derived rather than left asserting nothing", function)
		}
		if !refuses {
			t.Errorf("%s no longer raises a refusal; the enumeration is stale", function)
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
			emits, refuses := false, false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall {
					return true
				}
				switch calleeName(call) {
				case "recordPlanNarrowing", "recordCandidateNarrowing":
					emits = true
				case "refusalFrom", "planRefusal":
					refuses = true
				}
				return true
			})
			if !emits || !refuses {
				continue
			}
			listed := false
			for _, known := range refusalSiteFunctions {
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
		t.Errorf("these functions emit a narrowing event AND raise a refusal but are not in the bound "+
			"enumeration: %v. Each is a place where the event, the refusal and the measurement can "+
			"describe different documents -- the defect found twice already. Bind the documents and "+
			"add the site here.", unlisted)
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
