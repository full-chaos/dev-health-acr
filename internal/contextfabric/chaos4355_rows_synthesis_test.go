package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// --- CHAOS-4355: route producer-emitted Rows (CHAOS-4347) into synthesis and
// the projected answer, with the model NEVER authoring Rows itself. ---

// rowsClosureFixture is closureFixture (FactReadiness/release_ready=false,
// a valid claim restating it) PLUS a second, Rows-shaped field on the SAME
// canonical fact -- e.g. a producer's per-team breakdown of the same
// observation. The claim only ever cites release_ready; team_rows is
// supplementary detail on the fact it cites, exactly the shape
// MetricsProvider's project rollup uses (rollup scalars + team_breakdown).
func rowsClosureFixture() (SynthesisInput, SynthesisDraft, []FactValueRow) {
	input, draft := closureFixture()
	teamRows := []FactValueRow{
		{Fields: map[string]FactValue{
			"team_id":       StringFactValue("team_a"),
			"commits_count": IntegerFactValue(12),
		}},
		{Fields: map[string]FactValue{
			"team_id":       StringFactValue("team_b"),
			"commits_count": IntegerFactValue(7),
		}},
	}
	input.Facts.Facts[0].Fields["team_rows"] = RowsFactValue(teamRows)
	return input, draft, teamRows
}

// expectedTeamRows is rowsClosureFixture's team_rows canonical field,
// HAND-WRITTEN as the wire shape a claim should carry -- deliberately NOT
// built by calling convertFactValueRow (the same production converter
// under test), so this assertion cannot pass merely because the converter
// and the test agree with each other on a shared bug (codex CHAOS-4355 R1
// test-gap finding).
func expectedTeamRows() []ClaimedFactRow {
	teamA, teamB := "team_a", "team_b"
	commitsA, commitsB := int64(12), int64(7)
	return []ClaimedFactRow{
		{Fields: map[string]ScalarValue{"team_id": {String: &teamA}, "commits_count": {Integer: &commitsA}}},
		{Fields: map[string]ScalarValue{"team_id": {String: &teamB}, "commits_count": {Integer: &commitsB}}},
	}
}

// TestRuntimeAnswerSynthesizerAttachesCanonicalRowsToTheClaimThatCitesThem is
// the RED-FIRST proof for CHAOS-4355's core build: before attachCanonicalRows
// existed, Synthesize copied draft.ClaimedFacts straight through
// (cloneSlice(draft.ClaimedFacts)) and the model is forbidden from setting
// Rows itself (TestSynthesisDraftValidateAgainstRejectsClaimedFactSettingRows),
// so a canonical fact's renderable table NEVER reached the projected answer
// -- Rows was always absent, exactly the CHAOS-4347 handoff gap ("Rows not
// routed into synthesis"). After the fix, the claim's Rows must equal the
// canonical fact's own table, copied verbatim, field-for-field.
func TestRuntimeAnswerSynthesizerAttachesCanonicalRowsToTheClaimThatCitesThem(t *testing.T) {
	t.Parallel()
	input, draft, _ := rowsClosureFixture()
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    &fakeReceiptSink{},
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(result.ClaimedFacts) != 1 {
		t.Fatalf("ClaimedFacts = %v, want exactly 1", result.ClaimedFacts)
	}
	if want := expectedTeamRows(); !reflect.DeepEqual(result.ClaimedFacts[0].Rows, want) {
		t.Fatalf("ClaimedFacts[0].Rows = %+v, want %+v (verbatim copy of the canonical fact's team_rows field)", result.ClaimedFacts[0].Rows, want)
	}
}

