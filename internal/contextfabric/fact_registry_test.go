package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type factProviderStub struct {
	capability FactCapability
	result     FactProviderResult
	err        error
	wait       bool
	queries    []FactQuery
}

func (p *factProviderStub) Capability() FactCapability { return p.capability }

func (p *factProviderStub) ReadFacts(ctx context.Context, _ storage.Principal, query FactQuery) (FactProviderResult, error) {
	p.queries = append(p.queries, query)
	if p.wait {
		<-ctx.Done()
		return FactProviderResult{}, ctx.Err()
	}
	return p.result, p.err
}

func TestFactCapabilityRegistryBatchesCapabilitiesAndPreservesEvidenceVersions(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	observed := time.Date(2026, 8, 11, 18, 0, 0, 0, time.UTC)
	status := &factProviderStub{
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "status-v2", SupportedSubjectKinds: []SubjectKind{SubjectProject}, RequiresEvidence: true},
		result: FactProviderResult{
			State: SourceAvailable, ObservedAt: &observed, Watermark: "wm-status", Version: "status-v2",
			Facts: []CanonicalFact{{
				Kind: FactStatus, Subject: project, Fields: map[string]FactValue{"status": StringFactValue("in_progress")},
				ObservedAt: &observed, EvidenceRefIDs: []string{"evidence_status_1234"}, SourceState: SourceAvailable,
			}},
		},
	}
	readiness := &factProviderStub{
		capability: FactCapability{Kind: FactReadiness, Name: "ops-readiness", Version: "readiness-v3", SupportedSubjectKinds: []SubjectKind{SubjectProject}, RequiresEvidence: true},
		result: FactProviderResult{
			State: SourceAvailable, ObservedAt: &observed, Watermark: "wm-readiness", Version: "readiness-v3",
			Facts: []CanonicalFact{{
				Kind: FactReadiness, Subject: project, Fields: map[string]FactValue{"release_ready": BooleanFactValue(false)},
				ObservedAt: &observed, EvidenceRefIDs: []string{"evidence_readiness_1234"}, SourceState: SourceAvailable,
			}},
		},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{readiness, status}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}

	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, FactStatus, FactReadiness))
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(bundle.Facts) != 2 || bundle.Facts[0].Kind != FactStatus || bundle.Facts[1].Kind != FactReadiness {
		t.Fatalf("facts = %#v", bundle.Facts)
	}
	if bundle.Facts[0].Source != "ops-status" || bundle.Facts[0].SourceVersion != "status-v2" {
		t.Fatalf("normalized status fact = %#v", bundle.Facts[0])
	}
	if bundle.Versions[FactStatus] != "status-v2" || bundle.Versions[FactReadiness] != "readiness-v3" {
		t.Fatalf("versions = %#v", bundle.Versions)
	}
	if bundle.Watermarks[FactStatus] != "wm-status" || bundle.Watermarks[FactReadiness] != "wm-readiness" {
		t.Fatalf("watermarks = %#v", bundle.Watermarks)
	}
	if bundle.Coverage.Partial || len(bundle.Coverage.Sources) != 2 || len(status.queries) != 1 || len(readiness.queries) != 1 {
		t.Fatalf("coverage = %#v, status queries = %#v, readiness queries = %#v", bundle.Coverage, status.queries, readiness.queries)
	}
}

func TestFactCapabilityRegistryPreservesIndependentFactsWhenOneCapabilityDegrades(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	status := &factProviderStub{
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "status-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}},
		result: FactProviderResult{State: SourceAvailable, Version: "status-v1", Facts: []CanonicalFact{{
			Kind: FactStatus, Subject: project, Fields: map[string]FactValue{"status": StringFactValue("in_progress")}, SourceState: SourceAvailable,
		}}},
	}
	readiness := &factProviderStub{
		capability: FactCapability{Kind: FactReadiness, Name: "ops-readiness", Version: "readiness-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}},
		err:        &FactReadFailure{State: SourceUnavailable, Reason: "readiness service is unavailable"},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{status, readiness}, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}

	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, FactStatus, FactReadiness))
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(bundle.Facts) != 1 || bundle.Facts[0].Kind != FactStatus || !bundle.Coverage.Partial {
		t.Fatalf("bundle = %#v", bundle)
	}
	if len(bundle.Coverage.DegradedReasons) != 1 || !strings.Contains(bundle.Coverage.DegradedReasons[0], "readiness service") {
		t.Fatalf("degraded reasons = %#v", bundle.Coverage.DegradedReasons)
	}
}

