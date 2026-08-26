package contextfabric

import (
	"context"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4314 (chris "go" 2026-08-26): window_gated is flat at 65/212 cases
// across every two-turn replicate -- the window gate is a policy wall, not
// a ranking lever. This ticket turns the silent refusal into a human
// decision: when the class-default window gate refuses AND its own
// offers-only resolution (CHAOS-4234's gatedOfferMaterial) found a real,
// non-empty pool, compose a window_expand recommendation naming the class
// actually bound and the top pool candidate -- fail-closed, redeemed
// through the SAME winr_/PriorWindowReceipts/resolveWindowReceipts path an
// ordinary window confirmation already uses (no new grammar).

func chaos4314WindowOptionRegistry(now time.Time) []contractsv1.ContextFabricWindowOption {
	clarification := composeWindowClarification(&contractsv1.ContextFabricEffectiveEvidenceWindow{
		Provenance: WindowInferredDefault,
	}, "result_probe", now)
	return clarification.Options
}

func TestCHAOS4314_PickWindowExpandTarget(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	options := chaos4314WindowOptionRegistry(now)

	tests := []struct {
		name       string
		relativeID RelativeWindowID
		wantNil    bool
		wantTarget RelativeWindowID
	}{
		{name: "30d expands to 90d", relativeID: RelativeWindowTrailing30D, wantTarget: RelativeWindowTrailing90D},
		{name: "90d expands to 365d", relativeID: RelativeWindowTrailing90D, wantTarget: RelativeWindowTrailing365D},
		{name: "365d expands to all_time", relativeID: RelativeWindowTrailing365D, wantTarget: RelativeWindowAllTime},
		{name: "all_time has nothing wider", relativeID: RelativeWindowAllTime, wantNil: true},
		{name: "empty (explicit bound) defaults to all_time", relativeID: "", wantTarget: RelativeWindowAllTime},
		{name: "unrecognized relative id defaults to all_time", relativeID: "bogus", wantTarget: RelativeWindowAllTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective := contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: tt.relativeID, Provenance: WindowInferredDefault}
			got := pickWindowExpandTarget(effective, options)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("pickWindowExpandTarget(%q) = %#v, want nil", tt.relativeID, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("pickWindowExpandTarget(%q) = nil, want relative_id %q", tt.relativeID, tt.wantTarget)
			}
			if got.RelativeID != tt.wantTarget {
				t.Fatalf("pickWindowExpandTarget(%q).RelativeID = %q, want %q", tt.relativeID, got.RelativeID, tt.wantTarget)
			}
		})
	}
}

func TestCHAOS4314_PickWindowExpandTarget_TargetMissingFromOptionsReturnsNil(t *testing.T) {
	// A caller-supplied windowOptions list that does not carry the picked
	// tier (should never happen given composeWindowClarification's own
	// closed 4-tier registry, but pickWindowExpandTarget must degrade to
	// nil rather than fabricate an offer with no minted receipt).
	effective := contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowInferredDefault}
	got := pickWindowExpandTarget(effective, nil)
	if got != nil {
		t.Fatalf("pickWindowExpandTarget with no options = %#v, want nil", got)
	}
}

func TestCHAOS4314_ComposeWindowExpandOption_NilWhenPoolEmpty(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	options := chaos4314WindowOptionRegistry(now)
	effective := contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowInferredDefault}

	got := composeWindowExpandOption(StructureOfferMaterial{}, options, effective)
	if got != nil {
		t.Fatalf("composeWindowExpandOption with empty material = %#v, want nil: nothing to recommend against", got)
	}
}

