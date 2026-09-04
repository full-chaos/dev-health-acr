package contextfabric

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS3900_OrderingPin_ConfirmedWindowNeverHitsPreConfirmationCacheRow
// is the W1 acceptance criterion's own named pin test: "a confirmed-window
// request can never hit a pre-confirmation cache row."
//
// Setup: a prior turn's WindowClarification offers a winr_ receipt for
// trailing_90d. A SEPARATE row is already cached under the plain "current"
// TimeAxisKey -- exactly what an earlier, unconfirmed (or windowless)
// question would have saved under. A request REDEEMING the winr_ receipt
// must never be served that row.
//
// SUPERSEDED MECHANISM, STRONGER GUARANTEE (CHAOS-4998). W1 held this by
// KEYING: the redeeming request was looked up under its own FROZEN bounds
// rather than under "current", so the pre-confirmation row could not match.
// This test therefore used to assert on the key the reuse gate was consulted
// WITH, which required the gate to be consulted at all. It no longer is: a
// request naming a prior result through a window receipt now bypasses the
// reuse lookup entirely (reuseBypassReason, answer_reuse.go), because
// redeeming that receipt also makes the request eligible for a
// same-conversation carry whose value does not exist yet at lookup time.
//
// The acceptance criterion is held MORE strongly than before -- the request
// cannot hit the pre-confirmation row, or any other row -- so this test now
// asserts the gate is never consulted, which is the stronger statement. What
// it can no longer observe is the frozen-key DERIVATION, and that assertion
// is not dropped: it moves to
// TestCHAOS3900_ConfirmedWindowKeysOnItsOwnFrozenBounds below, which pins the
// same expected key string at canonicalizeEvidenceWindow, where the
// derivation actually lives and where it is still reachable. That derivation
// still matters after this ticket because windowCanon.KeyComponent is what
// SAVE keys on, even though no lookup consults it for a receipt-redeeming
// request any more.
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
	if len(gateKeys) != 0 {
		t.Fatalf("reuse gate consulted with keys %q, want none: a request redeeming a window receipt names a prior result the same-conversation carries can walk, so it must not reach the reuse lookup at all", gateKeys)
	}
	if result.Reused {
		t.Fatalf("result.Reused = true: a confirmed-window request was served the pre-confirmation cache row %q", preConfirmationCandidate.ResultID)
	}
	if result.ResultID != freshTestResultID {
		t.Errorf("result.ResultID = %q, want the fresh result %q", result.ResultID, freshTestResultID)
	}
}

