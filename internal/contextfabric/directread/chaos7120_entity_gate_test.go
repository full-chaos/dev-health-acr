package directread

import (
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7120: the embedded gate over the twelve entity kinds. The facts come
// from stub providers, but every capability is the PRODUCTION declaration
// (devhealthfacts.NewProviders), so the gate is tested against what ships.

func productionCapability(t *testing.T, kind contextfabric.FactKind) contextfabric.FactCapability {
	t.Helper()
	for _, provider := range devhealthfacts.NewProviders(nil) {
		if capability := provider.Capability(); capability.Kind == kind {
			return capability
		}
	}
	t.Fatalf("no production provider for %s", kind)
	return contextfabric.FactCapability{}
}

func derived(kind string, values ...string) string {
	id, omitted, err := identity.Derive(kind, values, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("derive %s %v: %v", kind, values, err))
	}
	return id
}

// Entities of graphOfOrgA's repositories A ("a", acme/a) and B ("b",
// acme/b), in the id forms the graph stores.
var (
	workA1     = subject(contractsv1.ContextFabricSubjectWorkItem, derived(identity.KindWorkItem, "a", "WA-1"))
	workA2     = subject(contractsv1.ContextFabricSubjectWorkItem, derived(identity.KindWorkItem, "a", "WA-2"))
	workB9     = subject(contractsv1.ContextFabricSubjectWorkItem, derived(identity.KindWorkItem, "b", "WB-9"))
	ciA1       = subject(contractsv1.ContextFabricSubjectCIRun, derived(identity.KindCIPipelineRun, "a", "run-1"))
	ciB9       = subject(contractsv1.ContextFabricSubjectCIRun, derived(identity.KindCIPipelineRun, "b", "run-9"))
	deployA1   = subject(contractsv1.ContextFabricSubjectDeployment, derived(identity.KindDeployment, "a", "dep-1"))
	deployB9   = subject(contractsv1.ContextFabricSubjectDeployment, derived(identity.KindDeployment, "b", "dep-9"))
	pullA1     = subject(contractsv1.ContextFabricSubjectPullRequest, "pull_request:a:1")
	pullB9     = subject(contractsv1.ContextFabricSubjectPullRequest, "pull_request:b:9")
	reviewA1   = subject(contractsv1.ContextFabricSubjectPullRequestReview, derived(identity.KindPullRequestReview, "a", "1", "rev-1"))
	incidentA1 = subject(contractsv1.ContextFabricSubjectIncident, "incident:inc-a")
	incidentB9 = subject(contractsv1.ContextFabricSubjectIncident, "incident:inc-b")
)

func graph7120() *fakeGraph {
	graph := graphOfOrgA()
	for _, s := range []contextfabric.SubjectRef{workA1, workA2, ciA1, deployA1, pullA1, reviewA1, incidentA1} {
		graph.nodes[graphrank.SubjectKey(s)] = repos("acme/a")
	}
	for _, s := range []contextfabric.SubjectRef{workB9, ciB9, deployB9, pullB9, incidentB9} {
		graph.nodes[graphrank.SubjectKey(s)] = repos("acme/b")
	}
	return graph
}

func evidence(entity contractsv1.ContextFabricEvidenceEntityType, raw string) string {
	return contractsv1.EvidenceRefID(entity, raw)
}

func readOne(t *testing.T, principal storage.Principal, kind contextfabric.FactKind, root contextfabric.SubjectRef, fact contextfabric.CanonicalFact) ServedFact {
	t.Helper()
	provider := &stubProvider{capability: productionCapability(t, kind), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		fact.Kind, fact.Subject, fact.SourceState = kind, query.Subjects[0], contextfabric.SourceAvailable
		return contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{fact}, State: contextfabric.SourceAvailable}, nil
	}}
	response, err := newTestFactsReader(t, graph7120(), provider).Read(requestContext(), principal, FactsRequest{
		Kinds: []string{string(kind)}, Subjects: []RequestSubject{{Kind: string(root.Kind), CanonicalID: root.CanonicalID}}})
	if err != nil {
		t.Fatalf("read %s: %v", kind, err)
	}
	if len(response.Facts) != 1 {
		t.Fatalf("read %s: facts = %d, want 1 (%s)", kind, len(response.Facts), mustJSON(t, response))
	}
	return response.Facts[0]
}

func withheldCount(fact ServedFact, field string) (int, bool) {
	for _, item := range fact.Withheld {
		if item.Field == field {
			return item.Count, true
		}
	}
	return 0, false
}

func hasKey(fact ServedFact, entity, id string) bool {
	for _, key := range fact.Provenance.NaturalKeys {
		if key.Entity == entity && key.ID == id {
			return true
		}
	}
	return false
}