func TestCHAOS4314_ComposeWindowExpandOption_NilWhenAlreadyAllTime(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	options := chaos4314WindowOptionRegistry(now)
	effective := contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime, Provenance: WindowInferredDefault}
	material := StructureOfferMaterial{
		Missing:     []StructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []KindOption{{Kind: SubjectPullRequest, Label: "a pull request", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
	}

	got := composeWindowExpandOption(material, options, effective)
	if got != nil {
		t.Fatalf("composeWindowExpandOption at all_time = %#v, want nil: nothing wider to recommend", got)
	}
}

func TestCHAOS4314_ComposeWindowExpandOption_ReusesTheExistingWindowOptionVerbatim(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	options := chaos4314WindowOptionRegistry(now)
	effective := contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, WindowClass: WindowClassRecentActivityLookup, Provenance: WindowInferredDefault}
	material := StructureOfferMaterial{
		Missing:          []StructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{{Kind: SubjectPullRequest, Label: "pull request #532", CanonicalID: "pr_532", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
	}

	got := composeWindowExpandOption(material, options, effective)
	if got == nil {
		t.Fatal("composeWindowExpandOption = nil, want a recommendation: the pool is non-empty and 90d is wider than 30d")
	}
	var want *contractsv1.ContextFabricWindowOption
	for i := range options {
		if options[i].RelativeID == RelativeWindowTrailing90D {
			want = &options[i]
		}
	}
	if want == nil {
		t.Fatal("test fixture bug: no trailing_90d option in the registry")
	}
	if got.ReceiptID != want.ReceiptID || got.OptionID != want.OptionID || got.Label != want.Label || got.RelativeID != want.RelativeID {
		t.Fatalf("composeWindowExpandOption = %#v, want it to copy window_options[trailing_90d] %#v verbatim on receipt_id/option_id/label/relative_id", got, want)
	}
	if got.WindowClass != WindowClassRecentActivityLookup {
		t.Fatalf("WindowClass = %q, want the currently-bound class %q", got.WindowClass, WindowClassRecentActivityLookup)
	}
	if got.CandidateLabel != "pull request #532" || got.CandidateKind != SubjectPullRequest {
		t.Fatalf("CandidateLabel/CandidateKind = %q/%q, want the candidate option's own label/kind", got.CandidateLabel, got.CandidateKind)
	}
}

func TestCHAOS4314_WindowExpandCandidateHint_PriorityOrder(t *testing.T) {
	candidate := contractsv1.ContextFabricCandidateOption{Kind: SubjectPullRequest, Label: "candidate wins", CanonicalID: "c1", OfferSource: contractsv1.ContextFabricStructureOfferEngine}
	anchor := contractsv1.ContextFabricAnchorOption{Kind: SubjectRepository, Label: "anchor wins", CanonicalID: "a1", MatchedTermHash: "aaaaaaaaaaaaaaaaaaaaaaaa", OfferSource: contractsv1.ContextFabricStructureOfferEngine}
	handle := HandleOption{Kind: SubjectPullRequest, Label: "handle wins", PatternID: "pull_request_number", Value: "1", SourceColumn: "number", OfferSource: contractsv1.ContextFabricStructureOfferEngine}
	kind := KindOption{Kind: SubjectWorkItem, Label: "kind wins", OfferSource: contractsv1.ContextFabricStructureOfferEngine}

	tests := []struct {
		name      string
		material  StructureOfferMaterial
		wantLabel string
		wantOK    bool
	}{
		{name: "candidate beats everything", material: StructureOfferMaterial{CandidateOptions: []contractsv1.ContextFabricCandidateOption{candidate}, AnchorOptions: []contractsv1.ContextFabricAnchorOption{anchor}, HandleOptions: []HandleOption{handle}, KindOptions: []KindOption{kind}}, wantLabel: "candidate wins", wantOK: true},
		{name: "anchor beats handle and kind", material: StructureOfferMaterial{AnchorOptions: []contractsv1.ContextFabricAnchorOption{anchor}, HandleOptions: []HandleOption{handle}, KindOptions: []KindOption{kind}}, wantLabel: "anchor wins", wantOK: true},
		{name: "handle beats kind", material: StructureOfferMaterial{HandleOptions: []HandleOption{handle}, KindOptions: []KindOption{kind}}, wantLabel: "handle wins", wantOK: true},
		{name: "kind alone", material: StructureOfferMaterial{KindOptions: []KindOption{kind}}, wantLabel: "kind wins", wantOK: true},
		{name: "nothing at all", material: StructureOfferMaterial{}, wantLabel: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, _, ok := windowExpandCandidateHint(tt.material)
			if ok != tt.wantOK || label != tt.wantLabel {
				t.Fatalf("windowExpandCandidateHint() = (%q, ok=%v), want (%q, ok=%v)", label, ok, tt.wantLabel, tt.wantOK)
			}
		})
	}
}

// TestCHAOS4314_Redemption_WindowExpandReceiptRecordsAccepted proves the
// fail-closed accepted signal: redeeming the EXACT SAME receipt a prior
// result's own WindowExpandOptions named fires RecordWindowExpandOfferRedeemed,
// while redeeming an ordinary WindowOption from the SAME result that was
// NOT recommended does not -- the two redemptions are otherwise
// byte-identical through resolveWindowReceipts.
func TestCHAOS4314_Redemption_WindowExpandReceiptRecordsAccepted(t *testing.T) {
	frozenStart90 := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	frozenStart30 := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	windowOptions := []WindowOption{
		{ReceiptID: "winr_ordinary30daaaaaaaa", OptionID: "opt_30d", Label: "the last 30 days", RelativeID: RelativeWindowTrailing30D, Start: &frozenStart30, End: &frozenEnd},
		{ReceiptID: "winr_recommended90daaaaa", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart90, End: &frozenEnd},
	}

	buildGated := func(resultID string) InvestigationResult {
		gated := validInvestigationResult()
		gated.ResultID = resultID
		gated.Status = InvestigationClarificationRequired
		gated.WindowClarification = &WindowClarification{Options: windowOptions}
		gated.StructureNeeds = &StructureNeeds{
			Missing:       []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
			WindowOptions: windowOptions,
			WindowExpandOptions: []contractsv1.ContextFabricWindowExpandOption{
				{ReceiptID: "winr_recommended90daaaaa", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, WindowClass: WindowClassRecentActivityLookup},
			},
		}
		return gated
	}

	t.Run("redeeming the recommended receipt records accepted", func(t *testing.T) {
		gated := buildGated("result_prior_4314_accept")
		store := &staticResultStore{results: map[string]InvestigationResult{gated.ResultID: gated}}
		interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
		graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
		telemetry := &recordingTelemetry{}
		engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

		request := validInvestigationRequest()
		request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: gated.ResultID, ReceiptID: "winr_recommended90daaaaa"}}

		result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if result.WindowClarification != nil {
			t.Fatalf("WindowClarification = %#v, want nil: the redeemed receipt confirmed the window", result.WindowClarification)
		}
		if telemetry.windowExpandOfferRedeemed != 1 {
			t.Fatalf("windowExpandOfferRedeemed = %d, want 1: this receipt was the result's own window_expand recommendation", telemetry.windowExpandOfferRedeemed)
		}
	})

	t.Run("redeeming an ordinary non-recommended receipt records nothing", func(t *testing.T) {
		gated := buildGated("result_prior_4314_ordinary")
		store := &staticResultStore{results: map[string]InvestigationResult{gated.ResultID: gated}}
		interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
		graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
		telemetry := &recordingTelemetry{}
		engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

		request := validInvestigationRequest()
		request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: gated.ResultID, ReceiptID: "winr_ordinary30daaaaaaaa"}}

		result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if result.WindowClarification != nil {
			t.Fatalf("WindowClarification = %#v, want nil: the redeemed receipt confirmed the window", result.WindowClarification)
		}
		if telemetry.windowExpandOfferRedeemed != 0 {
			t.Fatalf("windowExpandOfferRedeemed = %d, want 0: this receipt was never the result's own window_expand recommendation", telemetry.windowExpandOfferRedeemed)
		}
	})
}

