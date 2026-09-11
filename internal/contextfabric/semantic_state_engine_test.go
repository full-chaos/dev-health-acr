package contextfabric

// Engine-level pins for the persisted semantic snapshot: every Save site
// supplies a snapshot or a closed absence, the decision is on the trace with
// its values, and a continuation continues the persisted reading whole.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	semanticPresent = true
	semanticAbsent  = false
)

// assertSemanticPersistence asserts EXACTLY ONE persistence decision was
// recorded, at the named site, persisted, carrying a snapshot or exactly the
// named absence.
func assertSemanticPersistence(t *testing.T, telemetry *recordingTelemetry, site BudgetAssertStage, wantState bool, wantAbsence SemanticStateAbsence) {
	t.Helper()
	if len(telemetry.semanticStatePersistences) != 1 {
		t.Fatalf("got %d semantic-state persistence decisions, want exactly 1 per saved result: %+v", len(telemetry.semanticStatePersistences), telemetry.semanticStatePersistences)
	}
	event := telemetry.semanticStatePersistences[0]
	t.Logf("persistence: site=%s decision=%s absence=%q state=%v encoded_bytes=%d", event.Site, event.Decision, event.Absence, event.State != nil, event.EncodedBytes)
	if event.Site != site {
		t.Errorf("site=%q, want %q", event.Site, site)
	}
	if event.Decision != SemanticStatePersisted {
		t.Errorf("decision=%q, want persisted", event.Decision)
	}
	if (event.State != nil) != wantState {
		t.Errorf("state present=%v, want %v", event.State != nil, wantState)
	}
	if event.Absence != wantAbsence {
		t.Errorf("absence=%q, want %q", event.Absence, wantAbsence)
	}
	if wantState {
		if _, err := EncodeSemanticState(event.State); err != nil {
			t.Errorf("the persisted snapshot does not validate: %v", err)
		}
		if event.EncodedBytes <= 0 || event.EncodedBytes > SemanticStateMaxEncodedBytes {
			t.Errorf("encoded_bytes=%d, want a measured size within the cap", event.EncodedBytes)
		}
	}
}

