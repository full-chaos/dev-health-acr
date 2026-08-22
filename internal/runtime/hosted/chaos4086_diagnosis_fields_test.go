package hosted_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4086 tests.
//
// THE BAR, restated as a test oracle: a wrong_commit row must be fully
// diagnosable FROM THE REPORT ALONE. So every assertion below runs against
// the row's own SERIALIZED BYTES wherever the claim is about what a reader
// receives -- a struct-field assertion would prove the harness computed a
// value, which was already true before this ticket and is exactly what made
// the gap invisible.

// TestChaos4086_InnermostErrorTypeWalksBothUnwrapForms is the pin for the
// trap in CHAOS-4088's %T pattern.
//
// fmt.Errorf with ONE %w returns *fmt.wrapError, which has Unwrap() error.
// With TWO it returns *fmt.wrapErrors, which has Unwrap() []error instead --
// errors.Unwrap returns nil for it, so a naive loop stops dead and reports
// "*fmt.wrapErrors". That is not hypothetical: engine.go builds exactly that
// shape for a validation failure, which is the error this field most exists
// to fingerprint.
func TestChaos4086_InnermostErrorTypeWalksBothUnwrapForms(t *testing.T) {
	sentinel := errors.New("sentinel")
	rule := errors.New("clarification result requires a prompt")

	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"bare":            {rule, "*errors.errorString"},
		"single_wrap":     {fmt.Errorf("outer: %w", rule), "*errors.errorString"},
		"multi_wrap":      {fmt.Errorf("%w: %w", sentinel, rule), "*errors.errorString"},
		"staged_multi":    {&contextfabric.StageError{Stage: contextfabric.StageValidation, Err: fmt.Errorf("%w: %w", sentinel, rule)}, "*errors.errorString"},
		"custom_leaf":     {&contextfabric.StageError{Stage: contextfabric.StageGraph, Err: nil}, "*contextfabric.StageError"},
		"nested_multiple": {fmt.Errorf("a: %w", fmt.Errorf("%w: %w", sentinel, fmt.Errorf("b: %w", rule))), "*errors.errorString"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := twoTurnInnermostErrorType(tc.err); got != tc.want {
				t.Fatalf("innermost type = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestChaos4086_ArmErrorNamesItsStage proves the load-bearing half of the
// addendum's "a validation rejection names its rejecting rule".
//
// It names the STAGE, not the rule text -- see the honest limitation recorded
// on ArmInvalidErrorType. For CHAOS-4098's defect that would have read
// stage="validation", which places the failure in the Validate family
// immediately instead of after a re-read of raw exchange files.
func TestChaos4086_ArmErrorNamesItsStage(t *testing.T) {
	err := &contextfabric.StageError{
		Stage: contextfabric.StageValidation,
		Err:   fmt.Errorf("%w: clarification result requires a prompt", contextfabric.ErrInvalidResult),
	}
	stage, ok := contextfabric.FailureStage(err)
	if !ok || stage != contextfabric.StageValidation {
		t.Fatalf("FailureStage = %q (ok=%v), want validation", stage, ok)
	}
	// An unstaged error must leave the field EMPTY rather than inventing a
	// default: "no stage was recorded" and "the unknown stage" are
	// different facts, and only the first is true here.
	if _, ok := contextfabric.FailureStage(errors.New("bare")); ok {
		t.Fatal("a bare error must report no stage")
	}
}

// TestChaos4086_TheReportRowCarriesEveryDiagnosisKey is the acceptance test,
// asserted against real serialized bytes.
//
// The row is the case-60/case-61 shape the rerun could not diagnose: a
// commit that went to the wrong subject, under a refusal-shaped resolution.
// Every question a reader had to leave the artifact to answer is answered
// here.
func TestChaos4086_TheReportRowCarriesEveryDiagnosisKey(t *testing.T) {
	// A COMMITTED wrong-commit row: the shape the acceptance bar is written
	// about ("a wrong_commit row is fully diagnosable"), so the gate that
	// fired is present.
	raw := marshalDiagnosisRow(t, "evidence_census")
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	for _, key := range []string{
		"committed_subjects", "expected_kind", "expected_id",
		"commit_gate", "tied_statistical_top", "search_truncated",
		"kind_coverage_floor_fired", "kind_coverage_missing_kinds",
		"kind_coverage_floor_truncated",
	} {
		if _, present := decoded[key]; !present {
			t.Errorf("row omits %q -- the reader is back to re-reading the trace", key)
		}
	}
}

// TestChaos4086_TheRowNeverCarriesCorpusQuestionText is the corpus-safety
// pin. trialCase.Question is in scope at every stamping site; nothing may
// carry it across.
func TestChaos4086_TheRowNeverCarriesCorpusQuestionText(t *testing.T) {
	raw := string(marshalDiagnosisRow(t, "evidence_census"))
	for _, forbidden := range []string{chaos4086SentinelQuestion, "question"} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(forbidden)) {
			t.Fatalf("serialized row contains %q -- this artifact carries ids and closed enums only:\n%s", forbidden, raw)
		}
	}
}

// TestChaos4086_MirrorKeysMatchTheProducer is the drift pin this mirror
// never had.
//
// The merge tool is a HAND-MAINTAINED copy of these structs, guarded until
// now only by a runtime version check and human care -- and a field it fails
// to declare is silently dropped by json.Unmarshal, deleting the diagnosis
// from a merged artifact while every count still agrees. That has already
// happened once (trialProvenance.AnchorMembershipOffersEnabled).
//
// A _test.go type cannot be imported by cmd/, so the two sides cannot be
// compared directly. They are instead compared against the SAME checked-in
// key list, from both sides: this test pins the producer, and
// TestMirrorKeysMatchTheProducer in cmd/acr-trial-merge-two-turn pins the
// mirror. Drift on either side fails one of them.
func TestChaos4086_MirrorKeysMatchTheProducer(t *testing.T) {
	golden := filepath.Join("..", "..", "..", "testdata", "trial-report", "two_turn_case_result.keys")
	got := caseResultJSONKeys()
	if os.Getenv("ACR_UPDATE_TRIAL_REPORT_KEYS") == "1" {
		if err := os.WriteFile(golden, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s; update the merge mirror in the SAME change", golden)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read key golden: %v -- regenerate with ACR_UPDATE_TRIAL_REPORT_KEYS=1", err)
	}
	if !reflect.DeepEqual(got, strings.Fields(string(want))) {
		t.Fatalf("twoTurnCaseResult JSON keys drifted from the checked-in list.\ngot:  %v\nwant: %v\nIf this is a deliberate schema change: bump report_schema_version, update cmd/acr-trial-merge-two-turn's mirror IN THE SAME CHANGE, then regenerate with ACR_UPDATE_TRIAL_REPORT_KEYS=1", got, strings.Fields(string(want)))
	}
}

// caseResultJSONKeys returns twoTurnCaseResult's JSON keys, sorted.
func caseResultJSONKeys() []string {
	typ := reflect.TypeOf(twoTurnCaseResult{})
	keys := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		keys = append(keys, strings.Split(tag, ",")[0])
	}
	sort.Strings(keys)
	return keys
}

// chaos4086SentinelQuestion stands in for a corpus question: the one value a
// trialCase carries that this artifact may never persist.
const chaos4086SentinelQuestion = "ZZQUESTIONSENTINELZZ is Dev Health Ops behind on review turnaround"

// marshalDiagnosisRow builds a row through the SAME stamping helpers the arms
// use -- never by setting fields directly -- and returns its serialized
// bytes. Going through the helpers is the point: a test that hand-filled the
// struct would still pass if every arm stopped calling them.
func marshalDiagnosisRow(t *testing.T, commitGate string) []byte {
	t.Helper()
	tc := trialCase{
		Question:   chaos4086SentinelQuestion,
		ExpectKind: "repository",
		ExpectID:   "repository:acme/widgets",
	}
	committed := []contractsv1.ContextFabricSubjectRef{
		{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project:acme/widgets", Label: "Widgets"},
	}
	// commitGate is parameterised because the two shapes this artifact must
	// serve are genuinely different rows, not one row with a nullable
	// field: a COMMITTED row names the gate that fired, and a REFUSAL row
	// has no gate by construction and is readable only through the two
	// flags beside it. A single fixture asserting both would be asserting
	// something no real row can be.
	outcome := "committed"
	if commitGate == "" {
		outcome = "ambiguous"
	}
	trace := &twoTurnTraceCapture{events: []graphrank.ResolutionTraceEvent{
		{Stage: "kind_coverage_floor", KindCoverageFloorFired: true, KindCoverageMissingKinds: 3, KindCoverageFloorTruncated: true},
		{Stage: "decision", Outcome: outcome, CommitGate: commitGate, TiedStatisticalTop: true, SearchTruncated: true},
	}}

	res := twoTurnCaseResult{Index: 60, Member: "expected_kind", Arm: "positive", CommittedCount: 1, WrongCommit: true}
	twoTurnStampOutcome(&res, tc, committed)
	twoTurnStampDecision(&res, trace)

	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	return raw
}

// TestChaos4086_RefusalShapeIsArtifactAttestable is the ticket addendum's own
// acceptance target, stated directly.
//
// An ambiguous decision with an EMPTY CommitGate is the one shape a reader
// cannot interpret without the two flags beside it: tied-top-under-truncation
// (the CHAOS-4085 refusal) and ordinary ambiguity are otherwise identical on
// the wire. Note this asserts the flags survive serialization even though
// CommitGate itself is empty and omitted -- the absence of a gate is only
// readable because the flags are present.
func TestChaos4086_RefusalShapeIsArtifactAttestable(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal(marshalDiagnosisRow(t, ""), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := decoded["commit_gate"]; present {
		t.Fatal("an empty commit_gate must be omitted, not serialized as \"\" -- omitempty is the contract")
	}
	for _, key := range []string{"tied_statistical_top", "search_truncated"} {
		if got, _ := decoded[key].(bool); !got {
			t.Fatalf("%s = %v, want true -- without it a refusal is indistinguishable from ordinary ambiguity", key, decoded[key])
		}
	}
}

// TestChaos4086_EveryArmStampsItsRow is the WIRING pin, and it exists
// because a mutation test found the gap: unwiring twoTurnStampDecision from
// an arm broke nothing, since every other test in this file calls the
// stampers directly.
//
// It reads the harness source and asserts each arm function's body actually
// calls them. That is deliberately structural rather than behavioural: the
// failure this guards is "a new arm, or an edited one, quietly stops
// stamping", which is EXACTLY what already happened on this file once -- the
// inferred arm grew a trace capture and the other three never did, leaving
// the commit gate "categorically unreachable on two arms" until this ticket.
// Driving four live arms through a stub investigator would prove the same
// thing at many times the cost and would itself need a fixture per arm.
func TestChaos4086_EveryArmStampsItsRow(t *testing.T) {
	const source = "chaos3742_two_turn_confirmation_test.go"
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, source, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", source, err)
	}
	// Every arm calls Investigate, so every arm has both an outcome to
	// record and an error path to classify.
	//
	// twoTurnStampArmFailure, not twoTurnStampArmError: the arms reach the
	// stamper THROUGH the paired helper that sets the reason with it (see
	// TestChaos4086_EveryErrorDerivedReasonIsStamped). Requiring the inner
	// function here would push arms back toward calling it separately,
	// which is the split this suite exists to remove.
	want := []string{"twoTurnStampOutcome", "twoTurnStampDecision", "twoTurnStampArmFailure"}
	arms := map[string]bool{
		"runTwoTurnPositiveArm":       false,
		"runTwoTurnInferredTierArm":   false,
		"runTwoTurnConfirmedWrongArm": false,
		"runTwoTurnMutationArm":       false,
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if _, tracked := arms[fn.Name.Name]; !tracked {
			continue
		}
		arms[fn.Name.Name] = true
		called := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if ident, ok := call.Fun.(*ast.Ident); ok {
					called[ident.Name] = true
				}
			}
			return true
		})
		for _, helper := range want {
			if !called[helper] {
				t.Errorf("%s never calls %s -- its rows will carry no diagnosis, silently, and every other test in this file will still pass", fn.Name.Name, helper)
			}
		}
	}
	for name, seen := range arms {
		if !seen {
			t.Errorf("arm %s not found in %s -- if it was renamed, update this list rather than deleting the assertion", name, source)
		}
	}
}