// A restricted caller granted repository A reads a work item in A whose
// blocker is in B: the blocker's ids are withheld with a count, and the
// evidence naming the B item (and the dependency edge) is removed.
func TestChaos7120BlockerInUnseenRepositoryIsWithheld(t *testing.T) {
	blockers := contextfabric.CanonicalFact{
		Fields: map[string]contextfabric.FactValue{
			"blocked_by_work_item_id":  strValue("WB-9"),
			"blocked_by_work_item_ref": strValue(workB9.CanonicalID),
		},
		EvidenceRefIDs: []string{
			evidence(contractsv1.ContextFabricEvidenceEntityWorkItemDependency, "WB-9:WA-1"),
			evidence(contractsv1.ContextFabricEvidenceEntityWorkItem, "b:WB-9"),
			evidence(contractsv1.ContextFabricEvidenceEntityWorkItem, "a:WA-1"),
		},
	}
	t.Run("restricted to A", func(t *testing.T) {
		fact := readOne(t, restrictedToA(), contextfabric.FactBlockers, workA1, blockers)
		encoded := mustJSON(t, fact)
		if strings.Contains(encoded, "WB-9") {
			t.Errorf("the B blocker leaked: %s", encoded)
		}
		for _, field := range []string{"blocked_by_work_item_id", "blocked_by_work_item_ref"} {
			if _, served := fact.Fields[field]; served {
				t.Errorf("%s served to a caller who cannot see the blocker", field)
			}
			if _, listed := withheldCount(fact, field); !listed {
				t.Errorf("%s not listed as withheld: %+v", field, fact.Withheld)
			}
		}
		if count, _ := withheldCount(fact, "provenance.natural_keys"); count != 2 {
			t.Errorf("evidence withheld = %d, want 2 (the B work item and the dependency edge): %+v", count, fact.Withheld)
		}
		if !hasKey(fact, "work-item", "a:WA-1") || len(fact.Provenance.NaturalKeys) != 1 {
			t.Errorf("natural keys = %v, want only the fact's own work item", fact.Provenance.NaturalKeys)
		}
	})
	t.Run("restricted to A, blocker in A", func(t *testing.T) {
		inA := blockers
		inA.Fields = map[string]contextfabric.FactValue{
			"blocked_by_work_item_id":  strValue("WA-2"),
			"blocked_by_work_item_ref": strValue(workA2.CanonicalID),
		}
		inA.EvidenceRefIDs = []string{evidence(contractsv1.ContextFabricEvidenceEntityWorkItem, "a:WA-2"), evidence(contractsv1.ContextFabricEvidenceEntityWorkItem, "a:WA-1")}
		fact := readOne(t, restrictedToA(), contextfabric.FactBlockers, workA1, inA)
		if fact.Fields["blocked_by_work_item_ref"] != workA2.CanonicalID {
			t.Errorf("the canonical ref of a visible blocker was not served: %+v", fact.Fields)
		}
		// The bare id is opaque: withheld for a restricted caller even
		// when the blocker is visible.
		if _, served := fact.Fields["blocked_by_work_item_id"]; served {
			t.Errorf("the opaque bare id was served to a restricted caller")
		}
		if !hasKey(fact, "work-item", "a:WA-2") || !hasKey(fact, "work-item", "a:WA-1") {
			t.Errorf("natural keys = %v, want both A work items", fact.Provenance.NaturalKeys)
		}
	})
	t.Run("unrestricted", func(t *testing.T) {
		fact := readOne(t, unrestrictedA(), contextfabric.FactBlockers, workA1, blockers)
		if fact.Fields["blocked_by_work_item_id"] != "WB-9" || fact.Fields["blocked_by_work_item_ref"] != workB9.CanonicalID {
			t.Errorf("unrestricted fields = %+v, want both blocker ids", fact.Fields)
		}
		if len(fact.Provenance.NaturalKeys) != 3 || len(fact.Withheld) != 0 {
			t.Errorf("unrestricted keys = %v withheld = %+v, want all three and none", fact.Provenance.NaturalKeys, fact.Withheld)
		}
	})
}

// The same for required_children: the child's canonical ref is gated.
func TestChaos7120RequiredChildInUnseenRepositoryIsWithheld(t *testing.T) {
	fact := readOne(t, restrictedToA(), contextfabric.FactRequiredChildren, workA1, contextfabric.CanonicalFact{
		Fields: map[string]contextfabric.FactValue{
			"required_child_work_item_id":  strValue("WB-9"),
			"required_child_work_item_ref": strValue(workB9.CanonicalID),
			"relationship_type":            strValue("requires"),
		},
		EvidenceRefIDs: []string{evidence(contractsv1.ContextFabricEvidenceEntityWorkItem, "a:WA-1")},
	})
	if strings.Contains(mustJSON(t, fact), "WB-9") {
		t.Errorf("the B child leaked: %s", mustJSON(t, fact))
	}
	if fact.Fields["relationship_type"] != "requires" {
		t.Errorf("label field lost: %+v", fact.Fields)
	}
}

