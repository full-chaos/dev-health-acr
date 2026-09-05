package graphrank

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5218 -- an offered kind the pool cannot serve.
//
// MEASURED CHAIN (lane-corpus-bisect, on the wire, three corpus rows
// qb-scoped / cv-scoped-projects-by-team-bounded /
// basis-scoped-workitems-by-project, 3/3 served at b694589c, 0/3 at
// 248d7ca0): the frame declares member kind `project`; retrieval finds only
// teams and a pull request; CHAOS-4967's cardinality gate asks whether the
// pool holds ANY kind, not whether it holds THIS kind, so turn 1 offers
// `project` ranked FIRST; the client commits the obviously-correct-looking
// option; turn 2's filterCandidatesByConfirmedKind deletes every candidate;
// applyConfirmedKindRescue finds nothing (the org has no such project); and
// the honest downstream reports land as no_match with subjectless terminal
// reason=authz_filtered_to_empty and a confidence-1.0 prior subject echoed
// back as skipped_failed_reauth.
//
// The repair is at the OFFER: a declared kind is offered only when the pool
// holds at least one candidate of that kind. CHAOS-4967's own ask sanctions
// it -- "The offer must include the frame's declared kind when one exists, or
// the clarification must not be raised for that need at all" -- and a kind
// with nothing behind it can be honoured under neither branch.

// TestKindOfferMaterial_DeclaredKindAbsentFromPoolSuppressesTheNeed is the
// red-first pin, on the qb-scoped pool shape verbatim: two teams and a pull
// request, frame-declared kind `project`. At 248d7ca0's rule this asserts RED
// (project IS in KindOptions, ranked 0).
//
// The declared kind cannot be offered, so CHAOS-4967's own disjunction leaves
// exactly one lawful outcome: do not raise the need at all. Offering the pool's
// own kinds instead would hand back a list that omits the kind the question
// names, which is the state CHAOS-4967 filed.
func TestKindOfferMaterial_DeclaredKindAbsentFromPoolSuppressesTheNeed(t *testing.T) {
	t.Parallel()
	poolKinds := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectPullRequest,
	}
	declaredKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectProject}

	material, diag := kindOfferMaterial(poolKinds, nil, declaredKinds, heldFromKinds(poolKinds...))

	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Fatalf("kindOfferMaterial() = %+v, want NO need raised -- the pool holds zero project candidates, so the offer can neither include the declared kind nor honestly omit it", material)
	}
	if !diag.SuppressedByUnservableDeclaredKind {
		t.Fatalf("diag = %+v, want SuppressedByUnservableDeclaredKind true", diag)
	}
	if diag.SuppressedByCardinality {
		t.Fatalf("diag = %+v, want SuppressedByCardinality FALSE -- the pool had two distinct kinds, so cardinality is not why this was suppressed, and conflating the two reasons would tell an operator the wrong thing", diag)
	}
	if diag.DeclaredHintCount != 0 || diag.DeclaredWithheldNotInPoolCount != 1 {
		t.Fatalf("diag = %+v, want DeclaredHintCount 0 and DeclaredWithheldNotInPoolCount 1", diag)
	}
	if !reflect.DeepEqual(diag.DeclaredWithheldNotInPoolKinds, []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectProject}) {
		t.Fatalf("diag.DeclaredWithheldNotInPoolKinds = %v, want [project]", diag.DeclaredWithheldNotInPoolKinds)
	}
}