// TestChaos4086_EveryErrorDerivedReasonIsStamped is codex xhigh round 1's P2,
// closed mechanically rather than site by site.
//
// The review found the confirmed-wrong SETUP turn setting an ArmInvalidReason
// with no stage or error type; the anchor-seed failure beside it had the same
// gap and nobody had noticed. Seven error sites across four arms, and the two
// nobody was looking at are the two that drifted -- which is what "remember to
// call the stamper as well" buys you.
//
// twoTurnStampArmFailure now sets the reason AND the stamp together, and this
// test refuses any assignment that goes around it: an ArmInvalidReason
// derived from a rejection CLASSIFIER is, by definition, derived from an
// error, and therefore has a stage and a type worth recording. Fixed-string
// reasons (a structural exemption, an offer miss) are untouched -- there is no
// error behind them to stamp.
func TestChaos4086_EveryErrorDerivedReasonIsStamped(t *testing.T) {
	const source = "chaos3742_two_turn_confirmation_test.go"
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, source, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", source, err)
	}
	classifiers := map[string]bool{
		"contextFabricRejectionClass": true,
		"anchorSeedRejectionClass":    true,
	}
	ast.Inspect(parsed, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			selector, ok := lhs.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "ArmInvalidReason" || i >= len(assign.Rhs) {
				continue
			}
			ast.Inspect(assign.Rhs[i], func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok || !classifiers[ident.Name] {
					return true
				}
				t.Errorf("%s:%d assigns an error-derived ArmInvalidReason directly. Use twoTurnStampArmFailure(res, reason, err) so the row also names the stage and the error type -- an arm-invalid row without them sends the reader back to the exchange files.",
					source, fileSet.Position(assign.Pos()).Line)
				return false
			})
		}
		return true
	})
}