// TestSemanticState_EachComponentConflictIsNamedAlone changes ONE component of
// the fresh reading at a time and asserts conflict_fields names exactly that
// component -- so no component can hide behind another, and none is reported
// that did not move.
func TestSemanticState_EachComponentConflictIsNamedAlone(t *testing.T) {
	t.Parallel()
	carried := semanticFixture(t)
	decision := windowContinuationDecision{
		Observed: true, WindowOnlyShape: true, Disposition: ContinuationApplied,
		Accepted: &continuationCarriedContext{Family: carried.Family, GroupKind: carried.GroupKind, State: carried},
	}
	decision.Carried = decision.Accepted
	for _, tc := range []struct {
		want ContinuationConflictField
		edit func(*PersistedSemanticState)
	}{
		{ContinuationConflictFieldGoals, func(s *PersistedSemanticState) { s.Frame.Goals = s.Frame.Goals[:1] }},
		{ContinuationConflictFieldTemporal, func(s *PersistedSemanticState) { s.Frame.Temporal = TemporalIntentTimeSeries }},
		{ContinuationConflictFieldEmphasis, func(s *PersistedSemanticState) { s.Frame.Emphasis = []AnswerEmphasis{AnswerEmphasisVocabulary()[0]} }},
		{ContinuationConflictFieldDimensions, func(s *PersistedSemanticState) {
			s.Frame.Dimensions = []HealthDimension{HealthDimensionVocabulary()[0]}
		}},
		{ContinuationConflictFieldObligations, func(s *PersistedSemanticState) { s.Frame.Obligations = s.Frame.Obligations[:1] }},
		{ContinuationConflictFieldWidenedObligations, func(s *PersistedSemanticState) { s.Frame.WidenedObligations = nil }},
		{ContinuationConflictFieldRequirements, func(s *PersistedSemanticState) { s.Requirements = s.Requirements[:1] }},
		{ContinuationConflictFieldRoles, func(s *PersistedSemanticState) { s.Roles = s.Roles[:1] }},
		{ContinuationConflictFieldFrameGate, func(s *PersistedSemanticState) { s.Validation.GateOutcome = FrameGateNotEvaluated }},
		{ContinuationConflictFieldSubjectExpression, func(s *PersistedSemanticState) { s.GroupKind = SubjectProject }},
		{ContinuationConflictFieldScopeAnchor, func(s *PersistedSemanticState) { s.ScopeAnchor = SemanticScopeAnchor{Kind: SubjectTeam} }},
		{ContinuationConflictFieldEmittedShape, func(s *PersistedSemanticState) { s.Validation.EmittedShape = ShapeExplicitCohort }},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			fresh := semanticFixture(t)
			tc.edit(fresh)
			got := compareContinuationProposal(decision, continuationFreshProposal{Available: true, Family: carried.Family, GroupKind: carried.GroupKind, State: fresh})
			t.Logf("changed %s -> conflict_fields=%v agreement=%v", tc.want, got.ConflictFieldTokens(), got.Agreement)
			if len(got.ConflictFields) != 1 || got.ConflictFields[0] != tc.want {
				t.Errorf("conflict_fields=%v, want exactly [%s]", got.ConflictFieldTokens(), tc.want)
			}
			if got.Agreement || got.ConflictReason != ContinuationConflictNonWindowContext {
				t.Errorf("agreement=%v conflict_reason=%q on a real disagreement", got.Agreement, got.ConflictReason)
			}
		})
	}
	t.Run("identical readings agree", func(t *testing.T) {
		got := compareContinuationProposal(decision, continuationFreshProposal{Available: true, Family: carried.Family, GroupKind: carried.GroupKind, State: semanticFixture(t)})
		if !got.Agreement || len(got.ConflictFields) != 0 || got.ConflictReason != ContinuationConflictNone {
			t.Errorf("identical readings -> agreement=%v fields=%v reason=%q", got.Agreement, got.ConflictFieldTokens(), got.ConflictReason)
		}
	})
	t.Run("the family alone", func(t *testing.T) {
		got := compareContinuationProposal(decision, continuationFreshProposal{Available: true, Family: QuestionFamilyDiscoveredCohortRanking, GroupKind: carried.GroupKind, State: semanticFixture(t)})
		if len(got.ConflictFields) != 1 || got.ConflictFields[0] != ContinuationConflictFieldFamily {
			t.Errorf("fields=%v, want exactly [family]", got.ConflictFieldTokens())
		}
	})
}

