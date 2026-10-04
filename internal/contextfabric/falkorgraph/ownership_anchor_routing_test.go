package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const ownedSlug = "full-chaos/dev-health-acr"

func ownedRepository() contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acr", Label: ownedSlug}
}

// namedByLabelRequest is a team-members request whose one committed
// repository carries the statistical basis, which never binds, and whose
// candidate matched the frame's anchor term only when matched is true.
func namedByLabelRequest(declared contextfabric.SubjectKind, matched bool) contextfabric.GraphDiscoveryRequest {
	anchor := ownedRepository()
	request := ownershipRoutingRequest(repositoryAnchorFrame(), anchor)
	request.Bases = contextfabric.CommitBasisSet{}
	request.Bases.Record(anchor, contextfabric.CommitBasisStatistical)
	if !matched {
		request.Resolution.Candidates = nil
	}
	request.ScopeAnchorKind = declared
	return request
}

func teamRow(id string, repos ...string) row {
	r := fakeSubjectNodeRow("team", id, id)
	r["n"].(*node).Properties[propAuthzRepos] = repos
	return r
}

// ownershipConn answers the ownership census with census, the full-text index
// with text, and counts the census reads. Any other read returns no rows.
func ownershipConn(census []row, text []row, censusReads *int) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "$kinds"):
			*censusReads++
			return census, nil
		case strings.Contains(cypher, "fulltext"):
			return text, nil
		}
		return nil, nil
	}}
}

func ownedMemberIDs(result contextfabric.GraphContext) []string {
	var ids []string
	if result.Cohort != nil {
		for _, m := range result.Cohort.Members {
			ids = append(ids, m.Subject.CanonicalID)
		}
	}
	sort.Strings(ids)
	return ids
}

func twoOwnersCensus() []row {
	return []row{
		teamRow("team:one", ownedSlug), teamRow("team:two", ownedSlug, "full-chaos/other"),
		teamRow("team:none", "full-chaos/other"),
	}
}

// TestASoleCommittedRepositoryNamedByItsLabelRoutesThroughOwnership pins the
// served state of a repository named by its label under a declared repository
// anchor kind.
func TestASoleCommittedRepositoryNamedByItsLabelRoutesThroughOwnership(t *testing.T) {
	telemetry := &recordingTelemetry{}
	reads := 0
	adapter := newFakeAdapterWithTelemetry(t, ownershipConn(twoOwnersCensus(), nil, &reads), telemetry)
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, namedByLabelRequest(contextfabric.SubjectRepository, true))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if reads != 1 {
		t.Fatalf("ownership census reads = %d, want 1", reads)
	}
	if got := ownedMemberIDs(result); !reflect.DeepEqual(got, []string{"team:one", "team:two"}) {
		t.Fatalf("members = %v, want exactly the owning teams", got)
	}
	if result.CohortMemberSource != contextfabric.CohortMemberSourceOwnership {
		t.Errorf("CohortMemberSource = %q", result.CohortMemberSource)
	}
	for _, m := range result.Cohort.Members {
		if !reflect.DeepEqual(m.InclusionReasons, []string{ownershipInclusionReason}) {
			t.Errorf("member %s reasons = %q", m.Subject.CanonicalID, m.InclusionReasons)
		}
	}
	if result.Cohort.Rationale != ownershipCohortRationale {
		t.Errorf("rationale = %q", result.Cohort.Rationale)
	}
	want := OwnershipRoutingDecision{
		Outcome: OwnershipRoutingOwners, AnchorKind: contextfabric.SubjectRepository, AnchorBasis: AnchorBasisSoleCommit,
		Committed: 1, Census: 3, Owners: 2,
	}
	if len(telemetry.ownershipRoutings) != 1 || telemetry.ownershipRoutings[0] != want {
		t.Fatalf("decisions = %+v, want one %+v", telemetry.ownershipRoutings, want)
	}
}

// TestARepositoryUnderADifferentDeclaredAnchorKindIsNotRouted: the census must
// not run and the line says so.
func TestARepositoryUnderADifferentDeclaredAnchorKindIsNotRouted(t *testing.T) {
	telemetry := &recordingTelemetry{}
	reads := 0
	adapter := newFakeAdapterWithTelemetry(t, ownershipConn(twoOwnersCensus(), nil, &reads), telemetry)
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, namedByLabelRequest(contextfabric.SubjectProject, true))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if reads != 0 {
		t.Fatalf("ownership census ran %d times for a project-declared reading", reads)
	}
	if result.CohortMemberSource == contextfabric.CohortMemberSourceOwnership {
		t.Errorf("CohortMemberSource = %q", result.CohortMemberSource)
	}
	want := OwnershipRoutingDecision{Outcome: OwnershipRoutingNotRouted, AnchorKind: contextfabric.SubjectRepository, AnchorBasis: AnchorBasisNone, Committed: 1}
	if len(telemetry.ownershipRoutings) != 1 || telemetry.ownershipRoutings[0] != want {
		t.Fatalf("decisions = %+v, want one %+v", telemetry.ownershipRoutings, want)
	}
}

