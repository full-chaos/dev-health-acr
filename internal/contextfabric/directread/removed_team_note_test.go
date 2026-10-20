package directread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type removedTeams struct {
	gone  map[string]bool
	err   error
	asked [][]contextfabric.SubjectRef
}

func (r *removedTeams) RemovedTeams(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.SubjectRef, error) {
	r.asked = append(r.asked, slices.Clone(subjects))
	if r.err != nil {
		return nil, r.err
	}
	var out []contextfabric.SubjectRef
	for _, subject := range subjects {
		if r.gone[subject.CanonicalID] {
			out = append(out, subject)
		}
	}
	return out, nil
}

type removedFactsGraph struct {
	*fakeGraph
	*removedTeams
}

type removedEdgeGraph struct {
	*fakeEdgeGraph
	*removedTeams
}

func TestGateFlagsOnlyAdmittedRemovedTeams(t *testing.T) {
	graph := graphOfOrgA()
	removed := &removedTeams{gone: map[string]bool{teamT.CanonicalID: true, teamU.CanonicalID: true}}
	gate := NewSubjectGate(removedFactsGraph{graph, removed}, nil)
	_, decision := gate.Authorize(requestContext(), restrictedToA(), []contextfabric.SubjectRef{teamT, teamU})
	for _, gated := range decision.Outcomes {
		switch gated.Subject.CanonicalID {
		case teamT.CanonicalID:
			if gated.Outcome != SubjectAdmitted || !gated.Removed {
				t.Fatalf("admitted gone team not flagged: %+v", gated)
			}
		case teamU.CanonicalID:
			if gated.Outcome == SubjectAdmitted || gated.Removed || gated.StatusUnchecked {
				t.Fatalf("a team the caller may not read was flagged: %+v", gated)
			}
		}
	}
	for _, asked := range removed.asked {
		for _, subject := range asked {
			if subject.CanonicalID == teamU.CanonicalID {
				t.Fatalf("an unreadable team reached the removed-team lookup: %v", asked)
			}
		}
	}
	if got := logArg(AuthorizationLogArgs(restrictedToA(), decision), "removed_team_count"); got != 1 {
		t.Fatalf("removed_team_count = %v, want 1", got)
	}
}

func TestGateMarksUncheckedTeamsWhenTheRemovedLookupFails(t *testing.T) {
	removed := &removedTeams{err: errors.New("falkor down")}
	gate := NewSubjectGate(removedFactsGraph{graphOfOrgA(), removed}, nil)
	_, decision := gate.Authorize(requestContext(), unrestricted, []contextfabric.SubjectRef{teamT})
	got := decision.Outcomes[0]
	if got.Outcome != SubjectAdmitted || got.Removed || !got.StatusUnchecked {
		t.Fatalf("a failed lookup must serve the team and mark it unchecked: %+v", got)
	}
	if v := logArg(AuthorizationLogArgs(unrestricted, decision), "removed_lookup_failures"); v != 1 {
		t.Fatalf("removed_lookup_failures = %v, want 1", v)
	}
}

func TestReadFactsCarriesTheTeamNotes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		removed *removedTeams
		want    []RequestNote
	}{
		{"gone team", &removedTeams{gone: map[string]bool{teamT.CanonicalID: true}}, []RequestNote{{Kind: "team", Note: contractsv1.TeamRemovedNote}}},
		{"live team", &removedTeams{}, nil},
		{"lookup failed", &removedTeams{err: errors.New("falkor down")}, []RequestNote{{Kind: "team", Note: contractsv1.TeamStatusUnavailableNote}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &stubProvider{capability: healthLikeCapability(), read: func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
				return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
			}}
			reader := newTestFactsReader(t, removedFactsGraph{graphOfOrgA(), tc.removed}, provider)
			response, err := reader.Read(requestContext(), unrestricted, FactsRequest{Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}})
			if err != nil {
				t.Fatal(err)
			}
			var got []RequestNote
			for _, note := range response.RequestNotes {
				if note.Kind == "team" {
					got = append(got, note)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("team request_notes = %+v, want %+v (all: %+v)", got, tc.want, response.RequestNotes)
			}
		})
	}
}

func TestReadRelationshipsCarriesTheTeamNotes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		removed *removedTeams
		want    []string
	}{
		{"gone team", &removedTeams{gone: map[string]bool{teamT.CanonicalID: true}}, []string{contractsv1.TeamRemovedNote}},
		{"live team", &removedTeams{}, nil},
		{"lookup failed", &removedTeams{err: errors.New("falkor down")}, []string{contractsv1.TeamStatusUnavailableNote}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edges := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
			edges.edges = []EdgeCandidate{edgeBetween("e1", "OWNED_BY_TEAM", repoA, teamT, nil)}
			guard := removedEdgeGraph{edges, tc.removed}
			reader, err := NewRelationshipsReader(NewSubjectGate(guard, nil), guard, &relRecorder{}, testCursorKeyring())
			if err != nil {
				t.Fatal(err)
			}
			response, err := reader.Read(relCtx("removed-"+tc.name), unrestricted, RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(response.Notes, tc.want) {
				t.Fatalf("notes = %v, want %v", response.Notes, tc.want)
			}
			if len(response.Edges) == 0 {
				t.Fatal("a removed team's edges must still be served")
			}
		})
	}
}

func TestReadRelationshipsLooksUpRemovedTeamsOncePerPage(t *testing.T) {
	removed := &removedTeams{gone: map[string]bool{teamT.CanonicalID: true}}
	hub := hubGraph()
	guard := removedEdgeGraph{hub, removed}
	reader, err := NewRelationshipsReader(NewSubjectGate(guard, nil), guard, &relRecorder{}, testCursorKeyring())
	if err != nil {
		t.Fatal(err)
	}
	request := RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, Direction: "in", Limit: 1}
	for page := 1; page <= 2; page++ {
		before := len(removed.asked)
		response, err := reader.Read(relCtx(fmt.Sprintf("removed-page-%d", page)), unrestricted, request)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(response.Notes, []string{contractsv1.TeamRemovedNote}) {
			t.Fatalf("page %d notes = %v, want [team_removed]", page, response.Notes)
		}
		if got := len(removed.asked) - before; got != 1 {
			t.Fatalf("page %d removed-team lookups = %d, want 1", page, got)
		}
		if response.Page.NextCursor == "" {
			t.Fatalf("page %d has no continuation", page)
		}
		request.Cursor = response.Page.NextCursor
	}
}

func TestGoldenExamplesShowBothTeamNotes(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "contracts", "examples", "v1")
	for _, route := range []string{"mcp_read_facts", "mcp_read_relationships"} {
		matches, err := filepath.Glob(filepath.Join(dir, route+"*_response.v1.json"))
		if err != nil || len(matches) == 0 {
			t.Fatalf("%s examples: %v %v", route, matches, err)
		}
		var text strings.Builder
		for _, path := range matches {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text.Write(raw)
		}
		for _, note := range []string{contractsv1.TeamRemovedNote, contractsv1.TeamStatusUnavailableNote} {
			if !strings.Contains(text.String(), `"`+note+`"`) {
				t.Errorf("%s examples never show %q", route, note)
			}
		}
	}
}