// TestSemanticState_EverySnapshotKeyIsComparedOrExemptByName is the coverage
// half of the component pin above, enumerated from the PRODUCER: every JSON key
// the snapshot, its validation block and its frame encode is either compared
// under a named conflict field or exempt with the reason it cannot disagree. A
// key added to the snapshot without a decision here fails, so conflict_fields
// cannot silently stop covering the reading.
func TestSemanticState_EverySnapshotKeyIsComparedOrExemptByName(t *testing.T) {
	t.Parallel()
	compared := map[string]ContinuationConflictField{
		"family":                          ContinuationConflictFieldFamily,
		"group_kind":                      ContinuationConflictFieldSubjectExpression,
		"frame_present":                   ContinuationConflictFieldSubjectExpression,
		"scope_anchor":                    ContinuationConflictFieldScopeAnchor,
		"roles":                           ContinuationConflictFieldRoles,
		"requirements_declared":           ContinuationConflictFieldRequirements,
		"requirements":                    ContinuationConflictFieldRequirements,
		"validation.emitted_shape":        ContinuationConflictFieldEmittedShape,
		"validation.gate_outcome":         ContinuationConflictFieldFrameGate,
		"validation.failed_invariant":     ContinuationConflictFieldFrameGate,
		"validation.refuse_basis":         ContinuationConflictFieldFrameGate,
		"validation.declared_member_kind": ContinuationConflictFieldFrameGate,
		"frame.goals":                     ContinuationConflictFieldGoals,
		"frame.subject_expression":        ContinuationConflictFieldSubjectExpression,
		"frame.temporal":                  ContinuationConflictFieldTemporal,
		"frame.emphasis":                  ContinuationConflictFieldEmphasis,
		"frame.dimensions":                ContinuationConflictFieldDimensions,
		"frame.obligations":               ContinuationConflictFieldObligations,
		"frame.widened_obligations":       ContinuationConflictFieldWidenedObligations,
	}
	exempt := map[string]string{
		"format_version":                 "decode gate: another format reads unsupported_version and never reaches comparison",
		"family_table_version":           "admission gate: a table not in force is context_version_mismatch before comparison",
		"frame_version":                  "admission gate: a frame table not in force is context_version_mismatch before comparison",
		"requirement_derivation_version": "admission gate: a derivation not in force is context_version_mismatch before comparison",
		"frame.version":                  "validation requires it to equal frame_version, which admission gates",
		"family_source":                  "provenance of the family value, not a component of the reading; the family itself is compared",
		"narrowing_basis":                "the fresh side proposes none: it is derived inside PlanAnswer after this comparison",
		"frame":                          "container: each frame key is decided below",
		"validation":                     "container: each validation key is decided below",
	}
	keys := func(prefix string, typ reflect.Type) []string {
		out := []string{}
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			out = append(out, prefix+tag)
		}
		return out
	}
	all := append(append(keys("", reflect.TypeOf(PersistedSemanticState{})), keys("validation.", reflect.TypeOf(SemanticStateValidation{}))...), keys("frame.", reflect.TypeOf(QuestionFrame{}))...)
	comparable := map[ContinuationConflictField]bool{}
	for _, field := range continuationComparableFields() {
		comparable[field] = true
	}
	for _, key := range all {
		field, isCompared := compared[key]
		reason, isExempt := exempt[key]
		switch {
		case isCompared && isExempt:
			t.Errorf("key %q is both compared (%s) and exempt (%s)", key, field, reason)
		case isCompared:
			if !comparable[field] {
				t.Errorf("key %q is claimed compared under %q, which is not a comparable field", key, field)
			}
			t.Logf("%-36s compared as %s", key, field)
		case isExempt:
			t.Logf("%-36s exempt: %s", key, reason)
		default:
			t.Errorf("snapshot key %q is neither compared nor exempt by name -- decide it", key)
		}
	}
	for key := range compared {
		if !slices.Contains(all, key) {
			t.Errorf("compared key %q is not a key the snapshot encodes", key)
		}
	}
	for key := range exempt {
		if !slices.Contains(all, key) {
			t.Errorf("exempt key %q is not a key the snapshot encodes", key)
		}
	}
}

// TestSemanticState_TheLinesCarryValuesAndNeverARetrievalTerm drives the
// shipped slog sink: the persistence line and the decision line both carry the
// snapshot's closed values, and neither carries a retrieval term.
func TestSemanticState_TheLinesCarryValuesAndNeverARetrievalTerm(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-RETRIEVAL-TERM-5465"
	state := semanticFixture(t)
	kind := SubjectRepository
	state.Frame.SubjectExpression = SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
		{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{secret, secret + "-2"}, ExpectedKind: &kind}},
		{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{secret + "-anchor"}, MemberKind: SubjectRepository}},
	}}}
	state.GroupKind = ""
	state.Roles = semanticRoleSlots(state.Frame.SubjectExpression)
	state.Requirements = []SemanticRequirement{}
	if _, err := EncodeSemanticState(state); err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	var buf strings.Builder
	sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	sink.RecordSemanticStatePersistence(context.Background(), acceptancePrincipal(), SemanticStatePersistenceEvent{
		ResultID: "result_semantic_line_01", Site: BudgetAssertDecisive, Decision: SemanticStatePersisted, EncodedBytes: 1234, State: state,
	})
	decision := newWindowContinuationDecision(continuationRequest(validInvestigationRequest().Question))
	decision.Disposition = ContinuationApplied
	decision.Carried = &continuationCarriedContext{Family: state.Family, State: state, SourceResultID: continuationPriorID}
	decision.Accepted = decision.Carried
	decision.CarriedStateConsulted, decision.CarriedStateRead = true, SemanticStateReadAvailable
	sink.RecordWindowContinuationDecision(context.Background(), acceptancePrincipal(), decision)
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("a retrieval term reached a log line:\n%s", out)
	}
	var persistence, decisionLine map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("decode: %v", err)
		}
		switch line["msg"] {
		case "context fabric semantic state persistence":
			persistence = line
		case "context fabric window continuation decision":
			decisionLine = line
		}
	}
	if persistence == nil || decisionLine == nil {
		t.Fatalf("missing lines:\n%s", out)
	}
	for name, group := range map[string]any{"persistence.state": persistence["state"], "decision.carried_state": decisionLine["carried_state"]} {
		values, _ := group.(map[string]any)
		t.Logf("%s = %v", name, values)
		operands, _ := values["operands"].([]any)
		if values["present"] != true || values["retrieval_term_count"] != float64(3) || len(operands) != 2 ||
			operands[0] != "0:named_subject:repository" || operands[1] != "1:children_of_scope:repository" {
			t.Errorf("%s does not carry the operand slots and term count: %v", name, values)
		}
		if goals, _ := values["goals"].([]any); len(goals) != len(state.Frame.Goals) {
			t.Errorf("%s goals=%v, want %d values", name, values["goals"], len(state.Frame.Goals))
		}
	}
	if persistence["level"] != "INFO" || persistence["decision"] != "persisted" || persistence["absence"] != "none" || persistence["encoded_bytes"] != float64(1234) || persistence["encoded_cap"] != float64(SemanticStateMaxEncodedBytes) {
		t.Errorf("persistence line = %v", persistence)
	}
	if fresh, _ := decisionLine["fresh_state"].(map[string]any); fresh["present"] != false {
		t.Errorf("an unavailable fresh reading must render present=false, got %v", fresh)
	}
	if decisionLine["carried_state_read"] != "available" {
		t.Errorf("carried_state_read=%v", decisionLine["carried_state_read"])
	}
}

