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
	return FactCapability{Kind: kind, Name: name, Version: "v1", SupportedSubjectKinds: kinds}
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
	requirementType := reflect.TypeOf(factPlanRequirement{})
	if requirementType.NumField() != 2 {
		t.Fatalf("factPlanRequirement has %d fields, want 2 (Kind, Subjects)", requirementType.NumField())
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
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result:     FactProviderResult{State: SourceAvailable},
	}

	registry, err := NewFactCapabilityRegistry([]FactProvider{workload, metrics}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
		Subjects:     []SubjectRef{team},
		Requirements: []FactRequirement{{Kind: FactWorkload}, {Kind: FactMetrics}},
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
	pruned, ok := byKind["canonical_fact:metrics"]
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
		capability: planCapability(FactMetrics, "metrics", SubjectRepository),
		result:     FactProviderResult{State: SourceAvailable},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{workload, metrics}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, CanonicalFactRequest{
		Subjects:     []SubjectRef{subject(SubjectTeam, "team_platform")},
		Requirements: []FactRequirement{{Kind: FactWorkload}, {Kind: FactMetrics}},
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
		t.Fatalf("coverage = %+v, want both capabilities recorded as pruned", bundle.Coverage.Sources)
	}
	for _, source := range bundle.Coverage.Sources {
		if source.State != SourcePruned {
			t.Fatalf("source %q state = %q, want %q", source.Source, source.State, SourcePruned)
		}
		if strings.TrimSpace(source.Reason) == "" {
			t.Fatalf("source %q has no reason -- a prune must always be explainable", source.Source)
		}
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
	appendFactCoverage(&bundle, FactWorkload, SourcePruned, nil, "", strings.Repeat("x", maxCoverageReasonLength+500))
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
					SupportedSubjectKinds: []SubjectKind{SubjectRepository}, RequiresEvidence: true,
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
				SupportedSubjectKinds: []SubjectKind{SubjectRepository}, RequiresEvidence: true,
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
		SupportedSubjectKinds: []SubjectKind{SubjectRepository}, RequiresEvidence: true,
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
	appendFactCoverage(&bundle, FactOperationalDeficiencies, SourceUnavailable, nil, "", strings.Repeat("x", maxCoverageReasonLength))
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