// TestKindOfferMaterial_PartiallyServedDeclaredKindsStillRaiseTheNeed is the
// boundary between the two halves of the rule, and it is the reason suppression
// is decided on "did ANY declared kind reach the option list" rather than on the
// withheld count. Two declared kinds, ONE of them in the pool: the offer includes
// a declared kind, so CHAOS-4967's FIRST branch is satisfied, the need IS raised,
// and only the unservable sibling is withheld.
func TestKindOfferMaterial_PartiallyServedDeclaredKindsStillRaiseTheNeed(t *testing.T) {
	t.Parallel()
	poolKinds := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectPullRequest,
	}
	declaredKinds := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectProject,
	}

	material, diag := kindOfferMaterial(poolKinds, nil, declaredKinds, heldFromKinds(poolKinds...))

	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedExpectedKind {
		t.Fatalf("material.Missing = %v, want [expected_kind] -- a served declared kind means the offer CAN include the kind the question names", material.Missing)
	}
	if len(material.KindOptions) != 2 || material.KindOptions[0].Kind != contractsv1.ContextFabricSubjectTeam || material.KindOptions[1].Kind != contractsv1.ContextFabricSubjectPullRequest {
		t.Fatalf("KindOptions = %+v, want [team, pull_request] -- the served declared kind ranked first, the unservable one withheld", material.KindOptions)
	}
	if diag.SuppressedByUnservableDeclaredKind {
		t.Fatalf("diag = %+v, want SuppressedByUnservableDeclaredKind FALSE -- one declared kind was admitted", diag)
	}
	if diag.DeclaredHintCount != 1 || diag.DeclaredWithheldNotInPoolCount != 1 {
		t.Fatalf("diag = %+v, want DeclaredHintCount 1 (team) and DeclaredWithheldNotInPoolCount 1 (project)", diag)
	}
}

// TestKindOfferMaterial_ExplicitHintSurvivesAnUnservableDeclaredKind pins the
// precondition on the suppression: a caller-supplied ExpectedKinds hint is
// caller-verified intent on its own axis. Suppressing an offer the CALLER asked
// for because the FRAME declared something unservable would discard that intent,
// so the suppression only ever applies when there is no explicit hint at all.
func TestKindOfferMaterial_ExplicitHintSurvivesAnUnservableDeclaredKind(t *testing.T) {
	t.Parallel()
	poolKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}
	explicitKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectWorkItem}
	declaredKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectProject}

	material, diag := kindOfferMaterial(poolKinds, explicitKinds, declaredKinds, heldFromKinds(poolKinds...))

	if diag.SuppressedByUnservableDeclaredKind {
		t.Fatalf("diag = %+v, want SuppressedByUnservableDeclaredKind FALSE -- an explicit caller hint is present", diag)
	}
	if len(material.KindOptions) != 2 || material.KindOptions[0].Kind != contractsv1.ContextFabricSubjectWorkItem {
		t.Fatalf("KindOptions = %+v, want [work_item, team] with the explicit hint first", material.KindOptions)
	}
	// Control on the SAME fixture with the explicit hint removed: without it the
	// identical inputs DO suppress. Without this the assertion above would pass
	// for a rule that never suppresses anything.
	material, diag = kindOfferMaterial(poolKinds, nil, declaredKinds, heldFromKinds(poolKinds...))
	if !diag.SuppressedByUnservableDeclaredKind || len(material.KindOptions) != 0 {
		t.Fatalf("control: kindOfferMaterial(no explicit hint) = %+v diag=%+v, want suppressed", material, diag)
	}
}

// TestKindOfferMaterial_DeclaredKindPresentInPoolIsStillOfferedFirst is the
// complement on the SAME fixture: add one project candidate and CHAOS-4967's
// behaviour returns unchanged. This is the property the repair must NOT
// break -- the difference between the two tests is exactly one pool kind.
func TestKindOfferMaterial_DeclaredKindPresentInPoolIsStillOfferedFirst(t *testing.T) {
	t.Parallel()
	poolKinds := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectPullRequest,
		contractsv1.ContextFabricSubjectProject,
	}
	declaredKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectProject}

	material, diag := kindOfferMaterial(poolKinds, nil, declaredKinds, heldFromKinds(poolKinds...))

	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedExpectedKind {
		t.Fatalf("material.Missing = %v, want [expected_kind]", material.Missing)
	}
	if len(material.KindOptions) != 3 || material.KindOptions[0].Kind != contractsv1.ContextFabricSubjectProject {
		t.Fatalf("KindOptions = %+v, want project ranked FIRST ahead of the pool kinds (CHAOS-4967, preserved)", material.KindOptions)
	}
	if diag.DeclaredHintCount != 1 || diag.DeclaredWithheldNotInPoolCount != 0 {
		t.Fatalf("diag = %+v, want DeclaredHintCount 1 and DeclaredWithheldNotInPoolCount 0", diag)
	}
	if diag.DeclaredWithheldNotInPoolKinds != nil {
		t.Fatalf("diag.DeclaredWithheldNotInPoolKinds = %v, want nil", diag.DeclaredWithheldNotInPoolKinds)
	}
}

