package directread

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// deficiencyLikeCapability is a team-only kind whose provider classifies
// each subject (the operational_deficiencies shape).
func deficiencyLikeCapability() contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind: contextfabric.FactOperationalDeficiencies, Name: "deficiency_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectTeam},
		RequiresEvidence:      true,
		Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:                []contextfabric.FactFieldDeclaration{{Name: "rule_id", Type: contextfabric.FactFieldString}},
	}
}

func unrestrictedA() storage.Principal {
	return storage.Principal{OrgID: orgA, Subject: "user-2", CredentialID: "cred-2"}
}

func teamGraph(teams ...contextfabric.SubjectRef) *fakeGraph {
	graph := graphOfOrgA()
	for _, team := range teams {
		graph.nodes[graphrank.SubjectKey(team)] = repos("acme/a")
	}
	return graph
}

func coverageOf(t *testing.T, response FactsResponse, kind string, subject contextfabric.SubjectRef) CoverageRow {
	t.Helper()
	var found []CoverageRow
	for _, row := range response.Coverage {
		if row.Kind == kind && row.Subject.Kind == string(subject.Kind) && row.Subject.CanonicalID == subject.CanonicalID {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("coverage rows for (%s, %s) = %d, want exactly 1: %+v", kind, subject.CanonicalID, len(found), response.Coverage)
	}
	return found[0]
}

// T3. Rule 1: one coverage row per (kind, subject), carrying the ledger
// state. Every outcome of the closed vocabulary has a case (rule 4: a state
// with no case fails the test). Rule 2 plant: a provider returns no row for
// one subject -- the coverage must say read_no_fact for it, never omit it
// and never claim a fact.
func TestChaos7073CoveragePerKindAndSubject(t *testing.T) {
	type expectation struct {
		name    string
		outcome string
		state   string // provider member state, when classified
	}
	covered := map[string]bool{}
	check := func(t *testing.T, response FactsResponse, kind string, subject contextfabric.SubjectRef, want expectation) {
		t.Helper()
		row := coverageOf(t, response, kind, subject)
		if row.Outcome != want.outcome || row.ProviderState != want.state {
			t.Errorf("%s: outcome %q provider_state %q, want %q %q (row %+v)", want.name, row.Outcome, row.ProviderState, want.outcome, want.state, row)
		}
		covered[row.Outcome] = true
	}

	t.Run("classifying provider serves its own per-subject states", func(t *testing.T) {
		states := []string{"fired", "measured_zero", "stale", "before_range", "never_evaluated", "withheld_capped_read"}
		var teams []contextfabric.SubjectRef
		for _, state := range states {
			teams = append(teams, subject(contractsv1.ContextFabricSubjectTeam, "team:"+state))
		}
		provider := &stubProvider{capability: deficiencyLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Evaluation: &contextfabric.FactEvaluationCoverage{}}
			for index, team := range query.Subjects {
				result.Evaluation.Members = append(result.Evaluation.Members, contextfabric.FactEvaluationMember{Subject: team, State: states[index]})
				if states[index] == "fired" {
					result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: contextfabric.FactOperationalDeficiencies, Subject: team,
						Fields: map[string]contextfabric.FactValue{"rule_id": strValue("r1")}, EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, strings.TrimPrefix(team.CanonicalID, "team:"))}, SourceState: contextfabric.SourceAvailable})
				}
				if states[index] == "measured_zero" {
					result.EvaluatedSubjects = append(result.EvaluatedSubjects, team)
				}
			}
			return result, nil
		}}
		request := FactsRequest{Kinds: []string{"operational_deficiencies"}}
		for _, team := range teams {
			request.Subjects = append(request.Subjects, RequestSubject{Kind: "team", CanonicalID: team.CanonicalID})
		}
		response, err := newTestFactsReader(t, teamGraph(teams...), provider).Read(requestContext(), unrestrictedA(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Coverage) != len(teams) {
			t.Fatalf("coverage rows = %d, want %d", len(response.Coverage), len(teams))
		}
		for index, team := range teams {
			check(t, response, "operational_deficiencies", team, expectation{name: states[index], outcome: states[index], state: states[index]})
		}
	})

	t.Run("non-classifying provider: served, read_no_fact, measured_zero, not_applicable", func(t *testing.T) {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}
			for _, s := range query.Subjects {
				switch s.CanonicalID {
				case repoA.CanonicalID:
					result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: contextfabric.FactHealth, Subject: s,
						Fields: map[string]contextfabric.FactValue{"repo_count": intValue(1)}, EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "a")}, SourceState: contextfabric.SourceAvailable})
				case teamT.CanonicalID:
					result.EvaluatedSubjects = append(result.EvaluatedSubjects, s)
				}
				// repoB: the plant -- the provider returns no row for it.
			}
			return result, nil
		}}
		response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
			Kinds: []string{"health"},
			Subjects: []RequestSubject{
				{Kind: "repository", CanonicalID: repoA.CanonicalID}, {Kind: "repository", CanonicalID: repoB.CanonicalID},
				{Kind: "team", CanonicalID: teamT.CanonicalID}, {Kind: "work_item", CanonicalID: workA.CanonicalID},
			}})
		if err != nil {
			t.Fatal(err)
		}
		check(t, response, "health", repoA, expectation{name: "fact", outcome: OutcomeFactServed})
		check(t, response, "health", repoB, expectation{name: "no row", outcome: OutcomeReadNoFact})
		check(t, response, "health", teamT, expectation{name: "evaluated zero", outcome: OutcomeMeasuredZero})
		check(t, response, "health", workA, expectation{name: "unsupported kind", outcome: OutcomeNotApplicable})
		if row := coverageOf(t, response, "health", repoB); row.KindState != string(contextfabric.SourceAvailable) {
			t.Errorf("read_no_fact row must name the kind-level state, got %+v", row)
		}
	})

	t.Run("stale and truncated reads keep their facts and say so", func(t *testing.T) {
		for _, state := range []contextfabric.SourceState{contextfabric.SourceStale, contextfabric.SourceTruncated} {
			state := state
			provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
				return contextfabric.FactProviderResult{State: state, Truncated: state == contextfabric.SourceTruncated, Reason: "test " + string(state), Facts: []contextfabric.CanonicalFact{{
					Kind: contextfabric.FactHealth, Subject: query.Subjects[0], Fields: map[string]contextfabric.FactValue{"repo_count": intValue(1)},
					EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, "a")}, SourceState: state}}}, nil
			}}
			response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
				Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "repository", CanonicalID: repoA.CanonicalID}}})
			if err != nil {
				t.Fatal(err)
			}
			want := OutcomeStale
			if state == contextfabric.SourceTruncated {
				want = OutcomeTruncated
			}
			check(t, response, "health", repoA, expectation{name: string(state), outcome: want})
		}
	})

	t.Run("failed provider is unavailable, never no data", func(t *testing.T) {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			return contextfabric.FactProviderResult{}, errors.New("clickhouse down")
		}}
		response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
			Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "repository", CanonicalID: repoA.CanonicalID}}})
		if err != nil {
			t.Fatal(err)
		}
		check(t, response, "health", repoA, expectation{name: "failed", outcome: OutcomeUnavailable})
		if response.Status != StatusPartial {
			t.Errorf("status %q, want partial", response.Status)
		}
	})

	t.Run("response budget withholds whole facts and keeps coverage", func(t *testing.T) {
		response := budgetResponse(t, MinMaxBytes)
		for _, row := range response.Coverage {
			covered[row.Outcome] = true
		}
	})

	// Rule 4: every outcome of the closed vocabulary was observed.
	vocabulary := []string{OutcomeFactServed, OutcomeReadNoFact, OutcomeMeasuredZero, OutcomeNotApplicable, OutcomeUnavailable, OutcomeStale, OutcomeTruncated, OutcomeWithheldBudget,
		"fired", "measured_zero", "stale", "before_range", "never_evaluated", "withheld_capped_read"}
	for _, outcome := range vocabulary {
		if !covered[outcome] {
			t.Errorf("coverage outcome %q has no case", outcome)
		}
	}
}

