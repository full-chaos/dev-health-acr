package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS3900_OrderingPin_ConfirmedWindowNeverHitsPreConfirmationCacheRow
// is the W1 acceptance criterion's own named pin test: "a confirmed-window
// request can never hit a pre-confirmation cache row."
//
// Setup: a prior turn's WindowClarification offers a winr_ receipt for
// trailing_90d. A SEPARATE row is already cached under the plain "current"
// TimeAxisKey -- exactly what an earlier, unconfirmed (or windowless)
// question would have saved under. This test proves that a request
// REDEEMING the winr_ receipt is looked up under a DIFFERENT key
// ("current+w:rel:trailing_90d"), so the reuse gate's own "current" row can
// never be served to it -- the receipt-resolution-BEFORE-reuse ordering
// (canonicalizeEvidenceWindow, called before tryReuse in Investigate) is
// what makes this true structurally, not by chance.
func TestCHAOS3900_OrderingPin_ConfirmedWindowNeverHitsPreConfirmationCacheRow(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_window_0001"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0001", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()
	preConfirmationCandidate := validInvestigationResult()
	preConfirmationCandidate.ResultID = "result_pre_confirmation_cached"
	preConfirmationCandidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}

	var gateKeys []string
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: store,
		ReuseGate: reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
			gateKeys = append(gateKeys, key.TimeAxisKey)
			if key.TimeAxisKey == "current" {
				// The PRE-CONFIRMATION cache row: what an earlier,
				// window-unconfirmed turn saved under the plain axis key.
				return preConfirmationCandidate, true, nil
			}
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(gateKeys) != 1 {
		t.Fatalf("reuse gate called %d times, want exactly 1", len(gateKeys))
	}
	if gateKeys[0] == "current" {
		t.Fatalf("reuse lookup key = %q: a confirmed-window request keyed IDENTICALLY to the pre-confirmation row -- the ordering invariant is broken", gateKeys[0])
	}
	if gateKeys[0] != "current+w:rel:trailing_90d" {
		t.Errorf("reuse lookup key = %q, want %q", gateKeys[0], "current+w:rel:trailing_90d")
	}
	if result.Reused {
		t.Fatalf("result.Reused = true: a confirmed-window request was served the pre-confirmation cache row %q", preConfirmationCandidate.ResultID)
	}
	if result.ResultID != freshTestResultID {
		t.Errorf("result.ResultID = %q, want the fresh result %q", result.ResultID, freshTestResultID)
	}
}

// TestCHAOS3900_UnresolvedWindowReceiptVetoesTheWholeRequest is the R2 veto
// pin: a request carrying a PriorWindowReceipts entry that cannot be
// resolved terminates as no_match, WITHOUT ever calling the reuse gate, the
// interpreter, or the synthesizer -- no reuse lookup, no inference
// substituted, exactly the design brief's own closed failure-branch rule.
func TestCHAOS3900_UnresolvedWindowReceiptVetoesTheWholeRequest(t *testing.T) {
	t.Parallel()

	store := &staticResultStore{results: map[string]InvestigationResult{}} // named prior result does not exist
	telemetry := &recordingTelemetry{}

	engine := mustReuseTestEngine(t, EngineDependencies{
		Results:   store,
		Telemetry: telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a window-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "winr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q (design brief W1 scope: window veto is ALWAYS no_match)", result.Status, InvestigationNoMatch)
	}
	if result.Reused {
		t.Fatal("result.Reused = true on a window-veto terminal, want false")
	}
	found := false
	for _, outcome := range telemetry.windowCanonicalizationOutcomes {
		if outcome == WindowCanonicalizationVetoUnresolved {
			found = true
		}
	}
	if !found {
		t.Errorf("telemetry.windowCanonicalizationOutcomes = %v, want it to contain %q", telemetry.windowCanonicalizationOutcomes, WindowCanonicalizationVetoUnresolved)
	}
}

