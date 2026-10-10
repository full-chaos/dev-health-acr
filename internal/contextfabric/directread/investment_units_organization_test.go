package directread

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func organizationUnitsCapability() contextfabric.FactCapability {
	capability := unitsInvestmentCapability()
	capability.SupportedSubjectKinds = append(capability.SupportedSubjectKinds, contractsv1.ContextFabricSubjectOrganization)
	nullable := func(d contextfabric.FactFieldDeclaration) contextfabric.FactFieldDeclaration {
		d.Nullable = true
		return d
	}
	capability.Fields = append(capability.Fields,
		nullable(contextfabric.FactFieldDeclaration{Name: "unit_attribution_basis", Type: contextfabric.FactFieldString}),
		nullable(contextfabric.FactFieldDeclaration{Name: "scope_unattributed_rows", Type: contextfabric.FactFieldInteger, Aggregate: true}),
		nullable(contextfabric.FactFieldDeclaration{Name: "scope_unattributed_total", Type: contextfabric.FactFieldNumber, Aggregate: true}),
	)
	return capability
}

// unattributedRowFact is an organization unit row whose effort reaches no
// resolved repository: no repository_id, the unattributed basis.
func unattributedRowFact(org contextfabric.SubjectRef, workUnit string, share float64) contextfabric.CanonicalFact {
	return contextfabric.CanonicalFact{
		Kind: contextfabric.FactInvestment, Subject: org,
		Fields: map[string]contextfabric.FactValue{
			"unit_kind":              strValue(contextfabric.InvestmentUnitKind),
			"work_unit_id":           strValue(workUnit),
			"share_in_scope":         numValue(share),
			"unit_attribution_basis": strValue(contextfabric.InvestmentUnitAttributionUnattributed),
		},
		EvidenceRefIDs: []string{"acr:v1:organization:" + orgA},
	}
}

// The organization's listing serves effort that reaches no resolved repository
// as rows with no repository_id, states the count in the coverage reason, and
// seals a cursor whose last row is such a row so the next page resumes after it.
func TestInvestmentUnitsOrganizationServesUnattributedRowsAndResumesAfterThem(t *testing.T) {
	var seen []contextfabric.InvestmentUnitsRequest
	stub := &stubProvider{capability: organizationUnitsCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		org := query.Subjects[0]
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Facts: []contextfabric.CanonicalFact{
			{Kind: contextfabric.FactInvestment, Subject: org, Fields: map[string]contextfabric.FactValue{
				"unit_kind": strValue(contextfabric.InvestmentUnitPageKind), "units_returned": intValue(2),
				"scope_unattributed_rows": intValue(4), "scope_unattributed_total": numValue(2.5),
				"next_cursor": strValue(contextfabric.EncodeInvestmentUnitsCursor(contextfabric.InvestmentUnitsCursor{Share: 1.5, WorkUnitID: "wu-u", RepoID: contextfabric.InvestmentUnitsUnattributedRepo})),
			}, EvidenceRefIDs: []string{"acr:v1:organization:" + orgA}},
			unitRowFact(org, "wu-a", "a", 5),
			unattributedRowFact(org, "wu-u", 1.5),
		}}, nil
	}}
	provider := &unitsCapture{stubProvider: *stub, seen: &seen}
	reader := newUnitsReader(t, provider)
	unrestricted := storage.Principal{OrgID: orgA, Subject: "user-2", CredentialID: "cred-2"}
	organization := RequestSubject{Kind: "organization", CanonicalID: orgA}
	first, err := reader.Read(requestContext(), unrestricted, FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{organization}, Units: &RequestUnits{MaxUnits: 2}})
	if err != nil {
		t.Fatalf("an organization units request was refused: %v", err)
	}
	var unattributed, repositoryRows int
	for _, fact := range first.Facts {
		if fact.Fields["unit_kind"] != contextfabric.InvestmentUnitKind {
			continue
		}
		if _, has := fact.Fields["repository_id"]; has {
			repositoryRows++
			continue
		}
		unattributed++
		if fact.Fields["unit_attribution_basis"] != contextfabric.InvestmentUnitAttributionUnattributed {
			t.Errorf("unattributed row basis = %v", fact.Fields["unit_attribution_basis"])
		}
	}
	if unattributed != 1 || repositoryRows != 1 {
		t.Fatalf("rows: unattributed %d, repository %d, want 1 and 1: %s", unattributed, repositoryRows, mustJSON(t, first))
	}
	var reason string
	for _, row := range first.Coverage {
		reason += row.Reason
	}
	if !strings.Contains(reason, "units_unattributed 4 rows") {
		t.Errorf("coverage reason = %q, want units_unattributed 4 rows", reason)
	}
	token := pageCursor(t, first)
	if _, err := reader.Read(requestContext(), unrestricted, FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{organization}, Units: &RequestUnits{Cursor: token, MaxUnits: 2}}); err != nil {
		t.Fatalf("the cursor after an unattributed row was refused: %v", err)
	}
	last := seen[len(seen)-1]
	if last.Cursor == nil || last.Cursor.WorkUnitID != "wu-u" || last.Cursor.RepoID != contextfabric.InvestmentUnitsUnattributedRepo {
		t.Errorf("provider saw cursor %+v, want the unattributed row's position", last.Cursor)
	}
}

// The keyset position of an unattributed row, the one a byte-cut page resumes
// from, carries the unattributed word in place of a repository.
func TestUnitPositionOfAnUnattributedRowCarriesTheUnattributedWord(t *testing.T) {
	t.Parallel()
	fact := ServedFact{Kind: string(contextfabric.FactInvestment), Fields: map[string]any{
		"unit_kind": contextfabric.InvestmentUnitKind, "share_in_scope": 1.5, "work_unit_id": "wu-u",
		"unit_attribution_basis": contextfabric.InvestmentUnitAttributionUnattributed,
	}}
	position, ok := unitPositionOf(fact)
	if !ok {
		t.Fatal("an unattributed row has no resumable position")
	}
	cursor, err := contextfabric.DecodeInvestmentUnitsCursor(position)
	if err != nil || cursor.RepoID != contextfabric.InvestmentUnitsUnattributedRepo || cursor.WorkUnitID != "wu-u" {
		t.Fatalf("position decodes to %+v, %v", cursor, err)
	}
	delete(fact.Fields, "unit_attribution_basis")
	if _, ok := unitPositionOf(fact); ok {
		t.Fatal("a row with neither a repository nor the unattributed basis must have no position")
	}
}

// A project subject still has no units listing.
func TestInvestmentUnitsStillRefuseAProjectSubject(t *testing.T) {
	reader := newUnitsReader(t, &stubProvider{capability: organizationUnitsCapability(), read: func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceNoData}, nil
	}})
	_, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectP.CanonicalID}}, Units: &RequestUnits{}})
	if err == nil || !strings.Contains(err.Error(), "team, a repository or an organization") {
		t.Fatalf("err = %v, want the units subject refusal naming team, repository or organization", err)
	}
}