// TestChaos4100_CoverageIsDerivedFromRowsNotFromTheAssignedSet is codex
// xhigh round 1's P2, closed structurally.
//
// The first version derived a shard's coverage from its post-filter ENTRY
// list, which is what the shard was ASSIGNED. ACR_TEST_TRIAL_LIMIT truncates
// afterwards, so a deliberately-limited dry run recorded FULL coverage --
// and because the merge step unions these claims into the merged artifact's
// authoritative coverage record, a partial run presented itself as a
// complete one.
//
// Deriving from the rows removes the ordering question rather than answering
// it: a row exists if and only if an arm ran for that case, so no filter
// added later, in any order, can make this over-report.
func TestChaos4100_CoverageIsDerivedFromRowsNotFromTheAssignedSet(t *testing.T) {
	// Three cases assigned; the run produced rows for only two of them,
	// which is exactly what a limit (or an abort) leaves behind.
	rows := []twoTurnCaseResult{
		{Index: 7, Member: "expected_kind", Arm: "positive"},
		{Index: 7, Member: "expected_kind", Arm: "inferred_tier"},
		{Index: 2, Member: "window", Arm: "positive"},
	}
	got := twoTurnCaseIndicesFromResults(rows)
	if want := []int{2, 7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("coverage = %v, want %v -- ascending, de-duplicated, and only what produced rows", got, want)
	}
	if len(twoTurnCaseIndicesFromResults(nil)) != 0 {
		t.Fatal("a shard that produced no rows must report no coverage, not an empty-but-present claim")
	}
}