// TestKindOfferMaterial_NilPoolHeldKindsWithholdsDeclaredKinds pins the
// fail-closed reading of a missing membership set. The alternative -- treat
// "no information" as "present" -- is the CHAOS-5218 defect itself, so a
// caller that forgets to supply poolHeld must lose the declared kind, never
// silently regain the unservable offer.
func TestKindOfferMaterial_NilPoolHeldKindsWithholdsDeclaredKinds(t *testing.T) {
	t.Parallel()
	poolKinds := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectPullRequest,
	}
	declaredKinds := []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectTeam}

	// team IS in poolKinds; only the missing poolHeld withholds it, and with the
	// only declared kind withheld the whole need is suppressed.
	material, diag := kindOfferMaterial(poolKinds, nil, declaredKinds, nil)
	if diag.DeclaredHintCount != 0 || diag.DeclaredWithheldNotInPoolCount != 1 {
		t.Fatalf("diag = %+v, want a nil poolHeld to withhold every declared kind (fail closed)", diag)
	}
	if !diag.SuppressedByUnservableDeclaredKind || len(material.KindOptions) != 0 {
		t.Fatalf("material = %+v diag = %+v, want the need suppressed", material, diag)
	}

	// Positive control on the SAME inputs: with membership supplied, team is
	// offered and ranks first. Without this, the assertion above would pass
	// for a function that never offers a declared kind at all.
	material, diag = kindOfferMaterial(poolKinds, nil, declaredKinds, heldFromKinds(poolKinds...))
	if diag.DeclaredHintCount != 1 || diag.DeclaredWithheldNotInPoolCount != 0 {
		t.Fatalf("diag = %+v, want the supplied-membership control to OFFER team", diag)
	}
	if material.KindOptions[0].Kind != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("KindOptions = %+v, want team first in the control", material.KindOptions)
	}
}

// TestPoolHeldKindsOf_ReadsTheFullPoolNotTheTruncatedVisibleSet is why the
// membership set is its own argument rather than the poolKinds slice already
// passed. projectKindOfferKinds returns before==after (the TRUNCATED visible
// set) whenever committedCount > 0, but filterCandidatesByConfirmedKind
// narrows the UNTRUNCATED map -- so a kind truncation cut out of the visible
// set is still servable, and gating on the visible list would withdraw a
// declared kind the engine could have honoured.
func TestPoolHeldKindsOf_ReadsTheFullPoolNotTheTruncatedVisibleSet(t *testing.T) {
	t.Parallel()
	teamCandidate := offerPoolCandidate(contextfabric.SubjectTeam, "team:CHAOS")
	projectCandidate := offerPoolCandidate(contextfabric.SubjectProject, "project:atlas")
	fullPool := map[string]contextfabric.SubjectCandidate{
		SubjectKey(teamCandidate.Subject):    teamCandidate,
		SubjectKey(projectCandidate.Subject): projectCandidate,
	}
	// The visible set is the truncated one: project did not survive the cut.
	visible := []contextfabric.SubjectCandidate{teamCandidate}

	before, after := projectKindOfferKinds(visible, fullPool, 1 /* committedCount > 0: no repair */)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("projectKindOfferKinds(committed) = (%v, %v), want before == after", before, after)
	}
	if len(after) != 1 || after[0] != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("afterKinds = %v, want just [team] -- the truncated visible set", after)
	}

	held := poolHeldKindsOf(fullPool)
	if !held[contractsv1.ContextFabricSubjectProject] {
		t.Fatalf("poolHeldKindsOf = %v, want project HELD -- it is in the full pool even though truncation cut it from the visible set", held)
	}
	if !held[contractsv1.ContextFabricSubjectTeam] {
		t.Fatalf("poolHeldKindsOf = %v, want team held", held)
	}

	// Negative control: a kind in NEITHER set is not held.
	if held[contractsv1.ContextFabricSubjectCIRun] {
		t.Fatalf("poolHeldKindsOf = %v, want ci_pipeline_run NOT held", held)
	}
}

