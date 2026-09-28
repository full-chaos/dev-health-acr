package directread

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7073 tests for read_facts: T14 (embedded subjects), T3 (coverage per
// pair), T6 (budget and truncation), plus the expander-off pin.

// stubProvider is a FactProvider whose result is fixed per test. It runs
// through the REAL registry (contextfabric.NewFactCapabilityRegistry), so
// validation, merge, the outcome ledger and the planner are production code.
type stubProvider struct {
	capability contextfabric.FactCapability
	read       func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error)
	queries    []contextfabric.FactQuery
}

func (p *stubProvider) Capability() contextfabric.FactCapability { return p.capability }

func (p *stubProvider) ReadFacts(_ context.Context, _ storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	p.queries = append(p.queries, query)
	return p.read(query)
}

func strValue(v string) contextfabric.FactValue  { return contextfabric.StringFactValue(v) }
func numValue(v float64) contextfabric.FactValue { return contextfabric.FactValue{Number: &v} }
func intValue(v int64) contextfabric.FactValue   { return contextfabric.FactValue{Integer: &v} }

// healthLikeCapability declares the health shape: an aggregate score over
// the repositories a team or project reaches, with a drivers table whose
// rows name repositories and teams (scope + scope_id).
func healthLikeCapability() contextfabric.FactCapability {
	scopeRef := &contextfabric.FactSubjectRefDeclaration{
		KindColumn:  "scope",
		KindByValue: map[string]contextfabric.SubjectKind{"repo": contractsv1.ContextFabricSubjectRepository, "team": contractsv1.ContextFabricSubjectTeam},
		IDForm:      contextfabric.FactSubjectIDTeamID,
		FormByKind:  map[contextfabric.SubjectKind]contextfabric.FactSubjectIDForm{contractsv1.ContextFabricSubjectRepository: contextfabric.FactSubjectIDRepositoryUUID},
	}
	return contextfabric.FactCapability{
		Kind: contextfabric.FactHealth, Name: "health_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject},
		RequiresEvidence:      true,
		Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields: []contextfabric.FactFieldDeclaration{
			{Name: "compounding_risk", Type: contextfabric.FactFieldNumber, Score: true, DriversTable: "risk_breakdown", Aggregate: true},
			{Name: "repo_count", Type: contextfabric.FactFieldInteger, Aggregate: true},
			{Name: "risk_breakdown", Type: contextfabric.FactFieldTable, Columns: []contextfabric.FactColumnDeclaration{
				{Name: "scope", Type: contextfabric.FactFieldString},
				{Name: "scope_id", Type: contextfabric.FactFieldString, SubjectRef: scopeRef},
				{Name: "scope_name", Type: contextfabric.FactFieldString},
				{Name: "compounding_risk", Type: contextfabric.FactFieldNumber},
			}},
		},
	}
}

func riskRow(scope, id, name string, risk float64) contextfabric.FactValueRow {
	return contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
		"scope": strValue(scope), "scope_id": strValue(id), "scope_name": strValue(name), "compounding_risk": numValue(risk),
	}}
}

// projectQHealth is the health fact of project Q, owned by team T, which
// owns repositories A and B. B's values are distinctive so a leak is visible
// anywhere in the serialized response.
func projectQHealth(subject contextfabric.SubjectRef) contextfabric.CanonicalFact {
	return contextfabric.CanonicalFact{
		Kind: contextfabric.FactHealth, Subject: subject,
		Fields: map[string]contextfabric.FactValue{
			"compounding_risk": numValue(0.5),
			"repo_count":       intValue(2),
			"risk_breakdown": contextfabric.RowsFactValue([]contextfabric.FactValueRow{
				riskRow("repo", "a", "acme/a", 0.25),
				riskRow("repo", "b", "acme/b-secret-name", 0.8765),
			}),
		},
		EvidenceRefIDs: []string{
			contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, "jira:Q"),
			contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "a"),
			contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "b"),
		},
		SourceState: contextfabric.SourceAvailable,
	}
}