// TestCHAOS4314_StructureNeedsValidate_WindowExpandReferentialIntegrity
// pins the validation shape ContextFabricStructureNeeds.Validate enforces
// for window_expand_options (internal/contracts/v1): unlike every other
// offer list, it is EXEMPT from cross-list receipt/option_id uniqueness
// (duplicating an existing window_options entry is the intended shape),
// but a fabricated receipt/option_id pair naming no real window_options
// entry must still fail.
func TestCHAOS4314_StructureNeedsValidate_WindowExpandReferentialIntegrity(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	windowOptions := chaos4314WindowOptionRegistry(now)
	var trailing90 contractsv1.ContextFabricWindowOption
	for _, opt := range windowOptions {
		if opt.RelativeID == RelativeWindowTrailing90D {
			trailing90 = opt
		}
	}
	if trailing90.ReceiptID == "" {
		t.Fatal("test fixture bug: no trailing_90d option in the registry")
	}

	valid := contractsv1.ContextFabricStructureNeeds{
		Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
		WindowOptions: windowOptions,
		WindowExpandOptions: []contractsv1.ContextFabricWindowExpandOption{
			{ReceiptID: trailing90.ReceiptID, OptionID: trailing90.OptionID, Label: trailing90.Label, RelativeID: trailing90.RelativeID, WindowClass: WindowClassRecentActivityLookup},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want a valid referential window_expand entry to pass (it deliberately duplicates window_options' own receipt/option id)", err)
	}

	fabricated := valid
	fabricated.WindowExpandOptions = []contractsv1.ContextFabricWindowExpandOption{
		{ReceiptID: "winr_doesnotexistaaaaaaaa", OptionID: "opt_fabricated", Label: "fabricated", RelativeID: RelativeWindowTrailing90D},
	}
	if err := fabricated.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: window_expand_options named no real window_options entry")
	}

	// mismatchedLabel (codex xhigh review, confirmed Medium finding): a
	// real receipt/option id pair, but a label that disagrees with the
	// window_options entry it names -- must still fail, since the type's
	// own doc comment promises Label is copied VERBATIM.
	mismatchedLabel := valid
	mismatchedLabel.WindowExpandOptions = []contractsv1.ContextFabricWindowExpandOption{
		{ReceiptID: trailing90.ReceiptID, OptionID: trailing90.OptionID, Label: "a different label", RelativeID: trailing90.RelativeID},
	}
	if err := mismatchedLabel.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: label diverges from the named window_options entry")
	}

	// mismatchedRelativeID (codex xhigh review round 2, confirmed Medium
	// finding): the SAME real receipt/option/label, but a DIFFERENT
	// (itself valid) RelativeID than the window_options entry those ids
	// name -- must still fail. Mutating clause-by-clause (AGENTS.md's own
	// rule): the mismatchedLabel case above proves only the Label
	// comparison; without this case a regression dropping the RelativeID
	// clause from windowOptionMatches could pass every existing test.
	mismatchedRelativeID := valid
	mismatchedRelativeID.WindowExpandOptions = []contractsv1.ContextFabricWindowExpandOption{
		{ReceiptID: trailing90.ReceiptID, OptionID: trailing90.OptionID, Label: trailing90.Label, RelativeID: RelativeWindowTrailing365D},
	}
	if err := mismatchedRelativeID.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: relative_id diverges from the named window_options entry")
	}

	tooMany := valid
	tooMany.WindowExpandOptions = append(tooMany.WindowExpandOptions, tooMany.WindowExpandOptions[0])
	if err := tooMany.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: at most one window_expand recommendation")
	}
}

