package contextfabric_test

// The frame-validation line, certified against its eventspec declaration
// from the bytes the production slog JSON handler wrote: identity, level,
// every declared field's presence and closed vocabulary, and that the
// declared keys and the emitted keys are the SAME set, in both directions,
// on a real production emission -- the same discipline
// completeness_authority_certify_test.go already applies to its own line.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// frameValidationTestRequestID derives a wire-valid request id from seed,
// matching completenessAuthorityTestRequestID's own reasoning.
func frameValidationTestRequestID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "req_" + hex.EncodeToString(sum[:])[:32]
}

// recordFrameValidationJSON emits event through the real production sink
// and returns the raw JSON bytes.
func recordFrameValidationJSON(requestID string, event contextfabric.FrameValidationEvent) []byte {
	ctx := observability.WithRequestID(context.Background(), requestID)
	var buf bytes.Buffer
	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))).
		RecordFrameValidation(ctx, storage.Principal{OrgID: "org_1"}, event)
	return buf.Bytes()
}

// baseFrameValidationEvent is a repaired, non-trivial line: every field
// this sweep does not itself vary carries a real, non-zero value where one
// exists, so a setter that forgets to touch a field is not masked by every
// other field already being at its zero value.
func baseFrameValidationEvent() contextfabric.FrameValidationEvent {
	return contextfabric.FrameValidationEvent{
		Outcome:       contextfabric.FrameValidationOutcomeRepaired,
		ProposedKind:  contextfabric.SubjectExpressionNamed,
		ProposedGoals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		AcceptedGoals: []contextfabric.InvestigationGoal{contextfabric.GoalExplainChange},
		// AcceptedJudgment stays empty here: the repair below
		// (count_kind_collapse) never touches Goals, so a real line for
		// this exact shape carries none too (FrameRepairCarry's own rule).
		AcceptedJudgment:             "",
		OrderingPresent:              true,
		PredictedStrippedObligations: []contextfabric.AnswerObligation{},
		DerivedObligationCount:       2,
		WidenedObligationCount:       1,
		FrameVersion:                 contextfabric.QuestionFrameVersion,
		CohortDiscoverability:        contextfabric.CohortDiscoverable,
		Gate:                         contextfabric.FrameGate{Outcome: contextfabric.FrameGatePassed},
		RequirementDerivation: contextfabric.RequirementDerivationSummary{
			Version: contextfabric.RequirementDerivationVersion,
			Derived: 1, Served: 1, Unserved: 0,
		},
		Boundary: contextfabric.InterpretationBoundary{
			RequestedGroupHint:  "absent",
			RequestedMemberHint: "absent",
			ProposedGroupKind:   "not_applicable",
			ProposedMemberKind:  "not_applicable",
			GroupAxis:           contextfabric.GroupAxisNotRequested,
		},
		Repair: contextfabric.FrameRepair{
			Decision:   contextfabric.FrameRepairApplied,
			Name:       contextfabric.FrameRepairCountKindCollapse,
			Invariant:  contextfabric.FrameInvariantI9,
			KindBefore: contextfabric.SubjectExpressionNamed,
			KindAfter:  contextfabric.SubjectExpressionChildrenOfScope,
			MemberKind: contextfabric.SubjectTeam,
			TermsMatch: contextfabric.FrameRepairTermsSame,
			Attempts:   1,
			Carry:      contextfabric.FrameRepairCarry{ScopeAnchorKind: contextfabric.SubjectTeam},
		},
	}
}

// TestTheFrameValidationLineDeclaresExactlyTheEmittedKeys pins that every
// declared key reaches the line and every emitted key is declared, in
// both directions, on a real production emission.
func TestTheFrameValidationLineDeclaresExactlyTheEmittedKeys(t *testing.T) {
	t.Parallel()
	requestID := frameValidationTestRequestID("declared_keys")
	raw := recordFrameValidationJSON(requestID, baseFrameValidationEvent())
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	lines := log.LinesWithMsg(eventspec.FrameValidation.Msg)
	if len(lines) != 1 {
		t.Fatalf("got %d frame validation lines, want 1", len(lines))
	}
	line := lines[0]
	if line["request_id"] != requestID {
		t.Fatalf("request_id = %v, want %s", line["request_id"], requestID)
	}
	declared := map[string]bool{}
	for _, field := range eventspec.FrameValidation.Fields {
		declared[field.Key] = true
		if _, ok := line[field.Key]; !ok {
			t.Errorf("declared key %q is not on the emitted line", field.Key)
		}
	}
	for key := range line {
		switch key {
		case "time", "level", "msg":
			continue
		}
		if !declared[key] {
			t.Errorf("emitted key %q is not declared on %s", key, eventspec.FrameValidation.ID)
		}
	}
}