func newTestFactsReader(t *testing.T, graph GraphAuthority, providers ...contextfabric.FactProvider) *FactsReader {
	t.Helper()
	registry, err := contextfabric.NewFactCapabilityRegistry(providers, contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	gate := NewSubjectGate(graph, nil)
	reader := NewFactsReader(gate, NewFactReader(registry.WithoutScopeExpansion()), nil)
	reader.now = func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }
	return reader
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// T14 (BUILD ENTRY S2 first failing test). Rule 1: project Q, team T owns
// repositories A and B, caller granted A. The health fact of Q carries no
// id, name, risk value or evidence reference of B, and rows_withheld = 1.
// Rule 2 plant: root gate only (Filter passes facts through) -> this fails.
func TestChaos7073EmbeddedSubjectsOfUnseenRepositoryAreWithheld(t *testing.T) {
	provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		facts := []contextfabric.CanonicalFact{}
		for _, subject := range query.Subjects {
			facts = append(facts, projectQHealth(subject))
		}
		return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable}, nil
	}}
	reader := newTestFactsReader(t, graphOfOrgA(), provider)
	response, err := reader.Read(context.Background(), restrictedToA(), FactsRequest{
		Kinds:    []string{"health"},
		Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}},
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(response.Facts) != 1 {
		t.Fatalf("facts = %d, want 1 (%s)", len(response.Facts), mustJSON(t, response))
	}
	encoded := mustJSON(t, response)
	for _, leak := range []string{`"b"`, "repository:b", "acme/b-secret-name", "0.8765", "repository:b\"", "acr:v1:repository:b"} {
		if strings.Contains(encoded, leak) {
			t.Errorf("response leaks unseen repository B (%q): %s", leak, encoded)
		}
	}
	table := response.Facts[0].Tables["risk_breakdown"]
	if table.RowsWithheld != 1 || len(table.Rows) != 1 {
		t.Errorf("risk_breakdown rows=%d withheld=%d, want 1 and 1", len(table.Rows), table.RowsWithheld)
	}
	if !strings.Contains(encoded, "acme/a") {
		t.Errorf("the granted repository's row is missing: %s", encoded)
	}
	fact := response.Facts[0]
	if fact.AggregateScope != "all_owned_repositories" || len(fact.AggregateFields) != 2 {
		t.Errorf("aggregate label = %q %v, want all_owned_repositories over 2 fields (decision K2)", fact.AggregateScope, fact.AggregateFields)
	}
	if _, ok := fact.Fields["compounding_risk"]; !ok {
		t.Errorf("score with its drivers table served must be present: %v", fact.Fields)
	}
	keys := fact.Provenance.NaturalKeys
	if len(keys) != 2 {
		t.Errorf("natural keys = %v, want the project and repository A", keys)
	}
	if response.Status != StatusPartial {
		t.Errorf("status = %q, want partial (rows withheld)", response.Status)
	}
}

