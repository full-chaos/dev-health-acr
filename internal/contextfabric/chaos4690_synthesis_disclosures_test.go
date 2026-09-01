package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4690 Commit F, design §4.2: applyCoverageDisclosures is the guard
// itself, pure and side-effect-free -- these tests exercise each clause in
// isolation, directly against the guard, at the smallest reproducing unit.
// TestRuntimeAnswerSynthesizer* below exercises the SAME clauses through
// the full Synthesize call, proving the guard is actually wired in and
// that telemetry reports the outcome it decided.
//
// RED on parent f94df525: applyCoverageDisclosures, CoverageDisclosure,
// and SynthesisDraft.CoverageDisclosures do not exist on that commit, so
// this whole file fails to compile there -- proof the surface is new.

func twoDetailResult() *InvestigationResult {
	return &InvestigationResult{
		Coverage: Coverage{
			Details: []CoverageDetail{
				{DetailID: "cov-01", Source: "canonical_fact:readiness", Code: contractsv1.ContextFabricCoverageDetailFactPruned, Label: "Readiness facts were not evaluated"},
				{DetailID: "cov-02", Source: "canonical_fact:status", Code: contractsv1.ContextFabricCoverageDetailFactPruned, Label: "Status facts were not evaluated"},
			},
		},
	}
}

func TestApplyCoverageDisclosures_AbsentWhenNil(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	outcome, violation := applyCoverageDisclosures(result, nil)
	if outcome != CoverageDisclosureAbsent || violation != "" {
		t.Fatalf("applyCoverageDisclosures(nil) = (%q, %q), want (absent, \"\")", outcome, violation)
	}
	for _, d := range result.Coverage.Details {
		if d.Phrasing != "" {
			t.Fatalf("detail %q Phrasing = %q, want empty on an absent disclosure set", d.DetailID, d.Phrasing)
		}
	}
}

func TestApplyCoverageDisclosures_AbsentWhenEmpty(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	outcome, violation := applyCoverageDisclosures(result, []CoverageDisclosure{})
	if outcome != CoverageDisclosureAbsent || violation != "" {
		t.Fatalf("applyCoverageDisclosures([]) = (%q, %q), want (absent, \"\")", outcome, violation)
	}
}

func TestApplyCoverageDisclosures_PhrasedWhenEveryDetailCovered(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: "Readiness data could not be reached this time."},
		{DetailID: "cov-02", Text: "Status data was not part of this check."},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosurePhrased || violation != "" {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (phrased, \"\")", outcome, violation)
	}
	if result.Coverage.Details[0].Phrasing != disclosures[0].Text {
		t.Fatalf("Details[0].Phrasing = %q, want %q", result.Coverage.Details[0].Phrasing, disclosures[0].Text)
	}
	if result.Coverage.Details[1].Phrasing != disclosures[1].Text {
		t.Fatalf("Details[1].Phrasing = %q, want %q", result.Coverage.Details[1].Phrasing, disclosures[1].Text)
	}
}

func TestApplyCoverageDisclosures_PartialAbsentWhenSomeDetailsUncovered(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: "Readiness data could not be reached this time."},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosurePartialAbsent || violation != "" {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (partial_absent, \"\")", outcome, violation)
	}
	if result.Coverage.Details[0].Phrasing != disclosures[0].Text {
		t.Fatalf("Details[0].Phrasing = %q, want %q", result.Coverage.Details[0].Phrasing, disclosures[0].Text)
	}
	if result.Coverage.Details[1].Phrasing != "" {
		t.Fatalf("Details[1].Phrasing = %q, want empty -- the model did not phrase this detail", result.Coverage.Details[1].Phrasing)
	}
}

// --- Guard clauses: each planted individually, each proven to discard the
// WHOLE set (including an otherwise-valid sibling disclosure), never
// applying anything to result. ---