// TestAnUndeclaredAnchorKindRoutesOnlyWhenTheCandidateMatchedAnAnchorTerm.
func TestAnUndeclaredAnchorKindRoutesOnlyWhenTheCandidateMatchedAnAnchorTerm(t *testing.T) {
	for _, tc := range []struct {
		name    string
		matched bool
		reads   int
		outcome OwnershipRoutingOutcome
		basis   AnchorBasis
	}{
		{"term matched", true, 1, OwnershipRoutingOwners, AnchorBasisSoleCommit},
		{"no term matched", false, 0, OwnershipRoutingNotRouted, AnchorBasisNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			telemetry := &recordingTelemetry{}
			reads := 0
			adapter := newFakeAdapterWithTelemetry(t, ownershipConn(twoOwnersCensus(), nil, &reads), telemetry)
			result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, namedByLabelRequest("", tc.matched))
			if err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if reads != tc.reads {
				t.Fatalf("census reads = %d, want %d", reads, tc.reads)
			}
			if (result.CohortMemberSource == contextfabric.CohortMemberSourceOwnership) != (tc.reads == 1) {
				t.Errorf("CohortMemberSource = %q", result.CohortMemberSource)
			}
			if len(telemetry.ownershipRoutings) != 1 || telemetry.ownershipRoutings[0].Outcome != tc.outcome || telemetry.ownershipRoutings[0].AnchorBasis != tc.basis {
				t.Fatalf("decisions = %+v, want one with outcome %q basis %q", telemetry.ownershipRoutings, tc.outcome, tc.basis)
			}
		})
	}
}

// TestABoundRepositoryAnchorIsRecordedAsBound.
func TestABoundRepositoryAnchorIsRecordedAsBound(t *testing.T) {
	telemetry := &recordingTelemetry{}
	reads := 0
	adapter := newFakeAdapterWithTelemetry(t, ownershipConn(twoOwnersCensus(), nil, &reads), telemetry)
	request := ownershipRoutingRequest(repositoryAnchorFrame(), ownedRepository())
	if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if reads != 1 || len(telemetry.ownershipRoutings) != 1 || telemetry.ownershipRoutings[0].AnchorBasis != AnchorBasisBound {
		t.Fatalf("census reads = %d, decisions = %+v, want one bound decision", reads, telemetry.ownershipRoutings)
	}
}

func textTeamRow(id string, score float64, repos ...string) row {
	r := fulltextRow("team", id, id, "team ownership", &score)
	r["node"].(*node).Properties[propAuthzRepos] = repos
	return r
}

// TestAQuestionTextMatchDoesNotAddANonOwnerToAnOwnershipRoutedCohort.
func TestAQuestionTextMatchDoesNotAddANonOwnerToAnOwnershipRoutedCohort(t *testing.T) {
	reads := 0
	text := []row{textTeamRow("team:text-only", 2, "full-chaos/other"), textTeamRow("team:one", 1, ownedSlug)}
	census := []row{teamRow("team:one", ownedSlug)}
	adapter := newFakeAdapter(t, ownershipConn(census, text, &reads))
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, namedByLabelRequest(contextfabric.SubjectRepository, true))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if got := ownedMemberIDs(result); !reflect.DeepEqual(got, []string{"team:one"}) {
		t.Fatalf("members = %v, want the owner once", got)
	}
}

// TestACutQuestionTextArmDoesNotTruncateAnOwnershipCohort.
func TestACutQuestionTextArmDoesNotTruncateAnOwnershipCohort(t *testing.T) {
	reads := 0
	var text []row
	for i := 0; i < 30; i++ {
		text = append(text, textTeamRow(fmt.Sprintf("team:noise-%02d", i), 1, "full-chaos/other"))
	}
	census := []row{teamRow("team:one", ownedSlug)}
	adapter := newFakeAdapter(t, ownershipConn(census, text, &reads))
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, namedByLabelRequest(contextfabric.SubjectRepository, true))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if got := ownedMemberIDs(result); !reflect.DeepEqual(got, []string{"team:one"}) {
		t.Fatalf("members = %v, want the owner only", got)
	}
	if !result.Cohort.Complete || result.Cohort.Truncated {
		t.Fatalf("cohort complete = %v truncated = %v, want complete and not truncated", result.Cohort.Complete, result.Cohort.Truncated)
	}
}

