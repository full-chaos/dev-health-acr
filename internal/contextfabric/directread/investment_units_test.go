package directread

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
			nullable(contextfabric.FactFieldDeclaration{Name: "units_returned", Type: contextfabric.FactFieldInteger, Aggregate: true}),
			nullable(contextfabric.FactFieldDeclaration{Name: "units_refs_unresolved", Type: contextfabric.FactFieldInteger, Aggregate: true}),
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
				"repository_id": strValue("b"),
				"next_cursor":   strValue(hiddenRowCursor),
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
	reader := newUnitsReader(t, provider)
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
	pages := 0
	for _, fact := range response.Facts {
		if fact.Fields["unit_kind"] == contextfabric.InvestmentUnitPageKind {
			pages++
		}
	}
	for _, fact := range response.Facts {
		if fact.Fields["unit_kind"] == contextfabric.InvestmentUnitPageKind && (fact.AggregateScope != "all_owned_repositories" || len(fact.AggregateFields) != 2) {
			t.Errorf("page fact aggregate label = %q %v, want all_owned_repositories over its 2 declared aggregates", fact.AggregateScope, fact.AggregateFields)
		}
	}
	if pages != 1 {
		t.Fatalf("page summary facts = %d, want 1 (the page fact is never dropped with the rows it summarises)", pages)
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
	reader := newUnitsReader(t, provider)
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
	reader := newUnitsReader(t, provider)
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
	reader := newUnitsReader(t, provider)
	team := RequestSubject{Kind: "team", CanonicalID: teamT.CanonicalID}
	cases := map[string]FactsRequest{
		"no investment kind":  {Kinds: []string{"health"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{}},
		"two subjects":        {Kinds: []string{"investment"}, Subjects: []RequestSubject{team, {Kind: "repository", CanonicalID: repoA.CanonicalID}}, Units: &RequestUnits{}},
		"project subject":     {Kinds: []string{"investment"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectP.CanonicalID}}, Units: &RequestUnits{}},
		"page size too large": {Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{MaxUnits: contextfabric.InvestmentUnitsMaxMax + 1}},
		"negative page size":  {Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{MaxUnits: -1}},
		"foreign cursor":      {Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{Cursor: "not-a-cursor"}},
	}
	healthProvider := &stubProvider{capability: healthLikeCapability(), read: func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceNoData}, nil
	}}
	withHealth := newUnitsReader(t, provider, healthProvider)
	_, err := withHealth.Read(requestContext(), restrictedToA(), FactsRequest{Kinds: []string{"health"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{}})
	if err == nil || !strings.Contains(err.Error(), "investment") {
		t.Errorf("units without the investment kind: err = %v, want invalid_request naming investment", err)
	}
	for name, request := range cases {
		if _, err := reader.Read(requestContext(), restrictedToA(), request); err == nil || !strings.Contains(err.Error(), FactsRefusalInvalidRequest) {
			t.Errorf("%s: err = %v, want invalid_request", name, err)
		}
	}
	first, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{}})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	token := pageCursor(t, first)
	if _, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Units: &RequestUnits{Cursor: token, MaxUnits: 150}}); err != nil {
		t.Fatalf("a valid page request was refused: %v", err)
	}
	last := seen[len(seen)-1]
	if last.Cursor == nil || last.Cursor.Share != 4.5 || last.Cursor.WorkUnitID != "wu-b" || last.Cursor.RepoID != "b" || last.Max != 150 {
		t.Errorf("provider saw %+v, want the position the sealed cursor held and Max 150", last)
	}
}

var hiddenRowCursor = contextfabric.EncodeInvestmentUnitsCursor(contextfabric.InvestmentUnitsCursor{Share: 4.5, WorkUnitID: "wu-b", RepoID: "b"})

func unitsTestKeyring() CursorKeyring {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return CursorKeyring{ActiveKID: "k1", Keys: map[string][]byte{"k1": key}}
}

func newUnitsReader(t *testing.T, providers ...contextfabric.FactProvider) *FactsReader {
	t.Helper()
	return newTestFactsReader(t, graphOfOrgA(), providers...).WithCursorKeyring(unitsTestKeyring())
}

func pageCursor(t *testing.T, response FactsResponse) string {
	t.Helper()
	for _, fact := range response.Facts {
		if fact.Fields["unit_kind"] == contextfabric.InvestmentUnitPageKind {
			if token, ok := fact.Fields["next_cursor"].(string); ok {
				return token
			}
		}
	}
	t.Fatalf("no next_cursor on the page fact: %s", mustJSON(t, response))
	return ""
}