func TestApplyCoverageDisclosures_GuardUnknownDetailID(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: "Readiness data could not be reached this time."},
		{DetailID: "cov-99", Text: "This detail does not exist."},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosureRejectedByGuard || violation != CoverageDisclosureViolationUnknownDetailID {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (rejected_by_guard, unknown_detail_id)", outcome, violation)
	}
	if result.Coverage.Details[0].Phrasing != "" {
		t.Fatalf("Details[0].Phrasing = %q, want empty -- an otherwise-valid sibling must not survive the discard", result.Coverage.Details[0].Phrasing)
	}
}

func TestApplyCoverageDisclosures_GuardDuplicateDetailID(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: "Readiness data could not be reached this time."},
		{DetailID: "cov-01", Text: "A second, different thing about the same detail."},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosureRejectedByGuard || violation != CoverageDisclosureViolationDuplicateDetailID {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (rejected_by_guard, duplicate_detail_id)", outcome, violation)
	}
	if result.Coverage.Details[0].Phrasing != "" {
		t.Fatalf("Details[0].Phrasing = %q, want empty on a discarded set", result.Coverage.Details[0].Phrasing)
	}
}

func TestApplyCoverageDisclosures_GuardTextBound_Empty(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	// Text is exactly "" -- already trimmed, so this isolates the
	// empty-after-trim sub-clause from the separate untrimmed sub-clause
	// (TestApplyCoverageDisclosures_GuardTextBound_Untrimmed covers that
	// one; a whitespace-only string like "   " would trip BOTH and could
	// not tell a mutation of either sub-clause apart from the other).
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: "Readiness data could not be reached this time."},
		{DetailID: "cov-02", Text: ""},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosureRejectedByGuard || violation != CoverageDisclosureViolationTextBound {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (rejected_by_guard, text_bound)", outcome, violation)
	}
	if result.Coverage.Details[0].Phrasing != "" {
		t.Fatalf("Details[0].Phrasing = %q, want empty -- an otherwise-valid sibling must not survive the discard", result.Coverage.Details[0].Phrasing)
	}
}

func TestApplyCoverageDisclosures_GuardTextBound_Untrimmed(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: " Readiness data could not be reached this time."},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosureRejectedByGuard || violation != CoverageDisclosureViolationTextBound {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (rejected_by_guard, text_bound)", outcome, violation)
	}
}

func TestApplyCoverageDisclosures_GuardTextBound_OverMaxLength(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: strings.Repeat("a", contractsv1.ContextFabricCoverageDetailPhrasingMaxLength+1)},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosureRejectedByGuard || violation != CoverageDisclosureViolationTextBound {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (rejected_by_guard, text_bound)", outcome, violation)
	}
}

func TestApplyCoverageDisclosures_TextAtMaxLengthIsAccepted(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: strings.Repeat("a", contractsv1.ContextFabricCoverageDetailPhrasingMaxLength)},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosurePartialAbsent || violation != "" {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (partial_absent, \"\") -- exactly the max length must be accepted", outcome, violation)
	}
}

func TestApplyCoverageDisclosures_GuardDigitsForbidden(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: "3 readiness checks could not be reached."},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosureRejectedByGuard || violation != CoverageDisclosureViolationDigitsForbidden {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (rejected_by_guard, digits_forbidden)", outcome, violation)
	}
}

func TestApplyCoverageDisclosures_GuardDigitsForbidden_WholeSetDiscardedIncludingValidSibling(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	disclosures := []CoverageDisclosure{
		{DetailID: "cov-01", Text: "Readiness data could not be reached this time."},
		{DetailID: "cov-02", Text: "This mentions a digit like 2 in passing."},
	}
	outcome, violation := applyCoverageDisclosures(result, disclosures)
	if outcome != CoverageDisclosureRejectedByGuard || violation != CoverageDisclosureViolationDigitsForbidden {
		t.Fatalf("applyCoverageDisclosures() = (%q, %q), want (rejected_by_guard, digits_forbidden)", outcome, violation)
	}
	if result.Coverage.Details[0].Phrasing != "" || result.Coverage.Details[1].Phrasing != "" {
		t.Fatalf("Details = %#v, want NEITHER detail phrased -- the otherwise-valid cov-01 sibling must not survive the discard", result.Coverage.Details)
	}
}

