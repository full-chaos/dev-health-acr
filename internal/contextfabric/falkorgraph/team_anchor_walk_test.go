package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func teamWalkRequest() contextfabric.GraphDiscoveryRequest {
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	request := ownershipRoutingRequest(repositoriesOfAnchorFrame("Platform"), anchor)
	request.ScopeAnchorKind = contextfabric.SubjectTeam
	request.Request.Options.MaxCohortMembers = 50
	return request
}

var teamWalkAllowed = []string{"full-chaos/dev-health-acr"}

// teamWalkFixtures are the fakes, already answering the step read, and the
// principal each outcome is read as.
func teamWalkFixtures() (map[TeamAnchorWalkOutcome]*fakeConn, map[TeamAnchorWalkOutcome]storage.Principal) {
	// Every node and edge carries the grant, except the team in the denied case.
	granted := func(team []string) *fakeConn {
		fake := budgetTeamFakeWith(nil, 0)
		base := fake.queryFunc
		fake.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			rows, err := base(ctx, graphKey, cypher, params, readOnly)
			if err != nil || strings.Contains(cypher, "fulltext") {
				return rows, err
			}
			for _, r := range rows {
				if e, ok := r["r"].(*edge); ok {
					e.Properties["authorization_repositories"] = teamWalkAllowed
				}
				if n, ok := r["n"].(*node); ok {
					n.Properties["authorization_repositories"] = teamWalkAllowed
					if propStringValue(n.Properties[propCanonicalID]) == "team:platform" {
						n.Properties["authorization_repositories"] = team
					}
				}
			}
			return rows, nil
		}
		return withWalkStepReads(fake)
	}
	failing := withWalkStepReads(budgetTeamFakeWith(nil, 0))
	failBase := failing.queryFunc
	failing.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.HasPrefix(cypher, "UNWIND $ids") {
			return nil, errors.New("graph unavailable")
		}
		return failBase(ctx, graphKey, cypher, params, readOnly)
	}
	noMembers := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.Contains(cypher, "UNION") && !strings.Contains(cypher, "UNWIND") && !strings.Contains(cypher, "fulltext") && params["id"] == "team:platform" {
			return []row{fakeSubjectNodeRow("team", "team:platform", "Platform")}, nil
		}
		return nil, nil
	}}
	open := storage.Principal{OrgID: "org-1"}
	restricted := storage.Principal{OrgID: "org-1", RepositoryScopes: teamWalkAllowed}
	return map[TeamAnchorWalkOutcome]*fakeConn{
		TeamAnchorWalkMembers:    granted(teamWalkAllowed),
		TeamAnchorWalkNoMembers:  withWalkStepReads(noMembers),
		TeamAnchorWalkDenied:     granted([]string{"other/private"}),
		TeamAnchorWalkReadFailed: failing,
	}, map[TeamAnchorWalkOutcome]storage.Principal{
		TeamAnchorWalkMembers: restricted, TeamAnchorWalkNoMembers: open, TeamAnchorWalkDenied: restricted, TeamAnchorWalkReadFailed: open,
	}
}