// TestEveryClosedValueOnTheFrameValidationLineCertifies sweeps every member
// of every closed string vocabulary on the line through the real
// production sink -- including members no production call site exercises
// today -- the same discipline
// TestEveryClosedValueOnTheCompletenessAuthorityLineCertifies already
// applies to its own line.
//
// A handful of closed string fields render through a helper that maps the
// EMPTY underlying value to a sentinel word ("none", "not_evaluated",
// "unset") rather than passing the value straight through -- to reach that
// sentinel on the emitted line, the setter clears the underlying field
// instead of writing the sentinel's own text into it. sentinelOnEmpty
// names exactly those fields, keyed by the sentinel they render on empty.
func TestEveryClosedValueOnTheFrameValidationLineCertifies(t *testing.T) {
	t.Parallel()
	base := baseFrameValidationEvent()

	sentinelOnEmpty := map[string]string{
		"group_axis":                     "unset",
		"refuse_basis":                   "none",
		"repair_decision":                "not_evaluated",
		"repair":                         "none",
		"repair_invariant":               "none",
		"repair_kind_before":             "none",
		"repair_kind_after":              "none",
		"repair_member_kind":             "none",
		"repair_terms_match":             "not_evaluated",
		"repair_carry_scope_anchor_kind": "none",
	}

	set := map[string]func(*contextfabric.FrameValidationEvent, string){
		"outcome": func(e *contextfabric.FrameValidationEvent, v string) {
			e.Outcome = contextfabric.FrameValidationOutcome(v)
		},
		"failed_invariant": func(e *contextfabric.FrameValidationEvent, v string) {
			e.FailedInvariant = contextfabric.FrameInvariant(v)
		},
		"failed_phase": func(e *contextfabric.FrameValidationEvent, v string) {
			e.FailedPhase = contextfabric.FrameValidationPhase(v)
		},
		"failure_detail": func(e *contextfabric.FrameValidationEvent, v string) {
			e.FailureDetail = contextfabric.FrameFailureDetail(v)
		},
		"proposed_kind": func(e *contextfabric.FrameValidationEvent, v string) {
			e.ProposedKind = contextfabric.SubjectExpressionKind(v)
		},
		"emitted_shape": func(e *contextfabric.FrameValidationEvent, v string) {
			e.EmittedShape = contextfabric.InvestigationShape(v)
		},
		"derived_shape": func(e *contextfabric.FrameValidationEvent, v string) {
			e.DerivedShape = contextfabric.InvestigationShape(v)
		},
		"cohort_discoverability": func(e *contextfabric.FrameValidationEvent, v string) {
			e.CohortDiscoverability = contextfabric.CohortDiscoverability(v)
		},
		"refuse_basis": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["refuse_basis"] {
				e.Gate.RefuseBasis = ""
				return
			}
			e.Gate.RefuseBasis = contextfabric.CohortDiscoverability(v)
		},
		"requested_group_hint": func(e *contextfabric.FrameValidationEvent, v string) {
			e.Boundary.RequestedGroupHint = v
		},
		"group_hint_source": func(e *contextfabric.FrameValidationEvent, v string) {
			e.Boundary.GroupHintSource = v
		},
		"requested_member_hint": func(e *contextfabric.FrameValidationEvent, v string) {
			e.Boundary.RequestedMemberHint = v
		},
		"proposed_group_kind": func(e *contextfabric.FrameValidationEvent, v string) {
			e.Boundary.ProposedGroupKind = v
		},
		"proposed_member_kind": func(e *contextfabric.FrameValidationEvent, v string) {
			e.Boundary.ProposedMemberKind = v
		},
		"group_axis": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["group_axis"] {
				e.Boundary.GroupAxis = ""
				return
			}
			e.Boundary.GroupAxis = contextfabric.GroupAxisDecision(v)
		},
		"repair_decision": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["repair_decision"] {
				e.Repair.Decision = ""
				return
			}
			e.Repair.Decision = contextfabric.FrameRepairDecision(v)
		},
		"repair": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["repair"] {
				e.Repair.Name = ""
				return
			}
			e.Repair.Name = contextfabric.FrameRepairName(v)
		},
		"repair_invariant": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["repair_invariant"] {
				e.Repair.Invariant = ""
				return
			}
			e.Repair.Invariant = contextfabric.FrameInvariant(v)
		},
		"repair_kind_before": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["repair_kind_before"] {
				e.Repair.KindBefore = ""
				return
			}
			e.Repair.KindBefore = contextfabric.SubjectExpressionKind(v)
		},
		"repair_kind_after": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["repair_kind_after"] {
				e.Repair.KindAfter = ""
				return
			}
			e.Repair.KindAfter = contextfabric.SubjectExpressionKind(v)
		},
		"repair_member_kind": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["repair_member_kind"] {
				e.Repair.MemberKind = ""
				return
			}
			e.Repair.MemberKind = contextfabric.SubjectKind(v)
		},
		"repair_terms_match": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["repair_terms_match"] {
				e.Repair.TermsMatch = ""
				return
			}
			e.Repair.TermsMatch = contextfabric.FrameRepairTermsMatch(v)
		},
		"repair_carry_scope_anchor_kind": func(e *contextfabric.FrameValidationEvent, v string) {
			if v == sentinelOnEmpty["repair_carry_scope_anchor_kind"] {
				e.Repair.Carry.ScopeAnchorKind = ""
				return
			}
			e.Repair.Carry.ScopeAnchorKind = contextfabric.SubjectKind(v)
		},
	}
	sliceSet := map[string]func(*contextfabric.FrameValidationEvent, string){
		"proposed_goals": func(e *contextfabric.FrameValidationEvent, v string) {
			e.ProposedGoals = []contextfabric.InvestigationGoal{contextfabric.InvestigationGoal(v)}
		},
		"accepted_goals": func(e *contextfabric.FrameValidationEvent, v string) {
			e.AcceptedGoals = []contextfabric.InvestigationGoal{contextfabric.InvestigationGoal(v)}
		},
		"predicted_stripped_obligations": func(e *contextfabric.FrameValidationEvent, v string) {
			e.PredictedStrippedObligations = []contextfabric.AnswerObligation{contextfabric.AnswerObligation(v)}
		},
	}

	// requirement_accounting is STRUCTURAL (computed from whether Derived
	// equals Served+Unserved, never a settable string) -- swept separately
	// below rather than through the generic setter map.
	skipStructural := map[string]bool{"requirement_accounting": true}

	closed := 0
	for _, field := range eventspec.FrameValidation.Fields {
		if len(field.ClosedVocabulary) == 0 {
			continue
		}
		if skipStructural[field.Key] {
			continue
		}
		closed++
		apply, ok := set[field.Key]
		isSlice := false
		if !ok {
			apply, ok = sliceSet[field.Key]
			isSlice = true
		}
		if !ok {
			t.Fatalf("closed field %q has no setter in this sweep; add one", field.Key)
		}
		for _, member := range field.ClosedVocabulary {
			event := base
			apply(&event, member)
			requestID := frameValidationTestRequestID("sweep_" + field.Key + "_" + member)
			raw := recordFrameValidationJSON(requestID, event)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			var want any = member
			if isSlice {
				want = []any{member}
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.FrameValidation,
				Want:  map[string]any{"org_id": "org_1", field.Key: want},
			}); err != nil {
				t.Fatalf("%s=%q: the certifier refused a real production line: %v", field.Key, member, err)
			}
		}
	}
	if closed != len(set)+len(sliceSet) {
		t.Fatalf("the specification declares %d swept closed fields, this sweep sets %d", closed, len(set)+len(sliceSet))
	}
}

