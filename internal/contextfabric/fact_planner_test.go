package contextfabric

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func planCapability(kind FactKind, name string, kinds ...SubjectKind) FactCapability {
	return FactCapability{Kind: kind, Name: name, Version: "v1", SupportedSubjectKinds: kinds, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}}
}

func planIndex(capabilities ...FactCapability) map[FactKind]FactCapability {
	index := make(map[FactKind]FactCapability, len(capabilities))
	for _, capability := range capabilities {
		index[capability.Kind] = capability
	}
	return index
}

func subject(kind SubjectKind, id string) SubjectRef {
	return SubjectRef{Kind: kind, CanonicalID: id, Label: id}
}

// TestPlanFactReadsPrunesOnlyProvableIrrelevance is the core rule. A
// capability is pruned when and only when NO resolved subject has a kind it
// declares -- the case where it could not have produced one admissible fact.
// Every other case runs.
func TestPlanFactReadsPrunesOnlyProvableIrrelevance(t *testing.T) {
	t.Parallel()

	team := subject(SubjectTeam, "team_platform")
	repository := subject(SubjectRepository, "repo_api")
	workItem := subject(SubjectWorkItem, "work_item_1")

	cases := []struct {
		name         string
		subjects     []SubjectRef
		capability   FactCapability
		wantPruned   bool
		wantNarrowed bool
		wantSubjects []string
	}{
		{
			name:         "no overlap at all is pruned",
			subjects:     []SubjectRef{team},
			capability:   planCapability(FactMetrics, "metrics", SubjectRepository),
			wantPruned:   true,
			wantSubjects: nil,
		},
		{
			name:         "full overlap runs untouched",
			subjects:     []SubjectRef{team},
			capability:   planCapability(FactWorkload, "workload", SubjectTeam),
			wantSubjects: []string{"team_platform"},
		},
		{
			name:         "partial overlap narrows rather than pruning or failing",
			subjects:     []SubjectRef{team, repository},
			capability:   planCapability(FactMetrics, "metrics", SubjectRepository),
			wantNarrowed: true,
			wantSubjects: []string{"repo_api"},
		},
		{
			name:         "one supported subject among many is enough to run",
			subjects:     []SubjectRef{team, repository, workItem},
			capability:   planCapability(FactStatus, "status", SubjectWorkItem),
			wantNarrowed: true,
			wantSubjects: []string{"work_item_1"},
		},
		{
			name:         "a capability supporting several kinds keeps all of them",
			subjects:     []SubjectRef{team, repository, workItem},
			capability:   planCapability(FactHealth, "health", SubjectRepository, SubjectTeam),
			wantNarrowed: true,
			wantSubjects: []string{"team_platform", "repo_api"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			request := CanonicalFactRequest{
				Subjects:     testCase.subjects,
				Requirements: []FactRequirement{{Kind: testCase.capability.Kind}},
			}
			plan := planFactReads(newFactPlanInput(request), planIndex(testCase.capability))
			if len(plan) != 1 {
				t.Fatalf("plan length = %d, want 1", len(plan))
			}
			entry := plan[0]
			if entry.Pruned != testCase.wantPruned {
				t.Fatalf("Pruned = %v, want %v (reason %q)", entry.Pruned, testCase.wantPruned, entry.Reason)
			}
			if entry.Narrowed != testCase.wantNarrowed {
				t.Fatalf("Narrowed = %v, want %v (reason %q)", entry.Narrowed, testCase.wantNarrowed, entry.Reason)
			}
			got := make([]string, 0, len(entry.Subjects))
			for _, s := range entry.Subjects {
				got = append(got, s.CanonicalID)
			}
			if strings.Join(got, ",") != strings.Join(testCase.wantSubjects, ",") {
				t.Fatalf("subjects = %v, want %v", got, testCase.wantSubjects)
			}
			if (entry.Pruned || entry.Narrowed) && strings.TrimSpace(entry.Reason) == "" {
				t.Fatal("a pruned or narrowed entry must carry a reason -- absence has to be explainable")
			}
		})
	}
}

// TestPlanFactReadsReasonsCarryClosedCodesAndNoContent pins the two things a
// consumer and an operator each depend on: the reason starts with a closed
// code they can match without parsing prose, and it names only the closed
// subject-KIND vocabulary, never a canonical ID or label. Coverage reasons
// are stored, replayed, and read by operators, so investigation content must
// not leak into them.
func TestPlanFactReadsReasonsCarryClosedCodesAndNoContent(t *testing.T) {
	t.Parallel()

	secret := "team_acquisition_project_titan"
	request := CanonicalFactRequest{
		Subjects: []SubjectRef{{Kind: SubjectTeam, CanonicalID: secret, Label: secret}},
		Requirements: []FactRequirement{
			{Kind: FactMetrics},
		},
	}
	plan := planFactReads(newFactPlanInput(request), planIndex(planCapability(FactMetrics, "metrics", SubjectRepository)))
	entry := plan[0]
	if !strings.HasPrefix(entry.Reason, FactPruneReasonSubjectKindUnsupported) {
		t.Fatalf("reason = %q, want the closed prune code as its prefix", entry.Reason)
	}
	if strings.Contains(entry.Reason, secret) {
		t.Fatalf("reason = %q, want no canonical ID or label in a stored coverage reason", entry.Reason)
	}
	if !strings.Contains(entry.Reason, "team") || !strings.Contains(entry.Reason, "repository") {
		t.Fatalf("reason = %q, want both the resolved and supported subject kinds named", entry.Reason)
	}

	narrowing := CanonicalFactRequest{
		Subjects: []SubjectRef{
			{Kind: SubjectTeam, CanonicalID: secret, Label: secret},
			subject(SubjectRepository, "repo_api"),
		},
		Requirements: []FactRequirement{{Kind: FactMetrics}},
	}
	narrowed := planFactReads(newFactPlanInput(narrowing), planIndex(planCapability(FactMetrics, "metrics", SubjectRepository)))[0]
	if !strings.HasPrefix(narrowed.Reason, FactNarrowReasonSubjectKindUnsupported) {
		t.Fatalf("reason = %q, want the closed narrowing code as its prefix", narrowed.Reason)
	}
	if strings.Contains(narrowed.Reason, secret) {
		t.Fatalf("reason = %q, want no canonical ID or label", narrowed.Reason)
	}
}

