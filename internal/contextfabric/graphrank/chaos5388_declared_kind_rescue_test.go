package graphrank

// A DECLARED KIND THAT NEVER REACHES THE POOL MUST SAY WHY, BY ARM.
//
// CHAOS-5388, audit finding F1. On the rig a named-project row entered
// phase 4 with 70 candidates cut to 10, every one a ci_pipeline_run, zero
// projects, while the kind offer withheld `project` as not-in-pool
// (declared_withheld_not_in_pool_count=1). The withholding is the TERMINAL
// state. The loss is upstream, in pool construction on a named arm -- and the
// records could not say which upstream rule lost it.
//
// That is not a wording problem. Measured on this tree, driving the
// production entry point, these two resolutions emit BYTE-IDENTICAL Info:
//
//   - the kind-scoped rescue arm is NOT WIRED (deps.SearchKind nil), so it
//     never queried the declared kind at all;
//   - the arm IS wired, DID query, and matched zero rows.
//
// One is a deployment gap and one is a retrieval defect. They need different
// fixes and they read the same, which is exactly the first-loss attribution
// gap the audit's §6 names: "never queried, query returned zero, query
// matched but admission removed the subject, kind filtering removed it,
// ranking cut it, authorization denied it". An operator cannot act on either
// without guessing.
//
// The acceptance clause these pins serve, verbatim from the ticket: the pool
// that reaches ranked_cut carries the declared member kind whenever the graph
// holds a matching subject, "or the truncation that removed it is disclosed
// on the ranked_cut line by arm and count".

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// namedProjectFrame is the completion row's own shape: a named subject whose
// declared kind lives on Named.ExpectedKind, not MemberKind (CHAOS-4975).
func namedProjectFrame(term string) *contextfabric.QuestionFrame {
	kind := contextfabric.SubjectProject
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:  contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{term}, ExpectedKind: &kind},
		},
	}
}

// declaredKindCrowd builds the rig row's population: a crowd of a kind the
// question did NOT declare, larger than the budget, plus the declared
// project reachable ONLY under its own kind. searchKindRows says what the
// kind-scoped arm returns -- nil for "wired but matched nothing".
func declaredKindCrowd(term string, crowd int, wireSearchKind bool, searchKindRows []CandidateNode) *fakeGraphBackend {
	noise := make([]CandidateNode, 0, crowd)
	for i := 0; i < crowd; i++ {
		noise = append(noise, candidateNode(contractsv1.ContextFabricSubjectCIRun,
			fmt.Sprintf("ci_pipeline_run.v2:github:%s-build-%d", term, i),
			fmt.Sprintf("%s build %d", term, i), 0.9, "*"))
	}
	b := &fakeGraphBackend{searchResults: map[string][]CandidateNode{term: noise}}
	if wireSearchKind {
		b.enableSearchKind = true
		b.searchKindResults = map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {contextfabric.SubjectProject: searchKindRows},
		}
	}
	return b
}

// theAskDevProject is the canonical identity the audit proves exists in the
// same run that lost it, so a fixture asserting on it is asserting on a real
// subject rather than an invented one.
func theAskDevProject() []CandidateNode {
	return []CandidateNode{candidateNode(contextfabric.SubjectProject,
		"project.v2:linear:13e65c04-40ec-4a95-8216-f7c2ce233244", "Ask Dev", 0.85, "*")}
}

// namedRowInfo drives the production entry point and returns the Info log.
func namedRowInfo(t *testing.T, backend *fakeGraphBackend, frame *contextfabric.QuestionFrame) (contextfabric.SubjectResolution, string) {
	t.Helper()
	req := testRequest()
	req.Options.MaxSubjectCandidates = 10
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("ask-dev"),
		deps, nil, nil, frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return res, buf.String()
}