// TestClassifyCoverageDisclosures_UndecodableTakesPrecedence proves the
// dispatcher never re-derives undecodable from the (necessarily empty)
// disclosure slice -- it trusts SynthesisDraft's own decode-layer flag.
func TestClassifyCoverageDisclosures_UndecodableTakesPrecedence(t *testing.T) {
	t.Parallel()
	result := twoDetailResult()
	draft := SynthesisDraft{CoverageDisclosuresUndecodable: true}
	outcome, violation := classifyCoverageDisclosures(draft, result)
	if outcome != CoverageDisclosureDiscardedUndecodable || violation != CoverageDisclosureViolationParseFailed {
		t.Fatalf("classifyCoverageDisclosures() = (%q, %q), want (discarded_undecodable, parse_failed)", outcome, violation)
	}
}

// --- Full Synthesize() integration: proves the guard is actually wired
// in, applied against the SAME merged result.Coverage.Details the caller
// serves, and that telemetry reports the outcome it decided. ---

// synthesisInputWithTwoCoverageDetails is validSynthesisInputFixture()
// with two non-degrading fact_pruned details on Graph.Coverage -- chosen
// non-degrading specifically to sidestep mergeCoverageDetails' own
// degraded_reasons reconciliation (irrelevant to this guard), and to keep
// the Sources/DegradedReasons golden-anchor computation untouched.
func synthesisInputWithTwoCoverageDetails() SynthesisInput {
	input := validSynthesisInputFixture()
	input.Graph.Coverage = Coverage{
		Sources: []SourceObservation{}, DegradedReasons: []string{},
		Details: []CoverageDetail{
			detailFixture(contractsv1.ContextFabricCoverageDetailFactPruned, "canonical_fact:readiness", false, "", func(d *CoverageDetail) {
				d.FactKind = FactReadiness
			}),
			detailFixture(contractsv1.ContextFabricCoverageDetailFactPruned, "canonical_fact:status", false, "", func(d *CoverageDetail) {
				d.FactKind = FactStatus
			}),
		},
	}
	return input
}

// mintedDetailIDs runs the SAME normalizer Synthesize itself calls
// (MergeCoverage) so a test can address a detail by the ordinal id it will
// actually be minted under, without hardcoding an assumption about sort
// order that would silently drift from mergeCoverageDetails' own rules.
func mintedDetailIDs(t *testing.T, orgID string, input SynthesisInput) []string {
	t.Helper()
	merged := MergeCoverage(orgID, input.Graph.Coverage, input.Facts.Coverage)
	ids := make([]string, len(merged.Details))
	for i, d := range merged.Details {
		ids[i] = d.DetailID
	}
	if len(ids) != 2 {
		t.Fatalf("mintedDetailIDs() = %v, want exactly 2 (fixture drifted)", ids)
	}
	return ids
}

func TestRuntimeAnswerSynthesizer_CoverageDisclosures_HappyPathPhrasesBothDetails(t *testing.T) {
	t.Parallel()
	input := synthesisInputWithTwoCoverageDetails()
	ids := mintedDetailIDs(t, "org_1", input)
	draft := validSynthesisDraftFixture(input)
	draft.CoverageDisclosures = []CoverageDisclosure{
		{DetailID: ids[0], Text: "Readiness data could not be reached this time."},
		{DetailID: ids[1], Text: "Status data was not part of this check."},
	}
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v, want nil", err)
	}
	if len(result.Coverage.Details) != 2 || result.Coverage.Details[0].Phrasing == "" || result.Coverage.Details[1].Phrasing == "" {
		t.Fatalf("result.Coverage.Details = %#v, want both details carrying a Phrasing", result.Coverage.Details)
	}
	if len(telemetry.coverageDisclosurePhrasings) != 1 {
		t.Fatalf("coverageDisclosurePhrasings = %#v, want exactly one recorded call", telemetry.coverageDisclosurePhrasings)
	}
	got := telemetry.coverageDisclosurePhrasings[0]
	if got.outcome != CoverageDisclosurePhrased || got.phrased != 2 || got.total != 2 {
		t.Fatalf("telemetry record = %#v, want {phrased, 2, 2}", got)
	}
}