// TestFrameValidationRequirementAccountingCertifiesBothMembers sweeps
// requirement_accounting's own two members: "ok" when every derived
// requirement cell is served or unserved, "violated" when the two do not
// add up to Derived -- computed by RequirementDerivationSummary.Balanced,
// never a settable field.
func TestFrameValidationRequirementAccountingCertifiesBothMembers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		summary contextfabric.RequirementDerivationSummary
		want    string
	}{
		{"balanced", contextfabric.RequirementDerivationSummary{
			Version: contextfabric.RequirementDerivationVersion, Derived: 2, Served: 1, Unserved: 1,
		}, "ok"},
		{"unbalanced", contextfabric.RequirementDerivationSummary{
			Version: contextfabric.RequirementDerivationVersion, Derived: 2, Served: 1, Unserved: 0,
		}, "violated"},
	} {
		event := baseFrameValidationEvent()
		event.RequirementDerivation = tc.summary
		requestID := frameValidationTestRequestID("requirement_accounting_" + tc.name)
		raw := recordFrameValidationJSON(requestID, event)
		log, err := certify.Parse(raw)
		if err != nil {
			t.Fatalf("certify.Parse(): %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.FrameValidation,
			Want:  map[string]any{"org_id": "org_1", "requirement_accounting": tc.want},
		}); err != nil {
			t.Fatalf("%s: the certifier refused a real production line: %v", tc.name, err)
		}
	}
}

// frameJudgmentAlphabet is accepted_judgment's certified fragment alphabet
// -- goalJudgmentPhrase's own range, read through the one accessor built
// for this purpose, never a second, independently typed list.
func frameJudgmentAlphabet() map[string]bool {
	set := map[string]bool{}
	for _, phrase := range contextfabric.GoalJudgmentPhraseVocabulary() {
		set[phrase] = true
	}
	return set
}