// budgetResponse reads 20 repositories' large facts under maxBytes.
func budgetResponse(t *testing.T, maxBytes int) FactsResponse {
	t.Helper()
	graph := graphOfOrgA()
	var subjects []RequestSubject
	for index := 0; index < 20; index++ {
		id := "repository:r" + string(rune('a'+index))
		graph.nodes[graphrank.SubjectKey(subject(contractsv1.ContextFabricSubjectRepository, id))] = repos("acme/r")
		subjects = append(subjects, RequestSubject{Kind: "repository", CanonicalID: id})
	}
	provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}
		for _, s := range query.Subjects {
			var rows []contextfabric.FactValueRow
			for index := 0; index < 40; index++ {
				rows = append(rows, riskRow("repo", strings.TrimPrefix(s.CanonicalID, "repository:"), strings.Repeat("n", 40), 0.1))
			}
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: contextfabric.FactHealth, Subject: s,
				Fields:         map[string]contextfabric.FactValue{"repo_count": intValue(1), "risk_breakdown": contextfabric.RowsFactValue(rows)},
				EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, strings.TrimPrefix(s.CanonicalID, "repository:"))}, SourceState: contextfabric.SourceAvailable})
		}
		return result, nil
	}}
	response, err := newTestFactsReader(t, graph, provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"health"}, Subjects: subjects, MaxBytes: maxBytes})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// T6. Rule 1: truncation is disclosed, coverage is present for every pair,