func TestRuntimeAnswerSynthesizer_CoverageDisclosures_GuardRejectionShipsLabelOnlyAndNeverFailsTheAnswer(t *testing.T) {
	t.Parallel()
	input := synthesisInputWithTwoCoverageDetails()
	ids := mintedDetailIDs(t, "org_1", input)
	draft := validSynthesisDraftFixture(input)
	draft.CoverageDisclosures = []CoverageDisclosure{
		{DetailID: ids[0], Text: "5 readiness checks could not run."},
	}
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v, want nil -- a guard rejection must never fail the answer", err)
	}
	for _, d := range result.Coverage.Details {
		if d.Phrasing != "" {
			t.Fatalf("detail %q Phrasing = %q, want empty -- the whole set was guard-rejected", d.DetailID, d.Phrasing)
		}
		if d.Label == "" {
			t.Fatalf("detail %q Label = \"\", want the Label-only floor to still be present", d.DetailID)
		}
	}
	if len(telemetry.coverageDisclosurePhrasings) != 1 {
		t.Fatalf("coverageDisclosurePhrasings = %#v, want exactly one recorded call", telemetry.coverageDisclosurePhrasings)
	}
	got := telemetry.coverageDisclosurePhrasings[0]
	if got.outcome != CoverageDisclosureRejectedByGuard || got.phrased != 0 || got.total != 2 {
		t.Fatalf("telemetry record = %#v, want {rejected_by_guard, 0, 2}", got)
	}
}

func TestRuntimeAnswerSynthesizer_CoverageDisclosures_UndecodableRecordsTelemetryAndServesAnswer(t *testing.T) {
	t.Parallel()
	input := synthesisInputWithTwoCoverageDetails()
	draft := validSynthesisDraftFixture(input)
	draft.CoverageDisclosuresUndecodable = true
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v, want nil", err)
	}
	for _, d := range result.Coverage.Details {
		if d.Phrasing != "" {
			t.Fatalf("detail %q Phrasing = %q, want empty on an undecodable set", d.DetailID, d.Phrasing)
		}
	}
	if len(telemetry.coverageDisclosurePhrasings) != 1 {
		t.Fatalf("coverageDisclosurePhrasings = %#v, want exactly one recorded call", telemetry.coverageDisclosurePhrasings)
	}
	got := telemetry.coverageDisclosurePhrasings[0]
	if got.outcome != CoverageDisclosureDiscardedUndecodable || got.phrased != 0 || got.total != 2 {
		t.Fatalf("telemetry record = %#v, want {discarded_undecodable, 0, 2}", got)
	}
}

func TestRuntimeAnswerSynthesizer_CoverageDisclosures_AbsentRecordsTelemetry(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	_, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v, want nil", err)
	}
	if len(telemetry.coverageDisclosurePhrasings) != 1 {
		t.Fatalf("coverageDisclosurePhrasings = %#v, want exactly one recorded call", telemetry.coverageDisclosurePhrasings)
	}
	got := telemetry.coverageDisclosurePhrasings[0]
	if got.outcome != CoverageDisclosureAbsent || got.phrased != 0 {
		t.Fatalf("telemetry record = %#v, want outcome=absent, phrased=0", got)
	}
}
