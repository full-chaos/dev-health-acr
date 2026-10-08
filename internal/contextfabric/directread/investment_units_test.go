package directread

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func unitsInvestmentCapability() contextfabric.FactCapability {
	repoRef := &contextfabric.FactSubjectRefDeclaration{Kind: contractsv1.ContextFabricSubjectRepository, IDForm: contextfabric.FactSubjectIDRepositoryUUID}
	nullable := func(d contextfabric.FactFieldDeclaration) contextfabric.FactFieldDeclaration {
		d.Nullable = true
		return d
	}
	return contextfabric.FactCapability{
		Kind: contextfabric.FactInvestment, Name: "investment_units_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam},
		RequiresEvidence:      true,
		Dimension:             contextfabric.HealthDimensionInvestmentBalance,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields: []contextfabric.FactFieldDeclaration{
			nullable(contextfabric.FactFieldDeclaration{Name: "unit_kind", Type: contextfabric.FactFieldString}),
			nullable(contextfabric.FactFieldDeclaration{Name: "work_unit_id", Type: contextfabric.FactFieldString}),
			nullable(contextfabric.FactFieldDeclaration{Name: "repository_id", Type: contextfabric.FactFieldString, SubjectRef: repoRef}),
			nullable(contextfabric.FactFieldDeclaration{Name: "share_in_scope", Type: contextfabric.FactFieldNumber}),
			nullable(contextfabric.FactFieldDeclaration{Name: "units_returned", Type: contextfabric.FactFieldInteger}),
			nullable(contextfabric.FactFieldDeclaration{Name: "units_refs_unresolved", Type: contextfabric.FactFieldInteger}),
			nullable(contextfabric.FactFieldDeclaration{Name: "next_cursor", Type: contextfabric.FactFieldString}),
		},
	}
}

func unitRowFact(team contextfabric.SubjectRef, workUnit, repo string, share float64) contextfabric.CanonicalFact {
	return contextfabric.CanonicalFact{
		Kind: contextfabric.FactInvestment, Subject: team,
		Fields: map[string]contextfabric.FactValue{
			"unit_kind":      strValue(contextfabric.InvestmentUnitKind),
			"work_unit_id":   strValue(workUnit),
			"repository_id":  strValue(repo),
			"share_in_scope": numValue(share),
		},
		EvidenceRefIDs: []string{"acr:v1:repository:" + repo, "acr:v1:pull-request:" + repo + ":7"},
	}
}

func unitsProvider(seen *[]contextfabric.InvestmentUnitsRequest) *stubProvider {
	return &stubProvider{capability: unitsInvestmentCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		team := query.Subjects[0]
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Facts: []contextfabric.CanonicalFact{
			{Kind: contextfabric.FactInvestment, Subject: team, Fields: map[string]contextfabric.FactValue{
				"unit_kind": strValue(contextfabric.InvestmentUnitPageKind), "units_returned": intValue(2), "units_refs_unresolved": intValue(3),
			}, EvidenceRefIDs: []string{"acr:v1:team:t"}},
			unitRowFact(team, "wu-a", "a", 5),
			unitRowFact(team, "wu-b", "b", 7),
		}}, nil
	}}
}

type unitsCapture struct {
	stubProvider
	seen *[]contextfabric.InvestmentUnitsRequest
}

func (p *unitsCapture) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if request, ok := contextfabric.InvestmentUnitsFrom(ctx); ok {
		*p.seen = append(*p.seen, request)
	}
	return p.stubProvider.ReadFacts(ctx, principal, query)
}

