package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7127: the engine's embedded-subject gate, at the seam every engine
// fact read passes through (gatedFactReader). The real-ClickHouse proof is
// devhealthfacts' TestCHAOS7127EngineProjectHealthCarriesNoUngrantedRepository;
// these pin each clause of the gate in isolation.

const (
	chaos7127Allowed = "11111111-1111-1111-1111-111111111111"
	chaos7127Private = "22222222-2222-2222-2222-222222222222"
)

var chaos7127ScopeRef = &FactSubjectRefDeclaration{
	KindColumn:  "scope",
	KindByValue: map[string]SubjectKind{"repo": SubjectRepository, "team": SubjectTeam},
	IDForm:      FactSubjectIDTeamID,
	FormByKind:  map[SubjectKind]FactSubjectIDForm{SubjectRepository: FactSubjectIDRepositoryUUID},
}

// chaos7127Capabilities: health declares a project risk_breakdown table
// (scope/scope_id reference, label, risk), an aggregate scalar, and a
// work-scope scalar with an opaque reference; status declares nothing.
func chaos7127Capabilities() []FactCapability {
	return []FactCapability{
		{Kind: FactHealth, Fields: []FactFieldDeclaration{
			{Name: "severity", Type: FactFieldString, Aggregate: true},
			{Name: "owner_team", Type: FactFieldString, SubjectRef: &FactSubjectRefDeclaration{Kind: SubjectTeam, IDForm: FactSubjectIDTeamID}},
			{Name: "work_scope", Type: FactFieldString, SubjectRef: &FactSubjectRefDeclaration{Kind: SubjectProject, IDForm: FactSubjectIDOpaque}},
			{Name: "risk_breakdown", Type: FactFieldTable, Columns: []FactColumnDeclaration{
				{Name: "scope", Type: FactFieldString},
				{Name: "scope_id", Type: FactFieldString, SubjectRef: chaos7127ScopeRef},
				{Name: "scope_name", Type: FactFieldString, Nullable: true},
				{Name: "risk", Type: FactFieldNumber},
			}},
		}},
		{Kind: FactStatus},
	}
}

func chaos7127Row(scope, id, name string, risk float64) FactValueRow {
	return FactValueRow{Fields: map[string]FactValue{
		"scope": StringFactValue(scope), "scope_id": StringFactValue(id), "scope_name": StringFactValue(name), "risk": {Number: &risk},
	}}
}

func chaos7127Project() SubjectRef {
	return SubjectRef{Kind: SubjectProject, CanonicalID: "project:linear:pay", Label: "Payments"}
}

func chaos7127HealthFact() CanonicalFact {
	return CanonicalFact{
		Kind: FactHealth, Subject: chaos7127Project(),
		Fields: map[string]FactValue{
			"severity":     StringFactValue("high"),
			"composed_sum": StringFactValue("registry-composed, undeclared"),
			"risk_breakdown": TableFactValue(FactTable{Rows: []FactValueRow{
				chaos7127Row("repo", chaos7127Allowed, "acme/allowed", 0.3),
				chaos7127Row("repo", chaos7127Private, "acme/private", 0.9),
				chaos7127Row("team", "team-pay", "Payments Team", 0.4),
			}}),
		},
		EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, chaos7127Allowed), contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, chaos7127Private)},
		SourceState:    SourceAvailable, Source: "test", SourceVersion: "v1",
	}
}

type chaos7127Inner struct {
	facts []CanonicalFact
	err   error
}

func (r *chaos7127Inner) ReadFacts(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
	facts := make([]CanonicalFact, len(r.facts))
	copy(facts, r.facts)
	return CanonicalFactBundle{Facts: facts, Version: "v1"}, r.err
}

func (*chaos7127Inner) Capabilities() []FactCapability { return chaos7127Capabilities() }

