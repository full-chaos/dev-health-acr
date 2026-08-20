package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// fakePriorConsultant is a plain, in-memory PriorConsultant driven by a
// map keyed on "orgID|questionHash" -- every test below constructs exactly
// the entries it needs, no database involved (priors_consult.go's own
// business logic is deliberately DB-free -- see PriorConsultant's own doc
// comment).
type fakePriorConsultant struct {
	entries map[string][]StructurePriorEntry
	state   PriorDegradationState
	calls   int
}

func (f *fakePriorConsultant) Consult(_ context.Context, orgID, questionHash string) ([]StructurePriorEntry, PriorDegradationState) {
	f.calls++
	if f.state != PriorDegradationNone {
		return nil, f.state
	}
	return f.entries[orgID+"|"+questionHash], PriorDegradationNone
}

func priorTestPrincipal() storage.Principal { return storage.Principal{OrgID: "org_priors_test"} }

// --- fetchPriorEntries: the ONE I/O call site ---

func TestFetchPriorEntries_NilConsultant(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{})
	entries := engine.fetchPriorEntries(context.Background(), priorTestPrincipal(), "hash-1")
	require.Empty(t, entries)
}

func TestFetchPriorEntries_CallsConsultantExactlyOnce(t *testing.T) {
	t.Parallel()
	consultant := &fakePriorConsultant{entries: map[string][]StructurePriorEntry{
		"org_priors_test|hash-1": {{EntryID: "e1", QuestionHash: "hash-1", Member: contractsv1.ContextFabricStructureNeedExpectedKind, Value: "pull_request"}},
	}}
	engine := mustReuseTestEngine(t, EngineDependencies{PriorConsultant: consultant})
	entries := engine.fetchPriorEntries(context.Background(), priorTestPrincipal(), "hash-1")
	require.Len(t, entries, 1)
	require.Equal(t, 1, consultant.calls)
}

func TestFetchPriorEntries_Degraded_ReturnsNilAndRecords(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	consultant := &fakePriorConsultant{state: PriorDegradationStoreUnavailable}
	engine := mustReuseTestEngine(t, EngineDependencies{PriorConsultant: consultant, Telemetry: telemetry})
	entries := engine.fetchPriorEntries(context.Background(), priorTestPrincipal(), "hash-1")
	require.Empty(t, entries)
	require.Contains(t, telemetry.priorDegradations, PriorDegradationStoreUnavailable)
}

// --- consultPriorStructureOffers: DP4(a) site one ---

func TestConsultPriorStructureOffers_NoEntries_MaterialUnchanged(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{})
	material := StructureOfferMaterial{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind}}
	got := engine.consultPriorStructureOffers(context.Background(), priorTestPrincipal(), nil, material)
	require.Equal(t, material, got, "no entries must leave StructureOfferMaterial byte-identical")
}

func TestConsultPriorStructureOffers_KindOffered(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{Telemetry: telemetry})
	entries := []StructurePriorEntry{
		{EntryID: "entry-1", QuestionHash: "hash-1", Version: 3, Member: contractsv1.ContextFabricStructureNeedExpectedKind, Value: "pull_request", Rank: 0},
	}
	material := StructureOfferMaterial{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind}}

	got := engine.consultPriorStructureOffers(context.Background(), priorTestPrincipal(), entries, material)

	require.Len(t, got.KindOptions, 1)
	opt := got.KindOptions[0]
	require.Equal(t, contractsv1.ContextFabricSubjectPullRequest, opt.Kind)
	require.Equal(t, contractsv1.ContextFabricStructureOfferPrior, opt.OfferSource)
	require.Equal(t, "3", opt.PriorVersionID)
	require.Equal(t, "entry-1", opt.PriorEntryID)
	require.Empty(t, opt.ReceiptID, "receipt/option ids are minted later by composeStructureNeeds, never here")
	require.Contains(t, telemetry.priorConsulted, priorConsultedRecord{contractsv1.ContextFabricStructureNeedExpectedKind, PriorConsultedOffered})
}

func TestConsultPriorStructureOffers_MemberNotMissing_NeverOffered(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{})
	entries := []StructurePriorEntry{
		{EntryID: "entry-1", QuestionHash: "hash-1", Member: contractsv1.ContextFabricStructureNeedExpectedKind, Value: "pull_request"},
	}
	// Missing does NOT include expected_kind -- the class-conditional gate
	// (§1.3) must suppress it even though a candidate exists.
	material := StructureOfferMaterial{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectHandle}}

	got := engine.consultPriorStructureOffers(context.Background(), priorTestPrincipal(), entries, material)
	require.Empty(t, got.KindOptions)
}