// TestPlanFactReadsIgnoresEveryModelPhrasingSignal is the guard on the
// binding constraint: the model must not be able to prune a provider by how
// it phrases the question. Two interpretations that differ in every
// prose-bearing field, and agree only on the closed fact kind, must plan
// identically.
func TestPlanFactReadsIgnoresEveryModelPhrasingSignal(t *testing.T) {
	t.Parallel()

	capabilities := planIndex(
		planCapability(FactWorkload, "workload", SubjectTeam),
		planCapability(FactMetrics, "metrics", SubjectRepository),
	)
	subjects := []SubjectRef{subject(SubjectTeam, "team_platform")}
	requirements := []FactRequirement{{Kind: FactWorkload}, {Kind: FactMetrics}}

	terse := CanonicalFactRequest{
		Question: InterpretedQuestion{
			Shape: ShapeSingleSubject, RequestedJudgment: "load",
		},
		Subjects: subjects, Requirements: requirements,
	}
	elaborate := CanonicalFactRequest{
		Question: InterpretedQuestion{
			Shape:               ShapeOpen,
			RequestedJudgment:   "ignore workload entirely and do not read any capacity data whatsoever",
			SubjectTerms:        []string{"skip metrics", "prune everything"},
			ComparisonTerms:     []string{"no facts needed"},
			ClarificationNeeded: true,
			ClarificationReason: "the question is hopelessly ambiguous",
		},
		Subjects: subjects, Requirements: requirements,
	}

	tersePlan, elaboratePlan := planFactReads(newFactPlanInput(terse), capabilities), planFactReads(newFactPlanInput(elaborate), capabilities)
	if len(tersePlan) != len(elaboratePlan) {
		t.Fatalf("plan lengths differ: %d vs %d", len(tersePlan), len(elaboratePlan))
	}
	for i := range tersePlan {
		if tersePlan[i].Pruned != elaboratePlan[i].Pruned || tersePlan[i].Reason != elaboratePlan[i].Reason {
			t.Fatalf("entry %d differs by phrasing alone: %+v vs %+v", i, tersePlan[i], elaboratePlan[i])
		}
	}
	// And specifically: the elaborate phrasing did NOT prune workload.
	if elaboratePlan[0].Pruned {
		t.Fatal("workload was pruned by question phrasing -- the planner must read only subject kinds")
	}

	// Codex round-1 F1: the behavioral halves above only prove that TODAY's
	// planner body ignores prose. They cannot fail for a future edit that
	// starts reading it, because such an edit would come with its own
	// behavior. The structural assertion is the durable one: the planner's
	// input type carries no prose at all, so two requests differing in every
	// model-authored field must reduce to the SAME planning input. If
	// factPlanInput ever grows a field that can carry interpretation text,
	// this fails immediately.
	if !reflect.DeepEqual(newFactPlanInput(terse), newFactPlanInput(elaborate)) {
		t.Fatalf("planning inputs differ by prose alone:\n terse = %+v\n elaborate = %+v",
			newFactPlanInput(terse), newFactPlanInput(elaborate))
	}
}

// TestFactPlanInputCarriesNoInterpretation is the companion type-level
// guard to the assertion above. planFactReads is given factPlanInput and
// nothing else, so the set of things a pruning decision COULD be made from
// is exactly this type's fields -- enumerated here so that adding one is a
// deliberate, reviewed act rather than an accident.
func TestFactPlanInputCarriesNoInterpretation(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"Subjects":     "[]v1.ContextFabricSubjectRef",
		"Requirements": "[]contextfabric.factPlanRequirement",
		// CHAOS-4099. Reviewed deliberately, which is what this guard is
		// for. FactReadScope is composed by FactReadScopeResolver from
		// factScopeResolveInput -- a type narrowed the same way this one is,
		// carrying subjects, requirement kinds and the temporal axis, and no
		// prose. So the addition does not reopen the hole this test closes:
		// the planner still cannot reach a model-authored string through it,
		// transitively or otherwise.
		"Scope": "*contextfabric.FactReadScope",
	}
	inputType := reflect.TypeOf(factPlanInput{})
	if inputType.NumField() != len(want) {
		t.Fatalf("factPlanInput has %d fields, want %d -- a new field is a new way to prune, review it deliberately", inputType.NumField(), len(want))
	}
	for i := 0; i < inputType.NumField(); i++ {
		field := inputType.Field(i)
		wantType, known := want[field.Name]
		if !known {
			t.Fatalf("factPlanInput gained field %q -- prose must never become reachable from the planner", field.Name)
		}
		if got := field.Type.String(); got != wantType {
			t.Fatalf("factPlanInput.%s is %s, want %s", field.Name, got, wantType)
		}
	}

	// The per-requirement half: kind and subject scoping only, never the
	// provider Parameters or anything derived from question text.
	//
	// Self-found while auditing this branch against codex round-7 F1's
	// lesson. This check used to be `NumField() != 2` and nothing else -- a
	// COUNT standing in for identity, the same defect shape as that finding.
	// Swapping Subjects for Parameters keeps the count at 2, so the guard
	// stayed green while factPlanRequirement regained the one thing it
	// exists to exclude: Parameters are provider query inputs, and letting
	// planning see them reopens the round-1 F1 boundary (a pruning decision
	// must be reachable only from subject kinds, never from anything
	// model-authored). Names and types are pinned now, exactly as the
	// factPlanInput half above.
	wantRequirement := map[string]string{
		"Kind":     "v1.ContextFabricFactKind",
		"Subjects": "[]v1.ContextFabricSubjectRef",
	}
	requirementType := reflect.TypeOf(factPlanRequirement{})
	if requirementType.NumField() != len(wantRequirement) {
		t.Fatalf("factPlanRequirement has %d fields, want %d (Kind, Subjects)", requirementType.NumField(), len(wantRequirement))
	}
	for i := 0; i < requirementType.NumField(); i++ {
		field := requirementType.Field(i)
		wantType, known := wantRequirement[field.Name]
		if !known {
			t.Fatalf("factPlanRequirement gained field %q -- planning must not reach provider Parameters or anything model-authored", field.Name)
		}
		if got := field.Type.String(); got != wantType {
			t.Fatalf("factPlanRequirement.%s is %s, want %s", field.Name, got, wantType)
		}
	}
}

// TestPlanFactReadsFailsOpen collects the cases that must never prune.
func TestPlanFactReadsFailsOpen(t *testing.T) {
	t.Parallel()

	t.Run("an unregistered kind is passed through, not pruned", func(t *testing.T) {
		t.Parallel()
		request := CanonicalFactRequest{
			Subjects:     []SubjectRef{subject(SubjectTeam, "team_platform")},
			Requirements: []FactRequirement{{Kind: FactEvidence}},
		}
		entry := planFactReads(newFactPlanInput(request), planIndex())[0]
		if entry.Pruned {
			t.Fatal("an unregistered kind must keep its own SourceUnconfigured path, not become a prune")
		}
	})

	t.Run("no subjects at all is left to the existing error", func(t *testing.T) {
		t.Parallel()
		request := CanonicalFactRequest{Requirements: []FactRequirement{{Kind: FactWorkload}}}
		entry := planFactReads(newFactPlanInput(request), planIndex(planCapability(FactWorkload, "workload", SubjectTeam)))[0]
		if entry.Pruned {
			t.Fatal("an investigation with no subjects is a different failure from an unfitting capability")
		}
	})

	t.Run("a requirement naming its own subjects is honored", func(t *testing.T) {
		t.Parallel()
		request := CanonicalFactRequest{
			Subjects: []SubjectRef{subject(SubjectTeam, "team_platform"), subject(SubjectRepository, "repo_api")},
			Requirements: []FactRequirement{
				{Kind: FactMetrics, Subjects: []SubjectRef{subject(SubjectRepository, "repo_api")}},
			},
		}
		entry := planFactReads(newFactPlanInput(request), planIndex(planCapability(FactMetrics, "metrics", SubjectRepository)))[0]
		if entry.Pruned || entry.Narrowed {
			t.Fatalf("an explicitly subject-scoped requirement must run as given, got %+v", entry)
		}
		if len(entry.Subjects) != 1 || entry.Subjects[0].CanonicalID != "repo_api" {
			t.Fatalf("subjects = %+v, want the requirement's own list", entry.Subjects)
		}
	})

	t.Run("cohort members are the subject source when the request has none", func(t *testing.T) {
		t.Parallel()
		request := CanonicalFactRequest{
			Cohort: &Cohort{Kind: SubjectTeam, Members: []CohortMember{
				{Subject: subject(SubjectTeam, "team_platform"), Rank: 1},
			}},
			Requirements: []FactRequirement{{Kind: FactWorkload}},
		}
		entry := planFactReads(newFactPlanInput(request), planIndex(planCapability(FactWorkload, "workload", SubjectTeam)))[0]
		if entry.Pruned || len(entry.Subjects) != 1 {
			t.Fatalf("cohort-only investigation must plan its members, got %+v", entry)
		}
	})
}