// TestSemanticState_NoTermBearingPositionReachesALine is the class sweep of the
// test above: EVERY position a frame keeps a retrieval term in -- a named
// subject, a scope's anchor, and both inside an explicit set -- rendered on
// both lines through the shipped sink. Each position carries a distinct marker
// so a leak names the position, and each line still counts every term.
func TestSemanticState_NoTermBearingPositionReachesALine(t *testing.T) {
	t.Parallel()
	kind := SubjectRepository
	for _, tc := range []struct {
		name       string
		expression SubjectExpression
		markers    []string
	}{
		{"named subject", SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"LEAK-NAMED-A", "LEAK-NAMED-B"}, ExpectedKind: &kind}}, []string{"LEAK-NAMED-A", "LEAK-NAMED-B"}},
		{"children of scope", SubjectExpression{Kind: SubjectExpressionChildrenOfScope, Scoped: &ScopedSetExpression{AnchorTerms: []string{"LEAK-ANCHOR-A", "LEAK-ANCHOR-B"}, MemberKind: SubjectRepository}}, []string{"LEAK-ANCHOR-A", "LEAK-ANCHOR-B"}},
		{"explicit set, named and scoped operands", SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
			{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{"LEAK-OPERAND-NAMED"}, ExpectedKind: &kind}},
			{Kind: SubjectOperandScoped, Scoped: &ScopedSetExpression{AnchorTerms: []string{"LEAK-OPERAND-ANCHOR"}, MemberKind: SubjectRepository}},
		}}}, []string{"LEAK-OPERAND-NAMED", "LEAK-OPERAND-ANCHOR"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := semanticFixture(t)
			state.Frame.SubjectExpression = tc.expression
			var buf strings.Builder
			sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
			sink.RecordSemanticStatePersistence(context.Background(), acceptancePrincipal(), SemanticStatePersistenceEvent{
				ResultID: "result_semantic_sweep_01", Site: BudgetAssertDecisive, Decision: SemanticStatePersisted, EncodedBytes: 1, State: state,
			})
			decision := newWindowContinuationDecision(continuationRequest(validInvestigationRequest().Question))
			decision.Disposition = ContinuationApplied
			decision.Carried = &continuationCarriedContext{Family: state.Family, State: state, SourceResultID: continuationPriorID}
			decision.Accepted = decision.Carried
			decision.CarriedStateConsulted, decision.CarriedStateRead = true, SemanticStateReadAvailable
			sink.RecordWindowContinuationDecision(context.Background(), acceptancePrincipal(), decision)
			out := buf.String()
			for _, marker := range tc.markers {
				if strings.Contains(out, marker) {
					t.Errorf("the retrieval term %q reached a log line", marker)
				}
			}
			lines := 0
			for _, raw := range strings.Split(strings.TrimSpace(out), "\n") {
				var line map[string]any
				if err := json.Unmarshal([]byte(raw), &line); err != nil {
					t.Fatalf("decode: %v", err)
				}
				group, _ := line["state"].(map[string]any)
				if group == nil {
					group, _ = line["carried_state"].(map[string]any)
				}
				if group == nil {
					continue
				}
				lines++
				t.Logf("%s: %s retrieval_term_count=%v operands=%v", tc.name, line["msg"], group["retrieval_term_count"], group["operands"])
				if group["retrieval_term_count"] != float64(len(tc.markers)) {
					t.Errorf("%s retrieval_term_count=%v, want %d", line["msg"], group["retrieval_term_count"], len(tc.markers))
				}
			}
			if lines != 2 {
				t.Fatalf("rendered %d snapshot groups, want the persistence line and the decision line:\n%s", lines, out)
			}
		})
	}
}