// TestCHAOS3900_PluralWindowReceiptsVetoAsConflict pins the OTHER veto
// reason: two or more PriorWindowReceipts entries are ambiguous by
// construction, never first-match-wins.
func TestCHAOS3900_PluralWindowReceiptsVetoAsConflict(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a window-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{
		{ResultID: "result_prior_a_00001", ReceiptID: "winr_confirm0001"},
		{ResultID: "result_prior_b_00001", ReceiptID: "winr_confirm0002"},
	}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
}

// TestRelativeWindowBounds pins the ONLY function that may derive absolute
// bounds for a RelativeWindowID: the width table, and RelativeWindowAllTime's
// own "no bounds" contract.
func TestRelativeWindowBounds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		id       RelativeWindowID
		wantOK   bool
		wantDays int
	}{
		{RelativeWindowTrailing30D, true, 30},
		{RelativeWindowTrailing90D, true, 90},
		{RelativeWindowTrailing365D, true, 365},
		{RelativeWindowAllTime, false, 0},
		{RelativeWindowID("bogus"), false, 0},
	}
	for _, tc := range cases {
		start, end, ok := relativeWindowBounds(tc.id, now)
		if ok != tc.wantOK {
			t.Fatalf("relativeWindowBounds(%q) ok = %v, want %v", tc.id, ok, tc.wantOK)
		}
		if !ok {
			continue
		}
		if !end.Equal(now) {
			t.Errorf("relativeWindowBounds(%q) end = %v, want now (%v)", tc.id, end, now)
		}
		if got := int(end.Sub(start).Hours() / 24); got != tc.wantDays {
			t.Errorf("relativeWindowBounds(%q) width = %d days, want %d", tc.id, got, tc.wantDays)
		}
	}
}

// TestWindowKeyComponentAndComposeTimeAxisKey pins the rel:/abs: namespace
// scheme (design brief §5.1) and composeTimeAxisKey's byte-identical
// fallthrough when no window is in play.
func TestWindowKeyComponentAndComposeTimeAxisKey(t *testing.T) {
	t.Parallel()

	relative := EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Provenance: WindowQuestionStated}
	if got, want := windowKeyComponent(relative), "rel:trailing_90d"; got != want {
		t.Errorf("windowKeyComponent(relative) = %q, want %q", got, want)
	}

	allTime := EffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime, Provenance: WindowQuestionStated}
	if got, want := windowKeyComponent(allTime), "rel:all_time"; got != want {
		t.Errorf("windowKeyComponent(all_time) = %q, want %q -- a confirmed all-time answer must key distinctly from no window at all", got, want)
	}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	absolute := EffectiveEvidenceWindow{Start: &start, End: &end, Provenance: WindowQuestionStated}
	want := "abs:" + formatUnixNano(start) + ":" + formatUnixNano(end)
	if got := windowKeyComponent(absolute); got != want {
		t.Errorf("windowKeyComponent(absolute) = %q, want %q", got, want)
	}

	// composeTimeAxisKey must leave the axis key BYTE-IDENTICAL when no
	// window is in play -- the ordinary case for every investigation before
	// CHAOS-3900 W1 and the overwhelming majority after it.
	if got := composeTimeAxisKey("current", ""); got != "current" {
		t.Errorf(`composeTimeAxisKey("current", "") = %q, want "current" unchanged`, got)
	}
	if got := composeTimeAxisKey("", "rel:trailing_90d"); got != "" {
		t.Errorf(`composeTimeAxisKey("", "rel:trailing_90d") = %q, want "" (fail-closed axis key stays fail-closed)`, got)
	}
	if got, want := composeTimeAxisKey("current", "rel:trailing_90d"), "current+w:rel:trailing_90d"; got != want {
		t.Errorf("composeTimeAxisKey(current, window) = %q, want %q", got, want)
	}
}

// TestWindowExplicitProvenance pins the pivot brief's DP12(b) surface split:
// tier is a function of SURFACE alone. MCP's bare explicit evidence_window
// enters inferred; every other surface keeps the 3900 §4 stated-echo grant.
func TestWindowExplicitProvenance(t *testing.T) {
	t.Parallel()
	if got := windowExplicitProvenance(ConsumerInfo{Surface: "mcp"}); got != WindowInferredDefault {
		t.Errorf("windowExplicitProvenance(mcp) = %q, want %q (DP12(b))", got, WindowInferredDefault)
	}
	for _, surface := range []string{"workbench", "web_assertion", ""} {
		if got := windowExplicitProvenance(ConsumerInfo{Surface: surface}); got != WindowQuestionStated {
			t.Errorf("windowExplicitProvenance(%q) = %q, want %q (3900 §4 stated-echo, untouched by DP12(b))", surface, got, WindowQuestionStated)
		}
	}
}