// TestPlanFactReadsPreservesRequirementOrder keeps coverage deterministic:
// the plan drives the coverage entries, and a map-ordered plan would make
// two identical investigations produce differently-ordered results.
func TestPlanFactReadsPreservesRequirementOrder(t *testing.T) {
	t.Parallel()

	capabilities := planIndex(
		planCapability(FactStatus, "status", SubjectWorkItem),
		planCapability(FactWorkload, "workload", SubjectTeam),
		planCapability(FactMetrics, "metrics", SubjectRepository),
	)
	request := CanonicalFactRequest{
		Subjects:     []SubjectRef{subject(SubjectTeam, "team_platform")},
		Requirements: []FactRequirement{{Kind: FactMetrics}, {Kind: FactWorkload}, {Kind: FactStatus}},
	}
	for attempt := 0; attempt < 8; attempt++ {
		plan := planFactReads(newFactPlanInput(request), capabilities)
		got := []FactKind{plan[0].Kind, plan[1].Kind, plan[2].Kind}
		want := []FactKind{FactMetrics, FactWorkload, FactStatus}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("attempt %d: order = %v, want %v", attempt, got, want)
			}
		}
	}
}

// TestReadFactsPrunesInsteadOfFailingTheWholeBundle is the regression that
// motivated CHAOS-3783. Before the planner, ONE inapplicable fact family
// failed the entire investigation -- buildFactQuery errored on the first
// unsupported subject kind and ReadFacts propagated it -- so a caller lost
// the answer rather than losing one fact family. This pins the new
// behavior end to end through the registry.
func TestReadFactsPrunesInsteadOfFailingTheWholeBundle(t *testing.T) {
	t.Parallel()

	team := subject(SubjectTeam, "team_platform")
	workload := &factProviderStub{
		capability: planCapability(FactWorkload, "workload", SubjectTeam),
		result:     FactProviderResult{State: SourceNoData, Reason: "no forecast rows"},
	}
	metrics := &factProviderStub{
		capability: planCapability(FactIncidents, "incidents", SubjectRepository),
		result:     FactProviderResult{State: SourceAvailable},
	}

	registry, err := NewFactCapabilityRegistry([]FactProvider{workload, metrics}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
		Subjects:     []SubjectRef{team},
		Requirements: []FactRequirement{{Kind: FactWorkload}, {Kind: FactIncidents}},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v, want the inapplicable capability pruned rather than the bundle failed", err)
	}
	if len(metrics.queries) != 0 {
		t.Fatalf("metrics provider was queried %d time(s), want 0 -- a pruned capability must never be called", len(metrics.queries))
	}
	if len(workload.queries) != 1 {
		t.Fatalf("workload provider was queried %d time(s), want 1", len(workload.queries))
	}

	byKind := make(map[string]SourceObservation, len(bundle.Coverage.Sources))
	for _, source := range bundle.Coverage.Sources {
		byKind[source.Source] = source
	}
	pruned, ok := byKind["canonical_fact:incidents"]
	if !ok {
		t.Fatalf("coverage = %+v, want the pruned capability recorded -- absence must be explainable", bundle.Coverage.Sources)
	}
	if pruned.State != SourcePruned {
		t.Fatalf("pruned state = %q, want %q", pruned.State, SourcePruned)
	}
	if !strings.HasPrefix(pruned.Reason, FactPruneReasonSubjectKindUnsupported) {
		t.Fatalf("pruned reason = %q, want the closed code prefix", pruned.Reason)
	}
	if _, ok := byKind["canonical_fact:workload"]; !ok {
		t.Fatal("the capability that ran must still be recorded in coverage")
	}
}

// TestReadFactsPruningIsNotADegradation pins the semantic that separates a
// prune from every other non-available state: nothing is missing, so the
// answer is not partial. If a prune degraded, Coverage.Partial would be true
// for every correctly-scoped investigation and stop meaning anything.
func TestReadFactsPruningIsNotADegradation(t *testing.T) {
	t.Parallel()

	workload := &factProviderStub{
		capability: planCapability(FactWorkload, "workload", SubjectTeam),
		result:     FactProviderResult{State: SourceAvailable},
	}
	metrics := &factProviderStub{
		capability: planCapability(FactIncidents, "incidents", SubjectRepository),
		result:     FactProviderResult{State: SourceAvailable},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{workload, metrics}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
		Subjects:     []SubjectRef{subject(SubjectTeam, "team_platform")},
		Requirements: []FactRequirement{{Kind: FactWorkload}, {Kind: FactIncidents}},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if bundle.Coverage.Partial {
		t.Fatal("Coverage.Partial = true, want false -- a prune is explained absence, not a gap")
	}
	if len(bundle.Coverage.DegradedReasons) != 0 {
		t.Fatalf("DegradedReasons = %v, want none from a prune", bundle.Coverage.DegradedReasons)
	}
}

// TestReadFactsNarrowsSubjectsAndSaysSo covers the partial case: the
// capability runs, but only against the subjects it can answer for, and the
// subjects it could not be asked about are still explained. Coverage source
// names must be unique, so the narrowing has to ride on the capability's own
// observation rather than getting an entry of its own.
func TestReadFactsNarrowsSubjectsAndSaysSo(t *testing.T) {
	t.Parallel()

	metrics := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result:     FactProviderResult{State: SourceAvailable},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
		Subjects:     []SubjectRef{subject(SubjectTeam, "team_platform"), subject(SubjectRepository, "repo_api")},
		Requirements: []FactRequirement{{Kind: FactMetrics}},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(metrics.queries) != 1 {
		t.Fatalf("metrics queried %d time(s), want 1", len(metrics.queries))
	}
	asked := metrics.queries[0].Subjects
	if len(asked) != 1 || asked[0].Kind != SubjectRepository {
		t.Fatalf("provider was asked about %+v, want only the repository subject", asked)
	}
	if len(bundle.Coverage.Sources) != 1 {
		t.Fatalf("coverage = %+v, want exactly one entry -- source names must stay unique", bundle.Coverage.Sources)
	}
	if !strings.HasPrefix(bundle.Coverage.Sources[0].Reason, FactNarrowReasonSubjectKindUnsupported) {
		t.Fatalf("reason = %q, want the narrowing code recorded", bundle.Coverage.Sources[0].Reason)
	}
	if bundle.Coverage.Partial {
		t.Fatal("Coverage.Partial = true, want false -- narrowing is not a gap either")
	}
}

// TestReadFactsProjectCohortNowAnswers reproduces the exact live-reachable
// failure from the CHAOS-3783 probe. falkorgraph merges FactHealth and
// FactWorkload for EVERY discovered cohort, graphrank resolves a cohort of
// kind project for a question naming "project", and neither capability
// supports project -- so before the planner, "which projects are behind"
// could not be answered at all.
func TestReadFactsProjectCohortNowAnswers(t *testing.T) {
	t.Parallel()

	health := &factProviderStub{
		capability: planCapability(FactHealth, "health", SubjectRepository, SubjectTeam),
		result:     FactProviderResult{State: SourceAvailable},
	}
	workload := &factProviderStub{
		capability: planCapability(FactWorkload, "workload", SubjectTeam),
		result:     FactProviderResult{State: SourceAvailable},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{health, workload}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
		Cohort: &Cohort{Kind: SubjectProject, Members: []CohortMember{
			{Subject: subject(SubjectProject, "project_titan"), Rank: 1},
			{Subject: subject(SubjectProject, "project_atlas"), Rank: 2},
		}},
		Requirements: []FactRequirement{{Kind: FactHealth}, {Kind: FactWorkload}},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v, want a project cohort to be answerable", err)
	}
	if len(health.queries) != 0 || len(workload.queries) != 0 {
		t.Fatal("no capability supports project subjects, so none should have been queried")
	}
	if len(bundle.Coverage.Sources) != 2 {
		t.Fatalf("coverage = %+v, want both capabilities explained", bundle.Coverage.Sources)
	}
	for _, source := range bundle.Coverage.Sources {
		if strings.TrimSpace(source.Reason) == "" {
			t.Fatalf("source %q has no reason -- absence must always be explainable", source.Source)
		}
	}
	// CHAOS-4099 SPLIT THESE TWO APART, and the split is the whole point of
	// that ticket rather than an incidental churn to this one.
	//
	// This test's own invariant is unchanged and still pinned above: a
	// project cohort is ANSWERABLE, both capabilities are explained, and
	// neither is queried. What changed is WHICH explanation each gets, and
	// CHAOS-3783's original blanket `pruned` was right about exactly one of
	// them.
	byKind := make(map[string]SourceObservation, len(bundle.Coverage.Sources))
	for _, source := range bundle.Coverage.Sources {
		byKind[source.Source] = source
	}
	// health is REPOSITORY-scoped, and a project reaches repositories through
	// its linked work items -- a typed chain that exists in prod projection
	// code. So "no admissible fact could exist" was false, and the honest
	// report is a disclosed gap.
	if got := byKind["canonical_fact:health"]; got.State == SourcePruned {
		t.Fatalf("health = %+v, want a disclosed gap: its facts ARE reachable from a project", got)
	} else if !strings.HasPrefix(got.Reason, FactScopeReasonUnexpanded) {
		t.Fatalf("health reason = %q, want the unexpanded vocabulary", got.Reason)
	}
	// workload is TEAM-scoped. Reaching it from a project would have to run
	// through the computed team-attribution edge CHAOS-4101 is deliberately
	// holding back, so no chain is claimed -- and CHAOS-3783's proof of
	// absence stands, non-degrading, exactly as written.
	if got := byKind["canonical_fact:workload"]; got.State != SourcePruned {
		t.Fatalf("workload state = %q, want pruned: no chain to a team-scoped fact is claimed from a project", got.State)
	} else if !strings.HasPrefix(got.Reason, FactPruneReasonSubjectKindUnsupported) {
		t.Fatalf("workload reason = %q, want the prune vocabulary", got.Reason)
	}
}

// TestReadFactsRejectsAProviderClaimingPrunedState pins that pruned is a
// planner verdict only. A provider returning it has by definition already
// run, so the claim is incoherent and must not become coverage.
func TestReadFactsRejectsAProviderClaimingPrunedState(t *testing.T) {
	t.Parallel()

	liar := &factProviderStub{
		capability: planCapability(FactWorkload, "workload", SubjectTeam),
		result:     FactProviderResult{State: SourcePruned, Reason: "I pruned myself"},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{liar}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	_, err = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
		Subjects:     []SubjectRef{subject(SubjectTeam, "team_platform")},
		Requirements: []FactRequirement{{Kind: FactWorkload}},
	})
	if err == nil {
		t.Fatal("ReadFacts() error = nil, want a provider-claimed pruned state rejected")
	}
}