// namedRowInfoWithConfirmedKind is namedRowInfo plus a confirmed kind, which is
// what lets a resolution take more than one pass through the cut.
func namedRowInfoWithConfirmedKind(t *testing.T, backend *fakeGraphBackend, frame *contextfabric.QuestionFrame,
	confirmed *contextfabric.ConfirmedExpectedKind) (contextfabric.SubjectResolution, string) {
	t.Helper()
	req := testRequest()
	req.Options.MaxSubjectCandidates = 10
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("ask-dev"),
		deps, confirmed, nil, frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return res, buf.String()
}

// rankedCutLine returns the ONE ranked-cut summary line. Locating the line is
// the point: several stages carry overlapping keys, and an assertion made
// over the whole log passes on whichever line happens to be right.
func rankedCutLine(t *testing.T, log string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"msg":"context fabric resolution trace: ranked cut summary"`) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d ranked-cut summary lines, want exactly 1", len(found))
	}
	return found[0]
}

// P1 -- THE RED THAT NAMES THE DEFECT: the two upstream causes must not be
// telemetrically identical.
//
// This is the `sort -u` collapse written as an assertion. On the unfixed tree
// both resolutions emit the same ranked_cut line, so a reader cannot tell a
// rescue arm that never ran from one that ran and matched nothing.
func TestTheUnrunRescueArmAndTheEmptyOneDoNotReadAlike(t *testing.T) {
	t.Parallel()
	_, notRunLog := namedRowInfo(t, declaredKindCrowd("ask-dev", 70, false, nil), namedProjectFrame("ask-dev"))
	_, matchedZeroLog := namedRowInfo(t, declaredKindCrowd("ask-dev", 70, true, nil), namedProjectFrame("ask-dev"))

	notRun := rankedCutLine(t, notRunLog)
	matchedZero := rankedCutLine(t, matchedZeroLog)

	// NON-VACUITY: both must genuinely have lost the project, or this is not
	// the shape the ticket is about.
	for name, line := range map[string]string{"not-run": notRun, "matched-zero": matchedZero} {
		if strings.Contains(line, "project.v2:linear:") {
			t.Fatalf("%s: the project survived, so this is not the losing shape: %s", name, line)
		}
	}
	if stripTimestamp(notRun) == stripTimestamp(matchedZero) {
		t.Errorf("the ranked_cut line is IDENTICAL for an unrun rescue arm and one that ran and matched nothing.\n"+
			"A deployment gap and a retrieval defect need different fixes and read the same here.\nline: %s", stripTimestamp(notRun))
	}
}

// P2 -- NOT_RUN is named as such. The arm was never wired, so the declared
// kind was never queried; the line must say that rather than only that the
// kind is absent from the pool.
func TestAnUnrunRescueArmIsDisclosedAsNotRun(t *testing.T) {
	t.Parallel()
	_, log := namedRowInfo(t, declaredKindCrowd("ask-dev", 70, false, nil), namedProjectFrame("ask-dev"))
	line := rankedCutLine(t, log)
	for _, want := range []string{`"declared_kind_rescue"`, `"project"`, `"not_run"`} {
		if !strings.Contains(line, want) {
			t.Errorf("ranked_cut line missing %s -- an operator cannot tell the arm never queried this kind. line: %s", want, line)
		}
	}
}

// P3 -- RAN_MATCHED_ZERO is named as such, with its attempt count, so
// "queried and found nothing" is a MEASURED zero rather than a missing
// measurement.
func TestARescueArmThatMatchedNothingIsDisclosedWithItsAttemptCount(t *testing.T) {
	t.Parallel()
	_, log := namedRowInfo(t, declaredKindCrowd("ask-dev", 70, true, nil), namedProjectFrame("ask-dev"))
	line := rankedCutLine(t, log)
	for _, want := range []string{`"declared_kind_rescue"`, `"project"`, `"ran_matched_zero"`, `"terms_queried":1`, `"matched":0`} {
		if !strings.Contains(line, want) {
			t.Errorf("ranked_cut line missing %s -- a query that ran and matched nothing must be distinguishable from one that never ran. line: %s", want, line)
		}
	}
}

// P4 -- RAN_MATCHED_THEN_CUT is the third cause, and it is a DIFFERENT fix
// from the other two: the subject was retrieved and phase 4 dropped it.
//
// THE FIXTURE HAS TO WORK FOR IT. On a named frame this state is not
// reachable: the declared kind is reserved through the cut, so a matched
// project is admitted and the honest answer is "delivered". The reachable
// shape is a GROUPED frame, where BOTH axes are reserved and the budget is
// saturated by one of them -- then the other has no eligible victim and the
// existing reserve admits nothing. That is a real production shape, not a
// contrivance, and writing this pin against the named shape instead would
// have produced a test that skipped and asserted nothing.
func TestARescueArmWhoseMatchWasCutIsDisclosedAsCut(t *testing.T) {
	t.Parallel()
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:    contextfabric.SubjectExpressionGroupedMembers,
			Grouped: &contextfabric.GroupedSetExpression{GroupKind: contextfabric.SubjectTeam, MemberKind: contextfabric.SubjectProject},
		},
	}
	// The budget is filled by TEAMS -- the group axis, itself reserved -- so
	// the member axis has no eligible victim to take a slot from.
	teams := make([]CandidateNode, 0, 70)
	for i := 0; i < 70; i++ {
		teams = append(teams, candidateNode(contextfabric.SubjectTeam,
			fmt.Sprintf("team.v2:github:ask-dev-%d", i), fmt.Sprintf("ask-dev team %d", i), 0.9, "*"))
	}
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{"ask-dev": teams},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"ask-dev": {
				contextfabric.SubjectProject: theAskDevProject(),
				contextfabric.SubjectTeam:    nil,
			},
		},
	}
	res, log := namedRowInfo(t, backend, frame)
	if got := candidateKinds(res)[contextfabric.SubjectProject]; got != 0 {
		t.Fatalf("the project survived (kinds=%v), so this fixture is not the CUT shape and the pin below would assert the wrong state", candidateKinds(res))
	}
	line := rankedCutLine(t, log)
	for _, want := range []string{`"declared_kind_rescue"`, `"project"`, `"ran_matched_then_cut"`, `"matched":1`, `"survived":0`} {
		if !strings.Contains(line, want) {
			t.Errorf("ranked_cut line missing %s -- a subject that was RETRIEVED and then truncated away must not read like one that was never found. line: %s", want, line)
		}
	}
}

// P5 -- THE HEALTHY CASE IS STATED TOO, with explicit counts, so a pass where
// nothing went wrong cannot be mistaken for a build that stopped reporting.
func TestARescueArmThatDeliveredIsDisclosedAsDelivered(t *testing.T) {
	t.Parallel()
	res, log := namedRowInfo(t, declaredKindCrowd("ask-dev", 70, true, theAskDevProject()), namedProjectFrame("ask-dev"))
	if got := candidateKinds(res)[contextfabric.SubjectProject]; got == 0 {
		t.Fatalf("the declared project did not survive on the healthy path; kinds=%v", candidateKinds(res))
	}
	line := rankedCutLine(t, log)
	for _, want := range []string{`"declared_kind_rescue"`, `"project"`, `"ran_matched_survived"`, `"matched":1`, `"survived":1`} {
		if !strings.Contains(line, want) {
			t.Errorf("ranked_cut line missing %s on the healthy path; a pass that worked must say so explicitly. line: %s", want, line)
		}
	}
}

// P6 -- NEGATIVE CONTROL: a frame that declares NO kind reports an EMPTY
// rescue list, never a missing key. Absence of a declaration and absence of
// the measurement must not read alike.
func TestAFrameThatDeclaresNoKindReportsAnEmptyRescueList(t *testing.T) {
	t.Parallel()
	frame := namedProjectFrame("ask-dev")
	frame.SubjectExpression.Named.ExpectedKind = nil
	_, log := namedRowInfo(t, declaredKindCrowd("ask-dev", 70, true, theAskDevProject()), frame)
	line := rankedCutLine(t, log)
	if !strings.Contains(line, `"declared_kind_rescue":[]`) {
		t.Errorf("want an explicit EMPTY declared_kind_rescue list when nothing was declared; a missing key and a measured none must not read alike. line: %s", line)
	}
}

// P7 -- CONTROL, per the team-lead ruling on the possessive row: a named
// project question must not emit an anchor or group axis. The kind offer's
// own anchor-bearing fields stay absent on this shape.
func TestAPossessiveNamedProjectEmitsNoAnchorAxis(t *testing.T) {
	t.Parallel()
	_, log := namedRowInfo(t, declaredKindCrowd("ask-dev", 70, true, theAskDevProject()), namedProjectFrame("ask-dev"))
	line := rankedCutLine(t, log)
	if strings.Contains(line, `"anchor_slot_reserved":"project"`) || strings.Contains(line, `"anchor_slot_reserved":"team"`) {
		t.Errorf("a named-subject question emitted an anchor axis; possessive phrasing must not produce a scope anchor. line: %s", line)
	}
	if !strings.Contains(line, `"anchor_slot_reserved":"none"`) {
		t.Errorf("anchor_slot_reserved is not the explicit none token on a named frame. line: %s", line)
	}
}

// stripTimestamp removes the per-line clock so two structurally identical
// records compare equal. Without it P1 would pass on the timestamp alone and
// prove nothing.
func stripTimestamp(line string) string {
	i := strings.Index(line, `,"level"`)
	if i < 0 {
		return line
	}
	return line[i:]
}

// P8 -- EVERY PASS REPORTS THE SAME DECLARED-KIND TRUTH, and the LAST one
// most of all.
//
// r1 P1, found with an executed probe. A resolution can run more than one
// pass through the cut: the confirmed-kind re-decision and the evidence-census
// rescue both go round again. Those passes deliberately run with the RESERVE
// OFF, and the declared set used to be read from the cut's reservedKinds
// parameter — so their summary emitted an EMPTY list. Since the last summary
// reaching the tracer is the one describing the pass whose resolution was
// returned, an operator read `declared_kind_rescue: []` and would conclude the
// question had declared nothing, on a request that declared a kind and had it
// rescued. That is exactly the two-states-read-alike defect this file exists
// to remove, reintroduced one pass later.
//
// The fix put the declared set on the LEDGER, where it belongs: it is a fact
// about the REQUEST, not about whether a given pass happens to reserve slots.
// This pin asserts it on EVERY summary the resolution emits, not just the
// first, because asserting the first is what let the defect through.
func TestEverySummaryPassReportsTheSameDeclaredKinds(t *testing.T) {
	t.Parallel()
	// A MULTI-PASS FIXTURE, and the pin fails below unless it really is one.
	// The first version of this test used the single-pass fixture and could
	// not have seen the defect it exists for -- the same "control that cannot
	// fail" mistake the finding itself was about. The confirmed-kind scoped
	// re-decision fires on a truncated search with a confirmed kind and
	// nothing committed, which is what produces the SECOND cut.
	backend := declaredKindCrowd("ask-dev", 70, true, theAskDevProject())
	backend.searchTruncated = true
	_, log := namedRowInfoWithConfirmedKind(t, backend, namedProjectFrame("ask-dev"), confirmedProject())

	var summaries []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"msg":"context fabric resolution trace: ranked cut summary"`) {
			summaries = append(summaries, line)
		}
	}
	if len(summaries) < 2 {
		t.Fatalf("this fixture produced %d ranked-cut summary line(s); the defect this pin exists for lives in the SECOND "+
			"and later passes, so a single-pass fixture asserts nothing about it", len(summaries))
	}
	for i, line := range summaries {
		if strings.Contains(line, `"declared_kind_rescue":[]`) {
			t.Errorf("summary %d of %d reports an EMPTY declared_kind_rescue on a request that DID declare a kind. "+
				"The last summary is the one an operator reads, so a pass that forgets the declared set makes a "+
				"rescued kind look like a question that declared nothing. line: %s", i+1, len(summaries), line)
		}
		if !strings.Contains(line, `"kind":"`+string(contextfabric.SubjectProject)+`"`) {
			t.Errorf("summary %d of %d does not name the declared kind. line: %s", i+1, len(summaries), line)
		}
	}
}