func TestCHAOS4314_EngineTelemetryInterface_RecordingTelemetrySatisfiesIt(t *testing.T) {
	var _ EngineTelemetry = (*recordingTelemetry)(nil)
	var _ EngineTelemetry = SlogEngineTelemetry{}
}

func TestCHAOS4314_WindowGateOfferDisclosure_RecordedOnBothWindowGateOrigins(t *testing.T) {
	// Sanity: RecordWindowGateOfferDisclosure fires exactly once per
	// window-gated terminal, matching RecordWindowCanonicalization's own
	// call-count discipline right beside it.
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
	graph := chaos4234GatedGraph()
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, store, telemetry)

	_, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !reflect.DeepEqual(telemetry.windowGateOfferDisclosures, []bool{true}) {
		t.Fatalf("windowGateOfferDisclosures = %#v, want exactly one true call", telemetry.windowGateOfferDisclosures)
	}
	if got, want := len(telemetry.windowCanonicalizationOutcomes), len(telemetry.windowGateOfferDisclosures); got != want {
		t.Fatalf("RecordWindowCanonicalization fired %d times, RecordWindowGateOfferDisclosure fired %d times, want equal (both are unconditional per window-gated terminal)", got, want)
	}
}

// TestCHAOS4314_RedeemedWindowExpandReceipt_DecisionByteIdenticalToPlainWindowOption
// is the team-lead-requested proof (2026-08-26 design approval) that
// resolveWindowReceipts never special-cases a window_expand-flagged
// receipt: redeeming the SAME receipt through the SAME PriorWindowReceipts
// path must produce a BYTE-IDENTICAL decision (EffectiveEvidenceWindow,
// ConfirmedStructure) whether or not the prior result's own StructureNeeds
// happened to also carry it in WindowExpandOptions. The only observable
// difference is the additional RecordWindowExpandOfferRedeemed telemetry
// call (proven separately by TestCHAOS4314_Redemption_WindowExpandReceiptRecordsAccepted).
func TestCHAOS4314_RedeemedWindowExpandReceipt_DecisionByteIdenticalToPlainWindowOption(t *testing.T) {
	frozenStart := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	windowOptions := []WindowOption{
		{ReceiptID: "winr_sharedreceiptaaaaaaa", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}

	runRedemption := func(t *testing.T, resultID string, flagAsWindowExpand bool) InvestigationResult {
		t.Helper()
		gated := validInvestigationResult()
		gated.ResultID = resultID
		gated.Status = InvestigationClarificationRequired
		gated.WindowClarification = &WindowClarification{Options: windowOptions}
		needs := &StructureNeeds{Missing: []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}, WindowOptions: windowOptions}
		if flagAsWindowExpand {
			needs.WindowExpandOptions = []contractsv1.ContextFabricWindowExpandOption{
				{ReceiptID: "winr_sharedreceiptaaaaaaa", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, WindowClass: WindowClassRecentActivityLookup},
			}
		}
		gated.StructureNeeds = needs
		store := &staticResultStore{results: map[string]InvestigationResult{resultID: gated}}
		interpreter := &countingInterpreter{interpretation: bootstrapInterpretation()}
		graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
		engine := buildWindowGateEngine(t, interpreter, graph, store)

		request := validInvestigationRequest()
		request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: resultID, ReceiptID: "winr_sharedreceiptaaaaaaa"}}
		result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return result
	}

	// Deliberately the SAME resultID for both calls (each uses its own
	// isolated store) -- PriorResultID rides into ConfirmedStructure, so a
	// differing id would produce a superficial divergence unrelated to
	// whether resolveWindowReceipts special-cases the window_expand flag.
	const sharedResultID = "result_prior_4314_shared"
	plain := runRedemption(t, sharedResultID, false)
	flagged := runRedemption(t, sharedResultID, true)

	if !reflect.DeepEqual(plain.EffectiveEvidenceWindow, flagged.EffectiveEvidenceWindow) {
		t.Fatalf("EffectiveEvidenceWindow diverged: plain=%#v flagged=%#v, want byte-identical", plain.EffectiveEvidenceWindow, flagged.EffectiveEvidenceWindow)
	}
	if !reflect.DeepEqual(plain.ConfirmedStructure, flagged.ConfirmedStructure) {
		t.Fatalf("ConfirmedStructure diverged: plain=%#v flagged=%#v, want byte-identical", plain.ConfirmedStructure, flagged.ConfirmedStructure)
	}
	if plain.Status != flagged.Status {
		t.Fatalf("Status diverged: plain=%q flagged=%q, want identical", plain.Status, flagged.Status)
	}
}