func chaos7127Graph(err error) *storedSubjectGraph {
	return &storedSubjectGraph{
		capturingGraphReader: &capturingGraphReader{},
		outcomes: map[string]StoredSubjectOutcome{
			SubjectMapKey(SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:" + chaos7127Allowed}): StoredSubjectAdmitted,
			SubjectMapKey(SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:" + chaos7127Private}): StoredSubjectDenied,
			SubjectMapKey(SubjectRef{Kind: SubjectTeam, CanonicalID: "team:team-pay"}):                        StoredSubjectAdmitted,
		},
		err: err,
	}
}

type chaos7127Run struct {
	bundle CanonicalFactBundle
	err    error
	logs   string
	graph  *storedSubjectGraph
}

func runChaos7127Gate(t *testing.T, principal storage.Principal, inner *chaos7127Inner, graph *storedSubjectGraph) chaos7127Run {
	t.Helper()
	var buffer bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	reader := newGatedFactReader(inner, NewStoredResultGate(graph), telemetry)
	bundle, err := reader.ReadFacts(context.Background(), principal, CanonicalFactRequest{Subjects: []SubjectRef{chaos7127Project()}})
	return chaos7127Run{bundle: bundle, err: err, logs: buffer.String(), graph: graph}
}

var chaos7127Restricted = storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/allowed"}}

func chaos7127Names(t *testing.T, fact CanonicalFact) []string {
	t.Helper()
	var names []string
	for _, row := range fact.Fields["risk_breakdown"].Rows {
		names = append(names, *row.Fields["scope_name"].String)
	}
	return names
}

// T14 shape on the engine path: project P, team owns A+B, caller granted A
// -> no id, name, risk or evidence of B; rows withheld = 1; aggregate
// labelled; undeclared and composed fields kept.
func TestCHAOS7127GateWithholdsUngrantedRepositoryRowsAndEvidence(t *testing.T) {
	run := runChaos7127Gate(t, chaos7127Restricted, &chaos7127Inner{facts: []CanonicalFact{chaos7127HealthFact()}}, chaos7127Graph(nil))
	if run.err != nil {
		t.Fatalf("ReadFacts: %v", run.err)
	}
	fact := run.bundle.Facts[0]
	if got := strings.Join(chaos7127Names(t, fact), ","); got != "acme/allowed,Payments Team" {
		t.Errorf("rows = %s, want acme/allowed,Payments Team", got)
	}
	if got := fact.Fields["risk_breakdown"].Table; got == nil || len(got.Rows) != 2 {
		t.Errorf("declared table form not filtered with the rows: %+v", got)
	}
	for _, id := range fact.EvidenceRefIDs {
		if strings.Contains(id, chaos7127Private) {
			t.Errorf("evidence %s names the ungranted repository", id)
		}
	}
	if got := fact.Fields["risk_breakdown"+FactFieldRowsWithheldByGrantSuffix]; got.Integer == nil || *got.Integer != 1 {
		t.Errorf("rows withheld = %+v, want 1", got)
	}
	if got := fact.Fields[FactFieldReferencesWithheldByGrant]; got.Integer == nil || *got.Integer != 1 {
		t.Errorf("references withheld = %+v, want 1 (the evidence ref)", got)
	}
	if got := fact.Fields[FactFieldAggregateScope]; got.String == nil || *got.String != AggregateScopeAllOwnedRepositories {
		t.Errorf("aggregate_scope = %+v", got)
	}
	if _, ok := fact.Fields["composed_sum"]; !ok {
		t.Errorf("an undeclared, registry-composed field was removed on the engine path")
	}
	if strings.Contains(run.logs, chaos7127Private) || strings.Contains(run.logs, "acme/private") {
		t.Errorf("decision line names the withheld subject: %s", run.logs)
	}
	for _, want := range []string{EngineFactGateLogMessage, "decision=filtered", "rows_withheld=1", "evidence_withheld=1", "references_refused=1"} {
		if !strings.Contains(run.logs, want) {
			t.Errorf("decision line lacks %q: %s", want, run.logs)
		}
	}
}

// The root is not re-decided: the project the re-check admitted is never
// sent to the graph again, and a clean read adds no label.
func TestCHAOS7127GateDoesNotRedecideRootsAndLabelsNothingClean(t *testing.T) {
	clean := chaos7127HealthFact()
	clean.Fields["risk_breakdown"] = TableFactValue(FactTable{Rows: []FactValueRow{chaos7127Row("repo", chaos7127Allowed, "acme/allowed", 0.3)}})
	clean.EvidenceRefIDs = []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, chaos7127Allowed)}
	run := runChaos7127Gate(t, chaos7127Restricted, &chaos7127Inner{facts: []CanonicalFact{clean}}, chaos7127Graph(nil))
	if run.err != nil {
		t.Fatal(run.err)
	}
	for _, batch := range run.graph.asked {
		for _, subject := range batch {
			if subject.Kind == SubjectProject {
				t.Errorf("root %s was re-decided", subject.CanonicalID)
			}
		}
	}
	for name := range run.bundle.Facts[0].Fields {
		if strings.HasSuffix(name, FactFieldRowsWithheldByGrantSuffix) || name == FactFieldAggregateScope || name == FactFieldReferencesWithheldByGrant {
			t.Errorf("clean read labelled %s", name)
		}
	}
	if !strings.Contains(run.logs, "decision=clean") {
		t.Errorf("no clean decision line: %s", run.logs)
	}
}

