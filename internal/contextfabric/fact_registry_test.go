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
	}
	readiness := &factProviderStub{
		capability: FactCapability{Kind: FactReadiness, Name: "ops-readiness", Version: "readiness-v3", SupportedSubjectKinds: []SubjectKind{SubjectProject}, RequiresEvidence: true},
		result: FactProviderResult{
			State: SourceAvailable, ObservedAt: &observed, Watermark: "wm-readiness", Version: "readiness-v3",
			Facts: []CanonicalFact{{
				Kind: FactReadiness, Subject: project, Fields: map[string]FactValue{"release_ready": BooleanFactValue(false)},
				ObservedAt: &observed, EvidenceRefIDs: []string{"evidence_readiness_1234"}, SourceState: SourceAvailable,
			}},
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
