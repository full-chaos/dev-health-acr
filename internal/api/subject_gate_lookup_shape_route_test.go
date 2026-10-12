package api

import (
	"context"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The subject gate must run the same graph steps for a denied and an absent
// team, so a caller cannot tell them apart by repeated-request timing. These
// rows drive the real routes and count steps; they never read a clock.

type lookupShapeGraph struct {
	denied map[string]bool
	absent map[string]bool

	mu        sync.Mutex
	authorize int
	reach     int
	inactive  []int
}

func (g *lookupShapeGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{}, nil
}

func (g *lookupShapeGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.authorize++
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i, subject := range subjects {
		out[i] = contextfabric.StoredSubjectAdmitted
		switch {
		case g.denied[subject.CanonicalID]:
			out[i] = contextfabric.StoredSubjectDenied
		case g.absent[subject.CanonicalID]:
			out[i] = contextfabric.StoredSubjectAbsent
		}
	}
	return out, nil
}

func (g *lookupShapeGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reach++
	return make([][]string, len(subjects)), nil
}

func (g *lookupShapeGraph) InactiveTeams(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]directread.InactiveTeam, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inactive = append(g.inactive, len(subjects))
	return make([]directread.InactiveTeam, len(subjects)), nil
}

func (g *lookupShapeGraph) DirectEdgePage(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, directread.EdgePageQuery) (directread.EdgePage, error) {
	return directread.EdgePage{}, nil
}

func (g *lookupShapeGraph) steps() (int, int, []int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.authorize, g.reach, slices.Clone(g.inactive)
}

type teamHealthProvider struct{ chaos7073Provider }

func (teamHealthProvider) Capability() contextfabric.FactCapability {
	capability := chaos7073Provider{}.Capability()
	capability.SupportedSubjectKinds = []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam}
	return capability
}

var validAtPattern = regexp.MustCompile(`"valid_at":"[^"]*"`)

// sameBody compares two refusals with the subject id masked, because the
// refusal echoes the id the caller sent.
func sameBody(t *testing.T, a, b *httptest.ResponseRecorder, idA, idB string) {
	t.Helper()
	if a.Code != b.Code {
		t.Fatalf("status %d vs %d", a.Code, b.Code)
	}
	mask := func(body, id string) string {
		return validAtPattern.ReplaceAllString(strings.ReplaceAll(body, id, "ID"), `"valid_at":"T"`)
	}
	left, right := mask(a.Body.String(), idA), mask(b.Body.String(), idB)
	if left != right {
		t.Fatalf("served shape differs:\n%s\n%s", a.Body.String(), b.Body.String())
	}
}

func TestReadFactsRouteDeniedAndAbsentTeamServeOneShapeAndRunOneSteps(t *testing.T) {
	run := func(id string) (*httptest.ResponseRecorder, [3]any) {
		h := newChaos7071Harness(t, 100)
		token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
		graph := &lookupShapeGraph{denied: map[string]bool{"team:denied": true}, absent: map[string]bool{"team:absent": true}}
		setChaos7073Reader(t, h, graph, teamHealthProvider{})
		response := h.postFacts(token, `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"`+id+`"}]}`)
		authorize, reach, inactive := graph.steps()
		return response, [3]any{authorize, reach, len(inactive)}
	}
	denied, deniedSteps := run("team:denied")
	absent, absentSteps := run("team:absent")
	if !strings.Contains(denied.Body.String(), directread.FactsRefusalDeniedOrNotFound) {
		t.Fatalf("setup: %s", denied.Body.String())
	}
	sameBody(t, denied, absent, "team:denied", "team:absent")
	if deniedSteps != absentSteps {
		t.Fatalf("denied steps %v differ from absent steps %v", deniedSteps, absentSteps)
	}
	if deniedSteps[2] != 1 {
		t.Fatalf("want one inactive-team lookup, got %v", deniedSteps)
	}
}

func TestReadRelationshipsRouteDeniedAndAbsentTeamServeOneShapeAndRunOneSteps(t *testing.T) {
	run := func(id string) (*httptest.ResponseRecorder, [3]any) {
		h := newChaos7071Harness(t, 100)
		token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
		graph := &lookupShapeGraph{denied: map[string]bool{"team:denied": true}, absent: map[string]bool{"team:absent": true}}
		gate := directread.NewSubjectGate(graph, nil)
		h.app.runtime.DirectReadGate = gate
		reader, err := directread.NewRelationshipsReader(gate, graph, nil, directread.CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef-test-cursor-key")}})
		if err != nil {
			t.Fatal(err)
		}
		h.app.runtime.DirectRelationships = reader
		response := h.postRelationships(token, `{"subject":{"kind":"team","canonical_id":"`+id+`"}}`)
		authorize, reach, inactive := graph.steps()
		return response, [3]any{authorize, reach, len(inactive)}
	}
	denied, deniedSteps := run("team:denied")
	absent, absentSteps := run("team:absent")
	if !strings.Contains(denied.Body.String(), directread.RelationshipsRefusalDeniedOrNotFound) {
		t.Fatalf("setup: %s", denied.Body.String())
	}
	sameBody(t, denied, absent, "team:denied", "team:absent")
	if deniedSteps != absentSteps {
		t.Fatalf("denied steps %v differ from absent steps %v", deniedSteps, absentSteps)
	}
	if deniedSteps[2] != 1 {
		t.Fatalf("want one inactive-team lookup, got %v", deniedSteps)
	}
}