// The provider's cursor names the last row it READ, a row of a repository the
// caller may not read. It must leave the server sealed: no id, share or
// repository of that row is readable, it opens only for the subject and window
// it was issued for, and the provider then sees the position it held.
func TestInvestmentUnitsCursorIsSealedAndBoundToSubjectAndWindow(t *testing.T) {
	var seen []contextfabric.InvestmentUnitsRequest
	provider := &unitsCapture{stubProvider: *unitsProvider(&seen), seen: &seen}
	reader := newUnitsReader(t, provider)
	team := RequestSubject{Kind: "team", CanonicalID: teamT.CanonicalID}
	window := &RequestWindow{Mode: WindowTrailing, Days: 30}
	first, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Window: window, Units: &RequestUnits{MaxUnits: 1}})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	token := pageCursor(t, first)
	if token == hiddenRowCursor {
		t.Fatal("the provider's plain cursor was served")
	}
	if _, err := contextfabric.DecodeInvestmentUnitsCursor(token); err == nil {
		t.Fatal("the served cursor decodes as a plain keyset position")
	}
	for _, leak := range []string{"wu-b", hiddenRowCursor} {
		if strings.Contains(mustJSON(t, first), leak) {
			t.Fatalf("response carries %q of the row the caller may not read", leak)
		}
	}
	if opened, err := reader.openUnitsCursor(token, orgA, unitsRequestDigest(FactsRequest{Window: window}, readPlan{subjects: []contextfabric.SubjectRef{teamT}})); err != nil || opened != hiddenRowCursor {
		t.Fatalf("the sealed cursor holds %q, %v; want the provider position", opened, err)
	}
	unrestricted := storage.Principal{OrgID: orgA, Subject: "user-2", CredentialID: "cred-2"}
	next := func(subject RequestSubject, w *RequestWindow) error {
		_, err := reader.Read(requestContext(), unrestricted, FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{subject}, Window: w, Units: &RequestUnits{Cursor: token}})
		return err
	}
	if err := next(team, window); err != nil {
		t.Fatalf("the cursor was refused for its own subject and window: %v", err)
	}
	if last := seen[len(seen)-1]; last.Cursor == nil || last.Cursor.WorkUnitID != "wu-b" {
		t.Fatalf("provider saw %+v, want the position the cursor held", last)
	}
	if err := next(RequestSubject{Kind: "team", CanonicalID: teamU.CanonicalID}, window); err == nil {
		t.Error("a cursor issued for one team was accepted for another")
	}
	if err := next(team, &RequestWindow{Mode: WindowTrailing, Days: 7}); err == nil {
		t.Error("a cursor issued for one window was accepted for another")
	}
	if err := next(team, nil); err == nil {
		t.Error("a cursor issued for a window was accepted for the default one")
	}
	raw := []byte(token)
	raw[len(raw)-3] ^= 1
	if err := next(team, window); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(requestContext(), unrestricted, FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{team}, Window: window, Units: &RequestUnits{Cursor: string(raw)}}); err == nil {
		t.Error("an edited cursor was accepted")
	}
}

func TestInvestmentUnitsNeedAKeyring(t *testing.T) {
	var seen []contextfabric.InvestmentUnitsRequest
	provider := &unitsCapture{stubProvider: *unitsProvider(&seen), seen: &seen}
	reader := newTestFactsReader(t, graphOfOrgA(), provider)
	_, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}, Units: &RequestUnits{}})
	if err == nil || !errors.Is(err, ErrFactsUnavailable) {
		t.Fatalf("err = %v, want unavailable without a cursor keyring", err)
	}
	if len(seen) != 0 {
		t.Fatalf("the provider was read %d times without a keyring", len(seen))
	}
}

// The provider chose its cursor before the byte budget dropped unit rows; the
// cursor served resumes after the last row actually served, and the page says
// how many rows the budget cut.
func TestInvestmentUnitsByteBudgetKeepsTheCursorHonest(t *testing.T) {
	var seen []contextfabric.InvestmentUnitsRequest
	many := &unitsCapture{seen: &seen, stubProvider: stubProvider{capability: unitsInvestmentCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		team := query.Subjects[0]
		facts := []contextfabric.CanonicalFact{{Kind: contextfabric.FactInvestment, Subject: team, Fields: map[string]contextfabric.FactValue{
			"unit_kind": strValue(contextfabric.InvestmentUnitPageKind), "units_returned": intValue(60),
		}, EvidenceRefIDs: []string{"acr:v1:team:t"}}}
		for i := 0; i < 60; i++ {
			facts = append(facts, unitRowFact(team, fmt.Sprintf("wu-%02d-%s", i, strings.Repeat("x", 40)), "a", float64(100-i)))
		}
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Facts: facts}, nil
	}}}
	reader := newUnitsReader(t, many)
	response, err := reader.Read(requestContext(), restrictedToA(), FactsRequest{
		Kinds: []string{"investment"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}},
		Units: &RequestUnits{MaxUnits: 60}, MaxBytes: MinMaxBytes,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	served := countUnitRows(response.Facts)
	if served == 0 || served >= 60 {
		t.Fatalf("served %d unit rows, want some but not all 60 under the byte bound", served)
	}
	token := pageCursor(t, response)
	var lastID string
	for _, fact := range response.Facts {
		if fact.Fields["unit_kind"] == contextfabric.InvestmentUnitKind {
			lastID = fact.Fields["work_unit_id"].(string)
		}
	}
	for _, fact := range response.Facts {
		if fact.Fields["unit_kind"] == contextfabric.InvestmentUnitPageKind && fact.Fields["units_returned"] != strconv.Itoa(served) {
			t.Errorf("units_returned = %v, want the %d rows served", fact.Fields["units_returned"], served)
		}
	}
	position, err := reader.openUnitsCursor(token, orgA, unitsRequestDigest(FactsRequest{}, readPlan{subjects: []contextfabric.SubjectRef{teamT}}))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	cursor, err := contextfabric.DecodeInvestmentUnitsCursor(position)
	if err != nil || cursor.WorkUnitID != lastID {
		t.Fatalf("cursor resumes after %q (%v), want the last row served %q", cursor.WorkUnitID, err, lastID)
	}
	var reason string
	for _, row := range response.Coverage {
		reason = row.Reason
	}
	if !strings.Contains(reason, "units_cut_by_max_bytes") {
		t.Errorf("coverage reason = %q, want units_cut_by_max_bytes", reason)
	}
}