// the response fits the budget, and fact tables carry no cursor. Rule 2
// plant: a writer that drops coverage to fit.
func TestChaos7073BudgetAndTruncation(t *testing.T) {
	const budget = 16384
	response := budgetResponse(t, budget)
	encoded := mustJSON(t, response)
	if len(encoded) > budget {
		t.Errorf("response %d bytes > max_bytes %d", len(encoded), budget)
	}
	if response.Truncation != nil && response.Truncation.CoverageOverBudget {
		t.Errorf("coverage fits %d bytes but was flagged over budget", budget)
	}
	if response.Truncation == nil || response.Truncation.TruncatedBy != TruncatedByMaxBytes || response.Truncation.FactsOmitted == 0 {
		t.Fatalf("truncation not disclosed: %+v", response.Truncation)
	}
	if len(response.Coverage) != 20 {
		t.Errorf("coverage rows = %d, want 20 (coverage is never dropped)", len(response.Coverage))
	}
	withheld := 0
	for _, row := range response.Coverage {
		if row.Outcome == OutcomeWithheldBudget {
			withheld++
		}
	}
	if withheld != response.Truncation.FactsOmitted || len(response.Facts)+withheld != 20 {
		t.Errorf("facts %d + withheld %d != 20, omitted %d", len(response.Facts), withheld, response.Truncation.FactsOmitted)
	}
	if response.Status != StatusPartial {
		t.Errorf("status %q, want partial", response.Status)
	}
	if strings.Contains(encoded, "cursor") {
		t.Error("fact tables must carry no cursor (design C.5)")
	}
	// Coverage alone over the budget: every fact withheld, coverage whole,
	// flagged.
	tight := budgetResponse(t, MinMaxBytes)
	if tight.Truncation == nil || !tight.Truncation.CoverageOverBudget || len(tight.Facts) != 0 || len(tight.Coverage) != 20 {
		t.Errorf("coverage over budget: truncation %+v facts %d coverage %d", tight.Truncation, len(tight.Facts), len(tight.Coverage))
	}
	// A roomy budget truncates nothing.
	if full := budgetResponse(t, MaxMaxBytes); full.Truncation != nil || len(full.Facts) != 20 {
		t.Errorf("max budget: truncation %+v facts %d", full.Truncation, len(full.Facts))
	}
}

// recordingExpander counts calls: the direct path must never expand scope.
type recordingExpander struct{ calls int }

func (e *recordingExpander) ExpandFactScope(context.Context, contextfabric.FactScopeExpansionRequest) (contextfabric.FactScopeExpansionResult, error) {
	e.calls++
	return contextfabric.FactScopeExpansionResult{Targets: []contextfabric.SubjectRef{repoB}}, nil
}

// The expander-off pin (FINDINGS 1, ruled by the lead): derived subjects are
// not proved authorized on the direct path, so the direct registry runs with
// no expander. A project root for a kind that only a scope expansion could
// reach is disclosed as unavailable and never read.
func TestChaos7073DirectPathNeverExpandsScope(t *testing.T) {
	expander := &recordingExpander{}
	capability := contextfabric.FactCapability{
		Kind: contextfabric.FactMetrics, Name: "metrics_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository},
		RequiresEvidence:      true, Dimension: contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles: []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:       []contextfabric.FactFieldDeclaration{{Name: "commits", Type: contextfabric.FactFieldInteger}},
	}
	provider := &stubProvider{capability: capability, read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
	}}
	engineRegistry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{ScopeExpander: expander})
	if err != nil {
		t.Fatal(err)
	}
	gate := NewSubjectGate(graphOfOrgA(), nil)
	reader := NewFactsReader(gate, NewFactReader(engineRegistry.WithoutScopeExpansion()), nil)
	reader.now = func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }
	response, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"metrics"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	if expander.calls != 0 || len(provider.queries) != 0 {
		t.Fatalf("direct read expanded scope: expander calls %d provider reads %v", expander.calls, provider.queries)
	}
	row := coverageOf(t, response, "metrics", projectQ)
	if row.Outcome != OutcomeUnavailable || !strings.Contains(row.Reason+row.KindState, "") || row.KindState == "" {
		t.Errorf("gap not disclosed: %+v", row)
	}
	// The engine's own registry still expands (the direct copy changed
	// nothing for it).
	if _, err := engineRegistry.ReadFacts(context.Background(), unrestrictedA(), contextfabric.CanonicalFactRequest{
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Subjects:     []contextfabric.SubjectRef{{Kind: projectQ.Kind, CanonicalID: projectQ.CanonicalID, Label: "Q"}},
		Requirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactMetrics}},
	}); err != nil {
		t.Fatal(err)
	}
	if expander.calls == 0 {
		t.Error("the engine registry lost its expander")
	}
}