func TestConsultPriorStructureOffers_InvalidKindValue_Dropped(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{})
	entries := []StructurePriorEntry{
		{EntryID: "entry-1", QuestionHash: "hash-1", Member: contractsv1.ContextFabricStructureNeedExpectedKind, Value: "not_a_real_kind"},
	}
	material := StructureOfferMaterial{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind}}

	got := engine.consultPriorStructureOffers(context.Background(), priorTestPrincipal(), entries, material)
	require.Empty(t, got.KindOptions, "a value outside the closed structure-offer kind set must never become an offer")
}

func TestConsultPriorStructureOffers_RevokedEntry_SuppressedRevoked(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{Telemetry: telemetry})
	entries := []StructurePriorEntry{
		{EntryID: "entry-1", QuestionHash: "hash-1", Member: contractsv1.ContextFabricStructureNeedExpectedKind, Value: "pull_request", Revoked: true},
	}
	material := StructureOfferMaterial{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind}}

	got := engine.consultPriorStructureOffers(context.Background(), priorTestPrincipal(), entries, material)
	require.Empty(t, got.KindOptions, "a revoked entry must never become an offer")
	require.Contains(t, telemetry.priorConsulted, priorConsultedRecord{contractsv1.ContextFabricStructureNeedExpectedKind, PriorConsultedSuppressedRevoked})
}

func TestConsultPriorStructureOffers_HandleOffered_WithGrammarChecker(t *testing.T) {
	t.Parallel()
	checker := HandleGrammarChecker(func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (string, bool) {
		return "pull_request.number", true
	})
	engine := mustReuseTestEngine(t, EngineDependencies{PriorHandleGrammarChecker: checker})
	entries := []StructurePriorEntry{
		{EntryID: "entry-h1", QuestionHash: "hash-1", Version: 1, Member: contractsv1.ContextFabricStructureNeedSubjectHandle, Kind: contractsv1.ContextFabricSubjectPullRequest, PatternID: "pr_number", Value: "532"},
	}
	material := StructureOfferMaterial{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectHandle}}

	got := engine.consultPriorStructureOffers(context.Background(), priorTestPrincipal(), entries, material)
	require.Len(t, got.HandleOptions, 1)
	require.Equal(t, "532", got.HandleOptions[0].Value)
	require.Equal(t, "pull_request.number", got.HandleOptions[0].SourceColumn)
	require.Equal(t, contractsv1.ContextFabricStructureOfferPrior, got.HandleOptions[0].OfferSource)
}

func TestConsultPriorStructureOffers_HandleNeverOffered_WithoutGrammarChecker(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{}) // no PriorHandleGrammarChecker
	entries := []StructurePriorEntry{
		{EntryID: "entry-h1", QuestionHash: "hash-1", Member: contractsv1.ContextFabricStructureNeedSubjectHandle, Kind: contractsv1.ContextFabricSubjectPullRequest, PatternID: "pr_number", Value: "532"},
	}
	material := StructureOfferMaterial{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectHandle}}

	got := engine.consultPriorStructureOffers(context.Background(), priorTestPrincipal(), entries, material)
	require.Empty(t, got.HandleOptions, "nil PriorHandleGrammarChecker must never let a handle offer through")
}

// --- resolveWindowPriorProposal: DP4(a) site two ---

func TestResolveWindowPriorProposal_ValidRelativeID(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{})
	entries := []StructurePriorEntry{
		{EntryID: "entry-w1", QuestionHash: "hash-1", Version: 2, Member: contractsv1.ContextFabricStructureNeedWindow, Value: string(RelativeWindowTrailing90D)},
	}

	got := engine.resolveWindowPriorProposal(context.Background(), priorTestPrincipal(), entries, requestWindowCanonicalization{})
	require.True(t, got.OK)
	require.Equal(t, RelativeWindowTrailing90D, got.RelativeID)
	require.Equal(t, "2", got.PriorVersionID)
	require.Equal(t, "entry-w1", got.PriorEntryID)
}