// TestOfferableKindsInPool_IsTheRepairProjectionsOwnPredicate pins the
// factoring: projectKindOfferKinds' repair loop and kindOfferMaterial's
// declared-kind gate must ask "does the pool hold this kind" through ONE
// definition, or the offer can widen on one rule and narrow on another.
func TestOfferableKindsInPool_IsTheRepairProjectionsOwnPredicate(t *testing.T) {
	t.Parallel()
	projectCandidate := offerPoolCandidate(contextfabric.SubjectProject, "project:atlas")
	teamCandidate := offerPoolCandidate(contextfabric.SubjectTeam, "team:CHAOS")
	// A kind OUTSIDE the offer vocabulary must be invisible to both.
	documentCandidate := offerPoolCandidate(contextfabric.SubjectKind("document"), "document:d1")
	fullPool := map[string]contextfabric.SubjectCandidate{
		SubjectKey(projectCandidate.Subject):  projectCandidate,
		SubjectKey(teamCandidate.Subject):     teamCandidate,
		SubjectKey(documentCandidate.Subject): documentCandidate,
	}

	// With nothing visible, the repair projection adds exactly the full
	// pool's offerable kinds -- which is offerableKindsInPool by definition.
	_, after := projectKindOfferKinds(nil, fullPool, 0)
	if !reflect.DeepEqual(after, offerableKindsInPool(fullPool)) {
		t.Fatalf("projectKindOfferKinds after = %v, want offerableKindsInPool = %v", after, offerableKindsInPool(fullPool))
	}
	for _, kind := range offerableKindsInPool(fullPool) {
		if kind == contractsv1.ContextFabricSubjectKind("document") {
			t.Fatalf("offerableKindsInPool = %v, want no document -- it is outside structureOfferKinds", offerableKindsInPool(fullPool))
		}
	}
	if len(offerableKindsInPool(fullPool)) != 2 {
		t.Fatalf("offerableKindsInPool = %v, want exactly [project, team]", offerableKindsInPool(fullPool))
	}
}