// TestCHAOS3900_ConfirmedWindowKeysOnItsOwnFrozenBounds is the re-homed half
// of the ordering pin above: the frozen-key DERIVATION, asserted where it
// lives rather than where it used to be observable.
//
// A receipt confirms one specific already-minted option, so the key must be
// that option's own bounds (abs:) and never a re-derivable rel:trailing_90d
// -- two different frozen intervals can share one RelativeID, and keying on
// the id would collide them. windowKeyComponent's own encoding is unit-tested
// elsewhere in this file; what is pinned HERE is the engine wiring that gets
// a redeemed receipt's bounds into KeyComponent under the FROZEN encoding.
//
// This is still load-bearing after CHAOS-4998 even though no reuse LOOKUP
// consults it for such a request any more: Save keys on the same
// KeyComponent, so a wrong derivation here would store the answer under a
// key no later turn could find.
func TestCHAOS3900_ConfirmedWindowKeysOnItsOwnFrozenBounds(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_window_0001"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0001", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: store,
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0001"}}

	canon := engine.canonicalizeEvidenceWindow(context.Background(), reusePrincipal(), request)
	if canon.Veto != "" {
		t.Fatalf("canonicalizeEvidenceWindow veto = %q, want none -- the fixture must actually redeem the receipt for this pin to prove anything", canon.Veto)
	}
	if canon.Effective == nil {
		t.Fatal("canonicalizeEvidenceWindow.Effective = nil, want the receipt's confirmed window")
	}
	// The "w:" namespace prefix is composeTimeAxisKey's, not
	// windowKeyComponent's -- asserted on both halves below so a change that
	// moved the prefix between them could not pass by cancelling out.
	wantComponent := "abs:" + formatUnixNano(frozenStart) + ":" + formatUnixNano(frozenEnd)
	if canon.KeyComponent != wantComponent {
		t.Errorf("KeyComponent = %q, want %q (the receipt's own FROZEN bounds, not a re-derivable rel:trailing_90d)", canon.KeyComponent, wantComponent)
	}
	if got, want := composeTimeAxisKey("current", canon.KeyComponent), "current+w:"+wantComponent; got != want {
		t.Errorf("composeTimeAxisKey = %q, want %q -- the end-to-end key string the superseded ordering pin used to assert at the reuse gate", got, want)
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
	if got, want := windowKeyComponent(relative, windowKeyRederivable), "rel:trailing_90d"; got != want {
		t.Errorf("windowKeyComponent(relative, rederivable) = %q, want %q", got, want)
	}

	allTime := EffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime, Provenance: WindowQuestionStated}
	if got, want := windowKeyComponent(allTime, windowKeyRederivable), "rel:all_time"; got != want {
		t.Errorf("windowKeyComponent(all_time, rederivable) = %q, want %q -- a confirmed all-time answer must key distinctly from no window at all", got, want)
	}
	if got, want := windowKeyComponent(allTime, windowKeyFrozen), "rel:all_time"; got != want {
		t.Errorf("windowKeyComponent(all_time, frozen) = %q, want %q -- all_time is unambiguous regardless of encoding", got, want)
	}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	absolute := EffectiveEvidenceWindow{Start: &start, End: &end, Provenance: WindowQuestionStated}
	want := "abs:" + formatUnixNano(start) + ":" + formatUnixNano(end)
	if got := windowKeyComponent(absolute, windowKeyRederivable); got != want {
		t.Errorf("windowKeyComponent(absolute, rederivable) = %q, want %q -- no RelativeID to abbreviate by, so bounds are used regardless of encoding", got, want)
	}

	frozen := EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Start: &start, End: &end, Provenance: WindowClarificationConfirmed}
	if got := windowKeyComponent(frozen, windowKeyFrozen); got != want {
		t.Errorf("windowKeyComponent(frozen) = %q, want %q -- a frozen window keys on its own bounds, never RelativeID alone", got, want)
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

// TestWindowKeyComponent_InjectiveWithinEachEncoding is the codex review
// (W1 round 5) consolidation's own property test, per the team lead's
// ruling: ONE canonical derivation, proven injective within the
// equivalence each windowKeyEncoding defines.
//
//   - windowKeyFrozen (and the bare-absolute, no-RelativeID case of
//     windowKeyRederivable): injective over BOUNDS -- identical bounds
//     produce identical fragments regardless of RelativeID or Provenance;
//     distinct bounds ALWAYS produce distinct fragments, cross-source (an
//     explicit absolute request and a receipt confirming the identical
//     interval key identically -- correctly, since they describe the same
//     evidence).
//   - windowKeyRederivable WITH a RelativeID: injective over RelativeID --
//     distinct RelativeIDs always produce distinct fragments; the SAME
//     RelativeID collapses regardless of its (ephemeral, now()-derived)
//     bounds. This is INTENTIONAL, not a residual gap: see
//     windowKeyRederivable's own doc comment for why a re-derivable
//     window's reuse identity is its RelativeID, staleness-protected, not
//     its instantaneous computed bounds -- the same "as of whenever you
//     answer this" precedent reuseCurrentAxisKey already established for
//     the plain current axis.
//   - Different encodings of a window that DOES carry a RelativeID are
//     deliberately NOT compared against each other here: windowKeyFrozen
//     and windowKeyRederivable answer different questions ("what exact
//     interval" vs "which standing relative window") by design, and every
//     real call site passes a FIXED encoding for its own path -- there is
//     no call site that could compare one against the other.
func TestWindowKeyComponent_InjectiveWithinEachEncoding(t *testing.T) {
	t.Parallel()

	marchStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	marchEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	juneStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	juneEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	t.Run("frozen: identical bounds match regardless of RelativeID or Provenance", func(t *testing.T) {
		a := EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Start: &marchStart, End: &marchEnd, Provenance: WindowClarificationConfirmed}
		b := EffectiveEvidenceWindow{Start: &marchStart, End: &marchEnd, Provenance: WindowQuestionStated} // no RelativeID at all, different Provenance
		if windowKeyComponent(a, windowKeyFrozen) != windowKeyComponent(b, windowKeyRederivable) {
			t.Fatalf("identical bounds (%v, %v) keyed differently across a RelativeID-bearing frozen window and a bare absolute window: %q vs %q",
				marchStart, marchEnd, windowKeyComponent(a, windowKeyFrozen), windowKeyComponent(b, windowKeyRederivable))
		}
	})

	t.Run("frozen: distinct bounds never match, even with the SAME RelativeID", func(t *testing.T) {
		a := EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Start: &marchStart, End: &marchEnd, Provenance: WindowClarificationConfirmed}
		b := EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Start: &juneStart, End: &juneEnd, Provenance: WindowClarificationConfirmed}
		if windowKeyComponent(a, windowKeyFrozen) == windowKeyComponent(b, windowKeyFrozen) {
			t.Fatalf("two DIFFERENT frozen intervals sharing one RelativeID keyed IDENTICALLY: %q", windowKeyComponent(a, windowKeyFrozen))
		}
	})

	t.Run("rederivable: same RelativeID matches regardless of its (ephemeral) bounds -- intentional", func(t *testing.T) {
		a := EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Start: &marchStart, End: &marchEnd, Provenance: WindowQuestionStated}
		b := EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Start: &juneStart, End: &juneEnd, Provenance: WindowQuestionStated}
		if windowKeyComponent(a, windowKeyRederivable) != windowKeyComponent(b, windowKeyRederivable) {
			t.Fatalf("two rederivable windows with the SAME RelativeID but different computed-at-different-times bounds keyed DIFFERENTLY (%q vs %q) -- this defeats relative-window reuse across time",
				windowKeyComponent(a, windowKeyRederivable), windowKeyComponent(b, windowKeyRederivable))
		}
	})

	t.Run("rederivable: distinct RelativeIDs never match", func(t *testing.T) {
		a := EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Start: &marchStart, End: &marchEnd, Provenance: WindowQuestionStated}
		b := EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Start: &marchStart, End: &marchEnd, Provenance: WindowQuestionStated}
		if windowKeyComponent(a, windowKeyRederivable) == windowKeyComponent(b, windowKeyRederivable) {
			t.Fatalf("trailing_30d and trailing_90d keyed IDENTICALLY: %q", windowKeyComponent(a, windowKeyRederivable))
		}
	})

	t.Run("all_time is unambiguous under either encoding, regardless of Provenance", func(t *testing.T) {
		confirmed := EffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime, Provenance: WindowClarificationConfirmed}
		stated := EffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime, Provenance: WindowQuestionStated}
		if windowKeyComponent(confirmed, windowKeyFrozen) != windowKeyComponent(stated, windowKeyRederivable) {
			t.Fatalf("all_time keyed differently across encodings: %q vs %q", windowKeyComponent(confirmed, windowKeyFrozen), windowKeyComponent(stated, windowKeyRederivable))
		}
	})
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
	got := composeEffectiveWindow(historical, nil, WindowBindOutcome{}, windowPriorProposal{}, time.Now())
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

	got := composeEffectiveWindow(interpreted, nil, binder, windowPriorProposal{}, now)
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

	got := composeEffectiveWindow(interpreted, requestWindow, binder, windowPriorProposal{}, time.Now())
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