// Unrestricted and universal callers are not gated, and the graph is not
// asked.
func TestCHAOS7127GateLeavesUnrestrictedAndUniversalCallersUnchanged(t *testing.T) {
	for name, principal := range map[string]storage.Principal{
		"unrestricted": {OrgID: "org-1"},
		"universal":    {OrgID: "org-1", RepositoryScopes: []string{"*"}},
	} {
		run := runChaos7127Gate(t, principal, &chaos7127Inner{facts: []CanonicalFact{chaos7127HealthFact()}}, chaos7127Graph(nil))
		if run.err != nil {
			t.Fatalf("%s: %v", name, run.err)
		}
		if got := len(run.bundle.Facts[0].Fields["risk_breakdown"].Rows); got != 3 {
			t.Errorf("%s: rows = %d, want 3", name, got)
		}
		if len(run.graph.asked) != 0 || strings.Contains(run.logs, EngineFactGateLogMessage) {
			t.Errorf("%s: gate ran for a caller the predicate applies no repository check to", name)
		}
	}
}

// A graph read that fails fails the read closed: no fact is returned.
func TestCHAOS7127GateFailsClosedWhenTheGraphFails(t *testing.T) {
	run := runChaos7127Gate(t, chaos7127Restricted, &chaos7127Inner{facts: []CanonicalFact{chaos7127HealthFact()}}, chaos7127Graph(errors.New("graph down")))
	if !errors.Is(run.err, ErrUnavailable) || !errors.Is(run.err, ErrEmbeddedSubjectGateUnavailable) {
		t.Fatalf("err = %v, want unavailable", run.err)
	}
	if len(run.bundle.Facts) != 0 {
		t.Errorf("a failed gate returned %d facts", len(run.bundle.Facts))
	}
	if !strings.Contains(run.logs, "decision=unavailable") || !strings.Contains(run.logs, "level=WARN") {
		t.Errorf("no Warn unavailable decision line: %s", run.logs)
	}
}

// A graph that was never projected admits no embedded subject.
func TestCHAOS7127GateGraphNotProjectedAdmitsNoEmbeddedSubject(t *testing.T) {
	run := runChaos7127Gate(t, chaos7127Restricted, &chaos7127Inner{facts: []CanonicalFact{chaos7127HealthFact()}}, chaos7127Graph(ErrGraphNotProjected))
	if run.err != nil {
		t.Fatal(run.err)
	}
	if got := len(run.bundle.Facts[0].Fields["risk_breakdown"].Rows); got != 0 {
		t.Errorf("rows = %d, want 0", got)
	}
}

// An inner read error returns no fact rows alongside it.
func TestCHAOS7127GateInnerErrorReturnsNoFacts(t *testing.T) {
	run := runChaos7127Gate(t, chaos7127Restricted, &chaos7127Inner{facts: []CanonicalFact{chaos7127HealthFact()}, err: errors.New("read failed")}, chaos7127Graph(nil))
	if run.err == nil || len(run.bundle.Facts) != 0 {
		t.Fatalf("err=%v facts=%d, want the error and no facts", run.err, len(run.bundle.Facts))
	}
}

// Scalars: a refused team reference is removed; an opaque one is removed for
// a restricted caller (fail closed where the grant matters).
func TestCHAOS7127GateWithholdsRefusedAndOpaqueScalars(t *testing.T) {
	fact := chaos7127HealthFact()
	fact.Fields["owner_team"] = StringFactValue("team-hidden")
	fact.Fields["work_scope"] = StringFactValue("scope-7")
	run := runChaos7127Gate(t, chaos7127Restricted, &chaos7127Inner{facts: []CanonicalFact{fact}}, chaos7127Graph(nil))
	if run.err != nil {
		t.Fatal(run.err)
	}
	got := run.bundle.Facts[0].Fields
	if _, ok := got["owner_team"]; ok {
		t.Errorf("refused team scalar served")
	}
	if _, ok := got["work_scope"]; ok {
		t.Errorf("opaque scalar served to a restricted caller")
	}
	if v := got[FactFieldReferencesWithheldByGrant]; v.Integer == nil || *v.Integer != 3 {
		t.Errorf("references withheld = %+v, want 3 (two scalars, one evidence ref)", v)
	}
}