// TestResolveSubjects_QbScopedShapeRaisesNoNeedAndKeepsThePoolAcrossTurns is the
// END-TO-END half, through the production resolve path rather than the pure
// function, on the shape the three regressed corpus rows share: the frame
// declares `project`, retrieval returns a team and a pull request, and no
// project exists anywhere.
//
// Turn 1 must raise no expected_kind need at all, so there is no unservable
// option for a caller to commit. Turn 2 then runs with no confirmed kind and the
// candidate pool survives — which is the whole point: at 248d7ca0 turn 1 offered
// `project`, turn 2's confirmed-kind filter deleted every candidate, and the row
// came back no_match.
func TestResolveSubjects_QbScopedShapeRaisesNoNeedAndKeepsThePoolAcrossTurns(t *testing.T) {
	t.Parallel()
	teamNode := candidateNode(contextfabric.SubjectTeam, "team:CHAOS", "CHAOS", 0.55, "*")
	prNode := candidateNode(contextfabric.SubjectPullRequest, "pr:1", "CHAOS pull request", 0.5, "*")
	newBackend := func() *fakeGraphBackend {
		return &fakeGraphBackend{
			searchResults: map[string][]CandidateNode{"CHAOS": {teamNode, prNode}},
		}
	}
	frame := namedSubjectFrame("CHAOS", kindOf(contractsv1.ContextFabricSubjectProject))

	// Turn 1: no expected_kind need, and nothing for a caller to pick.
	tracer := &chaos4120Tracer{}
	turnOneDeps := newBackend().deps()
	turnOneDeps.ResolutionTracer = tracer
	_, offer, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("CHAOS"), turnOneDeps, nil, nil, frame, "")
	if err != nil {
		t.Fatalf("turn 1 ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	for _, need := range offer.Missing {
		if need == contractsv1.ContextFabricStructureNeedExpectedKind {
			t.Fatalf("turn 1 offer.Missing = %v, want NO expected_kind need -- the declared kind cannot be served and the pool's own kinds are not what the question asked about", offer.Missing)
		}
	}
	if len(offer.KindOptions) != 0 {
		t.Fatalf("turn 1 offer.KindOptions = %+v, want none", offer.KindOptions)
	}

	// The decision must be visible, not merely taken.
	withheld := tracer.eventsByStage("kind_offer_withheld")
	if len(withheld) != 1 {
		t.Fatalf("got %d kind_offer_withheld events, want exactly 1", len(withheld))
	}
	if withheld[0].KindOfferDeclaredWithheldNotInPoolCount != 1 {
		t.Fatalf("withheld count = %d, want 1", withheld[0].KindOfferDeclaredWithheldNotInPoolCount)
	}
	if !withheld[0].KindOfferSuppressedByUnservableDeclaredKind {
		t.Fatalf("withheld event = %+v, want KindOfferSuppressedByUnservableDeclaredKind true", withheld[0])
	}
	if !reflect.DeepEqual(withheld[0].KindOfferDeclaredWithheldKinds, []string{string(contractsv1.ContextFabricSubjectProject)}) {
		t.Fatalf("withheld kinds = %v, want [project]", withheld[0].KindOfferDeclaredWithheldKinds)
	}
	// The unconditional kind_offer line carries the same readings, so a 0 stays
	// observable.
	offers := tracer.eventsByStage("kind_offer")
	if len(offers) != 1 || offers[0].KindOfferDeclaredWithheldNotInPoolCount != 1 || !offers[0].KindOfferSuppressedByUnservableDeclaredKind {
		t.Fatalf("kind_offer events = %+v, want exactly 1 carrying withheld count 1 and the suppression reason", offers)
	}

	// Turn 2: with no offer there is no kind to confirm, so the pool survives.
	turnTwoDeps := newBackend().deps()
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("CHAOS"), turnTwoDeps, nil, nil, frame, "")
	if err != nil {
		t.Fatalf("turn 2 ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(resolution.Candidates) == 0 && len(resolution.Committed) == 0 {
		t.Fatalf("turn 2 left an EMPTY pool (candidates %d, committed %d) -- this is the regressed shape: the pool must survive when no unservable kind was ever offered", len(resolution.Candidates), len(resolution.Committed))
	}
}