func TestFactCapabilityRegistryReportsUnconfiguredCapability(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	registry, err := NewFactCapabilityRegistry(nil, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, FactStatus))
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(bundle.Facts) != 0 || len(bundle.Coverage.Sources) != 1 || bundle.Coverage.Sources[0].State != SourceUnconfigured || !bundle.Coverage.Partial {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestFactCapabilityRegistryRejectsParametersOutsideServerOwnedCapability(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	provider := &factProviderStub{capability: FactCapability{
		Kind: FactMetrics, Name: "ops-metrics", Version: "metrics-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, AllowedParameters: []string{"window_days"},
	}}
	registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := canonicalFactRequest(project, FactMetrics)
	request.Requirements[0].Parameters = map[string]string{"sql": "select *"}
	if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("ReadFacts() error = %v", err)
	}
}

func TestFactCapabilityRegistryRequiresEvidenceForObservedCapability(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	provider := &factProviderStub{
		capability: FactCapability{Kind: FactReadiness, Name: "ops-readiness", Version: "readiness-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, RequiresEvidence: true},
		result: FactProviderResult{State: SourceAvailable, Version: "readiness-v1", Facts: []CanonicalFact{{
			Kind: FactReadiness, Subject: project, Fields: map[string]FactValue{"release_ready": BooleanFactValue(false)}, SourceState: SourceAvailable, EvidenceRefIDs: []string{},
		}}},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, FactReadiness)); err == nil || !strings.Contains(err.Error(), "requires evidence") {
		t.Fatalf("ReadFacts() error = %v", err)
	}
}

func TestFactCapabilityRegistryBoundsProviderDeadline(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	provider := &factProviderStub{
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "status-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Timeout: 10 * time.Millisecond},
		wait:       true,
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{DefaultTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, FactStatus))
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(bundle.Coverage.Sources) != 1 || bundle.Coverage.Sources[0].State != SourceUnavailable || !strings.Contains(bundle.Coverage.Sources[0].Reason, "timed out") {
		t.Fatalf("coverage = %#v", bundle.Coverage)
	}
}

func TestFactCapabilityRegistryRejectsDuplicateCapability(t *testing.T) {
	t.Parallel()

	first := &factProviderStub{capability: FactCapability{Kind: FactStatus, Name: "one", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}}}
	second := &factProviderStub{capability: FactCapability{Kind: FactStatus, Name: "two", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}}}
	if _, err := NewFactCapabilityRegistry([]FactProvider{first, second}, FactRegistryOptions{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
}

func TestFactCapabilityRegistryCapabilitiesAreDeterministicCopies(t *testing.T) {
	t.Parallel()

	status := &factProviderStub{capability: FactCapability{Kind: FactStatus, Name: "status", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, AllowedParameters: []string{"window_days"}}}
	readiness := &factProviderStub{capability: FactCapability{Kind: FactReadiness, Name: "readiness", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}}}
	registry, err := NewFactCapabilityRegistry([]FactProvider{status, readiness}, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := registry.Capabilities()
	if got := []FactKind{capabilities[0].Kind, capabilities[1].Kind}; !reflect.DeepEqual(got, []FactKind{FactStatus, FactReadiness}) {
		t.Fatalf("capability kinds = %#v", got)
	}
	capabilities[0].AllowedParameters[0] = "mutated"
	if registry.Capabilities()[0].AllowedParameters[0] != "window_days" {
		t.Fatal("Capabilities() leaked mutable registry state")
	}
}

func canonicalFactRequest(project SubjectRef, kinds ...FactKind) CanonicalFactRequest {
	requirements := make([]FactRequirement, 0, len(kinds))
	for _, kind := range kinds {
		requirements = append(requirements, FactRequirement{Kind: kind, Parameters: map[string]string{}})
	}
	return CanonicalFactRequest{
		Question: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status_and_drivers", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: requirements},
		Subjects: []SubjectRef{project}, Requirements: requirements,
	}
}

var _ FactProvider = (*factProviderStub)(nil)
var _ CanonicalFactReader = (*FactCapabilityRegistry)(nil)
var _ = errors.Is

// TestFactCapabilityRegistryCapsTotalFactsAcrossProviders is the
// registry-level half of the H7 fix (Codex adversarial review,
// CHAOS-3755). Each provider bounds its own query, but nothing bounded the
// SUM across providers: a request may name up to 64 fact kinds, so even
// perfectly well-behaved providers could together hand the model an
// unbounded bundle. The cap is enforced at the merge point every provider
// result passes through, so it also holds for a provider that has no query
// limit of its own.
func TestFactCapabilityRegistryCapsTotalFactsAcrossProviders(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	// Two providers, each returning well over half the cap, so neither
	// alone exceeds it but together they would.
	const perProvider = maxCanonicalFactsPerBundle*2/3 + 10
	kinds := []FactKind{FactStatus, FactReadiness}
	providers := make([]FactProvider, 0, len(kinds))
	for _, kind := range kinds {
		providers = append(providers, &factProviderStub{
			capability: FactCapability{Kind: kind, Name: string(kind), Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}},
			result:     FactProviderResult{Facts: manyFacts(kind, project, perProvider), State: SourceAvailable, Version: "v1"},
		})
	}
	registry, err := NewFactCapabilityRegistry(providers, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}

	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, kinds...))
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(bundle.Facts) > maxCanonicalFactsPerBundle {
		t.Fatalf("len(bundle.Facts) = %d, want <= %d", len(bundle.Facts), maxCanonicalFactsPerBundle)
	}
	// Truncation must be visible outward, not silent: a caller and the
	// model both need to know the fact set is incomplete.
	if !bundle.Coverage.Partial {
		t.Fatal("bundle.Coverage.Partial = false, want a capped bundle to report itself as partial")
	}
	truncated := false
	for _, source := range bundle.Coverage.Sources {
		if source.State == SourceTruncated {
			truncated = true
		}
	}
	if !truncated {
		t.Fatalf("no coverage source reported %q, want the capped provider marked truncated: %#v", SourceTruncated, bundle.Coverage.Sources)
	}
}