// Every outcome of the team member read is one line, certified against the
// declared event, with counts and closed values only.
func TestTeamAnchorWalkWritesOneCertifiedLinePerOutcome(t *testing.T) {
	fixtures, principals := teamWalkFixtures()
	if len(fixtures) != len(TeamAnchorWalkOutcomeVocabulary()) {
		t.Fatalf("%d fixtures for %d declared outcomes", len(fixtures), len(TeamAnchorWalkOutcomeVocabulary()))
	}
	for _, outcome := range TeamAnchorWalkOutcomeVocabulary() {
		t.Run(string(outcome), func(t *testing.T) {
			var buf bytes.Buffer
			adapter := newFakeAdapterWithTelemetry(t, fixtures[outcome], SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))})
			_, err := adapter.DiscoverContext(context.Background(), principals[outcome], teamWalkRequest())
			if (err != nil) != (outcome == TeamAnchorWalkReadFailed) {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			log, perr := certify.Parse(buf.Bytes())
			if perr != nil {
				t.Fatal(perr)
			}
			if _, cerr := certify.Certify(log, certify.Assertion{
				Event: eventspec.TeamAnchorWalk,
				Want:  map[string]any{"org_id": "org-1", "outcome": string(outcome), "member_kind": "repository", "committed": 1},
			}); cerr != nil {
				t.Fatal(cerr)
			}
			lines := log.LinesWithMsg(eventspec.TeamAnchorWalk.Msg)
			if len(lines) != 1 {
				t.Fatalf("%d lines, want one", len(lines))
			}
			finished := outcome != TeamAnchorWalkReadFailed
			for _, key := range []string{"members", "denied", "truncated"} {
				if _, present := lines[0][key]; present != finished {
					t.Errorf("line %v: %s present = %v", lines[0], key, present)
				}
			}
			if _, present := lines[0]["error"]; present == finished {
				t.Errorf("line %v: error present = %v", lines[0], present)
			}
			switch outcome {
			case TeamAnchorWalkMembers:
				if lines[0]["members"] != float64(10) || lines[0]["denied"] != float64(0) {
					t.Errorf("line %v: want 10 members, 0 denied", lines[0])
				}
			case TeamAnchorWalkDenied:
				if lines[0]["members"] != float64(0) || lines[0]["denied"] != float64(10) {
					t.Errorf("line %v: want 0 members, 10 denied", lines[0])
				}
			}
			for key, value := range lines[0] {
				if text, isText := value.(string); isText && key != "error" && (strings.Contains(text, "team:") || strings.Contains(text, "repository:")) {
					t.Errorf("field %s = %q names a subject", key, text)
				}
			}
		})
	}
}

func TestTeamAnchorWalkClosedVocabularyMatchesEventspec(t *testing.T) {
	var declared []string
	for _, f := range eventspec.TeamAnchorWalk.Fields {
		if f.Key == "outcome" {
			declared = f.ClosedVocabulary
		}
	}
	var produced []string
	for _, o := range TeamAnchorWalkOutcomeVocabulary() {
		produced = append(produced, string(o))
	}
	if !reflect.DeepEqual(declared, produced) {
		t.Fatalf("eventspec declares %v, the producer %v", declared, produced)
	}
}

// A question that is not for a committed team's repositories or projects
// writes no team anchor line.
func TestTeamAnchorWalkIsNotWrittenForAnOtherwiseAnchoredQuestion(t *testing.T) {
	telemetry := &recordingTelemetry{}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:r", Label: "full-chaos/r"}
	if _, err := newFakeAdapterWithTelemetry(t, &fakeConn{}, telemetry).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, ownershipRoutingRequest(projectsOfAnchorFrame("r"), anchor)); err != nil {
		t.Fatal(err)
	}
	if len(telemetry.teamAnchorWalks) != 0 {
		t.Fatalf("decisions = %+v, want none", telemetry.teamAnchorWalks)
	}
}

// The reads of one call fold into one decision: counts add, a cut anywhere is
// a cut.
func TestTeamAnchorWalkTallySumsTheReadsOfOneCall(t *testing.T) {
	var tally teamAnchorWalkTally
	tally.add(treeWalk{nodes: make([]graphrank.CandidateNode, 2), denied: 1})
	tally.add(treeWalk{nodes: make([]graphrank.CandidateNode, 3), denied: 4, truncated: true})
	got := tally.decision(contextfabric.SubjectProject, 2, nil)
	want := TeamAnchorWalkDecision{Outcome: TeamAnchorWalkMembers, MemberKind: contextfabric.SubjectProject, Committed: 2, Members: 5, Denied: 5, Truncated: true}
	if got != want {
		t.Fatalf("decision = %+v, want %+v", got, want)
	}
}