// TestResolveSubjects_ServedDeclaredKindStillReachesTheOfferAndCommits is the
// end-to-end complement on the SAME shape with a project candidate present:
// the offer names project, and confirming it does NOT empty the pool. This
// is the turn-1-offer / turn-2-commit pair the ticket asks to be proven.
func TestResolveSubjects_ServedDeclaredKindStillReachesTheOfferAndCommits(t *testing.T) {
	t.Parallel()
	teamNode := candidateNode(contextfabric.SubjectTeam, "team:CHAOS", "CHAOS", 0.55, "*")
	projectNode := candidateNode(contextfabric.SubjectProject, "project:chaos", "CHAOS", 0.5, "*")
	newBackend := func() *fakeGraphBackend {
		return &fakeGraphBackend{
			searchResults: map[string][]CandidateNode{"CHAOS": {teamNode, projectNode}},
		}
	}
	frame := namedSubjectFrame("CHAOS", kindOf(contractsv1.ContextFabricSubjectProject))

	// Turn 1: the offer names the declared kind, because the pool serves it.
	turnOneDeps := newBackend().deps()
	tracer := &chaos4120Tracer{}
	turnOneDeps.ResolutionTracer = tracer
	_, offer, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("CHAOS"), turnOneDeps, nil, nil, frame, "")
	if err != nil {
		t.Fatalf("turn 1 ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	offered := false
	for _, option := range offer.KindOptions {
		if option.Kind == contractsv1.ContextFabricSubjectProject {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("turn 1 offer.KindOptions = %+v, want project offered -- the pool holds one", offer.KindOptions)
	}
	if got := len(tracer.eventsByStage("kind_offer_withheld")); got != 0 {
		t.Fatalf("got %d kind_offer_withheld events on turn 1, want 0 -- nothing was withheld", got)
	}

	// Turn 2: the caller commits the offered kind. The pool must survive.
	turnTwoDeps := newBackend().deps()
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contractsv1.ContextFabricSubjectProject}
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("CHAOS"), turnTwoDeps, confirmed, nil, frame, "")
	if err != nil {
		t.Fatalf("turn 2 ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(resolution.Candidates) == 0 && len(resolution.Committed) == 0 {
		t.Fatalf("turn 2 left an EMPTY pool (candidates %d, committed %d) -- confirming an offered, servable kind must not delete every candidate", len(resolution.Candidates), len(resolution.Committed))
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind != contractsv1.ContextFabricSubjectProject {
			t.Fatalf("turn 2 candidate %+v is not the confirmed kind -- the filter is still expected to narrow", candidate.Subject)
		}
	}
}

// TestChaos5218_ProductionSinkEmitsTheWithholdingAtTheProductionLogLevel is
// the sink-level pin, and it runs at slog.LevelInfo ON PURPOSE:
// internal/sidecar/config.go's defaultLogLevel is LevelInfo, so a decision
// line emitted at Debug does not exist in production at all. That is exactly
// why lane-corpus-bisect found no ResolutionTraceEvent line in any rig log,
// and why this stage is the one Info line on this sink. A test at LevelDebug
// would pass for a line no operator can ever read.
func TestChaos5218_ProductionSinkEmitsTheWithholdingAtTheProductionLogLevel(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	NewSlogResolutionTracer(logger).Trace(ResolutionTraceEvent{
		RequestID: "req_5218_sink", Stage: "kind_offer_withheld",
		KindOfferDeclaredWithheldNotInPoolCount:     1,
		KindOfferDeclaredWithheldKinds:              []string{string(contractsv1.ContextFabricSubjectProject)},
		KindOfferSuppressedByUnservableDeclaredKind: true,
		KindOfferDeclaredHintCount:                  0,
		KindOfferDistinctKindCount:                  2,
	})
	line := strings.TrimSpace(buffer.String())
	if line == "" {
		t.Fatal("the production sink emitted NOTHING at the production default log level -- a decision line below LevelInfo does not exist in production")
	}
	record := map[string]any{}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("trace line is not valid JSON: %v (%q)", err, line)
	}
	// Assertions read the emitted LINE, by the exact key an operator greps.
	if got := record["level"]; got != "INFO" {
		t.Fatalf("level = %v, want INFO", got)
	}
	if got := record["request_id"]; got != "req_5218_sink" {
		t.Fatalf("request_id = %v, want req_5218_sink -- the line must be joinable to its request", got)
	}
	if got := record["withheld_count"]; got != float64(1) {
		t.Fatalf("withheld_count = %v, want 1", got)
	}
	kinds, ok := record["withheld_kinds"].([]any)
	if !ok || len(kinds) != 1 || kinds[0] != string(contractsv1.ContextFabricSubjectProject) {
		t.Fatalf("withheld_kinds = %v, want [project]", record["withheld_kinds"])
	}
	if got := record["suppressed_by_unservable_declared_kind"]; got != true {
		t.Fatalf("suppressed_by_unservable_declared_kind = %v, want true -- asserted at a NON-ZERO value, since a bool asserted false would pass for a dropped assignment", got)
	}
	if !strings.Contains(line, "kind offer withheld") {
		t.Fatalf("line = %q, want the kind offer withheld message", line)
	}

	// NEGATIVE CONTROL for the level assertion itself: the unconditional
	// kind_offer stage is Debug, so at LevelInfo it emits nothing. Without
	// this, the test above would pass even if every stage were Info.
	var debugBuffer bytes.Buffer
	debugLogger := slog.New(slog.NewJSONHandler(&debugBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	NewSlogResolutionTracer(debugLogger).Trace(ResolutionTraceEvent{
		RequestID: "req_5218_sink", Stage: "kind_offer",
		KindOfferDeclaredWithheldNotInPoolCount: 1,
	})
	if got := strings.TrimSpace(debugBuffer.String()); got != "" {
		t.Fatalf("the kind_offer stage emitted %q at LevelInfo, want nothing -- the control proves this test measures the LEVEL, not merely the presence of a case", got)
	}
}

// offerPoolCandidate builds a minimal pool entry for the membership tests --
// only Subject.Kind and Subject.CanonicalID are read by poolHasKind /
// distinctOfferableKinds / SubjectKey.
func offerPoolCandidate(kind contextfabric.SubjectKind, canonicalID string) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		Subject: contextfabric.SubjectRef{Kind: kind, CanonicalID: canonicalID, Label: canonicalID},
	}
}