// TestAppendFactCoverageClampsReasonToContractBound guards the one bound
// this change can newly push past: a narrowing note prefixed onto an already
// long provider reason. Truncating the explanation is correct; failing the
// whole result's validation over it is not.
func TestAppendFactCoverageClampsReasonToContractBound(t *testing.T) {
	t.Parallel()

	bundle := CanonicalFactBundle{Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}}
	appendFactCoverage(&bundle, FactWorkload, SourcePruned, nil, "", strings.Repeat("x", maxCoverageReasonLength+500), coverageDetailSpec{})
	if got := len(bundle.Coverage.Sources[0].Reason); got != maxCoverageReasonLength {
		t.Fatalf("reason length = %d, want it clamped to %d", got, maxCoverageReasonLength)
	}
	if err := bundle.Coverage.Validate(); err != nil {
		t.Fatalf("Coverage.Validate() error = %v, want the clamped reason to stay within v1 bounds", err)
	}
}

// TestReadFactsNarrowedProviderThatFailsKeepsBothRecords is the codex
// round-1 F2 regression. A narrowed capability that then errors has TWO
// independent things to report: the read failed, and the planner had already
// cut its subject list. Before the fix the failure path recorded only the
// first, so the record that subjects were dropped vanished -- an absence with
// no explanation, which is exactly what the empty-states rule forbids.
func TestReadFactsNarrowedProviderThatFailsKeepsBothRecords(t *testing.T) {
	t.Parallel()

	metrics := &factProviderStub{
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		err:        &FactReadFailure{State: SourceUnavailable, Reason: "clickhouse is unreachable"},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
		// A team subject the capability cannot serve plus a repository it
		// can: that is what makes this read a NARROWED one.
		Subjects:     []SubjectRef{subject(SubjectTeam, "team_platform"), subject(SubjectRepository, "repo_api")},
		Requirements: []FactRequirement{{Kind: FactMetrics}},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v, want a provider failure to stay coverage", err)
	}
	if len(bundle.Coverage.Sources) != 1 {
		t.Fatalf("coverage = %+v, want exactly one entry", bundle.Coverage.Sources)
	}
	observation := bundle.Coverage.Sources[0]
	if observation.State != SourceUnavailable {
		t.Fatalf("state = %q, want the provider failure preserved as %q", observation.State, SourceUnavailable)
	}
	if !strings.HasPrefix(observation.Reason, FactNarrowReasonSubjectKindUnsupported) {
		t.Fatalf("reason = %q, want the narrowing note kept on a FAILED narrowed read", observation.Reason)
	}
	if !strings.Contains(observation.Reason, "clickhouse is unreachable") {
		t.Fatalf("reason = %q, want the provider's own failure reason kept too", observation.Reason)
	}
}

// TestMergeRejectsImpossiblePerFactSourceState is the codex round-1 F3
// regression. The provider RESULT state was validated, but each FACT's own
// SourceState was not -- and the evidence requirement is keyed on that exact
// field, so a fact stamped with a state other than available/stale silently
// skipped RequiresEvidence. An evidence-free fact could therefore ride inside
// an ordinary available result.
func TestMergeRejectsImpossiblePerFactSourceState(t *testing.T) {
	t.Parallel()

	repository := subject(SubjectRepository, "repo_api")
	cases := []struct {
		name  string
		state SourceState
	}{
		{"pruned is a planner verdict a provider must never mint", SourcePruned},
		{"no_data cannot carry a fact", SourceNoData},
		{"unavailable cannot carry a fact", SourceUnavailable},
		{"not_applicable cannot carry a fact", SourceNotApplicable},
		{"an unknown state is rejected outright", SourceState("invented")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			provider := &factProviderStub{
				capability: FactCapability{
					Kind: FactMetrics, Name: "metrics", Version: "v1",
					SupportedSubjectKinds: []SubjectKind{SubjectRepository}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
				},
				result: FactProviderResult{
					State: SourceAvailable,
					Facts: []CanonicalFact{{
						Kind: FactMetrics, Subject: repository,
						Fields: map[string]FactValue{"commits": IntegerFactValue(7)},
						// No EvidenceRefIDs at all: with the bad state this
						// fact would have bypassed RequiresEvidence entirely.
						SourceState: testCase.state, Source: "metrics", SourceVersion: "v1",
					}},
				},
			}
			registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
			if err != nil {
				t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
			}
			_, err = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
				Subjects:     []SubjectRef{repository},
				Requirements: []FactRequirement{{Kind: FactMetrics}},
			})
			if err == nil {
				t.Fatalf("ReadFacts() error = nil, want a fact carrying source state %q rejected", testCase.state)
			}
		})
	}
}