// Window (decision K5 and design C.5): no window = current, echoed as
// defaulted; trailing becomes a range by the server clock; limits refuse.
func TestChaos7073Window(t *testing.T) {
	provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
	}}
	reader := newTestFactsReader(t, graphOfOrgA(), provider)
	base := FactsRequest{Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "repository", CanonicalID: repoA.CanonicalID}}}
	response, err := reader.Read(requestContext(), unrestrictedA(), base)
	if err != nil {
		t.Fatal(err)
	}
	if w := response.Request.Window; w.Mode != WindowCurrent || !w.Defaulted || provider.queries[0].Time.Axis != contextfabric.TemporalCurrent {
		t.Errorf("default window %+v axis %q", w, provider.queries[0].Time.Axis)
	}
	trailing := base
	trailing.Window = &RequestWindow{Mode: WindowTrailing, Days: 30}
	response, err = reader.Read(requestContext(), unrestrictedA(), trailing)
	if err != nil {
		t.Fatal(err)
	}
	query := provider.queries[len(provider.queries)-1]
	wantEnd := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	if query.Time.Axis != contextfabric.TemporalRange || !query.Time.End.Equal(wantEnd) || !query.Time.Start.Equal(wantEnd.Add(-30*24*time.Hour)) {
		t.Errorf("trailing sent %+v", query.Time)
	}
	if w := response.Request.Window; w.Mode != WindowTrailing || w.Start == nil || !w.Start.Equal(*query.Time.Start) {
		t.Errorf("trailing echo %+v", w)
	}
	for name, window := range map[string]*RequestWindow{
		"range over 60 days": {Mode: WindowRange, Start: ptrTime(wantEnd.Add(-61 * 24 * time.Hour)), End: &wantEnd},
		"reversed range":     {Mode: WindowRange, Start: &wantEnd, End: ptrTime(wantEnd.Add(-time.Hour))},
		"future as_of":       {Mode: WindowAsOf, AsOf: ptrTime(wantEnd.Add(time.Hour))},
		"trailing 0":         {Mode: WindowTrailing},
		"unknown mode":       {Mode: "yesterday"},
		"current with days":  {Mode: WindowCurrent, Days: 3},
	} {
		request := base
		request.Window = window
		if _, err := reader.Read(requestContext(), unrestrictedA(), request); !isInvalid(err) {
			t.Errorf("%s: err %v, want invalid_request", name, err)
		}
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func isInvalid(err error) bool {
	var requestErr *RequestError
	return errors.As(err, &requestErr) && requestErr.Reason == RefusalInvalidRequest
}

// Kinds with no field declarations are refused by name, never guessed.
func TestChaos7073UndeclaredKindIsRefused(t *testing.T) {
	undeclared := deficiencyLikeCapability()
	undeclared.Fields = nil
	provider := &stubProvider{capability: undeclared, read: func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		t.Error("undeclared kind was read")
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
	}}
	response, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"operational_deficiencies"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(response.Request.KindsRefused, []RefusedKind{{Kind: "operational_deficiencies", Reason: RefusalKindNotServed}}) {
		t.Errorf("refused kinds %+v", response.Request.KindsRefused)
	}
	if _, err := newTestFactsReader(t, graphOfOrgA(), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"no_such_kind"}, Subjects: []RequestSubject{{Kind: "team", CanonicalID: teamT.CanonicalID}}}); !isInvalid(err) {
		t.Errorf("unknown kind: %v", err)
	}
}

// erroringSource is a registry seam that fails with a fixed error.
type erroringSource struct {
	err          error
	capabilities []contextfabric.FactCapability
}

func (s erroringSource) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{}, s.err
}

func (s erroringSource) Capabilities() []contextfabric.FactCapability { return s.capabilities }

// A gate decision refused by the reader is a tool defect (internal), never a
// subject refusal and never retryable unavailability; a registry failure is
// unavailability.
func TestChaos7073ReaderErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want error
	}{
		{ErrAuthorizationSpent, ErrFactsInternal},
		{ErrAuthorizationExpired, ErrFactsInternal},
		{ErrUngatedRead, ErrFactsInternal},
		{errors.New("clickhouse down"), ErrFactsUnavailable},
	} {
		source := erroringSource{err: tc.err, capabilities: []contextfabric.FactCapability{healthLikeCapability()}}
		reader := NewFactsReader(NewSubjectGate(graphOfOrgA(), nil), NewFactReader(source), nil)
		_, err := reader.Read(requestContext(), unrestrictedA(), FactsRequest{Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "repository", CanonicalID: repoA.CanonicalID}}})
		if !errors.Is(err, tc.want) {
			t.Errorf("%v: got %v, want %v", tc.err, err, tc.want)
		}
	}
}