// TestACutQuestionTextArmIsNoLossWhenNoTeamOwnsTheRepository: with no owner in
// the ownership read, nothing covers the cut question-text arm, and the arm
// still cannot add a member, so the pool is not reported as cut.
func TestACutQuestionTextArmIsNoLossWhenNoTeamOwnsTheRepository(t *testing.T) {
	reads := 0
	var text []row
	for i := 0; i < 30; i++ {
		text = append(text, textTeamRow(fmt.Sprintf("team:noise-%02d", i), 1, "full-chaos/other"))
	}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, ownershipConn(nil, text, &reads), telemetry)
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, namedByLabelRequest(contextfabric.SubjectRepository, true))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if reads != 1 || result.Cohort != nil {
		t.Fatalf("census reads = %d, cohort = %+v, want one ownership read and no cohort", reads, result.Cohort)
	}
	if len(telemetry.cohortKindBases) != 1 || telemetry.cohortKindBases[0].poolTruncation == CohortPoolTruncationTruncated {
		t.Fatalf("cohort kind basis = %+v, want one line whose pool is not cut: no team the question text matched can join the cohort", telemetry.cohortKindBases)
	}
}

type ownershipOutcomeFixture struct {
	request  func() contextfabric.GraphDiscoveryRequest
	census   []row
	failRead bool
	check    func(*testing.T, OwnershipRoutingDecision)
}

func ownershipOutcomeFixtures() map[OwnershipRoutingOutcome]ownershipOutcomeFixture {
	owned := func() contextfabric.GraphDiscoveryRequest {
		return namedByLabelRequest(contextfabric.SubjectRepository, true)
	}
	return map[OwnershipRoutingOutcome]ownershipOutcomeFixture{
		OwnershipRoutingNotRouted: {
			request: func() contextfabric.GraphDiscoveryRequest {
				return namedByLabelRequest(contextfabric.SubjectProject, false)
			},
			census: twoOwnersCensus(),
			check: func(t *testing.T, d OwnershipRoutingDecision) {
				if d.Census != 0 || d.Owners != 0 || d.Err != nil || d.AnchorKind != contextfabric.SubjectRepository {
					t.Errorf("decision = %+v", d)
				}
			},
		},
		OwnershipRoutingOwners: {
			request: owned, census: twoOwnersCensus(),
			check: func(t *testing.T, d OwnershipRoutingDecision) {
				if d.Census != 3 || d.Owners != 2 || d.Err != nil {
					t.Errorf("decision = %+v", d)
				}
			},
		},
		OwnershipRoutingNoOwner: {
			request: owned, census: []row{teamRow("team:none", "full-chaos/other")},
			check: func(t *testing.T, d OwnershipRoutingDecision) {
				if d.Census != 1 || d.Owners != 0 || d.Err != nil {
					t.Errorf("decision = %+v", d)
				}
			},
		},
		OwnershipRoutingReadFailed: {
			request: owned, failRead: true,
			check: func(t *testing.T, d OwnershipRoutingDecision) {
				if d.Err == nil || d.Census != 0 || d.Owners != 0 {
					t.Errorf("decision = %+v", d)
				}
			},
		},
	}
}

func runOwnershipOutcome(t *testing.T, fixture ownershipOutcomeFixture) ([]OwnershipRoutingDecision, []byte) {
	t.Helper()
	var decisions []OwnershipRoutingDecision
	var logged []byte
	for _, sink := range []string{"recording", "slog"} {
		reads := 0
		conn := ownershipConn(fixture.census, nil, &reads)
		if fixture.failRead {
			inner := conn.queryFunc
			conn.queryFunc = func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
				if strings.Contains(cypher, "$kinds") {
					return nil, errors.New("connection reset")
				}
				return inner(ctx, key, cypher, params, readOnly)
			}
		}
		var buf bytes.Buffer
		recording := &recordingTelemetry{}
		var telemetry GraphTelemetry = recording
		if sink == "slog" {
			telemetry = SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
		}
		adapter := newFakeAdapterWithTelemetry(t, conn, telemetry)
		_, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, fixture.request())
		if (err != nil) != fixture.failRead {
			t.Fatalf("DiscoverContext() error = %v, want an error only for a failed read (%v)", err, fixture.failRead)
		}
		decisions = append(decisions, recording.ownershipRoutings...)
		logged = buf.Bytes()
	}
	return decisions, logged
}