// TestMergeStillAcceptsLegitimatePerFactSourceStates is the companion guard:
// the F3 check must reject only impossible states, never tighten the two a
// provider legitimately stamps on an individual fact.
func TestMergeStillAcceptsLegitimatePerFactSourceStates(t *testing.T) {
	t.Parallel()

	repository := subject(SubjectRepository, "repo_api")
	for _, state := range []SourceState{SourceAvailable, SourceStale} {
		provider := &factProviderStub{
			capability: FactCapability{
				Kind: FactMetrics, Name: "metrics", Version: "v1",
				SupportedSubjectKinds: []SubjectKind{SubjectRepository}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
			},
			result: FactProviderResult{
				State: SourceAvailable,
				Facts: []CanonicalFact{{
					Kind: FactMetrics, Subject: repository,
					Fields:         map[string]FactValue{"commits": IntegerFactValue(7)},
					EvidenceRefIDs: []string{"evidence_metrics_0001"},
					SourceState:    state, Source: "metrics", SourceVersion: "v1",
				}},
			},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects:     []SubjectRef{repository},
			Requirements: []FactRequirement{{Kind: FactMetrics}},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v, want per-fact state %q still accepted", err, state)
		}
		if len(bundle.Facts) != 1 {
			t.Fatalf("facts = %d, want 1 for per-fact state %q", len(bundle.Facts), state)
		}
	}
}

// TestMergeRequiresEvidenceOnTruncatedFacts is the codex round-2 R2-1
// regression. Truncation is legitimately fact-bearing -- the registry mints
// SourceTruncated itself when the bundle cap trims a result -- but the
// evidence requirement used to be keyed on available/stale only, so an
// evidence-requiring provider could return an evidence-FREE truncated fact
// and have it accepted. Truncation says "there are more facts than these",
// never "these facts need less grounding".
func TestMergeRequiresEvidenceOnTruncatedFacts(t *testing.T) {
	t.Parallel()

	repository := subject(SubjectRepository, "repo_api")
	evidenceRequiring := FactCapability{
		Kind: FactMetrics, Name: "metrics", Version: "v1",
		SupportedSubjectKinds: []SubjectKind{SubjectRepository}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
	}
	factWith := func(evidence []string) CanonicalFact {
		return CanonicalFact{
			Kind: FactMetrics, Subject: repository,
			Fields: map[string]FactValue{"commits": IntegerFactValue(7)},
			// The state the registry itself assigns when a result is trimmed.
			SourceState: SourceTruncated, Source: "metrics", SourceVersion: "v1",
			EvidenceRefIDs: evidence,
		}
	}

	read := func(t *testing.T, fact CanonicalFact) error {
		t.Helper()
		provider := &factProviderStub{
			capability: evidenceRequiring,
			result:     FactProviderResult{State: SourceTruncated, Reason: "trimmed", Facts: []CanonicalFact{fact}},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		_, err = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects:     []SubjectRef{repository},
			Requirements: []FactRequirement{{Kind: FactMetrics}},
		})
		return err
	}

	if err := read(t, factWith(nil)); err == nil {
		t.Fatal("ReadFacts() error = nil, want an evidence-free truncated fact rejected when the capability requires evidence")
	}
	if err := read(t, factWith([]string{"evidence_metrics_0001"})); err != nil {
		t.Fatalf("ReadFacts() error = %v, want a truncated fact WITH evidence still accepted", err)
	}
}

// TestAppendFactCoverageDegradedReasonStaysWithinBounds is the codex
// round-2 R2-3 regression. The reason was clamped to its own bound and the
// "<kind>: " prefix was then added on top, pushing the DegradedReasons entry
// back over the limit by exactly the prefix length -- so the finished result
// failed contract validation, the very outcome this clamping exists to
// prevent. The live path is a narrowed provider that fails: the narrowing
// note and the failure reason are concatenated before they reach coverage.
func TestAppendFactCoverageDegradedReasonStaysWithinBounds(t *testing.T) {
	t.Parallel()

	bundle := CanonicalFactBundle{Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}}
	// Exactly at the reason bound: the old code left this untouched, then
	// prefixed it, producing an over-long degraded entry.
	appendFactCoverage(&bundle, FactOperationalDeficiencies, SourceUnavailable, nil, "", strings.Repeat("x", maxCoverageReasonLength), coverageDetailSpec{})
	if err := bundle.Coverage.Validate(); err != nil {
		t.Fatalf("Coverage.Validate() error = %v, want the composed degraded reason within v1 bounds", err)
	}
	if len(bundle.Coverage.DegradedReasons) != 1 {
		t.Fatalf("degraded reasons = %v, want one entry", bundle.Coverage.DegradedReasons)
	}
	if got := utf8.RuneCountInString(bundle.Coverage.DegradedReasons[0]); got > maxCoverageDegradedReasonLength {
		t.Fatalf("degraded reason length = %d runes, want <= %d", got, maxCoverageDegradedReasonLength)
	}
}

// TestClampCoverageTextIsRuneSafeAndNeverEmpty pins the two properties the
// clamp has to hold beyond raw length. The contract bounds are RUNE counts
// (contractsv1.stringLengthBetween), and DegradedReasons entries must equal
// their own strings.TrimSpace -- so a byte-slicing or trailing-space-leaving
// clamp would produce values the validator rejects for reasons unrelated to
// length.
func TestClampCoverageTextIsRuneSafeAndNeverEmpty(t *testing.T) {
	t.Parallel()

	multibyte := strings.Repeat("é", 3000)
	clamped := clampCoverageText(multibyte, maxCoverageReasonLength)
	if got := utf8.RuneCountInString(clamped); got != maxCoverageReasonLength {
		t.Fatalf("rune count = %d, want exactly %d -- the bound is runes, not bytes", got, maxCoverageReasonLength)
	}
	if !utf8.ValidString(clamped) {
		t.Fatal("clamped value is not valid UTF-8 -- a rune was cut in half")
	}

	// Truncation landing on whitespace must not leave a trailing space.
	spacey := strings.Repeat("a ", maxCoverageReasonLength)
	if got := clampCoverageText(spacey, maxCoverageReasonLength); got != strings.TrimSpace(got) {
		t.Fatalf("clamped value %q has surrounding whitespace -- DegradedReasons requires TrimSpace(v) == v", got)
	}

	// Leading whitespace is stripped BEFORE truncating, which is what makes
	// the result provably non-empty even when the head of the value is all
	// spaces.
	leading := strings.Repeat(" ", maxCoverageReasonLength+50) + "the real reason"
	if got := clampCoverageText(leading, maxCoverageReasonLength); got == "" {
		t.Fatal("clamped value is empty -- an empty reason on a non-available source is itself invalid")
	}
}