func TestInvestmentUnitsRestrictedCallerSeesOnlyGrantedRepositoryRows(t *testing.T) {
	var seen []contextfabric.InvestmentUnitsRequest
	provider := &unitsCapture{stubProvider: *unitsProvider(&seen), seen: &seen}
	reader := newTestFactsReader(t, graphOfOrgA(), provider)
	response, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{
		Kinds:    []string{"investment"},
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}},
		Units:    &RequestUnits{MaxUnits: 2},
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	encoded := mustJSON(t, response)
	var rows int
	for _, fact := range response.Facts {
		if fact.Fields["unit_kind"] == contextfabric.InvestmentUnitKind {
			rows++
			if fact.Fields["work_unit_id"] != "wu-a" {
				t.Errorf("a unit row outside the grant was served: %v", fact.Fields)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("unit rows = %d, want 1 (the repository the caller may read): %s", rows, encoded)
	}
	for _, leak := range []string{"wu-b", "repository:b", "acr:v1:repository:b"} {
		if strings.Contains(encoded, leak) {
			t.Errorf("response leaks the unseen repository row (%q): %s", leak, encoded)
		}
	}
	var reason string
	for _, row := range response.Coverage {
		reason = row.Reason
	}
	if !strings.Contains(reason, "units_not_visible 1") || !strings.Contains(reason, "refs_unresolved 3") {
		t.Errorf("coverage reason = %q, want units_not_visible 1 and refs_unresolved 3", reason)
	}
	if len(seen) != 1 || seen[0].Max != 2 || seen[0].Cursor != nil {
		t.Errorf("provider saw units requests %+v, want one with Max 2 and no cursor", seen)
	}
	if response.Request.Units == nil || response.Request.Units.MaxUnits != 2 {
		t.Errorf("request echo units = %+v", response.Request.Units)
	}
}

func TestInvestmentUnitsUnrestrictedCallerSeesEveryRow(t *testing.T) {
	var seen []contextfabric.InvestmentUnitsRequest
	provider := &unitsCapture{stubProvider: *unitsProvider(&seen), seen: &seen}
	reader := newTestFactsReader(t, graphOfOrgA(), provider)
	unrestricted := storage.Principal{OrgID: orgA, Subject: "user-2", CredentialID: "cred-2"}
	response, err := reader.Read(requestContext(), unrestricted, FactsRequest{
		Kinds:    []string{"investment"},
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}},
		Units:    &RequestUnits{},
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	rows := 0
	for _, fact := range response.Facts {
		if fact.Fields["unit_kind"] == contextfabric.InvestmentUnitKind {
			rows++
		}
	}
	if rows != 2 {
		t.Fatalf("unit rows = %d, want 2: %s", rows, mustJSON(t, response))
	}
	if len(seen) != 1 || seen[0].Max != contextfabric.InvestmentUnitsDefaultMax {
		t.Errorf("provider saw %+v, want the default page size", seen)
	}
}

func TestInvestmentUnitsAreNotRequestedWithoutTheArgument(t *testing.T) {
	var seen []contextfabric.InvestmentUnitsRequest
	provider := &unitsCapture{stubProvider: *unitsProvider(&seen), seen: &seen}
	reader := newTestFactsReader(t, graphOfOrgA(), provider)
	if _, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{
		Kinds:    []string{"investment"},
		Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}},
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("a plain read carried a units request: %+v", seen)
	}
}

func TestInvestmentUnitsRequestValidation(t *testing.T) {
	var seen []contextfabric.InvestmentUnitsRequest
	provider := &unitsCapture{stubProvider: *unitsProvider(&seen), seen: &seen}
	reader := newTestFactsReader(t, graphOfOrgA(), provider)
	team := RequestSubject{Kind: "team", CanonicalID: teamT.CanonicalID}
	cases := map[string]FactsRequest{
		"no investment kind":  {Kinds: []string{"health"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{}},
		"two subjects":        {Kinds: []string{"investment"}, Subjects: []RequestSubject{team, {Kind: "repository", CanonicalID: repoA.CanonicalID}}, Units: &RequestUnits{}},
		"project subject":     {Kinds: []string{"investment"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectP.CanonicalID}}, Units: &RequestUnits{}},
		"page size too large": {Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{MaxUnits: contextfabric.InvestmentUnitsMaxMax + 1}},
		"negative page size":  {Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{MaxUnits: -1}},
		"foreign cursor":      {Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{Cursor: "not-a-cursor"}},
	}
	for name, request := range cases {
		if _, err := reader.Read(requestContext(), restrictedToA(), request); err == nil || !strings.Contains(err.Error(), FactsRefusalInvalidRequest) {
			t.Errorf("%s: err = %v, want invalid_request", name, err)
		}
	}
	token := contextfabric.EncodeInvestmentUnitsCursor(contextfabric.InvestmentUnitsCursor{Share: 4.5, WorkUnitID: "wu-x", RepoID: "a"})
	if _, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{Cursor: token, MaxUnits: 150}}); err != nil {
		t.Fatalf("a valid page request was refused: %v", err)
	}
	last := seen[len(seen)-1]
	if last.Cursor == nil || last.Cursor.Share != 4.5 || last.Cursor.WorkUnitID != "wu-x" || last.Cursor.RepoID != "a" || last.Max != 150 {
		t.Errorf("provider saw %+v, want the decoded cursor and Max 150", last)
	}
}