// TestEveryOwnershipRoutingOutcomeIsEmittedByTheRealProducer quantifies over
// the outcome vocabulary: each value has a fixture, the real DiscoverContext
// emits exactly one decision with it, and the line the real slog sink writes
// certifies against the declared event.
func TestEveryOwnershipRoutingOutcomeIsEmittedByTheRealProducer(t *testing.T) {
	fixtures := ownershipOutcomeFixtures()
	vocabulary := OwnershipRoutingOutcomeVocabulary()
	if len(fixtures) != len(vocabulary) {
		t.Fatalf("%d fixtures for %d declared outcomes", len(fixtures), len(vocabulary))
	}
	for _, outcome := range vocabulary {
		fixture, ok := fixtures[outcome]
		if !ok {
			t.Fatalf("outcome %q has no fixture", outcome)
		}
		t.Run(string(outcome), func(t *testing.T) {
			decisions, logged := runOwnershipOutcome(t, fixture)
			if len(decisions) != 1 || decisions[0].Outcome != outcome {
				t.Fatalf("decisions = %+v, want exactly one with outcome %q", decisions, outcome)
			}
			fixture.check(t, decisions[0])

			log, err := certify.Parse(logged)
			if err != nil {
				t.Fatalf("certify.Parse() error = %v", err)
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.OwnershipRouting,
				Want:  map[string]any{"org_id": "org-1", "outcome": string(outcome), "committed": 1},
			}); err != nil {
				t.Fatalf("certify.Certify() error = %v", err)
			}
			lines := log.LinesWithMsg(eventspec.OwnershipRouting.Msg)
			if len(lines) != 1 {
				t.Fatalf("%d lines, want one", len(lines))
			}
			for _, key := range []string{"census", "owners", "truncated"} {
				measured := outcome != OwnershipRoutingNotRouted && outcome != OwnershipRoutingReadFailed
				if _, present := lines[0][key]; present != measured {
					t.Errorf("line %v: %s present = %v; a read count rides only on a read that finished", lines[0], key, present)
				}
			}
			if _, present := lines[0]["error"]; present != (outcome == OwnershipRoutingReadFailed) {
				t.Errorf("line %v: error present = %v", lines[0], present)
			}
			if outcome == OwnershipRoutingOwners && (lines[0]["census"] != float64(3) || lines[0]["owners"] != float64(2)) {
				t.Errorf("line %v: want census 3 owners 2", lines[0])
			}
			for key, value := range lines[0] {
				if text, isText := value.(string); isText && key != "error" && (strings.Contains(text, "dev-health-acr") || strings.Contains(text, "team:")) {
					t.Errorf("line field %s = %q names a subject; the line carries counts and closed values only", key, text)
				}
			}
		})
	}
}

// TestADiscoveryThatAsksNoTeamMembersOfANamedAnchorWritesNoOwnershipLine.
func TestADiscoveryThatAsksNoTeamMembersOfANamedAnchorWritesNoOwnershipLine(t *testing.T) {
	discoveredTeams := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	anchor := ownedRepository()
	for name, request := range map[string]contextfabric.GraphDiscoveryRequest{
		"deployment members": ownershipRoutingRequest(deploymentMembersFrame(), anchor),
		"no frame":           fakeDiscoveryRequest(anchor, 10),
		"discovered teams":   ownershipRoutingRequest(discoveredTeams, anchor),
	} {
		t.Run(name, func(t *testing.T) {
			telemetry := &recordingTelemetry{}
			adapter := newFakeAdapterWithTelemetry(t, &fakeConn{}, telemetry)
			if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			if len(telemetry.ownershipRoutings) != 0 {
				t.Fatalf("decisions = %+v, want none", telemetry.ownershipRoutings)
			}
		})
	}
}

// TestTwoUnboundCommitsWriteANotRoutedOwnershipLineWithNoAnchorKind.
func TestTwoUnboundCommitsWriteANotRoutedOwnershipLineWithNoAnchorKind(t *testing.T) {
	var buf bytes.Buffer
	adapter := newFakeAdapterWithTelemetry(t, &fakeConn{}, SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))})
	request := namedByLabelRequest(contextfabric.SubjectRepository, false)
	request.Resolution.Committed = append(request.Resolution.Committed,
		contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:other", Label: "full-chaos/other"})
	if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.OwnershipRouting,
		Want:  map[string]any{"org_id": "org-1", "outcome": "not_routed", "anchor_kind": "none", "anchor_basis": "none", "committed": 2},
	}); err != nil {
		t.Fatalf("certify.Certify() error = %v", err)
	}
}