// TestPruningIsInvisibleToTruncationAndCaps is the codex round-3 regression,
// stated at the level the finding was really about.
//
// The measurement harness compares two bundles to prove pruning removes work
// and not answer. That claim is only meaningful if both bundles come from the
// SAME pipeline: the registry rewrites a truncated result's state, enforces
// maxCanonicalFactsPerBundle across all providers, stamps omitted per-fact
// fields, and sorts. A comparison bundle assembled by hand re-implements
// those semantics and diverges from them one detail at a time -- which is
// what happened, three review rounds running.
//
// This pins the property the harness now relies on instead: for a provider
// pushed past the aggregate fact cap, asking for {supported, pruned-kind}
// yields identical facts to asking for {supported} alone. It fails against
// any implementation where the two sides do not share the real truncation and
// cap handling.
func TestPruningIsInvisibleToTruncationAndCaps(t *testing.T) {
	t.Parallel()

	repository := subject(SubjectRepository, "repo_api")
	// Deliberately over maxCanonicalFactsPerBundle so the registry's
	// aggregate cap trims the result and rewrites its state to
	// SourceTruncated -- the exact semantics a re-implemented baseline missed.
	overCap := make([]CanonicalFact, 0, maxCanonicalFactsPerBundle+250)
	for i := 0; i < maxCanonicalFactsPerBundle+250; i++ {
		overCap = append(overCap, CanonicalFact{
			Kind: FactMetrics, Subject: repository,
			Fields:         map[string]FactValue{"commits": IntegerFactValue(int64(i))},
			EvidenceRefIDs: []string{"evidence_metrics_0001"},
			Source:         "metrics", SourceVersion: "v1",
		})
	}

	newRegistry := func(t *testing.T) *FactCapabilityRegistry {
		t.Helper()
		metrics := &factProviderStub{
			capability: FactCapability{
				Kind: FactMetrics, Name: "metrics", Version: "v1",
				SupportedSubjectKinds: []SubjectKind{SubjectRepository}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
			},
			result: FactProviderResult{State: SourceAvailable, Facts: overCap},
		}
		// Team-only, so a repository investigation prunes it.
		workload := &factProviderStub{
			capability: FactCapability{
				Kind: FactWorkload, Name: "workload", Version: "v1",
				SupportedSubjectKinds: []SubjectKind{SubjectTeam}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
			},
			result: FactProviderResult{State: SourceAvailable},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{metrics, workload}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		return registry
	}

	read := func(t *testing.T, kinds ...FactKind) CanonicalFactBundle {
		t.Helper()
		requirements := make([]FactRequirement, 0, len(kinds))
		for _, kind := range kinds {
			requirements = append(requirements, FactRequirement{Kind: kind})
		}
		bundle, err := newRegistry(t).ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects: []SubjectRef{repository}, Requirements: requirements,
		})
		if err != nil {
			t.Fatalf("ReadFacts(%v) error = %v", kinds, err)
		}
		return bundle
	}

	withPruned := read(t, FactMetrics, FactWorkload)
	withoutPruned := read(t, FactMetrics)

	// Sanity: the cap really did fire, or this test proves nothing.
	if len(withPruned.Facts) != maxCanonicalFactsPerBundle {
		t.Fatalf("facts = %d, want the aggregate cap (%d) to have trimmed the result", len(withPruned.Facts), maxCanonicalFactsPerBundle)
	}
	truncated := false
	for _, source := range withPruned.Coverage.Sources {
		if source.Source == "canonical_fact:metrics" && source.State == SourceTruncated {
			truncated = true
		}
	}
	if !truncated {
		t.Fatal("want the metrics source recorded as truncated -- the cap rewrite is the semantic under test")
	}

	if len(withPruned.Facts) != len(withoutPruned.Facts) {
		t.Fatalf("fact counts differ with the pruned kind present: %d vs %d", len(withPruned.Facts), len(withoutPruned.Facts))
	}
	for i := range withPruned.Facts {
		if !reflect.DeepEqual(withPruned.Facts[i], withoutPruned.Facts[i]) {
			t.Fatalf("fact %d differs with the pruned kind present:\n with = %+v\n without = %+v", i, withPruned.Facts[i], withoutPruned.Facts[i])
		}
	}

	// And the pruned kind is still explained, not merely absent.
	prunedRecorded := false
	for _, source := range withPruned.Coverage.Sources {
		if source.Source == "canonical_fact:workload" && source.State == SourcePruned {
			prunedRecorded = true
		}
	}
	if !prunedRecorded {
		t.Fatal("want the pruned capability recorded in coverage even when the answer is unchanged")
	}
}

// TestReadFactsRejectsOutOfScopeExplicitSubjects is the codex round-5 R5-1
// regression.
//
// An explicit requirement.Subjects list is a caller ASSERTION -- "read this
// kind for exactly these subjects" -- so naming a subject outside the
// investigation set is an error about the request, not a statement about what
// a capability can answer. buildFactQuery has always rejected it, but once
// pruning was introduced a wholly-unsupported explicit list got pruned BEFORE
// that check could run, and an out-of-scope request quietly became a success
// with zero facts and a pruned coverage entry.
//
// The distinction this pins: out-of-scope is an ERROR, in-scope-but-the-
// capability-does-not-support-that-kind is a PRUNE.
func TestReadFactsRejectsOutOfScopeExplicitSubjects(t *testing.T) {
	t.Parallel()

	repository := subject(SubjectRepository, "repo_api")
	outOfScopeProject := subject(SubjectProject, "project_titan")
	inScopeTeam := subject(SubjectTeam, "team_platform")

	newRegistry := func(t *testing.T) (*FactCapabilityRegistry, *factProviderStub) {
		t.Helper()
		// Repository-only, so neither a project nor a team subject fits it.
		metrics := &factProviderStub{
			capability: planCapability(FactIncidents, "incidents", SubjectRepository),
			result:     FactProviderResult{State: SourceAvailable},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		return registry, metrics
	}

	t.Run("out-of-scope explicit subjects error rather than being pruned", func(t *testing.T) {
		t.Parallel()
		registry, metrics := newRegistry(t)
		_, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects: []SubjectRef{repository},
			Requirements: []FactRequirement{
				// project_titan is in NO part of this investigation's scope.
				{Kind: FactIncidents, Subjects: []SubjectRef{outOfScopeProject}},
			},
		})
		if err == nil {
			t.Fatal("ReadFacts() error = nil, want an out-of-scope explicit subject rejected -- pruning must not swallow a scope violation")
		}
		if !strings.Contains(err.Error(), "outside the discovered investigation set") {
			t.Fatalf("ReadFacts() error = %v, want the scope violation named", err)
		}
		if len(metrics.queries) != 0 {
			t.Fatal("the provider must not be queried for an out-of-scope request")
		}
	})

	t.Run("in-scope explicit subjects of an unsupported kind are still pruned", func(t *testing.T) {
		t.Parallel()
		registry, metrics := newRegistry(t)
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			// The team IS part of the investigation, so naming it is a
			// legitimate assertion -- the capability simply cannot serve it.
			Subjects: []SubjectRef{repository, inScopeTeam},
			Requirements: []FactRequirement{
				{Kind: FactIncidents, Subjects: []SubjectRef{inScopeTeam}},
			},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v, want an in-scope but unsupported kind pruned, not failed", err)
		}
		if len(metrics.queries) != 0 {
			t.Fatal("a pruned capability must never be queried")
		}
		if len(bundle.Coverage.Sources) != 1 || bundle.Coverage.Sources[0].State != SourcePruned {
			t.Fatalf("coverage = %+v, want a single pruned observation", bundle.Coverage.Sources)
		}
	})

	t.Run("in-scope explicit subjects of a supported kind still run", func(t *testing.T) {
		t.Parallel()
		registry, metrics := newRegistry(t)
		if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects: []SubjectRef{repository, inScopeTeam},
			Requirements: []FactRequirement{
				{Kind: FactIncidents, Subjects: []SubjectRef{repository}},
			},
		}); err != nil {
			t.Fatalf("ReadFacts() error = %v, want a valid scoped requirement honored", err)
		}
		if len(metrics.queries) != 1 {
			t.Fatalf("provider queried %d time(s), want 1", len(metrics.queries))
		}
		if len(metrics.queries[0].Subjects) != 1 || metrics.queries[0].Subjects[0].CanonicalID != "repo_api" {
			t.Fatalf("provider was asked about %+v, want only the requirement's own subject", metrics.queries[0].Subjects)
		}
	})
}