// TestSemanticState_ThePersistenceLineRefusesInventedValues: every closed field
// on the persistence line publishes the unrecognised sentinel for a value its
// vocabulary does not name, never the invented text.
func TestSemanticState_ThePersistenceLineRefusesInventedValues(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	sink := SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	sink.RecordSemanticStatePersistence(context.Background(), acceptancePrincipal(), SemanticStatePersistenceEvent{
		ResultID: "result_semantic_line_02", Site: BudgetAssertStage("invented-site"),
		Decision: SemanticStatePersistenceDecision("invented-decision"), Absence: SemanticStateAbsence("invented-absence"),
		Bound: SemanticStateBound("invented-bound"),
	})
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"site", "decision", "absence", "oversized_bound"} {
		if line[key] != continuationTelemetryUnrecognised {
			t.Errorf("%s=%v, want %q", key, line[key], continuationTelemetryUnrecognised)
		}
	}
	if strings.Contains(buf.String(), "invented-") {
		t.Errorf("free text reached a closed field: %s", buf.String())
	}
	if state, _ := line["state"].(map[string]any); state["present"] != false {
		t.Errorf("an absent snapshot must render present=false: %v", line["state"])
	}
}

// TestSemanticState_ThePersistenceDecisionClassifiesEveryStoreOutcome maps each
// store outcome to its closed decision.
func TestSemanticState_ThePersistenceDecisionClassifiesEveryStoreOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want SemanticStatePersistenceDecision
	}{
		{nil, SemanticStatePersisted},
		{fmt.Errorf("wrapped: %w", ErrSemanticStateReplayConflict), SemanticStateReplayConflictDecision},
		{fmt.Errorf("wrapped: %w", ErrSemanticStateRejected), SemanticStateSaveFailedDecision},
		{&ErrStructureOfferSuperseded{Members: []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}}, SemanticStateSupersededDecision},
		{errors.New("connection reset"), SemanticStateSaveFailedDecision},
	} {
		if got := classifySemanticStatePersistence(tc.err); got != tc.want {
			t.Errorf("classify(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	// Every member has a producer in the table above.
	if len(semanticStatePersistenceDecisions()) != 4 {
		t.Errorf("the decision vocabulary has %d members; the table drives 4", len(semanticStatePersistenceDecisions()))
	}
}

// TestSemanticState_ThePreloadCacheKeepsWholeCarriers: a carrier the
// subject-receipt resolution loaded is cached WITH its snapshot, so no reader
// of the cache sees a carrier narrowed to its payload.
func TestSemanticState_ThePreloadCacheKeepsWholeCarriers(t *testing.T) {
	t.Parallel()
	prior := validInvestigationResult()
	prior.ResultID = "result_semantic_preload_01"
	prior.SubjectResolution.Candidates = []SubjectCandidate{{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}, ReceiptID: "subr_semantic_preload_01", Confidence: 0.9}}
	state := semanticFixture(t)
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, states: map[string]*PersistedSemanticState{prior.ResultID: state}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})
	_, _, _, _, loaded := engine.resolvePriorSubjectHints(context.Background(), acceptancePrincipal(), ConsumerInfo{Name: "t", Version: "1", Surface: "workbench"},
		[]BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "subr_semantic_preload_01"}}, ResolvedGraphBinding{Epoch: 0})
	cached, ok := loaded[prior.ResultID]
	t.Logf("cached=%v read=%s snapshot=%v", ok, cached.SemanticStateRead, cached.SemanticState != nil)
	if !ok || cached.SemanticStateRead != SemanticStateReadAvailable || !SemanticStatesEqual(cached.SemanticState, state) {
		t.Fatalf("the preload cache narrowed the carrier: ok=%v read=%s", ok, cached.SemanticStateRead)
	}
}