// TestKindOfferMaterial_WithholdingOnlyEverRemovesOptions is the 413 /
// wire-budget claim, asserted rather than argued: for every combination of
// pool kinds, explicit hints and declared kinds over the CLOSED offer
// vocabulary, the KindOptions this repair produces are a SUBSET of the ones
// CHAOS-4967's rule produced. The offer can therefore only shrink, so the
// change adds ZERO bytes to the wire in every case -- bounded (the 200-row
// answer bound) and unbounded alike -- and no requirement row is added.
//
// The CHAOS-4967 rule is reconstructed here as "every declared kind held" --
// poolHeld true for everything -- which is exactly what the old code did by
// never consulting membership at all. That is a reconstruction of the OLD
// behaviour from the CURRENT function, not a second implementation of the
// new one, so it cannot drift into agreeing with the fix by construction.
func TestKindOfferMaterial_WithholdingOnlyEverRemovesOptions(t *testing.T) {
	t.Parallel()
	vocabulary := sortedKinds(structureOfferKinds)
	allHeld := make(poolHeldKinds, len(vocabulary))
	for _, kind := range vocabulary {
		allHeld[kind] = true
	}

	shrank := 0
	// Exhaustive over every single-kind declared hint against every
	// single-kind and two-kind pool in the vocabulary: small enough to
	// enumerate, wide enough that a case where the offer GREW would appear.
	for _, declared := range vocabulary {
		for i, poolA := range vocabulary {
			for _, poolB := range vocabulary[i:] {
				poolKinds := []contractsv1.ContextFabricSubjectKind{poolA, poolB}
				declaredKinds := []contractsv1.ContextFabricSubjectKind{declared}

				old, _ := kindOfferMaterial(poolKinds, nil, declaredKinds, allHeld)
				now, _ := kindOfferMaterial(poolKinds, nil, declaredKinds, heldFromKinds(poolKinds...))

				oldKinds := map[contractsv1.ContextFabricSubjectKind]bool{}
				for _, option := range old.KindOptions {
					oldKinds[option.Kind] = true
				}
				for _, option := range now.KindOptions {
					if !oldKinds[option.Kind] {
						t.Fatalf("pool=%v declared=%v: KindOptions GREW -- %q is offered now and was not before; the wire budget claim rests on this never happening", poolKinds, declaredKinds, option.Kind)
					}
				}
				if len(now.KindOptions) > len(old.KindOptions) {
					t.Fatalf("pool=%v declared=%v: len(KindOptions) %d > %d", poolKinds, declaredKinds, len(now.KindOptions), len(old.KindOptions))
				}
				if len(now.KindOptions) < len(old.KindOptions) {
					shrank++
				}
			}
		}
	}
	// Positive control: the sweep must actually EXERCISE the difference. A
	// subset assertion over a set that never changes is vacuous.
	if shrank == 0 {
		t.Fatal("no combination in the sweep produced a smaller offer -- the subset assertion above is vacuous")
	}
	t.Logf("combinations where the offer shrank: %d", shrank)
}