// TestCHAOS3900_MCPExplicitWindow_StillContributesReuseKeyFragment is the
// codex review (W1 round 3) fix, correcting round 1's own fix #5: an MCP
// bare explicit evidence_window is tier=inferred_default under DP12(b) (no
// DECISIVE authority), but it is still a CALLER-SUPPLIED, arbitrary value
// (independent of the question text) and so still MUST contribute its own
// rel:/abs: reuse-key fragment -- omitting it (round 1's mistake) let two
// MCP requests for the identical question but DIFFERENT explicit windows
// (e.g. trailing_30d vs trailing_90d) collapse onto the same "current" key.
// The tier distinction governs decisive AUTHORITY only (§3/W4, not
// implemented in W1), never reuse-key VALUE IDENTITY -- see
// canonicalizeEvidenceWindow's own call site comment for the full
// reasoning.
func TestCHAOS3900_MCPExplicitWindow_StillContributesReuseKeyFragment(t *testing.T) {
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
	if want := "rel:trailing_90d"; canon.KeyComponent != want {
		t.Errorf("KeyComponent = %q, want %q -- a caller-supplied window value must key by its own identity regardless of decisive tier", canon.KeyComponent, want)
	}
}

// TestCHAOS3900_MCPExplicitWindow_DifferentValuesKeyDifferently is the
// direct regression proof for the bug round 1's fix #5 introduced: two MCP
// requests naming DIFFERENT explicit windows must never share a reuse key.
func TestCHAOS3900_MCPExplicitWindow_DifferentValuesKeyDifferently(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}})
	mcp := ConsumerInfo{Name: "acr-mcp", Version: "0.1.0", Surface: "mcp"}

	request30 := validInvestigationRequest()
	request30.Consumer = mcp
	request30.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}
	canon30 := engine.canonicalizeEvidenceWindow(context.Background(), reusePrincipal(), request30)

	request90 := validInvestigationRequest()
	request90.Consumer = mcp
	request90.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}
	canon90 := engine.canonicalizeEvidenceWindow(context.Background(), reusePrincipal(), request90)

	if canon30.KeyComponent == "" || canon90.KeyComponent == "" {
		t.Fatalf("KeyComponent = (%q, %q), want both non-empty", canon30.KeyComponent, canon90.KeyComponent)
	}
	if canon30.KeyComponent == canon90.KeyComponent {
		t.Fatalf("trailing_30d and trailing_90d MCP requests keyed IDENTICALLY (%q): a caller asking for one window could be served the other's cached answer", canon30.KeyComponent)
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

// TestCHAOS3900_WindowKeyedReuseRejectsCandidateWithMismatchedWindowValue is
// the codex review (W1 round 3) strengthening of the round-2 recheck: a
// candidate carrying a NON-NIL, current-axis EffectiveEvidenceWindow still
// must be rejected when its OWN value (trailing_30d) does not match what
// the lookup's own key (rel:trailing_90d) named -- round 2's check alone
// (non-nil-ness only) would have let this one through.
func TestCHAOS3900_WindowKeyedReuseRejectsCandidateWithMismatchedWindowValue(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()

	mismatchedCandidate := validInvestigationResult()
	mismatchedCandidate.ResultID = "result_mismatched_cached"
	mismatchedCandidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}
	mismatchedCandidate.Interpretation.TimeContext = TimeContext{Axis: TemporalCurrent}
	// Valid, non-nil, current-axis window -- but the WRONG one: the lookup
	// below asks for trailing_90d.
	mismatchedCandidate.EffectiveEvidenceWindow = &EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowQuestionStated}

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
			return mismatchedCandidate, true, nil
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
		t.Fatal("result.Reused = true: a trailing_90d lookup was served a candidate whose own stored window was trailing_30d")
	}
	if result.ResultID != freshTestResultID {
		t.Errorf("result.ResultID = %q, want the fresh result %q", result.ResultID, freshTestResultID)
	}
}

// TestCHAOS3900_WindowKeyedReuseServesAMatchingCandidate is the positive
// counterpart the rejection tests above need: a candidate whose stored
// axis/window genuinely AGREE with the lookup's own key must still be
// served as an ordinary reuse hit -- the new defense-in-depth checks in
// tryReuse must not turn window-keyed reuse into a permanent miss.
func TestCHAOS3900_WindowKeyedReuseServesAMatchingCandidate(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	_, candidate := reusableCandidate()
	candidate.Interpretation.TimeContext = TimeContext{Axis: TemporalCurrent}
	candidate.EffectiveEvidenceWindow = &EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Provenance: WindowQuestionStated}

	var gateKeys []string
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(_ context.Context, _ storage.Principal, key ReuseKey) (InvestigationResult, bool, error) {
			gateKeys = append(gateKeys, key.TimeAxisKey)
			return candidate, true, nil
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
	if !result.Reused {
		t.Fatal("result.Reused = false, want true: a genuinely matching window candidate must still be servable as a hit")
	}
	if result.ResultID != candidate.ResultID {
		t.Errorf("result.ResultID = %q, want the reused candidate %q", result.ResultID, candidate.ResultID)
	}
}