// TestSemanticStateAdmission_TheRecordedStandardsMustBeInForce is admission's
// input domain over an AVAILABLE snapshot: each recorded standard out of
// force alone, the family disagreeing with the public plan, no plan at all,
// and the in-force control.
func TestSemanticStateAdmission_TheRecordedStandardsMustBeInForce(t *testing.T) {
	t.Parallel()
	plan := &AnswerPlan{Family: QuestionFamilyGroupedCohortStatus}
	for _, tc := range []struct {
		name string
		edit func(*PersistedSemanticState)
		plan *AnswerPlan
		want ContinuationDecisionReason
	}{
		{"every standard in force", func(*PersistedSemanticState) {}, plan, ContinuationReasonNone},
		{"family table not in force", func(s *PersistedSemanticState) { s.FamilyTableVersion = "question-family.v0" }, plan, ContinuationReasonContextVersionMismatch},
		{"frame table not in force", func(s *PersistedSemanticState) {
			s.FrameVersion, s.Frame.Version = "question-frame.v0", "question-frame.v0"
		}, plan, ContinuationReasonContextVersionMismatch},
		{"requirement derivation not in force", func(s *PersistedSemanticState) { s.RequirementDerivationVersion = "requirement-derivation.v1" }, plan, ContinuationReasonContextVersionMismatch},
		// EXACT, like the plan's own stamp: a stamp padded with whitespace names
		// no standard this build can verify, even when its trimmed text is the
		// one in force.
		{"family table in force, padded", func(s *PersistedSemanticState) { s.FamilyTableVersion = " " + QuestionFamilyTableVersion + "\t" }, plan, ContinuationReasonContextVersionMismatch},
		{"frame table in force, padded", func(s *PersistedSemanticState) {
			s.FrameVersion, s.Frame.Version = QuestionFrameVersion+" ", QuestionFrameVersion+" "
		}, plan, ContinuationReasonContextVersionMismatch},
		{"requirement derivation in force, padded", func(s *PersistedSemanticState) { s.RequirementDerivationVersion = " " + RequirementDerivationVersion }, plan, ContinuationReasonContextVersionMismatch},
		{"family disagrees with the public plan", func(*PersistedSemanticState) {}, &AnswerPlan{Family: QuestionFamilyDiscoveredCohortRanking}, ContinuationReasonSemanticStateInvalid},
		{"no public plan", func(*PersistedSemanticState) {}, nil, ContinuationReasonSemanticStateInvalid},
	} {
		state := semanticFixture(t)
		tc.edit(state)
		got := semanticStateAdmission(StoredInvestigationResult{SemanticState: state, SemanticStateRead: SemanticStateReadAvailable}, tc.plan)
		t.Logf("%-40s -> %s", tc.name, got)
		if got != tc.want {
			t.Errorf("%s: reason=%q, want %q", tc.name, got, tc.want)
		}
	}
	// Every read status other than available withholds, and an available
	// status with no snapshot is invalid.
	for status, want := range map[SemanticStateReadStatus]ContinuationDecisionReason{
		SemanticStateReadAbsent:             ContinuationReasonSemanticStateAbsent,
		SemanticStateReadUnsupportedVersion: ContinuationReasonContextVersionMismatch,
		SemanticStateReadMalformed:          ContinuationReasonSemanticStateInvalid,
		SemanticStateReadOversized:          ContinuationReasonSemanticStateInvalid,
		"":                                  ContinuationReasonSemanticStateInvalid,
		SemanticStateReadStatus("invented"): ContinuationReasonSemanticStateInvalid,
		SemanticStateReadAvailable:          ContinuationReasonSemanticStateInvalid, // with a nil snapshot
	} {
		if got := semanticStateAdmission(StoredInvestigationResult{SemanticStateRead: status}, plan); got != want {
			t.Errorf("status %q with no snapshot -> %q, want %q", status, got, want)
		}
	}
}