func TestResolveWindowPriorProposal_InvalidRelativeID_Skipped(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{})
	entries := []StructurePriorEntry{
		{EntryID: "entry-w1", QuestionHash: "hash-1", Member: contractsv1.ContextFabricStructureNeedWindow, Value: "not_a_real_window"},
	}
	got := engine.resolveWindowPriorProposal(context.Background(), priorTestPrincipal(), entries, requestWindowCanonicalization{})
	require.False(t, got.OK)
}

func TestResolveWindowPriorProposal_NoEntries(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{})
	got := engine.resolveWindowPriorProposal(context.Background(), priorTestPrincipal(), nil, requestWindowCanonicalization{})
	require.False(t, got.OK)
}

// TestResolveWindowPriorProposal_ConfirmedOrStatedWindow_NeverUsed proves
// the gate: a confirmed/stated window (precedence step 1) means the prior
// entries are never even consulted -- a caller's own confirmation can
// never be second-guessed by a historical aggregate.
func TestResolveWindowPriorProposal_ConfirmedOrStatedWindow_NeverUsed(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{})
	entries := []StructurePriorEntry{{QuestionHash: "hash-1", Member: contractsv1.ContextFabricStructureNeedWindow, Value: string(RelativeWindowTrailing90D)}}
	effective := &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D}
	canon := requestWindowCanonicalization{Effective: effective}

	got := engine.resolveWindowPriorProposal(context.Background(), priorTestPrincipal(), entries, canon)
	require.False(t, got.OK)
}

// TestResolveWindowPriorProposal_BinderRouted_NeverUsed proves the SAME
// gate for precedence step 2 (a guards-passing binder span).
func TestResolveWindowPriorProposal_BinderRouted_NeverUsed(t *testing.T) {
	t.Parallel()
	engine := mustReuseTestEngine(t, EngineDependencies{})
	entries := []StructurePriorEntry{{QuestionHash: "hash-1", Member: contractsv1.ContextFabricStructureNeedWindow, Value: string(RelativeWindowTrailing90D)}}
	canon := requestWindowCanonicalization{BinderProposal: WindowBindOutcome{Reason: WindowBindRoutedInferred, RelativeID: RelativeWindowTrailing30D}}

	got := engine.resolveWindowPriorProposal(context.Background(), priorTestPrincipal(), entries, canon)
	require.False(t, got.OK)
}

// --- composeEffectiveWindow: precedence ---

// TestComposeEffectiveWindow_PriorProposal_SubstitutesClassTableDefault pins
// the precedence composeEffectiveWindow itself enforces: a prior is used
// ONLY when the class table would otherwise have produced an inferred
// default (never to rescue a "refuse to guess" outcome).
func TestComposeEffectiveWindow_PriorProposal_SubstitutesClassTableDefault(t *testing.T) {
	t.Parallel()
	// Shape=discovered_cohort with no model window pick triggers
	// classifyFallback -> WindowClassTrendAssessment (chaos3900_window_class.go's
	// own fallbackClass), which DefaultRelativeID would otherwise turn into
	// windowDefaultPolicy's own TrendAssessment default (Trailing90D) --
	// exactly "the engine's own fallback would otherwise guess" this test
	// proves a prior can substitute into.
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, TimeContext: TimeContext{Axis: TemporalCurrent},
	}
	prior := windowPriorProposal{RelativeID: RelativeWindowTrailing30D, PriorVersionID: "1", PriorEntryID: "e1", OK: true}

	got := composeEffectiveWindow(interpretation, nil, WindowBindOutcome{}, prior, time.Now())
	require.NotNil(t, got)
	require.Equal(t, RelativeWindowTrailing30D, got.RelativeID)
	require.Equal(t, WindowInferredDefault, got.Provenance, "a prior-sourced default is STILL inferred_default provenance, never question_stated")
}

func TestComposeEffectiveWindow_BinderBeatsPrior(t *testing.T) {
	t.Parallel()
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, TimeContext: TimeContext{Axis: TemporalCurrent},
	}
	binder := WindowBindOutcome{Reason: WindowBindRoutedInferred, RelativeID: RelativeWindowTrailing90D}
	prior := windowPriorProposal{RelativeID: RelativeWindowTrailing30D, OK: true}

	got := composeEffectiveWindow(interpretation, nil, binder, prior, time.Now())
	require.NotNil(t, got)
	require.Equal(t, RelativeWindowTrailing90D, got.RelativeID, "the binder's own deterministic read of the question text must beat a prior")
}
