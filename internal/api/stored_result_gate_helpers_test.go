package api

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// testStoredResultGate is the gate a test composition hands the retrieval
// route. An engine investigator supplies its own gate, exactly as the hosted
// composition does. Any other investigator gets a gate over
// grantedSubjectGraph, whose graph holds every subject a stored result names
// with the test credential's repository as its grant -- the decision is still
// the shared predicate, so a test that stores a result it may read keeps
// reading it, and a test about the gate itself uses its own graph.
func testStoredResultGate(investigator contextfabric.Investigator) StoredResultAuthorizer {
	if engine, ok := investigator.(interface {
		StoredResultGate() *contextfabric.StoredResultGate
	}); ok && engine.StoredResultGate() != nil {
		return engine.StoredResultGate()
	}
	return contextfabric.NewStoredResultGate(grantedSubjectGraph{grant: []string{hostedTestRepository}})
}

// subjectNodeGraph is a GraphReader whose only live capability is the
// stored-subject lookup. nodes maps a subject key to its authorization
// attributes; a missing key is a subject with no node. grant, when set,
// answers every subject missing from nodes with a node granted to those
// repositories instead.
type subjectNodeGraph struct {
	nodes     map[string]map[string]interface{}
	grant     []string
	readErr   error
	bindErr   error
	lookedUp  *[]contextfabric.SubjectRef
	principal *storage.Principal
}

type grantedSubjectGraph = subjectNodeGraph

func (g subjectNodeGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	if g.bindErr != nil {
		return contextfabric.ResolvedGraphBinding{}, g.bindErr
	}
	return contextfabric.ResolvedGraphBinding{GraphKey: "stored-subject-test", Epoch: 1}, nil
}

func (subjectNodeGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, nil, nil, errors.New("subjectNodeGraph resolves no subjects")
}

func (subjectNodeGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{}, errors.New("subjectNodeGraph discovers nothing")
}

func (g subjectNodeGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	if g.readErr != nil {
		return nil, g.readErr
	}
	if g.lookedUp != nil {
		*g.lookedUp = append(*g.lookedUp, subjects...)
	}
	if g.principal != nil {
		*g.principal = principal
	}
	nodes := map[string]graphrank.CandidateNode{}
	for _, subject := range subjects {
		key := graphrank.SubjectKey(subject)
		if attributes, ok := g.nodes[key]; ok {
			nodes[key] = graphrank.CandidateNode{Attributes: attributes}
			continue
		}
		if g.grant != nil {
			nodes[key] = graphrank.CandidateNode{Attributes: map[string]interface{}{"authorization_repositories": append([]string(nil), g.grant...)}}
		}
	}
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, nodes), nil
}

// AuthorizeStoredSubjects: surfaceGraph commits its subjects for the test
// credential's repository, so its graph holds every stored subject under that
// grant.
func (surfaceGraph) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	return subjectNodeGraph{grant: []string{hostedTestRepository}}.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
}

// The work-item tuple fakes commit their subjects for whatever grant the test
// issues, so their graphs hold every stored subject under that grant.
func (g *lifetimeTupleGraph) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	return subjectNodeGraph{grant: principal.RepositoryScopes}.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
}

func (g *freshTupleGraph) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	return subjectNodeGraph{grant: principal.RepositoryScopes}.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
}