// TestSemanticStateComposition_APassedGateWithoutAFrameIsIncomplete: a snapshot
// whose gate certified a frame it no longer carries is not composed as a
// frameless reading.
func TestSemanticStateComposition_APassedGateWithoutAFrameIsIncomplete(t *testing.T) {
	t.Parallel()
	state := carriedStateFor(t, QuestionFamilyGroupedCohortStatus, SubjectTeam, nil, FrameGate{})
	control := composeAcceptedContext(compositionInput{Carried: state})
	state.Validation.GateOutcome = FrameGatePassed
	got := composeAcceptedContext(compositionInput{Carried: state})
	t.Logf("frameless not_evaluated -> %s usable=%v | frameless passed -> %s invariant=%s", control.Outcome, control.Usable(), got.Outcome, got.FailedInvariant)
	if !control.Usable() {
		t.Fatalf("control: a frameless not-evaluated reading must compose")
	}
	if got.Usable() || got.FailedInvariant != CompositionInvariantCarriedStateIncomplete {
		t.Errorf("a passed gate with no frame composed as %q (invariant %q)", got.Outcome, got.FailedInvariant)
	}
}

// TestSemanticStateApply_FrameObligationsMoveWithTheFrame: installing the
// carried frame installs its obligation set, and a frameless carrier installs
// none -- the frame and its obligations are never from two readings.
func TestSemanticStateApply_FrameObligationsMoveWithTheFrame(t *testing.T) {
	t.Parallel()
	state := semanticFixture(t)
	decision := windowContinuationDecision{Observed: true, WindowOnlyShape: true, Disposition: ContinuationApplied,
		Accepted: &continuationCarriedContext{Family: state.Family, GroupKind: state.GroupKind, State: state}}
	freshObligations := []AnswerObligation{ObligationRanking}
	before := QuestionFamilyOutcome{Family: QuestionFamilyDiscoveredCohortRanking, Source: QuestionFamilySourceModel, FrameObligations: freshObligations}
	accepted := composeAcceptedContext(compositionInput{Carried: state})
	after, applied := applyWindowContinuation(before, decision, accepted)
	if !applied || after.Frame == nil || !sameJSON(after.FrameObligations, after.Frame.Obligations) || sameJSON(after.FrameObligations, freshObligations) {
		t.Fatalf("applied=%v obligations=%v frame=%v -- the installed frame's obligations must replace the fresh ones", applied, after.FrameObligations, after.Frame != nil)
	}
	frameless := carriedStateFor(t, QuestionFamilyGroupedCohortStatus, SubjectTeam, nil, FrameGate{})
	decision.Accepted.State = frameless
	after, applied = applyWindowContinuation(before, decision, composeAcceptedContext(compositionInput{Carried: frameless}))
	t.Logf("frameless carrier -> applied=%v frame=%v obligations=%v", applied, after.Frame != nil, after.FrameObligations)
	if !applied || after.Frame != nil || after.FrameObligations != nil {
		t.Errorf("a frameless carrier must continue frameless with no obligations, got frame=%v obligations=%v", after.Frame != nil, after.FrameObligations)
	}
}