// certifyAcceptedJudgmentAgainstAlphabet is accepted_judgment's own
// certification. The field is OPEN at the whole-string level -- its
// length and phrase order both follow the accepted Goals list, so no flat
// ClosedVocabulary enumerates it (GoalJudgmentPhraseVocabulary's own doc
// comment) -- so it is certified at the fragment level instead: "none"
// certifies trivially; any other value must split on " and " into
// fragments that are ALL members of the phrase alphabet, and the one
// repair that ever changes Goals (repair_decision=applied,
// repair=compare_grouped_collapse) must not leave it empty, per
// FrameRepairCarry's own guarantee.
func certifyAcceptedJudgmentAgainstAlphabet(line certify.Line) error {
	value, _ := line["accepted_judgment"].(string)
	goalChangingRepairApplied := line["repair_decision"] == string(contextfabric.FrameRepairApplied) &&
		line["repair"] == string(contextfabric.FrameRepairCompareGroupedCollapse)
	if value == "none" {
		if goalChangingRepairApplied {
			return fmt.Errorf("accepted_judgment = %q but the goal-changing repair applied", value)
		}
		return nil
	}
	alphabet := frameJudgmentAlphabet()
	for _, fragment := range strings.Split(value, " and ") {
		if !alphabet[fragment] {
			return fmt.Errorf("accepted_judgment fragment %q is not a member of GoalJudgmentPhraseVocabulary()", fragment)
		}
	}
	return nil
}

// TestAcceptedJudgmentCertifiesAgainstThePhraseAlphabet proves the
// alphabet-level check above both accepts every real shape the sink
// produces and refuses a fragment, or an emptiness, it should not.
func TestAcceptedJudgmentCertifiesAgainstThePhraseAlphabet(t *testing.T) {
	t.Parallel()
	phrases := contextfabric.GoalJudgmentPhraseVocabulary()
	if len(phrases) == 0 {
		t.Fatal("GoalJudgmentPhraseVocabulary() is empty")
	}

	buildEvent := func(judgment string, goalChangingRepair bool) contextfabric.FrameValidationEvent {
		event := baseFrameValidationEvent()
		event.AcceptedJudgment = judgment
		event.Repair.Decision = contextfabric.FrameRepairApplied
		if goalChangingRepair {
			event.Repair.Name = contextfabric.FrameRepairCompareGroupedCollapse
		} else {
			event.Repair.Name = contextfabric.FrameRepairCountKindCollapse
		}
		return event
	}
	parseFor := func(t *testing.T, judgment string, goalChangingRepair bool, tag string) (*certify.Log, certify.Line) {
		t.Helper()
		requestID := frameValidationTestRequestID("judgment_" + tag)
		raw := recordFrameValidationJSON(requestID, buildEvent(judgment, goalChangingRepair))
		log, err := certify.Parse(raw)
		if err != nil {
			t.Fatalf("certify.Parse(): %v", err)
		}
		lines := log.LinesWithMsg(eventspec.FrameValidation.Msg)
		if len(lines) != 1 {
			t.Fatalf("got %d lines, want 1", len(lines))
		}
		return log, lines[0]
	}

	for _, tc := range []struct {
		name               string
		judgment           string
		goalChangingRepair bool
	}{
		{"empty_no_goal_changing_repair", "", false},
		{"single_phrase", phrases[0], true},
		{"three_phrase_join", strings.Join(phrases[:3], " and "), true},
		{"every_phrase_join", strings.Join(phrases, " and "), true},
	} {
		t.Run("accept_"+tc.name, func(t *testing.T) {
			log, line := parseFor(t, tc.judgment, tc.goalChangingRepair, "accept_"+tc.name)
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.FrameValidation,
				Want:  map[string]any{"org_id": "org_1"},
			}); err != nil {
				t.Fatalf("certify refused a real production line: %v", err)
			}
			if err := certifyAcceptedJudgmentAgainstAlphabet(line); err != nil {
				t.Fatalf("accepted_judgment failed the alphabet check: %v", err)
			}
		})
	}

	for _, tc := range []struct {
		name               string
		judgment           string
		goalChangingRepair bool
	}{
		{"fragment_outside_alphabet", "the subject's own standing", true},
		{"mixed_valid_and_invalid_fragment", phrases[0] + " and not a real phrase", true},
		{"empty_despite_goal_changing_repair", "", true},
	} {
		t.Run("refuse_"+tc.name, func(t *testing.T) {
			_, line := parseFor(t, tc.judgment, tc.goalChangingRepair, "refuse_"+tc.name)
			if err := certifyAcceptedJudgmentAgainstAlphabet(line); err == nil {
				t.Fatalf("the alphabet check accepted %q; it should have refused it", tc.judgment)
			}
		})
	}
}
