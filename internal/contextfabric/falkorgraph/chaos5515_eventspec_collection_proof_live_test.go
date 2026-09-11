package falkorgraph_test

// CHAOS-5515 A5 -- the collection-proof leg for the eventspec pilot
// (RankedCutSummary, AnchorSlotDisplaced), adapter-level per team-lead's
// ruling: a real falkorgraph.Adapter against a real, lane-labeled FalkorDB
// testcontainer, a real NewSlogResolutionTracer + slog.JSONHandler at
// production level, real contextfabric.ApplyProjectionBatch writes, and the
// REAL production entry point (Adapter.ResolveSubjects, which itself builds
// graphrank.ResolveDeps and calls graphrank.ResolveSubjectsWithCommitBasis --
// see reader.go). This is adapter-level container-backed proof; the full
// built-image HTTP leg over all backing stores is PR 6's collection matrix
// (RISK-NOTES).

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newLaneLabeledFalkorDBAdapter starts its OWN FalkorDB testcontainer (the
// same pinned image adapter_live_integration_test.go's newLiveAdapter uses)
// rather than reusing that shared helper, so this test controls the
// container's labels (lane name, per team-lead's operational rule for this
// lane's containers) without touching the shared live-test helpers other
// packages' tests depend on. Docker assigns the host port (ephemeral,
// never a reserved rig port).
func newLaneLabeledFalkorDBAdapter(t *testing.T, ctx context.Context, tracer graphrank.ResolutionTracer) *falkorgraph.Adapter {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: falkordbImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
			Labels: map[string]string{
				"lane":      "lane-thread-c-obs",
				"component": "eventspec-collection-proof",
			},
		},
		Started: true,
	})
	require.NoError(t, err, "start FalkorDB container")
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	t.Logf("lane-thread-c-obs FalkorDB collection-proof container: host=%s mappedPort=%s", host, port.Port())
	adapter, err := falkorgraph.New(falkorgraph.Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-live-eventspec-collection", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
		ResolutionTracer: tracer,
	})
	require.NoError(t, err, "construct falkorgraph.Adapter")
	return adapter
}