// TestSemanticStateCapture_AContinuationMaterializesTheCarriedShape: the
// continued turn's own snapshot records the CARRIED reading's emitted shape
// (the input its frame was validated with), not this turn's fresh one.
func TestSemanticStateCapture_AContinuationMaterializesTheCarriedShape(t *testing.T) {
	t.Parallel()
	request := continuationRequest(validInvestigationRequest().Question)
	prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, SubjectTeam)
	carried := framedCarrierState(t, prior, SubjectRepository)
	carried.Validation.EmittedShape = ShapeDiscoveredCohort
	if _, err := EncodeSemanticState(carried); err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, states: map[string]*PersistedSemanticState{prior.ResultID: carried}}
	harness := newContinuationHarness(t, store, frameBearingInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: SubjectProject, frameGroup: SubjectProject})
	harness.investigate(t, request)
	if d := harness.soleDecision(t); d.Disposition != ContinuationApplied {
		t.Fatalf("fixture defect: the continuation did not apply (%s/%s)", d.Disposition, d.Reason)
	}
	saved := store.savedSemantic
	if saved == nil || saved.State == nil {
		t.Fatalf("the continued turn saved no snapshot: %+v", saved)
	}
	t.Logf("carried shape=%s fresh shape=%s saved shape=%s", carried.Validation.EmittedShape, ShapeOpen, saved.State.Validation.EmittedShape)
	if saved.State.Validation.EmittedShape != ShapeDiscoveredCohort {
		t.Errorf("saved emitted_shape=%q, want the carried %q", saved.State.Validation.EmittedShape, ShapeDiscoveredCohort)
	}
	if !sameJSON(saved.State.Frame, carried.Frame) || saved.State.FamilySource != QuestionFamilySourceCarried {
		t.Errorf("the continued snapshot is not the carried reading: frame equal=%v source=%s", sameJSON(saved.State.Frame, carried.Frame), saved.State.FamilySource)
	}
}

// failingSaveStore is staticResultStore whose Save returns a fixed error.
type failingSaveStore struct {
	*staticResultStore
	saveErr error
}

func (s failingSaveStore) Save(ctx context.Context, p storage.Principal, r InvestigationResult, w SourceWatermarkSnapshot, e RebuildEpoch, k string, ri ReuseRetrievalIdentity, pv ReusePromptVersions, va ReuseVersionAuthorities, g int64, parent string, semantic SemanticStateWrite) error {
	_ = s.staticResultStore.Save(ctx, p, r, w, e, k, ri, pv, va, g, parent, semantic)
	return s.saveErr
}

// TestSemanticState_ThePersistenceLineReportsTheStoreOutcomeThroughARealExit
// drives a real engine exit (the continuation refusal over a legacy carrier)
// against a store whose Save fails each way, and reads the decision from the
// line the shipped sink emits. The classifier's own table is not enough: a
// saveResult that published a constant would agree with it on every success.
func TestSemanticState_ThePersistenceLineReportsTheStoreOutcomeThroughARealExit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want SemanticStatePersistenceDecision
	}{
		{"save succeeds", nil, SemanticStatePersisted},
		{"replay conflict", fmt.Errorf("pginvestigation: %w: identical payload, different semantic state", ErrSemanticStateReplayConflict), SemanticStateReplayConflictDecision},
		{"connection reset", errors.New("pginvestigation: save investigation result: connection reset by peer"), SemanticStateSaveFailedDecision},
		{"structure claim lost", &ErrStructureOfferSuperseded{Members: []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}}, SemanticStateSupersededDecision},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := continuationRequest(validInvestigationRequest().Question)
			prior := continuationPrior(t, continuationPriorID, request.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			inner := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}, noCarrierStates: true}
			harness := newContinuationHarness(t, inner, forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})
			var buf strings.Builder
			harness.engine.telemetry = SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
			harness.engine.results = failingSaveStore{staticResultStore: inner, saveErr: tc.err}
			_, investigateErr := harness.engine.Investigate(context.Background(), acceptancePrincipal(), request)
			var lines []map[string]any
			for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
				var line map[string]any
				if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == "context fabric semantic state persistence" {
					lines = append(lines, line)
				}
			}
			if len(lines) != 1 {
				t.Fatalf("persistence lines = %d, want 1 (investigate err=%v)", len(lines), investigateErr)
			}
			line := lines[0]
			t.Logf("store error=%q -> investigate_err=%v | level=%v site=%v decision=%v absence=%v", fmt.Sprint(tc.err), investigateErr != nil, line["level"], line["site"], line["decision"], line["absence"])
			if line["level"] != "INFO" || line["decision"] != string(tc.want) || line["site"] != string(BudgetAssertContinuationRefusal) || line["absence"] != string(SemanticStateAbsenceContinuationRefused) {
				t.Errorf("persistence line = %v, want decision=%s site=%s absence=%s at INFO", line, tc.want, BudgetAssertContinuationRefusal, SemanticStateAbsenceContinuationRefused)
			}
		})
	}
}