// TestAttachCanonicalRowsOnlyAttachesToTheClaimThatCitesTheMatchingFact is
// the wrong-fact-routing coverage codex R1 asked for: a bundle with TWO
// canonical facts (different subjects), only one of them Rows-shaped, and
// TWO claims, one per fact. The rows-bearing fact's rows must land on ONLY
// the claim that cites it (same Kind+Subject) -- never on the other claim,
// and never merged across facts.
func TestAttachCanonicalRowsOnlyAttachesToTheClaimThatCitesTheMatchingFact(t *testing.T) {
	t.Parallel()
	projectSubject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	repoSubject := SubjectRef{Kind: SubjectRepository, CanonicalID: "repo_acr", Label: "acr"}
	rowsFact := CanonicalFact{
		Kind: FactMetrics, Subject: projectSubject,
		Fields: map[string]FactValue{
			"commits_count": IntegerFactValue(19),
			"team_rows":     RowsFactValue([]FactValueRow{{Fields: map[string]FactValue{"team_id": StringFactValue("team_a")}}}),
		},
		EvidenceRefIDs: []string{"evidence_1"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
	scalarOnlyFact := CanonicalFact{
		Kind: FactMetrics, Subject: repoSubject,
		Fields:         map[string]FactValue{"commits_count": IntegerFactValue(4)},
		EvidenceRefIDs: []string{"evidence_2"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
	claims := []ClaimedFact{
		{ClaimID: "claim_repo", Kind: FactMetrics, Subject: repoSubject, Field: "commits_count", Value: ScalarValue{Integer: int64Ptr(4)}},
		{ClaimID: "claim_project", Kind: FactMetrics, Subject: projectSubject, Field: "commits_count", Value: ScalarValue{Integer: int64Ptr(19)}},
	}
	got, count, _, truncated := attachCanonicalRows(claims, []CanonicalFact{scalarOnlyFact, rowsFact})
	if truncated {
		t.Fatalf("truncated = true, want false")
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if got[0].Rows != nil {
		t.Fatalf("claim_repo.Rows = %+v, want nil -- its own canonical fact carries no rows, the OTHER fact's rows must not leak onto it", got[0].Rows)
	}
	if len(got[1].Rows) != 1 {
		t.Fatalf("claim_project.Rows = %+v, want exactly 1 row from its own canonical fact", got[1].Rows)
	}
}

// TestCanonicalFieldRowsFailsClosedWhenMultipleFieldsAreRowsShaped is codex
// CHAOS-4355 R2's sharpened P1 finding: a canonical fact carrying MORE than
// one Rows-shaped field must not have any single one picked arbitrarily
// either (R1's fix picked the lexically-first field, which is
// deterministic but semantically arbitrary -- a claim carries no
// field-level identity to say which table it means, so presenting a
// lexically-chosen one risks presenting the WRONG table as if it were
// authoritative). Rows must come back nil, with the drop reported via
// truncated, run twice to defend against the removed sort masking a
// leftover map-iteration dependency.
func TestCanonicalFieldRowsFailsClosedWhenMultipleFieldsAreRowsShaped(t *testing.T) {
	t.Parallel()
	fact := CanonicalFact{
		Kind: FactMetrics, Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"},
		Fields: map[string]FactValue{
			"a_rows": RowsFactValue([]FactValueRow{{Fields: map[string]FactValue{"x": IntegerFactValue(1)}}}),
			"b_rows": RowsFactValue([]FactValueRow{{Fields: map[string]FactValue{"y": IntegerFactValue(2)}}, {Fields: map[string]FactValue{"y": IntegerFactValue(3)}}}),
		},
		EvidenceRefIDs: []string{"evidence_1"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
	for i := 0; i < 10; i++ {
		rows, truncated := canonicalFieldRows(fact)
		if !truncated {
			t.Fatalf("iteration %d: truncated = false, want true (fact carries 2 Rows-shaped fields, ambiguous which one a claim means)", i)
		}
		if rows != nil {
			t.Fatalf("iteration %d: rows = %+v, want nil -- ambiguous which of a_rows/b_rows a claim means, so neither may be guessed", i, rows)
		}
	}
}

// TestAttachCanonicalRowsReportsTruncatedOnAmbiguousDropEvenThoughNothingWasAttached
// is codex CHAOS-4355 R3's P2 finding: attachCanonicalRows used to union
// wasTruncated into its return value AFTER the `len(rows) == 0` early
// continue, so canonicalFieldRows' fail-closed ambiguous-fields case (which
// returns rows=nil, truncated=true) never reached the caller -- an
// ambiguous drop silently reported truncated=false, contradicting this
// function's own "reported unconditionally" doc comment for exactly the
// case it exists to cover. This is the end-to-end proof: a claim citing a
// fact with two Rows-shaped fields must come out with Rows nil AND
// attachCanonicalRows' own truncated return value true.
func TestAttachCanonicalRowsReportsTruncatedOnAmbiguousDropEvenThoughNothingWasAttached(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	fact := CanonicalFact{
		Kind: FactMetrics, Subject: subject,
		Fields: map[string]FactValue{
			"a_rows": RowsFactValue([]FactValueRow{{Fields: map[string]FactValue{"x": IntegerFactValue(1)}}}),
			"b_rows": RowsFactValue([]FactValueRow{{Fields: map[string]FactValue{"y": IntegerFactValue(2)}}}),
		},
		EvidenceRefIDs: []string{"evidence_1"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
	claims := []ClaimedFact{{
		ClaimID: "claim_1", Kind: FactMetrics, Subject: subject, Field: "team_count", Value: ScalarValue{Integer: int64Ptr(2)},
	}}
	got, count, _, truncated := attachCanonicalRows(claims, []CanonicalFact{fact})
	if !truncated {
		t.Fatalf("truncated = false, want true -- the ambiguous-fields drop must be reported even though nothing was attached")
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0 (nothing was attached)", count)
	}
	if got[0].Rows != nil {
		t.Fatalf("Rows = %+v, want nil -- ambiguous which of a_rows/b_rows the claim means", got[0].Rows)
	}
}

// TestRuntimeAnswerSynthesizerLeavesClaimRowsNilWhenCanonicalFactCarriesNone
// is the negative half: a claim against a canonical fact with no Rows-shaped
// field must come out byte-identical to pre-CHAOS-4355 behavior -- nil, not
// an empty non-nil slice, and never invented content.
func TestRuntimeAnswerSynthesizerLeavesClaimRowsNilWhenCanonicalFactCarriesNone(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    &fakeReceiptSink{},
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if result.ClaimedFacts[0].Rows != nil {
		t.Fatalf("ClaimedFacts[0].Rows = %v, want nil", result.ClaimedFacts[0].Rows)
	}
}

// TestRuntimeAnswerSynthesizerRecordsProjectedRowsCount proves the
// projected_rows_count telemetry dimension fires, unconditionally, with the
// real row count -- and that a Synthesize call producing no rows still
// reports zero rather than staying silent (same "zero must be as visible as
// nonzero" discipline every other EngineTelemetry method in this package
// follows).
func TestRuntimeAnswerSynthesizerRecordsProjectedRowsCount(t *testing.T) {
	t.Parallel()
	input, draft, teamRows := rowsClosureFixture()
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(telemetry.projectedRowsCounts) != 1 {
		t.Fatalf("projectedRowsCounts = %v, want exactly 1 record", telemetry.projectedRowsCounts)
	}
	if got := telemetry.projectedRowsCounts[0]; got.count != len(teamRows) || got.truncated {
		t.Fatalf("projectedRowsCounts[0] = %+v, want count=%d truncated=false", got, len(teamRows))
	}

	telemetry = &recordingTelemetry{}
	zeroInput, zeroDraft := closureFixture()
	synthesizer = RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: zeroDraft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, zeroInput); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(telemetry.projectedRowsCounts) != 1 || telemetry.projectedRowsCounts[0].count != 0 {
		t.Fatalf("projectedRowsCounts = %v, want exactly 1 record with count=0 (a quiet call must still report zero)", telemetry.projectedRowsCounts)
	}
}

// TestSynthesisDraftValidateAgainstRejectsModelAuthoredRowsEvenWhenTheyMatchCanonical
// hardens TestSynthesisDraftValidateAgainstRejectsClaimedFactSettingRows: the
// rejection must be unconditional, not a "rows must match canonical" check
// that a lucky (or prompt-context-copied) model output could pass. The model
// is never the source of Rows, regardless of correctness.
func TestSynthesisDraftValidateAgainstRejectsModelAuthoredRowsEvenWhenTheyMatchCanonical(t *testing.T) {
	t.Parallel()
	input, draft, _ := rowsClosureFixture()
	draft.ClaimedFacts[0].Rows = expectedTeamRows()
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "sets rows") {
		t.Fatalf("ValidateAgainst() error = %v, want a sets-rows rejection even though Rows matches canonical exactly", err)
	}
}

// TestRuntimeAnswerSynthesizerDoesNotRecordProjectedRowsCountOnRejectedDraft
// locks in RecordProjectedRowsCount's corrected doc comment (codex CHAOS-4355
// R1 P2 finding: it is NOT called unconditionally -- a Synthesize call that
// never reaches claim assembly reports nothing here, because there are no
// claims to report rows for and the rejection is already the receipt sink's
// own outcome to record).
func TestRuntimeAnswerSynthesizerDoesNotRecordProjectedRowsCountOnRejectedDraft(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	draft.EvidenceRefIDs = []string{"evidence_invented_by_model"}
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); !errors.Is(err, ErrSynthesisRejected) {
		t.Fatalf("Synthesize() error = %v, want ErrSynthesisRejected (test setup precondition)", err)
	}
	if len(telemetry.projectedRowsCounts) != 0 {
		t.Fatalf("projectedRowsCounts = %v, want none recorded for a rejected draft", telemetry.projectedRowsCounts)
	}
}

// TestAttachCanonicalRowsTruncatesAtBoundAndReportsTruncated is the
// cap/pruning half CHAOS-4355 asks for: a canonical fact whose Rows-shaped
// field(s) exceed ContextFabricClaimedFactMaxRows must still produce a
// contract-valid claim (capped, never rejected outright), and the caller
// must be told truncation happened rather than it being silent.
func TestAttachCanonicalRowsTruncatesAtBoundAndReportsTruncated(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	rows := make([]FactValueRow, contractsv1.ContextFabricClaimedFactMaxRows+5)
	for i := range rows {
		rows[i] = FactValueRow{Fields: map[string]FactValue{"team_id": IntegerFactValue(int64(i))}}
	}
	fact := CanonicalFact{
		Kind: FactMetrics, Subject: subject,
		Fields:         map[string]FactValue{"team_rows": RowsFactValue(rows)},
		EvidenceRefIDs: []string{"evidence_1"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
	claims := []ClaimedFact{{
		ClaimID: "claim_1", Kind: FactMetrics, Subject: subject, Field: "team_count", Value: ScalarValue{Integer: int64Ptr(2)},
	}}
	got, count, _, truncated := attachCanonicalRows(claims, []CanonicalFact{fact})
	if !truncated {
		t.Fatalf("truncated = false, want true (source had %d rows, cap is %d)", len(rows), contractsv1.ContextFabricClaimedFactMaxRows)
	}
	if count != contractsv1.ContextFabricClaimedFactMaxRows {
		t.Fatalf("count = %d, want %d (capped)", count, contractsv1.ContextFabricClaimedFactMaxRows)
	}
	if len(got[0].Rows) != contractsv1.ContextFabricClaimedFactMaxRows {
		t.Fatalf("len(Rows) = %d, want %d", len(got[0].Rows), contractsv1.ContextFabricClaimedFactMaxRows)
	}
}

// TestAttachCanonicalRowsByKindIncludesClaimedButZeroKind is CHAOS-4418's
// own pin: byKind must carry an entry for every FactKind a claim named,
// including a kind whose canonical fact carried no Rows-shaped field at
// all (attached zero rows) -- that kind must NOT be indistinguishable from
// a kind nobody claimed this call. This is the exact "repository subject
// commits, facts compose, rows_count=0" diagnostic gap this ticket exists
// to close: before this fix, seeing rows_count=0 in the aggregate
// telemetry could not tell a reader WHICH fact kind's producer was the
// one still emitting scalar-only fields.
func TestAttachCanonicalRowsByKindIncludesClaimedButZeroKind(t *testing.T) {
	t.Parallel()
	repoSubject := SubjectRef{Kind: SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	// FactIdentity: scalar-only canonical fact (identity.go stays scalar,
	// team-lead ruling) -- claimed, but attaches zero rows.
	identityFact := CanonicalFact{
		Kind: FactIdentity, Subject: repoSubject,
		Fields:         map[string]FactValue{"provider": IntegerFactValue(1)},
		EvidenceRefIDs: []string{"evidence_identity"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
	// FactMetrics: a real Rows-shaped field -- claimed, attaches 2 rows.
	metricsRows := []FactValueRow{
		{Fields: map[string]FactValue{"day": StringFactValue("2026-08-01")}},
		{Fields: map[string]FactValue{"day": StringFactValue("2026-08-02")}},
	}
	metricsFact := CanonicalFact{
		Kind: FactMetrics, Subject: repoSubject,
		Fields:         map[string]FactValue{"daily_metrics": RowsFactValue(metricsRows)},
		EvidenceRefIDs: []string{"evidence_metrics"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
	claims := []ClaimedFact{
		{ClaimID: "claim_identity", Kind: FactIdentity, Subject: repoSubject, Field: "provider", Value: ScalarValue{Integer: int64Ptr(1)}},
		{ClaimID: "claim_metrics", Kind: FactMetrics, Subject: repoSubject, Field: "day_count", Value: ScalarValue{Integer: int64Ptr(2)}},
	}
	_, count, byKind, truncated := attachCanonicalRows(claims, []CanonicalFact{identityFact, metricsFact})
	if truncated {
		t.Fatalf("truncated = true, want false")
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	got, ok := byKind[FactIdentity]
	if !ok {
		t.Fatalf("byKind = %#v, want FactIdentity present (claimed this call, even though it attaches zero rows)", byKind)
	}
	if got != 0 {
		t.Fatalf("byKind[FactIdentity] = %d, want 0", got)
	}
	if byKind[FactMetrics] != 2 {
		t.Fatalf("byKind[FactMetrics] = %d, want 2", byKind[FactMetrics])
	}
	if _, ok := byKind[FactHealth]; ok {
		t.Fatalf("byKind = %#v, want FactHealth absent -- it was never claimed this call, a different fact from a claimed-but-zero kind", byKind)
	}
}

// TestRuntimeAnswerSynthesizerRecordsProjectedRowsByFactKind pins the
// caller side: RuntimeAnswerSynthesizer.Synthesize must actually call
// RecordProjectedRowsByFactKind (not just attachCanonicalRows compute it)
// on a successful claim-assembly call, with the same map attachCanonicalRows
// returned.
func TestRuntimeAnswerSynthesizerRecordsProjectedRowsByFactKind(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	telemetry := &recordingTelemetry{}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime:   fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:      &fakeReceiptSink{},
		Telemetry: telemetry,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(telemetry.projectedRowsByFactKind) != 1 {
		t.Fatalf("projectedRowsByFactKind = %#v, want exactly 1 record", telemetry.projectedRowsByFactKind)
	}
}

func int64Ptr(v int64) *int64 { return &v }