// TestFactCapabilityRegistryDoesNotCapOrdinaryBundles is the over-blocking
// guard: the cap is a backstop against pathological fanout, so a normal
// multi-kind investigation must pass through untouched and unmarked.
func TestFactCapabilityRegistryDoesNotCapOrdinaryBundles(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	kinds := []FactKind{FactStatus, FactReadiness}
	providers := make([]FactProvider, 0, len(kinds))
	for _, kind := range kinds {
		providers = append(providers, &factProviderStub{
			capability: FactCapability{Kind: kind, Name: string(kind), Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}},
			result:     FactProviderResult{Facts: manyFacts(kind, project, 3), State: SourceAvailable, Version: "v1"},
		})
	}
	registry, err := NewFactCapabilityRegistry(providers, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}

	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, kinds...))
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(bundle.Facts) != 6 {
		t.Fatalf("len(bundle.Facts) = %d, want 6", len(bundle.Facts))
	}
	if bundle.Coverage.Partial {
		t.Fatal("bundle.Coverage.Partial = true, want an ordinary bundle to be complete")
	}
}

// manyFacts builds count minimal valid CanonicalFacts of one kind about
// one subject, standing in for a pathological provider fanout.
func manyFacts(kind FactKind, subject SubjectRef, count int) []CanonicalFact {
	facts := make([]CanonicalFact, 0, count)
	value := "open"
	for i := 0; i < count; i++ {
		facts = append(facts, CanonicalFact{
			Kind: kind, Subject: subject, Fields: map[string]FactValue{"state": {String: &value}},
			SourceState: SourceAvailable, Source: "test", SourceVersion: "v1",
		})
	}
	return facts
}

// grainProviderStub builds a provider returning one fact at a declared
// state and grain, using this file's existing stub.
func grainProviderStub(kind FactKind, subject SubjectRef, state SourceState, grain TemporalGrain) *factProviderStub {
	observed := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return &factProviderStub{
		capability: FactCapability{Kind: kind, Name: "ops-" + string(kind), Version: "v1", SupportedSubjectKinds: []SubjectKind{subject.Kind}, RequiresEvidence: true},
		result: FactProviderResult{
			State: state, ObservedAt: &observed, Version: "v1", Grain: grain,
			Reason: "seeded for the grain-composition test",
			Facts: []CanonicalFact{{
				Kind: kind, Subject: subject, Fields: map[string]FactValue{"value": StringFactValue("x")},
				ObservedAt: &observed, EvidenceRefIDs: []string{"evidence_grain_1234"}, SourceState: state,
			}},
		},
	}
}