// TestCHAOS3900_TwoReceiptsSameRelativeIDDifferentFrozenBoundsKeyDifferently
// is the codex review (W1 round 4) direct regression proof: two prior
// WindowClarification options both named "trailing_90d" but minted (and
// frozen) at different wall-clock moments must NOT collapse onto the same
// reuse key -- windowKeyComponent's own windowKeyRederivable "rel:<id>"
// abstraction is correct for a re-derivable (non-receipt) window, but
// wrong for a receipt's own FROZEN commitment; resolveWindowReceipts uses
// windowKeyFrozen instead.
func TestCHAOS3900_TwoReceiptsSameRelativeIDDifferentFrozenBoundsKeyDifferently(t *testing.T) {
	t.Parallel()

	marchStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	marchEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	priorA := validInvestigationResult()
	priorA.ResultID = "result_prior_window_A"
	priorA.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirmA001", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &marchStart, End: &marchEnd},
	}}

	juneStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	juneEnd := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	priorB := validInvestigationResult()
	priorB.ResultID = "result_prior_window_B"
	priorB.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirmB001", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &juneStart, End: &juneEnd},
	}}

	store := &staticResultStore{results: map[string]InvestigationResult{
		priorA.ResultID: priorA, priorB.ResultID: priorB,
	}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	requestA := validInvestigationRequest()
	requestA.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorA.ResultID, ReceiptID: "winr_confirmA001"}}
	canonA := engine.canonicalizeEvidenceWindow(context.Background(), reusePrincipal(), requestA)

	requestB := validInvestigationRequest()
	requestB.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorB.ResultID, ReceiptID: "winr_confirmB001"}}
	canonB := engine.canonicalizeEvidenceWindow(context.Background(), reusePrincipal(), requestB)

	if canonA.Veto != windowVetoNone || canonB.Veto != windowVetoNone {
		t.Fatalf("veto = (%q, %q), want none", canonA.Veto, canonB.Veto)
	}
	if canonA.KeyComponent == "" || canonB.KeyComponent == "" {
		t.Fatalf("KeyComponent = (%q, %q), want both non-empty", canonA.KeyComponent, canonB.KeyComponent)
	}
	if canonA.KeyComponent == canonB.KeyComponent {
		t.Fatalf("two DIFFERENT frozen trailing_90d offers (March vs June bounds) keyed IDENTICALLY (%q): redeeming one receipt could serve the other's confirmed interval", canonA.KeyComponent)
	}
}

// TestCHAOS3900_BinderRoleCheckUsesTheSameTerminalPunctuationQuestionHashDoes
// is the codex review (W1 round 4) regression proof: the binder's
// clause-final check must strip the IDENTICAL closed punctuation set
// QuestionHash's own CanonicalizeQuestion strips, or two questions that
// hash IDENTICALLY (and so would share a plain, non-window-keyed reuse
// row) could reach DIFFERENT binder outcomes.
func TestCHAOS3900_BinderRoleCheckUsesTheSameTerminalPunctuationQuestionHashDoes(t *testing.T) {
	t.Parallel()

	bare := "what shipped last month"
	semicolon := "what shipped last month;"
	colon := "what shipped last month:"

	if QuestionHash(bare) != QuestionHash(semicolon) || QuestionHash(bare) != QuestionHash(colon) {
		t.Fatalf("QuestionHash disagrees across trailing punctuation variants -- CanonicalizeQuestion's own closed set no longer matches this test's assumption")
	}
	for _, question := range []string{bare, semicolon, colon} {
		got := ProposeWindowFromSpans(question)
		if got.Reason != WindowBindRoutedInferred {
			t.Errorf("ProposeWindowFromSpans(%q) = %#v, want WindowBindRoutedInferred (must agree with the bare-phrasing outcome, since all three share one QuestionHash)", question, got)
		}
	}
}

// TestComposeWindowClarification_NilOnNoWindowOrConfirmedWindow (CHAOS-3900
// W2) pins the two cases NO disclosure is minted: no window in play at all,
// and a window that is already caller-asserted/confirmed (nothing to
// disambiguate).
func TestComposeWindowClarification_NilOnNoWindowOrConfirmedWindow(t *testing.T) {
	t.Parallel()
	if got := composeWindowClarification(nil, "result_1", time.Now()); got != nil {
		t.Errorf("composeWindowClarification(nil) = %+v, want nil", got)
	}
	confirmed := &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed,
	}
	if got := composeWindowClarification(confirmed, "result_1", time.Now()); got != nil {
		t.Errorf("composeWindowClarification(confirmed window) = %+v, want nil -- nothing to disambiguate", got)
	}
}

// TestComposeWindowClarification_InferredWindowOffersTheClosedRegistry
// (CHAOS-3900 W2) proves an INFERRED effective window mints exactly the
// closed four-member relative-window registry, receipt-bound (winr_), with
// frozen absolute bounds on every non-all_time option and deterministic
// minting (same result id + same content -> same ids on repeat calls).
func TestComposeWindowClarification_InferredWindowOffersTheClosedRegistry(t *testing.T) {
	t.Parallel()
	inferred := &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowInferredDefault,
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	got := composeWindowClarification(inferred, "result_00000001", now)
	if got == nil {
		t.Fatal("composeWindowClarification(inferred) = nil, want a disclosure")
	}
	if len(got.Options) != 4 {
		t.Fatalf("len(got.Options) = %d, want 4 (the closed relative-window registry)", len(got.Options))
	}
	seenReceipt, seenOption := map[string]bool{}, map[string]bool{}
	for _, opt := range got.Options {
		if !strings.HasPrefix(opt.ReceiptID, contractsv1.ContextFabricWindowOptionReceiptPrefix) {
			t.Errorf("option %+v: receipt_id lacks the winr_ prefix", opt)
		}
		if seenReceipt[opt.ReceiptID] || seenOption[opt.OptionID] {
			t.Errorf("option %+v: duplicate receipt_id/option_id", opt)
		}
		seenReceipt[opt.ReceiptID], seenOption[opt.OptionID] = true, true
		if opt.RelativeID != RelativeWindowAllTime && (opt.Start == nil || opt.End == nil) {
			t.Errorf("option %+v: non-all_time option must carry frozen start/end bounds", opt)
		}
		if opt.RelativeID == RelativeWindowAllTime && (opt.Start != nil || opt.End != nil) {
			t.Errorf("option %+v: all_time option must carry no bounds", opt)
		}
	}
	if err := got.Validate(); err != nil {
		t.Errorf("composeWindowClarification result failed Validate(): %v", err)
	}
	again := composeWindowClarification(inferred, "result_00000001", now)
	if !reflect.DeepEqual(got, again) {
		t.Errorf("composeWindowClarification is not deterministic across repeat calls with identical inputs:\n first = %+v\n second = %+v", got, again)
	}
}