// TestChaos4100_AnEmptyShardAssignmentIsDistinctFromNoAssignment is codex
// xhigh round 4's P2.
//
// The reported CONSEQUENCE did not reproduce: the launcher computes its
// layout with the same modulo rule over the same index set the harness
// applies, so a shard the launcher leaves empty is one modulo also leaves
// empty, and no case is duplicated. The DEFECT is real anyway -- that
// safety holds only because two independent implementations agree, and an
// empty string cannot say which of the two states the launcher meant.
//
// With a sentinel the states are distinguishable, so a future divergence
// fails loudly instead of silently rerunning another shard's cases.
func TestChaos4100_AnEmptyShardAssignmentIsDistinctFromNoAssignment(t *testing.T) {
	t.Setenv("ACR_TEST_TRIAL_SHARD_CASE_INDICES", twoTurnShardNoCasesSentinel)
	indices, set := twoTurnShardCaseIndices(t)
	if set == nil {
		t.Fatal("an explicit no-cases assignment must return a non-nil set -- a nil set is the signal to fall back to modulo, which is the ambiguity being removed")
	}
	if len(indices) != 0 || len(set) != 0 {
		t.Fatalf("explicit no-cases must select nothing, got %v", indices)
	}

	// The unset case must STILL mean "select by modulo", or every existing
	// invocation changes behaviour.
	t.Setenv("ACR_TEST_TRIAL_SHARD_CASE_INDICES", "")
	if indices, set := twoTurnShardCaseIndices(t); set != nil || indices != nil {
		t.Fatalf("an unset assignment must fall back to modulo, got indices=%v set=%v", indices, set)
	}
}