// TestScopePrecedenceIsSharedBetweenPlannerAndRegistry is the codex round-6
// F1 regression.
//
// "In scope" was defined twice and the two disagreed. The planner applied
// request.Subjects ELSE cohort members -- a FALLBACK -- while the registry
// keyed its allowed-subject map on the UNION of both. A request naming both,
// with an explicit requirement subject drawn from the cohort, therefore
// passed the pre-planner scope check on the union and was then pruned by a
// planner that had already scoped it out. Worse, a capability that DID
// support that subject kind would have been queried against a subject the
// request scoped away.
//
// The round-5 test could not catch this: it never set a Cohort, so the union
// and the fallback agreed for every case it exercised. This one is built
// specifically on the disagreement.
func TestScopePrecedenceIsSharedBetweenPlannerAndRegistry(t *testing.T) {
	t.Parallel()

	repository := subject(SubjectRepository, "repo_api")
	cohortOnlyProject := subject(SubjectProject, "project_titan")
	// request.Subjects is NON-EMPTY, so the fallback scope is exactly
	// [repo_api] and the cohort member is deliberately out of scope.
	mixedScope := func(requirements ...FactRequirement) CanonicalFactRequest {
		return CanonicalFactRequest{
			Subjects: []SubjectRef{repository},
			Cohort: &Cohort{Kind: SubjectProject, Members: []CohortMember{
				{Subject: cohortOnlyProject, Rank: 1},
			}},
			Requirements: requirements,
		}
	}

	t.Run("a cohort-only explicit subject is out of scope and must error", func(t *testing.T) {
		t.Parallel()
		// Repository-only capability: under the old union scope this was
		// pruned instead of rejected.
		metrics := &factProviderStub{
			capability: planCapability(FactMetrics, "metrics", SubjectRepository),
			result:     FactProviderResult{State: SourceAvailable},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		_, err = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
			mixedScope(FactRequirement{Kind: FactMetrics, Subjects: []SubjectRef{cohortOnlyProject}}))
		if err == nil {
			t.Fatal("ReadFacts() error = nil, want a cohort-only subject rejected when request.Subjects scopes it out")
		}
		if !strings.Contains(err.Error(), "outside the discovered investigation set") {
			t.Fatalf("ReadFacts() error = %v, want the scope violation named", err)
		}
	})

	t.Run("a capable provider is never queried against an out-of-scope subject", func(t *testing.T) {
		t.Parallel()
		// This capability DOES support project subjects, so under the old
		// union scope it would have been queried against a subject the
		// request had scoped away -- the more serious half of the finding.
		health := &factProviderStub{
			capability: planCapability(FactHealth, "health", SubjectProject, SubjectRepository),
			result:     FactProviderResult{State: SourceAvailable},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{health}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		_, err = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
			mixedScope(FactRequirement{Kind: FactHealth, Subjects: []SubjectRef{cohortOnlyProject}}))
		if err == nil {
			t.Fatal("ReadFacts() error = nil, want the out-of-scope subject rejected even for a capability that supports its kind")
		}
		if len(health.queries) != 0 {
			t.Fatalf("provider was queried %d time(s) against an out-of-scope subject", len(health.queries))
		}
	})

	t.Run("implicit fan-out is untouched by the precedence fix", func(t *testing.T) {
		t.Parallel()
		// No explicit requirement.Subjects: the planner fans out over the
		// fallback scope (request.Subjects), exactly as before.
		metrics := &factProviderStub{
			capability: planCapability(FactMetrics, "metrics", SubjectRepository),
			result:     FactProviderResult{State: SourceAvailable},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
			mixedScope(FactRequirement{Kind: FactMetrics})); err != nil {
			t.Fatalf("ReadFacts() error = %v, want implicit fan-out unaffected", err)
		}
		if len(metrics.queries) != 1 {
			t.Fatalf("provider queried %d time(s), want 1", len(metrics.queries))
		}
		asked := metrics.queries[0].Subjects
		if len(asked) != 1 || asked[0].CanonicalID != "repo_api" {
			t.Fatalf("provider was asked about %+v, want only the request-scoped repository", asked)
		}
	})

	t.Run("a cohort-only request still scopes to its members", func(t *testing.T) {
		t.Parallel()
		// request.Subjects empty: the fallback selects the cohort, so its
		// members ARE in scope. The precedence fix must not break this.
		health := &factProviderStub{
			capability: planCapability(FactHealth, "health", SubjectProject),
			result:     FactProviderResult{State: SourceAvailable},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{health}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Cohort: &Cohort{Kind: SubjectProject, Members: []CohortMember{{Subject: cohortOnlyProject, Rank: 1}}},
			Requirements: []FactRequirement{
				{Kind: FactHealth, Subjects: []SubjectRef{cohortOnlyProject}},
			},
		}); err != nil {
			t.Fatalf("ReadFacts() error = %v, want cohort members in scope when the request names no subjects", err)
		}
		if len(health.queries) != 1 {
			t.Fatalf("provider queried %d time(s), want 1", len(health.queries))
		}
	})
}

// TestReadFactsRejectsDuplicateSubjectsEvenWhenEverythingPrunes is the codex
// round-8 F1 regression, and the same family as the round-5 scope fix:
// request VALIDATION must complete before any pruning short-circuit.
//
// buildFactQuery has always rejected a duplicated subject, and both the v1
// schema (uniqueItems) and the Go resolution validator reject one on the
// wire. But when every requirement prunes, no provider is queried, so
// buildFactQuery never runs and its rejection never fires -- an invalid
// request returned SUCCESS with pruned coverage. Validity is a property of
// the request, not of how much work the request happens to imply.
func TestReadFactsRejectsDuplicateSubjectsEvenWhenEverythingPrunes(t *testing.T) {
	t.Parallel()

	team := subject(SubjectTeam, "team_platform")
	// Repository-only, so a team-subject investigation prunes it entirely and
	// no provider is ever queried.
	newRegistry := func(t *testing.T) (*FactCapabilityRegistry, *factProviderStub) {
		t.Helper()
		metrics := &factProviderStub{
			capability: planCapability(FactIncidents, "incidents", SubjectRepository),
			result:     FactProviderResult{State: SourceAvailable},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		return registry, metrics
	}

	t.Run("duplicate investigation subjects error even though everything prunes", func(t *testing.T) {
		t.Parallel()
		registry, metrics := newRegistry(t)
		_, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			// The same subject twice: rejected by uniqueItems on the wire, so
			// it must not be quietly accepted here either.
			Subjects:     []SubjectRef{team, team},
			Requirements: []FactRequirement{{Kind: FactIncidents}},
		})
		if err == nil {
			t.Fatal("ReadFacts() error = nil, want a duplicated subject rejected even when every requirement prunes")
		}
		if !strings.Contains(err.Error(), "unique") {
			t.Fatalf("ReadFacts() error = %v, want the uniqueness violation named", err)
		}
		if len(metrics.queries) != 0 {
			t.Fatal("no provider should be queried for an invalid request")
		}
	})

	t.Run("duplicate explicit requirement subjects error even though everything prunes", func(t *testing.T) {
		t.Parallel()
		registry, _ := newRegistry(t)
		_, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects: []SubjectRef{team},
			Requirements: []FactRequirement{
				// In scope, but duplicated within the requirement's own list.
				{Kind: FactIncidents, Subjects: []SubjectRef{team, team}},
			},
		})
		if err == nil {
			t.Fatal("ReadFacts() error = nil, want a duplicated explicit subject rejected even when the requirement prunes")
		}
		if !strings.Contains(err.Error(), "unique") {
			t.Fatalf("ReadFacts() error = %v, want the uniqueness violation named", err)
		}
	})

	t.Run("control: duplicate-free all-unsupported still prunes", func(t *testing.T) {
		t.Parallel()
		registry, metrics := newRegistry(t)
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects:     []SubjectRef{team},
			Requirements: []FactRequirement{{Kind: FactIncidents}},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v, want a valid all-unsupported request to prune, not fail", err)
		}
		if len(metrics.queries) != 0 {
			t.Fatal("a pruned capability must never be queried")
		}
		if len(bundle.Coverage.Sources) != 1 || bundle.Coverage.Sources[0].State != SourcePruned {
			t.Fatalf("coverage = %+v, want a single pruned observation", bundle.Coverage.Sources)
		}
	})

	t.Run("control: duplicate-free request that actually runs is unaffected", func(t *testing.T) {
		t.Parallel()
		repository := subject(SubjectRepository, "repo_api")
		registry, metrics := newRegistry(t)
		if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects:     []SubjectRef{repository},
			Requirements: []FactRequirement{{Kind: FactIncidents}},
		}); err != nil {
			t.Fatalf("ReadFacts() error = %v, want a valid request unaffected by the uniqueness guard", err)
		}
		if len(metrics.queries) != 1 {
			t.Fatalf("provider queried %d time(s), want 1", len(metrics.queries))
		}
	})
}