// A kind no capability declares passes unchanged and is counted.
func TestCHAOS7127GatePassesUndeclaredKindsAndCountsThem(t *testing.T) {
	status := CanonicalFact{Kind: FactStatus, Subject: chaos7127Project(), Fields: map[string]FactValue{"state": StringFactValue("open")}, SourceState: SourceAvailable, Source: "t", SourceVersion: "v1"}
	run := runChaos7127Gate(t, chaos7127Restricted, &chaos7127Inner{facts: []CanonicalFact{status, chaos7127HealthFact()}}, chaos7127Graph(nil))
	if run.err != nil {
		t.Fatal(run.err)
	}
	if run.bundle.Facts[0].Kind != FactStatus || run.bundle.Facts[0].Fields["state"].String == nil {
		t.Errorf("undeclared kind changed or reordered: %+v", run.bundle.Facts[0])
	}
	if !strings.Contains(run.logs, "facts_undeclared=1") {
		t.Errorf("undeclared kind not counted: %s", run.logs)
	}
}

// The engine wraps its fact reader, and hands the direct tools the raw
// registry (they gate through their own SubjectGate).
func TestCHAOS7127EngineWrapsFactsAndDirectToolsGetTheRegistry(t *testing.T) {
	var reads []CanonicalFactRequest
	graph, _ := chaos7080ProjectGraph(StoredSubjectAdmitted, nil)
	engine := chaos7080Engine(t, graph, &reads, nil)
	if _, ok := engine.facts.(*gatedFactReader); !ok {
		t.Fatalf("engine.facts is %T, want *gatedFactReader", engine.facts)
	}
	_, facts := engine.DirectReadSources()
	if _, gated := facts.(*gatedFactReader); gated || facts == nil {
		t.Fatalf("DirectReadSources returned %T, want the raw registry", facts)
	}
}