// Each entity evidence form maps to the fact's own subject (served to a
// restricted caller whose grant covers it) or to another subject, which is
// gated: in the grant -> served, outside it -> removed. A review reference
// that is not the fact's own is unresolvable and removed.
func TestChaos7120EntityEvidenceIsOwnOrGated(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     contextfabric.FactKind
		root     contextfabric.SubjectRef
		entity   contractsv1.ContextFabricEvidenceEntityType
		own      string
		visible  string // another subject of the same kind in A ("" = none)
		foreign  string // a subject of the same kind in B
		fields   map[string]contextfabric.FactValue
		wantKeys int
	}{
		{"work item", contextfabric.FactStatus, workA1, contractsv1.ContextFabricEvidenceEntityWorkItem, "a:WA-1", "a:WA-2", "b:WB-9", map[string]contextfabric.FactValue{"status": strValue("done")}, 2},
		{"ci run", contextfabric.FactContinuousIntegration, ciA1, contractsv1.ContextFabricEvidenceEntityCI, "a:run-1", "", "b:run-9", map[string]contextfabric.FactValue{"status": strValue("success")}, 1},
		{"deployment", contextfabric.FactDeployments, deployA1, contractsv1.ContextFabricEvidenceEntityDeployment, "a:dep-1", "", "b:dep-9", map[string]contextfabric.FactValue{"status": strValue("success")}, 1},
		{"pull request", contextfabric.FactPullRequests, pullA1, contractsv1.ContextFabricEvidenceEntityPullRequest, "a:1", "", "b:9", map[string]contextfabric.FactValue{"state": strValue("open")}, 1},
		{"incident", contextfabric.FactIncidents, incidentA1, contractsv1.ContextFabricEvidenceEntityIncident, "inc-a", "", "inc-b", map[string]contextfabric.FactValue{"status": strValue("open")}, 1},
		{"review", contextfabric.FactReviews, reviewA1, contractsv1.ContextFabricEvidenceEntityReview, "a:rev-1", "", "a:rev-2", map[string]contextfabric.FactValue{"state": strValue("approved")}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := []string{evidence(tc.entity, tc.own), evidence(tc.entity, tc.foreign)}
			if tc.visible != "" {
				refs = append(refs, evidence(tc.entity, tc.visible))
			}
			base := contextfabric.CanonicalFact{Fields: tc.fields, EvidenceRefIDs: refs}
			fact := readOne(t, restrictedToA(), tc.kind, tc.root, base)
			if !hasKey(fact, string(tc.entity), tc.own) {
				t.Errorf("own evidence %s withheld from a caller granted its repository: keys=%v withheld=%+v", tc.own, fact.Provenance.NaturalKeys, fact.Withheld)
			}
			if tc.visible != "" && !hasKey(fact, string(tc.entity), tc.visible) {
				t.Errorf("evidence of a visible %s withheld: %v", tc.name, fact.Provenance.NaturalKeys)
			}
			if hasKey(fact, string(tc.entity), tc.foreign) {
				t.Errorf("evidence %s outside the grant served: %v", tc.foreign, fact.Provenance.NaturalKeys)
			}
			if count, _ := withheldCount(fact, "provenance.natural_keys"); count != 1 || len(fact.Provenance.NaturalKeys) != tc.wantKeys {
				t.Errorf("keys = %v withheld = %d, want %d keys and 1 withheld", fact.Provenance.NaturalKeys, count, tc.wantKeys)
			}
			for name := range tc.fields {
				if _, ok := fact.Fields[name]; !ok {
					t.Errorf("declared field %s not served: %+v", name, fact.Withheld)
				}
			}
		})
	}
}

// The resolveEvidence mapping table moved with the gate core to
// internal/contextfabric (chaos7120_resolve_evidence_test.go, CHAOS-7127).

// CHAOS-7120 codex r1 P1: a CallerScoped field is served with
// population_scope "caller_authorized_items" and named in
// population_scoped_fields; an unscoped fact carries no label.
func TestChaos7120CallerScopedFieldsAreLabelled(t *testing.T) {
	capability := contextfabric.FactCapability{
		Kind: contextfabric.FactActualCompletion, Name: "completion_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository},
		RequiresEvidence:      true, Dimension: contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles: []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields: []contextfabric.FactFieldDeclaration{
			{Name: "work_item_count", Type: contextfabric.FactFieldInteger, CallerScoped: true},
			{Name: "rollup_basis", Type: contextfabric.FactFieldString},
		},
	}
	provider := &stubProvider{capability: capability, read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Facts: []contextfabric.CanonicalFact{{
			Kind: contextfabric.FactActualCompletion, Subject: query.Subjects[0],
			Fields:         map[string]contextfabric.FactValue{"work_item_count": intValue(2), "rollup_basis": strValue("x")},
			EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "a")}, SourceState: contextfabric.SourceAvailable}}}, nil
	}}
	response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(requestContext(), restrictedToA(), FactsRequest{
		Kinds: []string{"actual_completion"}, Subjects: []RequestSubject{{Kind: "repository", CanonicalID: repoA.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Facts) != 1 {
		t.Fatalf("facts %d", len(response.Facts))
	}
	fact := response.Facts[0]
	if fact.PopulationScope != "caller_authorized_items" || len(fact.PopulationScopedFields) != 1 || fact.PopulationScopedFields[0] != "work_item_count" {
		t.Errorf("population label = %q %v, want caller_authorized_items over [work_item_count]", fact.PopulationScope, fact.PopulationScopedFields)
	}
}