func TestOwnershipRoutingClosedVocabulariesMatchEventspec(t *testing.T) {
	declared := map[string][]string{}
	for _, f := range eventspec.OwnershipRouting.Fields {
		declared[f.Key] = f.ClosedVocabulary
	}
	var outcomes, bases []string
	for _, o := range OwnershipRoutingOutcomeVocabulary() {
		outcomes = append(outcomes, string(o))
	}
	for _, b := range AnchorBasisVocabulary() {
		bases = append(bases, string(b))
	}
	if !reflect.DeepEqual(declared["outcome"], outcomes) {
		t.Errorf("eventspec declares outcome %v, the producer %v", declared["outcome"], outcomes)
	}
	if !reflect.DeepEqual(declared["anchor_basis"], bases) {
		t.Errorf("eventspec declares anchor_basis %v, the producer %v", declared["anchor_basis"], bases)
	}
}

func pathEdgeEndpoints(result contextfabric.GraphContext) []string {
	var out []string
	for _, p := range result.Paths {
		for _, e := range p.Edges {
			out = append(out, string(e.Type)+":"+e.From.CanonicalID+">"+e.To.CanonicalID)
		}
	}
	sort.Strings(out)
	return out
}

// TestAnOwnershipRoutedDiscoveryStillWalksTheEdgesOfWhatTheQuestionTextMatched
// pins that routing narrows the team pool only: a non-team subject the text
// matched and an owner the text matched keep their edges.
func TestAnOwnershipRoutedDiscoveryStillWalksTheEdgesOfWhatTheQuestionTextMatched(t *testing.T) {
	s := seedOwnedRepository()
	s.text["team|team:owner"] = "teams own"
	s.text["repository|repository:github:acme/bravo-service"] = "teams own"
	s.nodes = append(s.nodes, seededNode{kind: "repository", id: "repository:github:acme/gamma", label: "acme/gamma", repos: []string{"acme/gamma"}})
	s.edges = append(s.edges, seededEdge{"OWNED_BY_TEAM", "repository", "repository:github:acme/gamma", "team", "team:owner"})
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: routeOwnedRepository, Label: routeOwnedSlug}
	request := namedByLabelRequest(contextfabric.SubjectRepository, true)
	request.Resolution.Committed = []contextfabric.SubjectRef{anchor}
	request.Resolution.Candidates = []contextfabric.SubjectCandidate{{
		ReceiptID: "receipt_anchor", Subject: anchor, State: contextfabric.ResolutionCommitted,
		MatchedTerms: request.Frame.SubjectExpression.Scoped.AnchorTerms, MatchReasons: []string{"matched"},
		Confidence: 1, EvidenceRefIDs: []string{},
	}}
	request.Bases = contextfabric.CommitBasisSet{}
	request.Bases.Record(anchor, contextfabric.CommitBasisStatistical)
	request.Request.Question = "which teams own repository " + routeOwnedSlug
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, s.conn(), telemetry)
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.ownershipRoutings) != 1 || telemetry.ownershipRoutings[0].Outcome != OwnershipRoutingOwners {
		t.Fatalf("decisions = %+v, want one owners decision", telemetry.ownershipRoutings)
	}
	if got := ownedMemberIDs(result); !reflect.DeepEqual(got, []string{"team:owner"}) {
		t.Fatalf("members = %v", got)
	}
	edges := strings.Join(pathEdgeEndpoints(result), ",")
	for _, want := range []string{"OWNED_BY_TEAM:repository:github:acme/gamma>team:owner", "BELONGS_TO_REPOSITORY:deployment:acme/bravo-service:0>repository:github:acme/bravo-service"} {
		if !strings.Contains(edges, want) {
			t.Errorf("paths lack %s: %s", want, edges)
		}
	}
}