// TestComposeEffectiveWindow_NonCurrentAxisNeverCarriesAWindow pins that a
// window is representable ONLY on the current axis -- an interpreted
// historical axis must never carry an inferred window disclosure, even when
// precedence step 1 somehow resolved one (defense in depth: the wire
// contract already refuses this shape, but composeEffectiveWindow must not
// rely on that alone).
func TestComposeEffectiveWindow_NonCurrentAxisNeverCarriesAWindow(t *testing.T) {
	t.Parallel()
	historical := InterpretedQuestion{Shape: ShapeSingleSubject, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: timePtr(time.Now())}}
	got := composeEffectiveWindow(historical, nil, WindowBindOutcome{}, time.Now())
	if got != nil {
		t.Errorf("composeEffectiveWindow(historical axis) = %+v, want nil", got)
	}
}

// TestComposeEffectiveWindow_BinderProposalOverridesClassTableDefault pins
// design brief §1.2: a guards-passing binder span's RelativeID overrides the
// class table's own pick, but the provenance stays inferred_default either
// way -- a binder proposal never mints question_stated authority.
func TestComposeEffectiveWindow_BinderProposalOverridesClassTableDefault(t *testing.T) {
	t.Parallel()
	interpreted := InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "status",
		TimeContext: TimeContext{Axis: TemporalCurrent},
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	binder := WindowBindOutcome{Reason: WindowBindRoutedInferred, RelativeID: RelativeWindowTrailing365D, SpansBound: 1}

	got := composeEffectiveWindow(interpreted, nil, binder, now)
	if got == nil {
		t.Fatal("composeEffectiveWindow = nil, want an inferred window")
	}
	if got.Provenance != WindowInferredDefault {
		t.Errorf("Provenance = %q, want %q -- a binder proposal never mints question_stated", got.Provenance, WindowInferredDefault)
	}
	if got.RelativeID != RelativeWindowTrailing365D {
		t.Errorf("RelativeID = %q, want the binder's own proposal %q (overriding the class table's own recent_activity_lookup->trailing_30d pick)", got.RelativeID, RelativeWindowTrailing365D)
	}
}

// TestComposeEffectiveWindow_RequestWindowNeverOverridden pins precedence
// step 1's own priority: a confirmed/stated window resolved before Interpret
// is returned UNCHANGED, never re-decided by the class table or the binder.
func TestComposeEffectiveWindow_RequestWindowNeverOverridden(t *testing.T) {
	t.Parallel()
	requestWindow := &EffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime, Provenance: WindowClarificationConfirmed}
	interpreted := InterpretedQuestion{Shape: ShapeSingleSubject, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}
	binder := WindowBindOutcome{Reason: WindowBindRoutedInferred, RelativeID: RelativeWindowTrailing30D, SpansBound: 1}

	got := composeEffectiveWindow(interpreted, requestWindow, binder, time.Now())
	if got != requestWindow {
		t.Errorf("composeEffectiveWindow did not return the request-side window unchanged: got %+v, want %+v", got, requestWindow)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// TestCHAOS3900_AxisConflict_InterpreterFlipVetoesInsteadOfSilentlyDropping
// is the codex review (W1 round 1) HIGH-severity fix: a confirmed window
// resolved against the REQUEST's own current axis must not silently vanish
// (composeEffectiveWindow's own interpreted-axis gate) when Interpret goes
// on to move the axis away from current -- it must veto as
// window_axis_conflict, BEFORE ResolveSubjects/DiscoverContext/ReadFacts/
// Synthesize ever run (the default mustReuseTestEngine stubs for all four
// fail the test if reached, proving the short-circuit).
func TestCHAOS3900_AxisConflict_InterpreterFlipVetoesInsteadOfSilentlyDropping(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_window_0002"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0002", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	// mustReuseTestEngine's fixed clock is time.Unix(200, 0) -- asOf must
	// stay at or before it, or resolveTimeContext's future-skew check
	// rejects it before this test's own window-conflict check is reached.
	asOf := time.Unix(100, 0).UTC()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return InvestigationResult{}, false, nil // miss: proceed to Interpret
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			// The model reads "as of last spring" out of the question and
			// moves the axis to historical, despite the wire request's own
			// axis=current.
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &asOf}}, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0002"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q (window_axis_conflict is always no_match)", result.Status, InvestigationNoMatch)
	}
	if result.EffectiveEvidenceWindow != nil {
		t.Errorf("result.EffectiveEvidenceWindow = %+v, want nil on a window_axis_conflict veto", result.EffectiveEvidenceWindow)
	}
	// The veto result must report the REAL interpretation (historical axis),
	// not a placeholder -- see windowVetoResult's own interpretation
	// parameter doc comment.
	if result.Interpretation.TimeContext.Axis != TemporalValidTime {
		t.Errorf("result.Interpretation.TimeContext.Axis = %q, want %q (the real, post-Interpret axis)", result.Interpretation.TimeContext.Axis, TemporalValidTime)
	}
}