// unit_unresolved_refs is an opaque reference: withheld from a repository-
// restricted caller, served to an unrestricted one.
func TestInvestmentUnitsUnresolvedHandlesAreWithheldFromARestrictedCaller(t *testing.T) {
	capability := unitsInvestmentCapability()
	capability.Fields = append(capability.Fields, contextfabric.FactFieldDeclaration{
		Name: "unit_unresolved_refs", Type: contextfabric.FactFieldString, Nullable: true,
		SubjectRef: &contextfabric.FactSubjectRefDeclaration{Kind: contractsv1.ContextFabricSubjectRepository, IDForm: contextfabric.FactSubjectIDOpaque},
	})
	provider := &stubProvider{capability: capability, read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		team := query.Subjects[0]
		row := unitRowFact(team, "wu-a", "a", 5)
		row.Fields["unit_unresolved_refs"] = strValue("ghpr:private/repo#17")
		page := contextfabric.CanonicalFact{Kind: contextfabric.FactInvestment, Subject: team, Fields: map[string]contextfabric.FactValue{
			"unit_kind": strValue(contextfabric.InvestmentUnitPageKind),
		}, EvidenceRefIDs: []string{"acr:v1:team:t"}}
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Facts: []contextfabric.CanonicalFact{page, row}}, nil
	}}
	reader := newUnitsReader(t, provider)
	request := FactsRequest{Kinds: []string{"investment"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}, Units: &RequestUnits{}}
	restricted, err := reader.Read(requestContext(), restrictedToA(), request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mustJSON(t, restricted), "private/repo") {
		t.Fatalf("a restricted caller received a handle naming a repository outside its grant: %s", mustJSON(t, restricted))
	}
	open, err := newUnitsReader(t, provider).Read(requestContext(), storage.Principal{OrgID: orgA, Subject: "user-2", CredentialID: "cred-2"}, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustJSON(t, open), "ghpr:private/repo#17") {
		t.Fatalf("an unrestricted caller lost the handle: %s", mustJSON(t, open))
	}
}

// A cursor opens only for the organization that was issued it.
func TestInvestmentUnitsCursorIsBoundToTheOrganization(t *testing.T) {
	reader := newUnitsReader(t)
	token, err := reader.sealUnitsCursor(orgA, "digest", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.openUnitsCursor(token, orgA, "digest"); err != nil {
		t.Fatalf("own organization: %v", err)
	}
	if _, err := reader.openUnitsCursor(token, orgA+"-other", "digest"); err == nil {
		t.Error("a cursor issued in one organization opened in another")
	}
	if _, err := reader.openUnitsCursor(token, orgA, "other-digest"); err == nil {
		t.Error("a cursor issued for one request opened for another")
	}
}

// A page the provider did not cut, and the budget did not cut, serves no
// cursor: an empty token is not one.
func TestInvestmentUnitsCompletePageServesNoCursor(t *testing.T) {
	reader := newUnitsReader(t)
	response := FactsResponse{Facts: []ServedFact{{
		Kind: string(contextfabric.FactInvestment),
		Fields: map[string]any{"unit_kind": contextfabric.InvestmentUnitPageKind, "next_cursor": "", "units_returned": "0"},
	}}}
	if _, err := reader.finishUnitsPage(&response, orgA, "digest", "", 0); err != nil {
		t.Fatal(err)
	}
	if token, present := response.Facts[0].Fields["next_cursor"]; present {
		t.Fatalf("next_cursor = %v on a complete page", token)
	}
}