// TestF3_TruncatedProviderStillContributesItsGrain is CHAOS-3781 round-2
// F3, red-green.
//
// The bundle RETAINS facts from a truncated (and from a fact-bearing
// stale) provider, but the grain composition only counted
// State == SourceAvailable. A day-grain provider that was truncated
// therefore contributed its FACTS to the answer while its GRAIN was
// dropped -- so an answer built from an instant-grain provider plus a
// truncated daily rollup composed to instant, overstating the precision
// of the very data it was built from.
//
// "Contributing" now means facts retained, from the same predicate the
// retention branch uses, so the two cannot drift apart again.
func TestF3_TruncatedProviderStillContributesItsGrain(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	for _, testCase := range []struct {
		name  string
		state SourceState
	}{
		{"available", SourceAvailable},
		// The two states that KEEP their facts must also keep their
		// grain -- this pair is the regression.
		{"truncated", SourceTruncated},
		{"stale", SourceStale},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			exact := grainProviderStub(FactStatus, project, SourceAvailable, GrainInstant)
			daily := grainProviderStub(FactReadiness, project, testCase.state, GrainDay)
			registry, err := NewFactCapabilityRegistry([]FactProvider{exact, daily}, FactRegistryOptions{})
			if err != nil {
				t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
			}
			bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, CanonicalFactRequest{
				Question: InterpretedQuestion{
					Shape: ShapeSingleSubject, RequestedJudgment: "status",
					TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &asOf},
				},
				Subjects:     []SubjectRef{project},
				Requirements: []FactRequirement{{Kind: FactStatus}, {Kind: FactReadiness}},
			})
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			// Sanity: the day-grain provider's facts really did land in
			// the bundle, or this proves nothing about its grain.
			var sawDaily bool
			for _, fact := range bundle.Facts {
				if fact.Kind == FactReadiness {
					sawDaily = true
				}
			}
			if !sawDaily {
				t.Fatalf("the %s provider contributed no facts; the test cannot show its grain was dropped", testCase.state)
			}
			if bundle.TemporalGrain != GrainDay {
				t.Fatalf("composed grain = %q, want %q: a provider whose facts were kept must contribute its grain too",
					bundle.TemporalGrain, GrainDay)
			}
		})
	}
}

// TestF3_ProviderWithNoRetainedFactsContributesNoGrain is the
// over-blocking guard: a provider whose facts are REJECTED must not
// coarsen the answer either.
func TestF3_ProviderWithNoRetainedFactsContributesNoGrain(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	exact := grainProviderStub(FactStatus, project, SourceAvailable, GrainInstant)
	// not_applicable keeps no facts -- a Tier C provider declining a
	// historical question. It still reports a grain, which must be ignored.
	declining := grainProviderStub(FactReadiness, project, SourceNotApplicable, GrainDay)
	declining.result.Facts = nil

	registry, err := NewFactCapabilityRegistry([]FactProvider{exact, declining}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, CanonicalFactRequest{
		Question: InterpretedQuestion{
			Shape: ShapeSingleSubject, RequestedJudgment: "status",
			TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &asOf},
		},
		Subjects:     []SubjectRef{project},
		Requirements: []FactRequirement{{Kind: FactStatus}, {Kind: FactReadiness}},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if bundle.TemporalGrain != GrainInstant {
		t.Fatalf("composed grain = %q, want %q: a provider that contributed no facts must not coarsen the answer",
			bundle.TemporalGrain, GrainInstant)
	}
}

// TestR4_2_OmissionsSurfaceAsPartialCoverage is round-4 R4-2 at the
// registry boundary: a provider that dropped rows must not produce a
// bundle claiming complete coverage.
//
// The defect shape is "measurement fails toward fine" -- the answer looks
// whole, and the omission is invisible precisely when it matters. The
// registry derives the degradation from the count so no provider can
// report omissions and forget to degrade.
func TestR4_2_OmissionsSurfaceAsPartialCoverage(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	observed := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	omitting := &factProviderStub{
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, RequiresEvidence: true},
		result: FactProviderResult{
			State: SourceAvailable, ObservedAt: &observed, Version: "v1",
			OmittedCount: 2,
			Facts: []CanonicalFact{{
				Kind: FactStatus, Subject: project, Fields: map[string]FactValue{"status": StringFactValue("in_progress")},
				ObservedAt: &observed, EvidenceRefIDs: []string{"evidence_status_1234"}, SourceState: SourceAvailable,
			}},
		},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{omitting}, FactRegistryOptions{})
	if err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, CanonicalFactRequest{
		Question:     InterpretedQuestion{Shape: ShapeSingleSubject, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}},
		Subjects:     []SubjectRef{project},
		Requirements: []FactRequirement{{Kind: FactStatus}},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if !bundle.Coverage.Partial {
		t.Fatal("rows were omitted but coverage reports complete; an answer must never look whole while something was withheld")
	}
	// The surviving fact is still there -- omission degrades, it does not
	// sink the answer (§8.6).
	if len(bundle.Facts) != 1 {
		t.Fatalf("Facts = %#v, want the fact that was fine to survive", bundle.Facts)
	}
	// And the count is legible, not just a boolean.
	var named bool
	for _, reason := range bundle.Coverage.DegradedReasons {
		if strings.Contains(reason, "omitted 2") {
			named = true
		}
	}
	if !named {
		t.Fatalf("DegradedReasons = %#v, want the omission count stated", bundle.Coverage.DegradedReasons)
	}
}