// TestLiveEventspecCertifiesTheAnchorSlotPilotThroughARealFalkorDBAdapter is
// A5's collection proof: the two piloted events, driven through the real
// production graph adapter against a real, containerized FalkorDB (not a
// Go fake), collected from the real slog JSON sink, certified against
// eventspec's declaration with exact multiplicity.
func TestLiveEventspecCertifiesTheAnchorSlotPilotThroughARealFalkorDBAdapter(t *testing.T) {
	ctx := context.Background()

	var buf bytes.Buffer
	tracer := graphrank.NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	adapter := newLaneLabeledFalkorDBAdapter(t, ctx, tracer)

	orgID := "live-eventspec-collection-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	const repoSlug = "full-chaos/eventspec-collection-proof"
	const term = "chaosproof"
	// The real falkorgraph.Adapter's Search HONORS the caller's own limit
	// per call (unlike the Go-fake fixture the piloted unit tests use,
	// which deliberately ignores it) -- one search term can never return
	// more than the MaxSubjectCandidates budget (20) below, so ONE term's
	// crowd can never reach phase 4 oversized. TWO distinct terms, each
	// capped at 20 but together exceeding the budget, is what a real
	// multi-term question does, and is what actually drives real
	// truncation -- discovered empirically (see RISK-NOTES).
	termsAndCounts := []struct {
		term  string
		count int
	}{
		{term + "a", 13},
		{term + "b", 13},
	}

	observed := time.Now().UTC()
	var entities []contextfabric.EntityProjection
	var subjectTerms []string
	for _, tc := range termsAndCounts {
		subjectTerms = append(subjectTerms, tc.term)
		for i := 0; i < tc.count; i++ {
			entities = append(entities, contextfabric.EntityProjection{
				Subject: contextfabric.SubjectRef{
					Kind: contextfabric.SubjectProject, CanonicalID: fmt.Sprintf("project.v2:github:%s-%02d", tc.term, i),
					Label: fmt.Sprintf("%s project %02d", tc.term, i),
				},
				Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
				Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{repoSlug}},
				EvidenceRefIDs: []string{"evidence_" + tc.term},
				ObservedAt:     observed, SourceVersion: "v1",
			})
		}
	}
	// One real TEAM-kind candidate, same term set as the project crowd, so
	// the pool is saturated with TWO reserved kinds (project, the frame's
	// MemberKind; team, the scope anchor's own kind) and zero non-reserved
	// candidates -- the exact shape CHAOS-5434 exists for: the ORDINARY
	// victim rule has nothing non-reserved to evict, so only the WIDENED
	// rule this ticket's pilot certifies can rescue it.
	entities = append(entities, contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{
			Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:" + termsAndCounts[0].term + "-team",
			Label: termsAndCounts[0].term + " team",
		},
		Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
		Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{repoSlug}},
		EvidenceRefIDs: []string{"evidence_" + termsAndCounts[0].term + "_team"},
		ObservedAt:     observed, SourceVersion: "v1",
	})
	crowd := len(entities)
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_" + term + "_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: entities, Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{repoSlug}}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("ResolveInvestigationBinding() error = %v", err)
	}

	req := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_" + term + "00000001",
		Question: "What is driving " + term + "?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 20, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: subjectTerms,
		TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	frame := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: subjectTerms, MemberKind: contextfabric.SubjectProject,
			},
		},
	}

	if _, _, _, _, err := adapter.ResolveSubjects(ctx, principal, req, interpreted, binding,
		nil, nil, frame, contextfabric.SubjectTeam); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	t.Logf("collected log (%d bytes):\n%s", buf.Len(), buf.String())

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real containerized-FalkorDB production output error = %v", err)
	}

	// candidate_count is deterministic for this fixture: every seeded
	// entity is authorized, matches exactly one of the two search terms
	// (or the anchor's own kind), and nothing else in a freshly purged org
	// competes -- so the collected set is asserted to the EXACT declared
	// multiplicity, not merely ">=". pool_truncated_n = crowd - budget for
	// the identical reason.
	summaryResult, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.RankedCutSummary,
		Want: map[string]any{
			"request_id": req.RequestID,
			// r3: Want must include "pass" for a pass-keyed event -- this
			// fixture's single resolveSubjects call never re-decides.
			"pass":                  1,
			"stage":                 "ranked_cut",
			"max":                   20,
			"candidate_count":       crowd,
			"survived_count":        20,
			"anchor_slot_reserved":  string(contextfabric.SubjectTeam),
			"anchor_slot_source":    "receipt",
			"anchor_slot_displaced": 1,
			"pool_truncated_n":      crowd - 20,
		},
	})
	if err != nil {
		t.Fatalf("certify RankedCutSummary: %v (real fulltext search over a real FalkorDB may not have surfaced the seeded %d-member crowd through 'Search' -- see this test's own t.Log for the raw collected line)", err, crowd)
	}
	t.Logf("RankedCutSummary line: %v", summaryResult.Line)

	// Surface enumeration: every RankedCutSummary field Certify's Want
	// mechanism cannot express a nested value for (survived_ids' own
	// length, declared_kind_rescue's nested per-kind rows) is still
	// asserted here, directly against the real collected line -- the
	// full declared field set is exercised, not the flat subset Want
	// alone can reach.
	survivedIDs, _ := summaryResult.Line["survived_ids"].([]any)
	if len(survivedIDs) != 20 {
		t.Errorf("survived_ids has %d entries, want 20 (== survived_count)", len(survivedIDs))
	}
	rescueRows, _ := summaryResult.Line["declared_kind_rescue"].([]any)
	if len(rescueRows) != 2 {
		t.Fatalf("declared_kind_rescue has %d rows, want 2 (project + team)", len(rescueRows))
	}
	rescueByKind := map[string]map[string]any{}
	for _, raw := range rescueRows {
		row, _ := raw.(map[string]any)
		kind, _ := row["kind"].(string)
		rescueByKind[kind] = row
	}
	if got := rescueByKind["project"]["state"]; got != "ran_matched_survived" {
		t.Errorf("declared_kind_rescue[project].state = %v, want ran_matched_survived", got)
	}
	if got := rescueByKind["team"]["state"]; got != "ran_matched_survived" {
		t.Errorf("declared_kind_rescue[team].state = %v, want ran_matched_survived", got)
	}
	teamMatched, _ := rescueByKind["team"]["matched"].(float64)
	if teamMatched != 1 {
		t.Errorf("declared_kind_rescue[team].matched = %v, want 1 (the one seeded team candidate)", rescueByKind["team"]["matched"])
	}

	// The displacement line: the real widened victim rule evicted a real
	// PROJECT candidate to admit the real TEAM candidate that ranking alone
	// left outside the budget -- the CHAOS-5434 mechanism this pilot
	// certifies, exercised through real production code against a real
	// graph, not a Go fake.
	displacedResult, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.AnchorSlotDisplaced,
		Want: map[string]any{
			"request_id":            req.RequestID,
			"pass":                  1,
			"stage":                 "anchor_slot_displaced",
			"anchor_slot_reserved":  string(contextfabric.SubjectTeam),
			"anchor_slot_source":    "receipt",
			"anchor_slot_displaced": 1,
			"pool_truncated_n":      crowd - 20,
			"subject_kind":          string(contextfabric.SubjectProject),
		},
	})
	if err != nil {
		t.Fatalf("certify AnchorSlotDisplaced: %v", err)
	}
	t.Logf("AnchorSlotDisplaced line: %v", displacedResult.Line)

	// The displaced subject must itself be one of the seeded project
	// candidates -- the collection is checked, not merely counted.
	displacedID, _ := displacedResult.Line["subject_canonical_id"].(string)
	found := false
	for _, e := range entities {
		if e.Subject.CanonicalID == displacedID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("displaced subject_canonical_id = %q, not one of the seeded entities -- the collected identity does not trace back to this fixture", displacedID)
	}
	// r1 P3: the reported victim must ALSO be genuinely absent from
	// survived_ids -- naming a subject as displaced while it still
	// actually survived would be a self-contradictory certificate.
	for _, v := range summaryResult.Line["survived_ids"].([]any) {
		if v == displacedID {
			t.Errorf("displaced subject_canonical_id %q is ALSO present in RankedCutSummary.survived_ids -- a subject reported displaced must be genuinely absent from what survived", displacedID)
		}
	}

	// CHAOS-5516 B7 -- the collection-proof leg for the decision_summary
	// pilot scope (typed construction, clauses 2+4), per the ticket's own
	// "Acceptance = certificate + collection proof in the same PR. PR 6
	// cannot serve as deferred acceptance for this PR." This is the SAME
	// real production entry point (adapter.ResolveSubjects), the SAME real
	// containerized FalkorDB, and the SAME collected slog output A5 above
	// already drove and captured -- decision_summary is emitted on this
	// exact call (it flushes unconditionally, once per resolveSubjects
	// call), so this leg reads it from the SAME log rather than a second
	// resolution.
	t.Logf("decision_summary raw line (collection proof): %v", log.LinesWithMsg(eventspec.DecisionSummary.Msg))
	decisionSummaryResult, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.DecisionSummary,
		Want: map[string]any{
			"request_id": req.RequestID,
			"stage":      "decision_summary",
			// This call passed confirmedKind=nil to adapter.ResolveSubjects
			// (line 179 below) -- the same construction-time stamp
			// TestAFailedResolutionStillCarriesExplicitNoneTokens proves.
			// "none" is graphrank's own unexported anchorPoolKindScopeNone
			// token value -- this file is package falkorgraph_test (an
			// external test package), so it is cited by value, not by name.
			"member_kind_confirmed": "none",
			// The frame built above (line 168) passes ValidateFramePhaseA1
			// and cohortKindFromFrame cleanly -- the same real, executed
			// verdict frameGateObservable produces for every other frame in
			// this package's tests that reaches a decision at all.
			"frame_gate":   "passed",
			"refuse_basis": "none",
			// The anchor-pool scope this call decided is TEAM (the same
			// scope RankedCutSummary/AnchorSlotDisplaced above certify
			// against real collected data) -- decision_summary's own copy
			// of the wiring it handed to the same three consumers.
			"anchor_pool_kind_scope":        string(contextfabric.SubjectTeam),
			"anchor_pool_kind_scope_source": "receipt",
			// reserved_kinds is BOTH kinds this call's frame reserved
			// (project, the frame's own MemberKind; team, the scope
			// anchor's kind) -- the real, executed value observed from this
			// same collected log (t.Log above), not assumed from the
			// scope-anchor value alone.
			"reserved_kinds": []string{string(contextfabric.SubjectProject), string(contextfabric.SubjectTeam)},
			// This question runs one real commit decision over the crowd
			// (decision_event_count=1) that resolves AMBIGUOUS, not
			// committed -- the real, executed outcome for this fixture's
			// own terms/corroboration shape, observed from this same
			// collected log rather than assumed.
			"decision_event_count": 1,
			"committed_count":      0,
			"ambiguous_count":      1,
			"no_commit_count":      0,
			"committed_ids":        []string{},
			"commit_gates":         []string{},
			"commit_bases":         []string{},
		},
	})
	if err != nil {
		t.Fatalf("certify DecisionSummary (collection proof, real containerized FalkorDB production output): %v", err)
	}
	t.Logf("DecisionSummary line: %v", decisionSummaryResult.Line)

	// GRAPH REBUILD (chris, 2026-09-10, cf-lane-rules): "If we can't build a
	// graph from the trace we didn't add the right / enough observability."
	// This reconstructs the pilot's own decision graph -- requested ->
	// measured -> decision+reason -> served -- from the COLLECTED JSON
	// ALONE (certify.Log lines only), never from `res` (the Go result this
	// test never even captures) or any other struct. See RISK-NOTES for
	// which of the four states RankedCutSummary/AnchorSlotDisplaced
	// themselves carry, and which neighbouring (not-yet-certified) lines in
	// the SAME pass this reconstruction also reads.
	t.Run("graph rebuild from the collected trace alone", func(t *testing.T) {
		// 1. PRE-ENTRY (requested): what this pass asked to reserve, before
		// any candidate was measured or cut.
		requested := log.LinesWithMsg("context fabric resolution trace: anchor pool kind scope")
		if len(requested) != 1 {
			t.Fatalf("requested-state line count = %d, want 1", len(requested))
		}
		if got := requested[0]["anchor_pool_kind_scope"]; got != string(contextfabric.SubjectTeam) {
			t.Fatalf("requested anchor_pool_kind_scope = %v, want %q", got, contextfabric.SubjectTeam)
		}

		// 2. PRE-DECISION (measured): the population size the cut actually
		// saw, read from RankedCutSummary's own candidate_count -- already
		// certified above; re-derived here from the raw line only.
		measured, _ := summaryResult.Line["candidate_count"].(float64)
		if int(measured) != crowd {
			t.Fatalf("measured candidate_count = %v, want %d", summaryResult.Line["candidate_count"], crowd)
		}

		// 3. DECISION + REASON: the reserved-slot decision and WHY -- which
		// candidate it cost. Two lines, cross-checked against each other:
		// the summary's own count must agree with the named displacement.
		displacedCount, _ := summaryResult.Line["anchor_slot_displaced"].(float64)
		if int(displacedCount) != 1 {
			t.Fatalf("decision anchor_slot_displaced = %v, want 1", summaryResult.Line["anchor_slot_displaced"])
		}
		reason := displacedResult.Line["subject_canonical_id"]
		if reason == nil || reason == "" {
			t.Fatal("decision reason (the displaced subject) is empty -- a decision with no reason is not rebuildable")
		}

		// 4. POST-DECISION (served): the rescued candidate must appear BOTH
		// in the summary's own survived_ids (what the cut actually kept)
		// AND in a "reserved kind admitted" line naming it survived=true
		// (the operator-visible admission proof) -- two independent lines
		// agreeing on the same identity is what makes "served" verifiable
		// from the trace, not asserted from one line alone.
		survivedIDsRaw, _ := summaryResult.Line["survived_ids"].([]any)
		rescuedID := "team.v2:github:" + termsAndCounts[0].term + "-team"
		servedInSurvivedIDs := false
		for _, v := range survivedIDsRaw {
			if v == rescuedID {
				servedInSurvivedIDs = true
				break
			}
		}
		if !servedInSurvivedIDs {
			t.Fatalf("served subject %q not present in RankedCutSummary.survived_ids %v", rescuedID, survivedIDsRaw)
		}
		admitted := log.LinesWithMsg("context fabric resolution trace: reserved kind admitted")
		var admittedServed bool
		for _, line := range admitted {
			if line["subject_canonical_id"] == rescuedID && line["survived"] == true {
				admittedServed = true
			}
		}
		if !admittedServed {
			t.Fatalf("no \"reserved kind admitted\" line names %q with survived=true -- served state not observable", rescuedID)
		}

		// THE FULL CHAIN, restated as one assertion: requested kind ==
		// decision's reserved kind == the served subject's own kind ==
		// team, and the served subject is the SAME identity the decision
		// displaced a project FOR. Rebuilt entirely from JSON keys read
		// above -- no struct field was read anywhere in this subtest.
		t.Logf("graph rebuild: requested(kind=%v) -> measured(candidates=%v) -> decision(displaced=%v, reason=%v) -> served(id=%v, survived=true)",
			requested[0]["anchor_pool_kind_scope"], int(measured), int(displacedCount), reason, rescuedID)
	})
}