// TestCHAOS3900_ReceiptAndExplicitBoundsDisagreement_VetoesAsConflict is the
// codex review (W1 round 1) HIGH-severity windowsAgree fix: the wire
// contract legally admits a RequestedEvidenceWindow carrying a RelativeID
// TOGETHER WITH explicit bounds, so a caller could send a RelativeID that
// matches the receipt while ALSO sending explicit bounds that contradict
// it -- that must veto as a conflict, not silently pass because the
// RelativeID half agreed.
func TestCHAOS3900_ReceiptAndExplicitBoundsDisagreement_VetoesAsConflict(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_window_0003"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0003", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a window-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0003"}}
	// Same RelativeID as the receipt (trailing_90d), but explicit bounds
	// that flatly disagree with the receipt's own frozen Start/End -- a
	// contradiction windowsAgree must catch even though the RelativeID
	// half matches.
	disagreeingStart := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	disagreeingEnd := time.Date(2020, 2, 1, 0, 0, 0, 0, time.UTC)
	request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Start: &disagreeingStart, End: &disagreeingEnd,
	}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
}

// TestCHAOS3900_NonCurrentAxisWithWindowReceipt_Vetoes is the codex review
// (W1 round 1) HIGH-severity fix: a window receipt can never apply outside
// the current axis, but the request contract does not forbid sending one
// alongside a non-current TimeContext -- it must veto (never be silently
// ignored, never reach the reuse gate or Interpret).
func TestCHAOS3900_NonCurrentAxisWithWindowReceipt_Vetoes(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a window-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	// mustReuseTestEngine's fixed clock is time.Unix(200, 0) -- see the
	// matching comment in TestCHAOS3900_AxisConflict_... above.
	asOf := time.Unix(100, 0).UTC()
	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_prior_window_0004", ReceiptID: "winr_confirm0004"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
}

// TestCHAOS3900_MCPExplicitWindow_OmitsReuseKeyFragment is the codex review
// (W1 round 1) MEDIUM-severity fix: an MCP bare explicit evidence_window is
// tier=inferred_default under DP12(b), so it must NOT also contribute a
// rel:/abs: reuse-key fragment -- that dimension is reserved for a DECISIVE
// (question_stated/clarification_confirmed) window; an inferred-tier one
// relies on WindowInferenceVersion instead.
func TestCHAOS3900_MCPExplicitWindow_OmitsReuseKeyFragment(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}})

	request := validInvestigationRequest()
	request.Consumer = ConsumerInfo{Name: "acr-mcp", Version: "0.1.0", Surface: "mcp"}
	request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}

	canon := engine.canonicalizeEvidenceWindow(context.Background(), reusePrincipal(), request)
	if canon.Veto != windowVetoNone {
		t.Fatalf("canonicalizeEvidenceWindow veto = %q, want none", canon.Veto)
	}
	if canon.Effective == nil {
		t.Fatal("canonicalizeEvidenceWindow.Effective = nil, want a resolved inferred-tier window")
	}
	if canon.Effective.Provenance != WindowInferredDefault {
		t.Errorf("Provenance = %q, want %q (DP12(b): MCP bare explicit window is never decisive)", canon.Effective.Provenance, WindowInferredDefault)
	}
	if canon.KeyComponent != "" {
		t.Errorf("KeyComponent = %q, want \"\" -- an inferred-tier window must not contribute a rel:/abs: reuse-key fragment", canon.KeyComponent)
	}
}