func TestCHAOS7127ReuseKeyCarriesTheGrantOfRestrictedCallersOnly(t *testing.T) {
	for _, principal := range []storage.Principal{{OrgID: "o"}, {OrgID: "o", RepositoryScopes: []string{"*"}}} {
		if got := GrantScopedTimeAxisKey(principal, "current"); got != "current" {
			t.Errorf("%v: key = %q, want unchanged", principal.RepositoryScopes, got)
		}
	}
	a := GrantScopedTimeAxisKey(storage.Principal{OrgID: "o", RepositoryScopes: []string{"acme/a", "acme/b"}}, "current")
	b := GrantScopedTimeAxisKey(storage.Principal{OrgID: "o", RepositoryScopes: []string{"acme/b", " acme/a", "acme/a"}}, "current")
	c := GrantScopedTimeAxisKey(storage.Principal{OrgID: "o", RepositoryScopes: []string{"acme/a"}}, "current")
	if a == "current" || a != b {
		t.Errorf("same grant in another order keyed differently: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("different grants share a key: %q", a)
	}
	if got := GrantScopedTimeAxisKey(storage.Principal{OrgID: "o", RepositoryScopes: []string{"acme/a"}}, ""); got != "" {
		t.Errorf("an unkeyed axis became keyed: %q", got)
	}
	if got := GrantScopedTimeAxisKey(storage.Principal{OrgID: "o", RepositoryScopes: []string{"acme/a"}}, strings.Repeat("x", 100)); got != "" {
		t.Errorf("an over-long key = %q, want never reusable", got)
	}
}

// Both sides of the reuse key carry the grant: a restricted caller's save
// and lookup agree, a different grant and an unrestricted caller never match
// it, and the unrestricted key is byte-for-byte what it was.
func TestCHAOS7127ReuseLookupAndSaveAgreeOnTheGrantScopedKey(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := &keyRecordingResultStore{}
	var lookupKeys []string
	engine := clampReuseEngine(t, func() time.Time { return now }, store, &lookupKeys)
	request := validInvestigationRequest()
	request.TimeContext = TimeContext{Axis: TemporalCurrent}
	grantA := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/a"}}

	if _, err := engine.Investigate(context.Background(), grantA, request); err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	base := TimeAxisKeyFor(request.TimeContext)
	if !strings.HasPrefix(store.savedKey, base+"+g:") {
		t.Fatalf("restricted save keyed %q, want %q plus the grant digest", store.savedKey, base)
	}
	if got := lookupKeys[len(lookupKeys)-1]; got != store.savedKey {
		t.Fatalf("restricted lookup keyed %q, save keyed %q", got, store.savedKey)
	}
	for name, principal := range map[string]storage.Principal{
		"other grant":  {OrgID: "org_1", RepositoryScopes: []string{"acme/b"}},
		"unrestricted": {OrgID: "org_1"},
	} {
		result, err := engine.Investigate(context.Background(), principal, request)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.Reused {
			t.Errorf("%s reused the grant-A answer", name)
		}
		if name == "unrestricted" && lookupKeys[len(lookupKeys)-1] != base {
			t.Errorf("unrestricted lookup keyed %q, want the unchanged %q", lookupKeys[len(lookupKeys)-1], base)
		}
	}
}

// Cohort members are roots the engine already admitted: a reference to one
// is not re-decided (the graph here does not know it and would refuse it).
// The organization rule admits only the caller's own organization.
func TestCHAOS7127GateTreatsCohortMembersAsRootsAndAppliesTheOrganizationRule(t *testing.T) {
	member, omitted, err := identity.Derive(identity.KindProject, []string{"linear", "q"}, nil)
	if err != nil || omitted {
		t.Fatalf("derive: %v %v", err, omitted)
	}
	fact := chaos7127HealthFact()
	fact.Fields["risk_breakdown"] = TableFactValue(FactTable{Rows: []FactValueRow{chaos7127Row("repo", chaos7127Allowed, "acme/allowed", 0.3)}})
	own := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityOrganization, "org-1")
	foreign := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityOrganization, "org-2")
	cohortRef := contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, "linear:q")
	fact.EvidenceRefIDs = []string{own, foreign, cohortRef}
	graph := chaos7127Graph(nil)
	var buffer bytes.Buffer
	reader := newGatedFactReader(&chaos7127Inner{facts: []CanonicalFact{fact}}, NewStoredResultGate(graph), NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buffer, nil))))
	bundle, err := reader.ReadFacts(context.Background(), chaos7127Restricted, CanonicalFactRequest{
		Subjects: []SubjectRef{chaos7127Project()},
		Cohort:   &Cohort{Kind: SubjectProject, Members: []CohortMember{{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: member}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(bundle.Facts[0].EvidenceRefIDs, ",")
	if got != own+","+cohortRef {
		t.Errorf("evidence = %s, want %s,%s", got, own, cohortRef)
	}
}

// A table carried only in its declared form (Rows nil) is gated all the
// same: both passes read the table's own rows.
func TestCHAOS7127GateFiltersATableCarriedOnlyInDeclaredForm(t *testing.T) {
	fact := chaos7127HealthFact()
	table := fact.Fields["risk_breakdown"].Table
	fact.Fields["risk_breakdown"] = FactValue{Table: table}
	run := runChaos7127Gate(t, chaos7127Restricted, &chaos7127Inner{facts: []CanonicalFact{fact}}, chaos7127Graph(nil))
	if run.err != nil {
		t.Fatal(run.err)
	}
	var names []string
	for _, row := range run.bundle.Facts[0].Fields["risk_breakdown"].Table.Rows {
		names = append(names, *row.Fields["scope_name"].String)
	}
	if got := strings.Join(names, ","); got != "acme/allowed,Payments Team" {
		t.Fatalf("declared-form table rows = %s, want acme/allowed,Payments Team (the private row withheld, the granted rows kept)", got)
	}
}

// The aggregate label is only for a fact that carries declared aggregate
// scalars: a withheld-from fact without one keeps its count, no label.
func TestCHAOS7127GateLabelsTheAggregateOnlyWhenOneIsCarried(t *testing.T) {
	fact := chaos7127HealthFact()
	delete(fact.Fields, "severity")
	run := runChaos7127Gate(t, chaos7127Restricted, &chaos7127Inner{facts: []CanonicalFact{fact}}, chaos7127Graph(nil))
	if run.err != nil {
		t.Fatal(run.err)
	}
	fields := run.bundle.Facts[0].Fields
	if _, ok := fields[FactFieldAggregateScope]; ok {
		t.Errorf("aggregate_scope set on a fact carrying no aggregate scalar")
	}
	if got := fields["risk_breakdown"+FactFieldRowsWithheldByGrantSuffix]; got.Integer == nil || *got.Integer != 1 {
		t.Errorf("rows withheld = %+v, want 1", got)
	}
}