// T14 clause cases (rule 3): the row filter on a team reference, the
// evidence filter, and the project wildcard rule (a project reached only
// through B is refused at the root).
func TestChaos7073EmbeddedGateClauses(t *testing.T) {
	principal := restrictedToA()
	t.Run("team row naming a team the caller cannot see is withheld", func(t *testing.T) {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			fact := projectQHealth(query.Subjects[0])
			fact.Fields["risk_breakdown"] = contextfabric.RowsFactValue([]contextfabric.FactValueRow{
				riskRow("team", "t", "Team T", 0.3), riskRow("team", "u", "Team U secret", 0.91),
			})
			fact.EvidenceRefIDs = fact.EvidenceRefIDs[:1]
			return contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{fact}, State: contextfabric.SourceAvailable}, nil
		}}
		response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(context.Background(), principal, FactsRequest{
			Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
		if err != nil {
			t.Fatal(err)
		}
		encoded := mustJSON(t, response)
		if strings.Contains(encoded, "Team U secret") || strings.Contains(encoded, "0.91") {
			t.Errorf("team U row leaked: %s", encoded)
		}
		if got := response.Facts[0].Tables["risk_breakdown"].RowsWithheld; got != 1 {
			t.Errorf("rows_withheld = %d, want 1", got)
		}
	})
	t.Run("evidence reference to an unseen repository is removed", func(t *testing.T) {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			fact := projectQHealth(query.Subjects[0])
			fact.Fields["risk_breakdown"] = contextfabric.RowsFactValue([]contextfabric.FactValueRow{riskRow("repo", "a", "acme/a", 0.25)})
			return contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{fact}, State: contextfabric.SourceAvailable}, nil
		}}
		response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(context.Background(), principal, FactsRequest{
			Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range response.Facts[0].Provenance.NaturalKeys {
			if key.Entity == "repository" && key.ID == "b" {
				t.Errorf("evidence of repository B served: %v", response.Facts[0].Provenance.NaturalKeys)
			}
		}
	})
	t.Run("project reached only through an unseen repository is refused at the root", func(t *testing.T) {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			t.Errorf("provider read for a refused root: %v", query.Subjects)
			return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
		}}
		response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(context.Background(), principal, FactsRequest{
			Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectP.CanonicalID}}})
		if err != nil {
			t.Fatal(err)
		}
		if response.Status != StatusDenied || len(response.Request.SubjectsRefused) != 1 || response.Request.SubjectsRefused[0].Answer != RefusalDeniedOrNotFound {
			t.Errorf("project P: status %q refused %v, want denied with denied_or_not_found", response.Status, response.Request.SubjectsRefused)
		}
	})
	t.Run("unrestricted caller sees every row", func(t *testing.T) {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			return contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{projectQHealth(query.Subjects[0])}, State: contextfabric.SourceAvailable}, nil
		}}
		unrestricted := storage.Principal{OrgID: orgA, Subject: "user-2", CredentialID: "cred-2"}
		response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(context.Background(), unrestricted, FactsRequest{
			Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
		if err != nil {
			t.Fatal(err)
		}
		if table := response.Facts[0].Tables["risk_breakdown"]; len(table.Rows) != 2 || table.RowsWithheld != 0 {
			t.Errorf("unrestricted rows=%d withheld=%d, want 2 and 0", len(table.Rows), table.RowsWithheld)
		}
	})
	t.Run("graph failure on embedded references fails the read closed", func(t *testing.T) {
		graph := graphOfOrgA()
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			graph.authErr = contextfabric.ErrUnavailable // the root decision already ran
			return contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{projectQHealth(query.Subjects[0])}, State: contextfabric.SourceAvailable}, nil
		}}
		response, err := newTestFactsReader(t, graph, provider).Read(context.Background(), principal, FactsRequest{
			Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
		if err == nil || len(response.Facts) != 0 {
			t.Fatalf("embedded gate failure served %d facts, err %v", len(response.Facts), err)
		}
	})
	t.Run("undeclared field is not served", func(t *testing.T) {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			fact := projectQHealth(query.Subjects[0])
			fact.Fields["undeclared_probe"] = strValue("should-not-leave")
			return contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{fact}, State: contextfabric.SourceAvailable}, nil
		}}
		response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(context.Background(), principal, FactsRequest{
			Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(mustJSON(t, response), "should-not-leave") {
			t.Error("undeclared field value served")
		}
	})
	t.Run("score without its drivers table is withheld", func(t *testing.T) {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			fact := projectQHealth(query.Subjects[0])
			delete(fact.Fields, "risk_breakdown")
			return contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{fact}, State: contextfabric.SourceAvailable}, nil
		}}
		unrestricted := storage.Principal{OrgID: orgA, Subject: "user-2", CredentialID: "cred-2"}
		response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(context.Background(), unrestricted, FactsRequest{
			Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := response.Facts[0].Fields["compounding_risk"]; ok {
			t.Error("a bare score was served (decision K7)")
		}
		if len(response.Coverage) != 1 || len(response.Coverage[0].Withheld) != 1 || response.Coverage[0].Withheld[0] != WithheldDriversNotServed {
			t.Errorf("coverage withheld = %+v, want drivers_not_served", response.Coverage)
		}
	})
	t.Run("tables omit keeps the drivers table", func(t *testing.T) {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			return contextfabric.FactProviderResult{Facts: []contextfabric.CanonicalFact{projectQHealth(query.Subjects[0])}, State: contextfabric.SourceAvailable}, nil
		}}
		response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(context.Background(), principal, FactsRequest{
			Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}, Tables: TablesOmit})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := response.Facts[0].Tables["risk_breakdown"]; !ok {
			t.Error("tables=omit dropped the drivers table of a served score")
		}
	})
}