// TestCHAOS3900_NonMCPExplicitWindow_StillContributesReuseKeyFragment is the
// companion positive case: a decisive (non-MCP, question_stated) explicit
// window DOES contribute its rel:/abs: fragment, exactly as before -- the
// MCP fix must not regress the ordinary web/workbench path.
func TestCHAOS3900_NonMCPExplicitWindow_StillContributesReuseKeyFragment(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}})

	request := validInvestigationRequest()
	request.Consumer = ConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"}
	request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}

	canon := engine.canonicalizeEvidenceWindow(context.Background(), reusePrincipal(), request)
	if canon.Veto != windowVetoNone {
		t.Fatalf("canonicalizeEvidenceWindow veto = %q, want none", canon.Veto)
	}
	if canon.Effective == nil || canon.Effective.Provenance != WindowQuestionStated {
		t.Fatalf("Effective = %+v, want a question_stated window", canon.Effective)
	}
	if want := "rel:trailing_90d"; canon.KeyComponent != want {
		t.Errorf("KeyComponent = %q, want %q", canon.KeyComponent, want)
	}
}

// TestCHAOS3900_WindowKeyedReuseRejectsCandidateWithDisagreeingAxis is the
// codex review (W1 round 2) fix: tryReuse's own defense-in-depth recheck
// (answer_reuse.go, right after FindReusable) must reject -- as an ordinary
// miss, never an error -- ANY candidate a window-keyed lookup returns whose
// own stored Interpretation.TimeContext.Axis is not current, or whose own
// EffectiveEvidenceWindow is nil. This closes the gap independently of
// window.go's own write-path guarantees: a candidate could disagree with
// the window-fragment key for a reason this package's own write path never
// produces (a differently-ruled binary, a direct write, an earlier-deployed
// version of this code).
func TestCHAOS3900_WindowKeyedReuseRejectsCandidateWithDisagreeingAxis(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()

	asOf := time.Unix(50, 0).UTC() // mustReuseTestEngine's fixed clock is Unix(200,0)
	inconsistentCandidate := validInvestigationResult()
	inconsistentCandidate.ResultID = "result_inconsistent_cached"
	inconsistentCandidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}
	// A candidate that disagrees with what a window-keyed lookup's own
	// fragment claims: its OWN stored interpretation is historical, not
	// current -- exactly the shape a pre-fix write path could have
	// produced, or any other source this package's own write path did not
	// itself vouch for.
	inconsistentCandidate.Interpretation.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}
	inconsistentCandidate.EffectiveEvidenceWindow = nil

	var gateKeys []string
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
			gateKeys = append(gateKeys, key.TimeAxisKey)
			// Always "finds" the inconsistent candidate, regardless of the
			// key -- proving the REJECTION comes from tryReuse's own
			// recheck, not from the gate declining to match.
			return inconsistentCandidate, true, nil
		}),
	})

	request := validInvestigationRequest()
	request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(gateKeys) != 1 || gateKeys[0] != "current+w:rel:trailing_90d" {
		t.Fatalf("reuse gate keys = %v, want exactly one call keyed \"current+w:rel:trailing_90d\"", gateKeys)
	}
	if result.Reused {
		t.Fatal("result.Reused = true: a window-keyed lookup served a candidate whose own stored axis/window disagreed with the key")
	}
	if result.ResultID != freshTestResultID {
		t.Errorf("result.ResultID = %q, want the fresh result %q", result.ResultID, freshTestResultID)
	}
}