// TestCHAOS4003_SupersededWindowReceiptVetoesAsStale is CHAOS-4003's own
// acceptance pin, window's analogue of
// TestCHAOS3927P4_SupersededKindReceiptVetoesAsStale (structure_test.go): a
// winr_ receipt that resolves cleanly against its stored offer must STILL
// veto if a StructureSupersessionChecker reports the (org, prior_result_id,
// "window") tuple already claimed by a newer result -- closing the gap the
// ticket names ("window never consulted the P4 supersession-claim
// machinery"). supersedingResultStore is the SAME test double
// canonicalizeStructure's own P4 tests use (structure_test.go); its
// IsStructureSuperseded is member-generic, so no new fake is needed.
func TestCHAOS4003_SupersededWindowReceiptVetoesAsStale(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_window_0005"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0005", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	principal := reusePrincipal()
	store := &supersedingResultStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}},
		superseded:        map[string]bool{supersessionKey(principal.OrgID, priorResult.ResultID, contractsv1.ContextFabricStructureNeedWindow): true},
	}
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
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0005"}}

	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if len(store.checkCalls) == 0 {
		t.Fatal("IsStructureSuperseded was never consulted -- window never reached the P4 pre-flight consult")
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1 (the stale echo entry)", len(result.ConfirmedStructure))
	}
	entry := result.ConfirmedStructure[0]
	if entry.Member != contractsv1.ContextFabricStructureNeedWindow || entry.Disposition != contractsv1.ContextFabricStructureDispositionVetoedStale {
		t.Errorf("result.ConfirmedStructure[0] = %+v, want member=window disposition=vetoed_stale", entry)
	}
	if entry.PriorResultID != priorResult.ResultID || entry.ReceiptID != "winr_confirm0005" {
		t.Errorf("result.ConfirmedStructure[0] receipt identity = (%q, %q), want (%q, %q)", entry.PriorResultID, entry.ReceiptID, priorResult.ResultID, "winr_confirm0005")
	}
	if entry.AppliedValue != string(RelativeWindowTrailing90D) {
		t.Errorf("result.ConfirmedStructure[0].AppliedValue = %q, want %q", entry.AppliedValue, RelativeWindowTrailing90D)
	}
	found := false
	for _, outcome := range telemetry.windowCanonicalizationOutcomes {
		if outcome == WindowCanonicalizationVetoStaleSupersededOffer {
			found = true
		}
	}
	if !found {
		t.Errorf("telemetry.windowCanonicalizationOutcomes = %v, want it to contain %q", telemetry.windowCanonicalizationOutcomes, WindowCanonicalizationVetoStaleSupersededOffer)
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS4003_FreshWindowReceiptStillResolves proves the pre-flight
// consult is a pure fail-CLOSED guard, never a fail-OPEN one: a winr_
// receipt against a prior result NO checker reports as superseded must
// still resolve exactly as before CHAOS-4003 -- AND now also echoes a
// member=window, disposition=applied ConfirmedStructure entry (window
// riding the SAME echo/claim pipeline kind/anchor/handle already use,
// mergeConfirmedMembers), so a Save-time supersession claim actually gets
// written for it (structureSupersessionClaims scans ConfirmedStructure
// generically -- this is what makes a LATER redemption of the SAME offer
// detectable at all).
func TestCHAOS4003_FreshWindowReceiptStillResolves(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_window_0006"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0006", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	principal := reusePrincipal()
	// supersedingResultStore with an EMPTY superseded map: the checker IS
	// wired (proving the consult ran) but reports nothing stale, so this is
	// a genuine fail-closed-guard-says-fresh case, not merely "no checker".
	store := &supersedingResultStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}},
		superseded:        map[string]bool{},
	}

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()
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
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return InvestigationResult{}, false, nil // miss: proceed to Interpret/Synthesize
		}),
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0006"}}

	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(store.checkCalls) == 0 {
		t.Fatal("IsStructureSuperseded was never consulted")
	}
	if result.EffectiveEvidenceWindow == nil || result.EffectiveEvidenceWindow.Provenance != WindowClarificationConfirmed {
		t.Fatalf("result.EffectiveEvidenceWindow = %+v, want a clarification_confirmed window", result.EffectiveEvidenceWindow)
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1 (the applied window echo)", len(result.ConfirmedStructure))
	}
	entry := result.ConfirmedStructure[0]
	if entry.Member != contractsv1.ContextFabricStructureNeedWindow || entry.Disposition != contractsv1.ContextFabricStructureDispositionApplied || entry.Source != contractsv1.ContextFabricStructureSourceReceipt {
		t.Errorf("result.ConfirmedStructure[0] = %+v, want member=window disposition=applied source=receipt", entry)
	}
	if entry.PriorResultID != priorResult.ResultID || entry.ReceiptID != "winr_confirm0006" {
		t.Errorf("result.ConfirmedStructure[0] receipt identity = (%q, %q), want (%q, %q)", entry.PriorResultID, entry.ReceiptID, priorResult.ResultID, "winr_confirm0006")
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS4003_MixedKindAndWindowReceipts_BothConfirmIndependently is the
// "other three members' behavior is unchanged" pin: a single request
// redeeming BOTH a kindr_ receipt (canonicalizeStructure's own loop) and a
// winr_ receipt (resolveWindowReceipts) confirms BOTH, independently, with
// window's entry correctly APPENDED after structure's own via
// mergeConfirmedMembers -- proves the merge neither drops nor reorders
// structure's own confirmed members.
func TestCHAOS4003_MixedKindAndWindowReceipts_BothConfirmIndependently(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_mixed_0001"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0001", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()
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
	})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.ConfirmedStructure) != 2 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 2 (kind AND window, both confirmed)", len(result.ConfirmedStructure))
	}
	if result.ConfirmedStructure[0].Member != "expected_kind" {
		t.Errorf("result.ConfirmedStructure[0].Member = %q, want expected_kind (structure's own members first)", result.ConfirmedStructure[0].Member)
	}
	if result.ConfirmedStructure[1].Member != contractsv1.ContextFabricStructureNeedWindow {
		t.Errorf("result.ConfirmedStructure[1].Member = %q, want window (appended after structure's own)", result.ConfirmedStructure[1].Member)
	}
	for _, entry := range result.ConfirmedStructure {
		if entry.Disposition != contractsv1.ContextFabricStructureDispositionApplied {
			t.Errorf("entry %+v: disposition = %q, want applied", entry, entry.Disposition)
		}
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS4003_SaveTimeRace_WindowLosesConflict mirrors
// TestCHAOS3927P4_SaveTimeSupersessionConflict_EchoesEveryLostMember
// (structure_test.go) for window: a winr_ receipt resolves cleanly with NO
// checker wired (supersessionRacingResultStore embeds staticResultStore
// only -- no IsStructureSuperseded -- so the pre-flight consult is skipped,
// exactly the "detected only at Save time" scenario), builds a real
// ConfirmedMember, and then loses the atomic (org, prior_result_id,
// "window") claim race on Save -- proving the Save-time veto path
// (structureSupersessionVetoResult, now taking the MERGED confirmed list)
// correctly echoes a window-shaped vetoed_stale entry, not just an empty
// or structure-only echo. Also pins the codex xhigh review fix
// (recordWindowSupersessionRaceTelemetry): the pre-Save
// RecordWindowCanonicalization call already reported receipt_confirmed
// before the race was known -- the FINAL telemetry entry must be the
// corrected veto_stale_superseded_offer outcome, not a dangling
// "confirmed" that contradicts the actual no_match result.
func TestCHAOS4003_SaveTimeRace_WindowLosesConflict(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_window_0007"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0007", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	store := &supersessionRacingResultStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}},
		conflictMembers:   []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
	}
	telemetry := &recordingTelemetry{}

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()
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
		Results:   store,
		Telemetry: telemetry,
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0007"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if result.ResultID == freshResult.ResultID {
		t.Error("result.ResultID equals the discarded decisive result's own id -- the superseded computation must never be returned")
	}
	if len(telemetry.windowCanonicalizationOutcomes) == 0 {
		t.Fatal("telemetry.windowCanonicalizationOutcomes is empty, want at least the corrective veto_stale_superseded_offer entry")
	}
	last := telemetry.windowCanonicalizationOutcomes[len(telemetry.windowCanonicalizationOutcomes)-1]
	if last != WindowCanonicalizationVetoStaleSupersededOffer {
		t.Errorf("telemetry.windowCanonicalizationOutcomes = %v, want the LAST entry to be %q (correcting the earlier pre-Save receipt_confirmed record)", telemetry.windowCanonicalizationOutcomes, WindowCanonicalizationVetoStaleSupersededOffer)
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1 (the raced window member's own vetoed_stale echo)", len(result.ConfirmedStructure))
	}
	entry := result.ConfirmedStructure[0]
	if entry.Member != contractsv1.ContextFabricStructureNeedWindow || entry.Disposition != contractsv1.ContextFabricStructureDispositionVetoedStale {
		t.Errorf("result.ConfirmedStructure[0] = %+v, want member=window disposition=vetoed_stale", entry)
	}
	if entry.PriorResultID != priorResult.ResultID || entry.ReceiptID != "winr_confirm0007" {
		t.Errorf("result.ConfirmedStructure[0] receipt identity = (%q, %q), want (%q, %q)", entry.PriorResultID, entry.ReceiptID, priorResult.ResultID, "winr_confirm0007")
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS4003_WindowConfirmedAppliedValue_FallsBackToBoundsWhenNoRelativeID
// is the codex xhigh review fix for windowConfirmedAppliedValue: the wire
// contract legally admits a persisted WindowOption carrying explicit
// bounds with NO relative_id (WindowOption.Validate's own "explicit bounds
// alone" branch, mirroring RequestedEvidenceWindow) -- this binary's own
// composeWindowClarification never mints that shape, but resolveWindowReceipts
// reads back an ARBITRARY prior result's stored option, which is not
// guaranteed to have come from this binary's own minting. Redeeming such
// an option must still produce a non-empty AppliedValue (an empty one
// fails ConfirmedStructureEntry.Validate's own minLength 1 rule), not a
// StageValidation error on an otherwise-legal redemption.
func TestCHAOS4003_WindowConfirmedAppliedValue_FallsBackToBoundsWhenNoRelativeID(t *testing.T) {
	t.Parallel()

	if got := windowConfirmedAppliedValue(contractsv1.ContextFabricWindowOption{RelativeID: RelativeWindowTrailing90D}); got != string(RelativeWindowTrailing90D) {
		t.Errorf("windowConfirmedAppliedValue(with RelativeID) = %q, want %q", got, RelativeWindowTrailing90D)
	}

	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	boundsOnly := contractsv1.ContextFabricWindowOption{Start: &start, End: &end}
	got := windowConfirmedAppliedValue(boundsOnly)
	if got == "" {
		t.Fatal("windowConfirmedAppliedValue(bounds-only, no relative_id) is empty, want a non-empty fallback")
	}
	want := "abs:" + formatUnixNano(start) + ":" + formatUnixNano(end)
	if got != want {
		t.Errorf("windowConfirmedAppliedValue(bounds-only) = %q, want %q", got, want)
	}

	// End-to-end: redeeming a bounds-only stored option must still resolve
	// to a Validate()-passing result, not error out.
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_window_boundsonly_0001"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirmboundsonly1", OptionID: "opt_bounds", Label: "an explicit range", Start: &start, End: &end},
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()
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
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirmboundsonly1"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.ConfirmedStructure) != 1 || result.ConfirmedStructure[0].AppliedValue == "" {
		t.Fatalf("result.ConfirmedStructure = %+v, want one entry with a non-empty AppliedValue", result.ConfirmedStructure)
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS4003_MixedWindowConfirmedAndStructureVetoed_BothEchoed is the
// codex xhigh review fix for the pre-flight structure-veto dispatch
// (engine.go): a request whose window receipt resolves cleanly BEFORE
// canonicalizeStructure ever runs, followed by a kindr_ receipt that
// canonicalizeStructure itself vetoes (unresolved), must echo BOTH --
// composeConfirmedStructure's own "one entry per carried member, including
// vetoed ones" rule applies to window's already-confirmed member too, not
// only to structure's own vetoed one. Before the fix, this dispatch path
// built echoEntries from structureCanon alone and silently dropped
// windowCanon.ConfirmedMember.
func TestCHAOS4003_MixedWindowConfirmedAndStructureVetoed_BothEchoed(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_mixed_veto_0001"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0002", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	// AnchorOptions gives canonicalizeStructure a receipt that RESOLVES
	// cleanly against its stored offer but then fails reverify (no
	// AnchorVerifier wired below) -- the ONE structureVetoConfirmationUnresolved
	// sub-case that actually echoes a VetoedEntries entry (structure.go's
	// own triggeringMemberEntry, reached only on a reverify failure -- a
	// receipt that never resolves at all, e.g. naming a missing offer,
	// echoes NOTHING per that function's own doc comment, so it cannot
	// exercise this window-drop bug on the structure side).
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"subject_anchor"},
		AnchorOptions: []AnchorOption{
			{
				ReceiptID: "ancr_confirm0001", OptionID: "opt_anchor", Label: "the widget-service repository",
				Kind: SubjectRepository, CanonicalID: "repository_widget_service",
				MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: "engine",
			},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a structure-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0002"}}
	request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "ancr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if len(result.ConfirmedStructure) != 2 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 2 (window's applied entry AND subject_anchor's vetoed_unresolved entry)", len(result.ConfirmedStructure))
	}
	var sawWindowApplied, sawAnchorUnresolved bool
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == contractsv1.ContextFabricStructureNeedWindow && entry.Disposition == contractsv1.ContextFabricStructureDispositionApplied {
			sawWindowApplied = true
		}
		if entry.Member == "subject_anchor" && entry.Disposition == contractsv1.ContextFabricStructureDispositionVetoedUnresolved {
			sawAnchorUnresolved = true
		}
	}
	if !sawWindowApplied {
		t.Errorf("result.ConfirmedStructure = %+v, want an applied window entry (must not be dropped by the structure veto)", result.ConfirmedStructure)
	}
	if !sawAnchorUnresolved {
		t.Errorf("result.ConfirmedStructure = %+v, want a vetoed_unresolved subject_anchor entry", result.ConfirmedStructure)
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS4335_UnresolvedWindowVeto_EchoesExplicitStructureHint is the
// red-first pin for CHAOS-4335: an MCP request carrying a bare explicit
// structure field (ExpectedKinds, Source=explicit_unattributed,
// Provenance=inferred_default per structureExplicitAuthority) alongside a
// PriorWindowReceipts entry that fails to resolve must still echo that
// explicit hint in ConfirmedStructure. Before this fix, windowVetoResult
// (window.go) never received anything to echo -- Engine.Investigate's
// window-canonicalization short-circuit (engine.go, immediately after
// canonicalizeEvidenceWindow) returns BEFORE canonicalizeStructure ever
// runs, so a genuinely-sent explicit kind/handle hint silently vanished
// from the wire response whenever window on the SAME request happened to
// be unconfirmed -- exactly the shape the two-turn trial harness's
// "inferred_tier" arm hit under case 30/34 (a NEGATIVE kind hint paired
// with a window receipt): TierRoutedCorrectly reads false because
// ConfirmedStructure carries no explicit_unattributed/inferred_default
// entry at all, not because the tier was wrong.
//
// Mirrors TestCHAOS3900_UnresolvedWindowReceiptVetoesTheWholeRequest's own
// scaffolding (same veto: a named prior result that does not exist), with
// an explicit ExpectedKinds field added on top.
func TestCHAOS4335_UnresolvedWindowVeto_EchoesExplicitStructureHint(t *testing.T) {
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
	request.Consumer.Surface = "mcp"
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectWorkItem}
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_4335", ReceiptID: "winr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("result.Status = %q, want %q (window veto is ALWAYS no_match)", result.Status, InvestigationNoMatch)
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1 (the explicit expected_kind hint, never dropped just because window vetoed the request)", len(result.ConfirmedStructure))
	}
	entry := result.ConfirmedStructure[0]
	if entry.Member != contractsv1.ContextFabricStructureNeedExpectedKind {
		t.Errorf("entry.Member = %q, want %q", entry.Member, contractsv1.ContextFabricStructureNeedExpectedKind)
	}
	if entry.Source != contractsv1.ContextFabricStructureSourceExplicitUnattributed {
		t.Errorf("entry.Source = %q, want %q (MCP surface, DP12(b))", entry.Source, contractsv1.ContextFabricStructureSourceExplicitUnattributed)
	}
	if entry.Provenance != contractsv1.ContextFabricStructureInferredDefault {
		t.Errorf("entry.Provenance = %q, want %q", entry.Provenance, contractsv1.ContextFabricStructureInferredDefault)
	}
	if entry.Disposition != contractsv1.ContextFabricStructureDispositionApplied {
		t.Errorf("entry.Disposition = %q, want %q", entry.Disposition, contractsv1.ContextFabricStructureDispositionApplied)
	}
	if entry.AppliedValue != string(SubjectWorkItem) {
		t.Errorf("entry.AppliedValue = %q, want %q", entry.AppliedValue, string(SubjectWorkItem))
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS4335_AxisConflictVeto_EchoesExplicitStructureHint is CHAOS-4335's
// companion pin for windowVetoResult's OTHER call site (engine.go, the
// post-Interpret windowVetoAxisConflict branch): unlike the pre-Interpret
// veto above, canonicalizeStructure has ALREADY run by this point (it is
// unconditional, right after gate 1, well before Interpret), so this call
// site can and must thread the REAL structureCanon through rather than a
// fresh pre-Interpret-only resolution. Mirrors
// TestCHAOS3900_AxisConflict_InterpreterFlipVetoesInsteadOfSilentlyDropping's
// own scaffolding, with an explicit ExpectedKinds field added.
func TestCHAOS4335_AxisConflictVeto_EchoesExplicitStructureHint(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_window_4335"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm4335", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	asOf := time.Unix(100, 0).UTC()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return InvestigationResult{}, false, nil // miss: proceed to Interpret
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &asOf}}, nil
		}),
	})

	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectWorkItem}
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm4335"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("result.Status = %q, want %q (window_axis_conflict is always no_match)", result.Status, InvestigationNoMatch)
	}
	// Exactly 1, not "at least 1": window's OWN confirmed member is a KNOWN,
	// documented, out-of-scope gap on this specific veto (windowVetoResult's
	// own explicitStructure parameter doc comment) -- this pins the CURRENT
	// exact shape so that gap's eventual fix is a deliberate, visible test
	// change here, not a silent cardinality drift nobody notices.
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want exactly 1 (the explicit expected_kind hint only -- window's own confirmed member is a documented, separate gap on this path): %+v", len(result.ConfirmedStructure), result.ConfirmedStructure)
	}
	entry := result.ConfirmedStructure[0]
	if entry.Member != contractsv1.ContextFabricStructureNeedExpectedKind {
		t.Errorf("entry.Member = %q, want %q", entry.Member, contractsv1.ContextFabricStructureNeedExpectedKind)
	}
	if entry.Source != contractsv1.ContextFabricStructureSourceExplicitUnattributed {
		t.Errorf("entry.Source = %q, want %q", entry.Source, contractsv1.ContextFabricStructureSourceExplicitUnattributed)
	}
	if entry.Provenance != contractsv1.ContextFabricStructureInferredDefault {
		t.Errorf("entry.Provenance = %q, want %q", entry.Provenance, contractsv1.ContextFabricStructureInferredDefault)
	}
	if entry.Disposition != contractsv1.ContextFabricStructureDispositionApplied {
		t.Errorf("entry.Disposition = %q, want %q", entry.Disposition, contractsv1.ContextFabricStructureDispositionApplied)
	}
	if entry.AppliedValue != string(SubjectWorkItem) {
		t.Errorf("entry.AppliedValue = %q, want %q", entry.AppliedValue, string(SubjectWorkItem))
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS4335_ReceiptCarryingRequest_WindowVetoDefersStructureEcho pins
// preInterpretExplicitStructure's OWN receipt-deferral guard: a request that
// carries BOTH a structure receipt (PriorKindReceipts) and an unresolvable
// PriorWindowReceipts entry must echo NOTHING for structure on the veto --
// even though ExpectedKinds is ALSO set on the same request -- because a
// receipt-carrying request needs the result-store read canonicalizeStructure
// itself performs (CHAOS-3478's deferred-past-every-pre-Interpret-window-gate
// ordering), which this pre-Interpret helper must never attempt early. This
// is the "existing deferred behavior... unchanged" half of
// preInterpretExplicitStructure's own doc comment -- without this test no
// coverage exercises the has-receipts branch at all.
func TestCHAOS4335_ReceiptCarryingRequest_WindowVetoDefersStructureEcho(t *testing.T) {
	t.Parallel()

	store := &staticResultStore{results: map[string]InvestigationResult{}} // named prior results do not exist
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a window-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectWorkItem}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_4335b", ReceiptID: "kindr_confirm0001"}}
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_4335c", ReceiptID: "winr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if len(result.ConfirmedStructure) != 0 {
		t.Fatalf("result.ConfirmedStructure = %+v, want empty -- a receipt-carrying request must still defer to canonicalizeStructure's own (unreached) result-store read, never echo the explicit field early", result.ConfirmedStructure)
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS4335_UnresolvedWindowVeto_EchoesExplicitSubjectHandle is the
// SubjectHandles twin of TestCHAOS4335_UnresolvedWindowVeto_
// EchoesExplicitStructureHint above -- ExpectedKinds is not the only bare
// explicit field resolveExplicitStructure/preInterpretExplicitStructure
// handle.
func TestCHAOS4335_UnresolvedWindowVeto_EchoesExplicitSubjectHandle(t *testing.T) {
	t.Parallel()

	store := &staticResultStore{results: map[string]InvestigationResult{}} // named prior result does not exist
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a window-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532"}}
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_4335d", ReceiptID: "winr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1: %+v", len(result.ConfirmedStructure), result.ConfirmedStructure)
	}
	entry := result.ConfirmedStructure[0]
	if entry.Member != contractsv1.ContextFabricStructureNeedSubjectHandle {
		t.Errorf("entry.Member = %q, want %q", entry.Member, contractsv1.ContextFabricStructureNeedSubjectHandle)
	}
	if entry.Source != contractsv1.ContextFabricStructureSourceExplicitUnattributed {
		t.Errorf("entry.Source = %q, want %q", entry.Source, contractsv1.ContextFabricStructureSourceExplicitUnattributed)
	}
	if entry.Provenance != contractsv1.ContextFabricStructureInferredDefault {
		t.Errorf("entry.Provenance = %q, want %q", entry.Provenance, contractsv1.ContextFabricStructureInferredDefault)
	}
	if entry.AppliedValue != "532" {
		t.Errorf("entry.AppliedValue = %q, want %q", entry.AppliedValue, "532")
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestAppendUniqueWarning (CHAOS-3900 W2) pins the dedup discipline the
// nudge-mode Warnings append relies on: appending the SAME sentence twice
// must not duplicate it, and the contract's own Warnings cap must never be
// exceeded.
func TestAppendUniqueWarning(t *testing.T) {
	t.Parallel()
	warnings := appendUniqueWarning(nil, "a nudge")
	warnings = appendUniqueWarning(warnings, "a nudge")
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly 1 (duplicate append must be a no-op)", warnings)
	}
	full := make([]string, contractsv1.ContextFabricWarningsMaxCount)
	for i := range full {
		full[i] = "existing warning " + strconv.Itoa(i)
	}
	if got := appendUniqueWarning(full, "a nudge"); len(got) != len(full) {
		t.Errorf("appendUniqueWarning at the cap grew the list to %d, want it to stay at %d (never push over the wire bound)", len(got), len(full))
	}
}