// ownershipEdgeConn answers the ownership census with census and the
// repository's ownership edge read with edges teams (or err).
func ownershipEdgeConn(census []row, edges int, err error) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "$kinds"):
			return census, nil
		case params["fromKind"] == string(contextfabric.SubjectRepository) && params["toKind"] == string(contextfabric.SubjectTeam):
			if err != nil {
				return nil, err
			}
			limit, _ := params["limit"].(int)
			var rows []row
			for i := 0; i < edges && i < limit; i++ {
				rows = append(rows, row{
					"id": ownedRepository().CanonicalID,
					"b":  teamRow(fmt.Sprintf("team:edge-%04d", i), "acr-context-fabric:team-repository-ownership-over-bound")["n"],
					"r":  &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: fmt.Sprintf("rel_%04d", i)}},
				})
			}
			return rows, nil
		}
		return nil, nil
	}}
}

// TestACutOwnershipEdgeReadIsACutCensus: owners past the edge read's bound may
// exist, so the cohort does not read complete.
func TestACutOwnershipEdgeReadIsACutCensus(t *testing.T) {
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, ownershipEdgeConn([]row{teamRow("team:one", ownedSlug)}, exactNameCandidateQueryLimit+1, nil), telemetry)
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, namedByLabelRequest(contextfabric.SubjectRepository, true))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(telemetry.ownershipRoutings) != 1 || !telemetry.ownershipRoutings[0].Truncated {
		t.Fatalf("decisions = %+v, want one decision recording the cut", telemetry.ownershipRoutings)
	}
	if result.Cohort == nil || result.Cohort.Complete {
		t.Fatalf("cohort = %+v, want the owner in a cohort that does not read complete", result.Cohort)
	}
}

// TestAFailedOwnershipEdgeReadFailsTheCall: the edge read is part of the
// ownership read, so its failure is the read's.
func TestAFailedOwnershipEdgeReadFailsTheCall(t *testing.T) {
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, ownershipEdgeConn([]row{teamRow("team:one", ownedSlug)}, 0, errors.New("connection reset")), telemetry)
	if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, namedByLabelRequest(contextfabric.SubjectRepository, true)); err == nil {
		t.Fatal("DiscoverContext() error = nil, want the failed edge read")
	}
	if len(telemetry.ownershipRoutings) != 1 || telemetry.ownershipRoutings[0].Outcome != OwnershipRoutingReadFailed {
		t.Fatalf("decisions = %+v, want one read_failed decision", telemetry.ownershipRoutings)
	}
}

// ownershipTimeConn answers the ownership census with census, records the
// edge read's parameters, and returns no edge.
func ownershipTimeConn(census []row, bound *[]map[string]interface{}) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "$kinds"):
			return census, nil
		case isOwnershipEdgeRead(params):
			*bound = append(*bound, params)
		}
		return nil, nil
	}}
}

// TestOwnershipEdgesAreReadAtTheClockForAQuestionAboutNow: an ended ownership
// edge is history, so for a question about now the edge read is bound to the
// adapter clock.
func TestOwnershipEdgesAreReadAtTheClockForAQuestionAboutNow(t *testing.T) {
	var bound []map[string]interface{}
	adapter := newFakeAdapter(t, ownershipTimeConn([]row{teamRow("team:one", ownedSlug)}, &bound))
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	adapter.now = func() time.Time { return now }
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, namedByLabelRequest(contextfabric.SubjectRepository, true))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(bound) != 1 || bound[0][temporalParamStart] != nsTimestamp(now) || bound[0][temporalParamEnd] != nsTimestamp(now) {
		t.Fatalf("edge reads = %v, want one read bound to the adapter clock", bound)
	}
	if got := ownedMemberIDs(result); !reflect.DeepEqual(got, []string{"team:one"}) {
		t.Fatalf("members = %v, want the team whose list names the repository now", got)
	}
}

// TestAStatedWindowDecidesOwnershipByItsEdgesOnly: the repository list says
// who owns the repository now; under a stated window a team the list names
// but no ownership edge of that window names is not an owner.
func TestAStatedWindowDecidesOwnershipByItsEdgesOnly(t *testing.T) {
	var bound []map[string]interface{}
	adapter := newFakeAdapter(t, ownershipTimeConn([]row{teamRow("team:one", ownedSlug)}, &bound))
	start, end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	request := namedByLabelRequest(contextfabric.SubjectRepository, true)
	request.Interpretation.TimeContext = contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end}
	result, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(bound) != 1 || bound[0][temporalParamStart] != nsTimestamp(start) || bound[0][temporalParamEnd] != nsTimestamp(end) {
		t.Fatalf("edge reads = %v, want one read bound to the stated window", bound)
	}
	if got := ownedMemberIDs(result); len(got) != 0 {
		t.Fatalf("members = %v, want none: no ownership edge of the window names a team", got)
	}
}