// TestReadFactsRejectsDisallowedParametersEvenWhenEverythingPrunes closes the
// LAST buildFactQuery check that was reachable only when a capability is not
// pruned. Self-found while auditing this branch against codex round-8 F1's
// class, then independently confirmed as codex round-9's finding.
//
// Same defect one field over: a requirement carrying a parameter key the
// capability does not allow never reached buildFactQuery once its capability
// pruned, so an invalid request returned SUCCESS with pruned coverage.
// Whether a request is VALID must not depend on how much work it happens to
// imply.
func TestReadFactsRejectsDisallowedParametersEvenWhenEverythingPrunes(t *testing.T) {
	t.Parallel()

	team := subject(SubjectTeam, "team_platform")
	repository := subject(SubjectRepository, "repo_api")
	// Repository-only capability that allows exactly one parameter key.
	newRegistry := func(t *testing.T) (*FactCapabilityRegistry, *factProviderStub) {
		t.Helper()
		metrics := &factProviderStub{
			capability: FactCapability{
				Kind: FactIncidents, Name: "incidents", Version: "v1",
				SupportedSubjectKinds: []SubjectKind{SubjectRepository},
				AllowedParameters:     []string{"window_days"}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
			},
			result: FactProviderResult{State: SourceAvailable},
		}
		registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		return registry, metrics
	}

	t.Run("codex round-9 scenario: disallowed parameter on an all-pruned request errors", func(t *testing.T) {
		t.Parallel()
		registry, metrics := newRegistry(t)
		_, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			// Team-only subjects against a repository-only capability: every
			// requirement prunes, so buildFactQuery never ran.
			Subjects: []SubjectRef{team},
			Requirements: []FactRequirement{
				{Kind: FactIncidents, Parameters: map[string]string{"sql": "select *"}},
			},
		})
		if err == nil {
			t.Fatal("ReadFacts() error = nil, want a disallowed parameter rejected even when every requirement prunes")
		}
		if !strings.Contains(err.Error(), "parameter") || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("ReadFacts() error = %v, want the disallowed parameter named", err)
		}
		if len(metrics.queries) != 0 {
			t.Fatal("no provider should be queried for an invalid request")
		}
	})

	t.Run("control: an ALLOWED parameter on an all-pruned request still prunes", func(t *testing.T) {
		t.Parallel()
		registry, metrics := newRegistry(t)
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects: []SubjectRef{team},
			Requirements: []FactRequirement{
				{Kind: FactIncidents, Parameters: map[string]string{"window_days": "30"}},
			},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v, want a valid pruned requirement to prune, not fail", err)
		}
		if len(metrics.queries) != 0 {
			t.Fatal("a pruned capability must never be queried")
		}
		if len(bundle.Coverage.Sources) != 1 || bundle.Coverage.Sources[0].State != SourcePruned {
			t.Fatalf("coverage = %+v, want a single pruned observation", bundle.Coverage.Sources)
		}
	})

	t.Run("control: a disallowed parameter on a RUNNING request still errors", func(t *testing.T) {
		t.Parallel()
		registry, _ := newRegistry(t)
		if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects: []SubjectRef{repository},
			Requirements: []FactRequirement{
				{Kind: FactIncidents, Parameters: map[string]string{"sql": "select *"}},
			},
		}); err == nil {
			t.Fatal("ReadFacts() error = nil, want the pre-existing rejection preserved on the running path")
		}
	})

	t.Run("control: an unregistered kind's parameters are not newly rejected", func(t *testing.T) {
		t.Parallel()
		registry, _ := newRegistry(t)
		// FactWorkload has no provider here, so there is no capability to
		// declare an allowlist against and the kind already degrades to
		// SourceUnconfigured without ever building a query. The pre-pass
		// must not invent a rejection for it.
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
			Subjects: []SubjectRef{repository},
			Requirements: []FactRequirement{
				{Kind: FactWorkload, Parameters: map[string]string{"anything": "goes"}},
			},
		})
		if err != nil {
			t.Fatalf("ReadFacts() error = %v, want an unregistered kind to stay SourceUnconfigured", err)
		}
		if len(bundle.Coverage.Sources) != 1 || bundle.Coverage.Sources[0].State != SourceUnconfigured {
			t.Fatalf("coverage = %+v, want a single unconfigured observation", bundle.Coverage.Sources)
		}
	})
}

// TestChaos4347_MetricsWidenedToTeamAndProjectSubjectsIsNotPruned pins
// CHAOS-4347's widening at the planner seam: a capability that now
// declares [repository, team, project] (the real MetricsProvider's shape
// as of CHAOS-4347 -- devhealthfacts/metrics.go) must run, unpruned and
// unnarrowed, for a lone team subject and for a lone project subject, and
// the registry must return the facts the stubbed provider produced for
// each. Before CHAOS-4347, the real MetricsProvider declared only
// {repository}, so a team or project subject alone pruned FactMetrics
// entirely (§3's "no subject has a supported kind" case) -- this test
// exercises exactly the shape that flips.
func TestChaos4347_MetricsWidenedToTeamAndProjectSubjectsIsNotPruned(t *testing.T) {
	t.Parallel()

	capability := planCapability(FactMetrics, "devhealthfacts.metrics", SubjectRepository, SubjectTeam, SubjectProject)

	for _, testCase := range []struct {
		name    string
		subject SubjectRef
	}{
		{name: "team subject", subject: subject(SubjectTeam, "team_platform")},
		{name: "project subject", subject: subject(SubjectProject, "project_titan")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// Planner level: the requirement must run against this
			// subject, not prune or narrow it away.
			request := CanonicalFactRequest{
				Subjects:     []SubjectRef{testCase.subject},
				Requirements: []FactRequirement{{Kind: FactMetrics}},
			}
			plan := planFactReads(newFactPlanInput(request), planIndex(capability))
			if len(plan) != 1 {
				t.Fatalf("plan length = %d, want 1", len(plan))
			}
			if plan[0].Pruned {
				t.Fatalf("Pruned = true (reason %q), want FactMetrics to run for %s -- CHAOS-4347 widened it to this kind", plan[0].Reason, testCase.subject.Kind)
			}
			if plan[0].Narrowed {
				t.Fatalf("Narrowed = true (reason %q), want the lone subject to survive untouched", plan[0].Reason)
			}

			// Registry level: the widened capability must actually be
			// QUERIED and its fact actually returned -- proving the whole
			// path, not only the planner's own decision.
			metrics := &factProviderStub{
				capability: capability,
				result: FactProviderResult{
					State:   SourceAvailable,
					Version: "devhealthfacts.clickhouse.v1",
					Facts: []CanonicalFact{{
						Kind: FactMetrics, Subject: testCase.subject,
						Fields:         map[string]FactValue{"commits_count": IntegerFactValue(7)},
						EvidenceRefIDs: []string{"acr:v1:team:team_platform"},
						SourceState:    SourceAvailable,
					}},
				},
			}
			registry, err := NewFactCapabilityRegistry([]FactProvider{metrics}, FactRegistryOptions{})
			if err != nil {
				t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
			}
			bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			if len(metrics.queries) != 1 {
				t.Fatalf("provider queried %d times, want exactly 1 -- a pruned capability is never called", len(metrics.queries))
			}
			if len(bundle.Facts) != 1 || bundle.Facts[0].Kind != FactMetrics || bundle.Facts[0].Subject.CanonicalID != testCase.subject.CanonicalID {
				t.Fatalf("facts = %#v, want one FactMetrics fact for %s", bundle.Facts, testCase.subject.CanonicalID)
			}
		})
	}
}
