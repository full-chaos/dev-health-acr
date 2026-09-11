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
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "status-v2", SupportedSubjectKinds: []SubjectKind{SubjectProject}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
		result: FactProviderResult{
			State: SourceAvailable, ObservedAt: &observed, Watermark: "wm-status", Version: "status-v2",
			Facts: []CanonicalFact{{
				Kind: FactStatus, Subject: project, Fields: map[string]FactValue{"status": StringFactValue("in_progress")},
				ObservedAt: &observed, EvidenceRefIDs: []string{"evidence_status_1234"}, SourceState: SourceAvailable,
			}},
		},
	}
	readiness := &factProviderStub{
		capability: FactCapability{Kind: FactReadiness, Name: "ops-readiness", Version: "readiness-v3", SupportedSubjectKinds: []SubjectKind{SubjectProject}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
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
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "status-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
		result: FactProviderResult{State: SourceAvailable, Version: "status-v1", Facts: []CanonicalFact{{
			Kind: FactStatus, Subject: project, Fields: map[string]FactValue{"status": StringFactValue("in_progress")}, SourceState: SourceAvailable,
		}}},
	}
	readiness := &factProviderStub{
		capability: FactCapability{Kind: FactReadiness, Name: "ops-readiness", Version: "readiness-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
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

// TestFactCapabilityRegistryRejectsParametersOutsideServerOwnedCapability
// covers ReadFacts' pre-pass rejection site (CHAOS-3783/CHAOS-3854).
//
// CHAOS-3854: a disallowed fact_requirements[].parameters key is the
// model's OWN interpretation output being rejected by ACR's OWN fact
// capability registry -- the same business-rule-rejection SHAPE as an
// invalid fact_requirements[].kind, which InterpretedQuestion.Validate
// already classifies as ErrInterpretationRejected (contracts/v1's
// validFactKind check). Before this fix, ReadFacts returned a bare,
// sentinel-less fmt.Errorf here: it matched none of
// internal/api/context_fabric_routes.go's errors.Is branches and fell
// through to the "unclassified" 500 -- exactly the trial's measured
// production symptom (fact_read: parameter "term"/"item_name"/etc. is not
// allowed, surfacing as an opaque internal_error). errors.Is is the
// load-bearing assertion below; the message substring check is secondary.
//
// This test is RED against the pre-fix registry: reverting
// fact_registry.go's ErrInterpretationRejected wrap (keeping this test)
// fails it on the errors.Is check while leaving the substring check green
// -- proving the fix is the classification, not the rejection itself
// (which already existed and this test already covered before CHAOS-3854).
func TestFactCapabilityRegistryRejectsParametersOutsideServerOwnedCapability(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	provider := &factProviderStub{capability: FactCapability{
		Kind: FactMetrics, Name: "ops-metrics", Version: "metrics-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, AllowedParameters: []string{"window_days"}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
	}}
	registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := canonicalFactRequest(project, FactMetrics)
	request.Requirements[0].Parameters = map[string]string{"sql": "select *"}
	_, err = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if !errors.Is(err, ErrInterpretationRejected) {
		t.Fatalf("ReadFacts() error = %v, want it to wrap ErrInterpretationRejected so internal/api/context_fabric_routes.go classifies it as interpretation_rejected (422) instead of falling through to unclassified (500)", err)
	}
}

// TestBuildFactQueryRejectsDisallowedParameterAsInterpretationRejected
// covers buildFactQuery's OWN parameter-allowlist check (CHAOS-3854) --
// the second of the two rejection sites, distinct from ReadFacts' pre-pass
// above. buildFactQuery is called directly (bypassing ReadFacts) because,
// per this function's own doc comment, the pre-pass now makes this branch
// unreachable in the ordinary ReadFacts path for every REGISTERED
// capability (the same way the subject-kind check just above it already
// is) -- it survives as the defense-in-depth invariant for a planned read
// that reaches buildFactQuery with a parameter the pre-pass did not (or,
// after some future change, no longer does) catch. It must classify
// identically to the pre-pass site: wrapping the same ErrInterpretationRejected
// sentinel, not a bespoke or absent one.
func TestBuildFactQueryRejectsDisallowedParameterAsInterpretationRejected(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	capability := FactCapability{
		Kind: FactMetrics, Name: "ops-metrics", Version: "metrics-v1",
		SupportedSubjectKinds: []SubjectKind{SubjectProject}, AllowedParameters: []string{"window_days"}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
	}
	requirement := FactRequirement{Kind: FactMetrics, Parameters: map[string]string{"sql": "select *"}}
	request := canonicalFactRequest(project, FactMetrics)
	allowed := investigationScopeSubjectSet(request)

	_, err := buildFactQuery(request, requirement, capability, allowed, []SubjectRef{project})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("buildFactQuery() error = %v", err)
	}
	if !errors.Is(err, ErrInterpretationRejected) {
		t.Fatalf("buildFactQuery() error = %v, want it to wrap ErrInterpretationRejected", err)
	}
}

func TestFactCapabilityRegistryRequiresEvidenceForObservedCapability(t *testing.T) {
	t.Parallel()

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	provider := &factProviderStub{
		capability: FactCapability{Kind: FactReadiness, Name: "ops-readiness", Version: "readiness-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
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
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "status-v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Timeout: 10 * time.Millisecond, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
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

	first := &factProviderStub{capability: FactCapability{Kind: FactStatus, Name: "one", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}}}
	second := &factProviderStub{capability: FactCapability{Kind: FactStatus, Name: "two", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}}}
	if _, err := NewFactCapabilityRegistry([]FactProvider{first, second}, FactRegistryOptions{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
}

func TestFactCapabilityRegistryCapabilitiesAreDeterministicCopies(t *testing.T) {
	t.Parallel()

	status := &factProviderStub{capability: FactCapability{Kind: FactStatus, Name: "status", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, AllowedParameters: []string{"window_days"}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}}}
	readiness := &factProviderStub{capability: FactCapability{Kind: FactReadiness, Name: "readiness", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}}}
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

// TestChaos5547_ProviderCapabilityDeclarationsAreCopiedNotAliasedAtRegistration
// pins the r3 class-sweep finding, in its FULL, corrected form (codex r3
// review, confirmed real P1): the first pass at this fix copied only
// Tables/Obligations at registration and left SupportedSubjectKinds,
// AllowedParameters and SubjectRoles aliased -- the same NewFactCapabilityRegistry
// bug, unfinished. Reproduced live: mutating a registered provider's own
// AllowedParameters slice after registration made ReadFacts reject a
// parameter its capability still declared allowed.
//
// One subtest per FactCapability field that is a slice or map (every field
// enumerated from the struct definition that is NOT a plain scalar: Kind,
// Name, Version, RequiresEvidence, Timeout, Dimension and EstimatedItems
// are values, not references, and copy for free on the struct assignment
// already made at registration).
//
// Two fields have a LIVE ReadFacts consumer today (capabilityIndex feeds
// ReadFacts's own pre-pass and buildFactQuery, not classifyUnavailable --
// classifyUnavailable's own capabilities argument comes from the already-
// defended Capabilities(), via DeriveRequirements) and are driven through
// ReadFacts itself, never the accessor: SupportedSubjectKinds,
// AllowedParameters. The other three have no live ReadFacts (or any)
// consumer as of this diff -- verified by
// `grep -rn "\.Tables\b|\.Obligations\b|\.SubjectRoles\b" internal/contextfabric/*.go`
// outside fact_registry.go/its own tests and DeriveRequirements/Capabilities()
// -- so their only observable surface is Capabilities(), and that is what
// their subtests assert against; a future consumer of any of these three
// gets exactly the same protection this fix already put in place.
func TestChaos5547_ProviderCapabilityDeclarationsAreCopiedNotAliasedAtRegistration(t *testing.T) {
	t.Parallel()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	newProvider := func() *factProviderStub {
		return &factProviderStub{
			capability: FactCapability{
				Kind: FactStatus, Name: "status", Version: "v1",
				SupportedSubjectKinds: []SubjectKind{SubjectProject},
				AllowedParameters:     []string{"window_days"},
				Dimension:             HealthDimensionExecutionCompletion,
				SubjectRoles:          []FactRole{FactRoleSubject},
				Tables:                map[SubjectKind][]FactTableShape{SubjectProject: {FactTableTimeSeries}},
				Obligations:           map[SubjectKind][]AnswerObligation{SubjectProject: {ObligationState}},
				ObservationKey:        map[SubjectKind][]ObservationKey{SubjectProject: {"project_status_rollup"}},
			},
			result: FactProviderResult{State: SourceAvailable},
		}
	}
	rows := []struct {
		name   string
		mutate func(p *factProviderStub)
		check  func(t *testing.T, registry *FactCapabilityRegistry)
	}{
		{
			// Driven through ReadFacts: mutate the provider's own
			// SupportedSubjectKinds to a kind the request does NOT use;
			// if aliased, the request's original, still-registered subject
			// kind would stop being served.
			name:   "SupportedSubjectKinds",
			mutate: func(p *factProviderStub) { p.capability.SupportedSubjectKinds[0] = SubjectRepository },
			check: func(t *testing.T, registry *FactCapabilityRegistry) {
				bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(project, FactStatus))
				if err != nil {
					t.Fatalf("ReadFacts() error = %v, want the ORIGINAL SubjectProject support unaffected by the provider's post-registration mutation", err)
				}
				if len(bundle.Coverage.Sources) != 1 || bundle.Coverage.Sources[0].State != SourceAvailable {
					t.Fatalf("coverage = %#v, want the capability still serving the originally-registered subject kind", bundle.Coverage)
				}
			},
		},
		{
			// Driven through ReadFacts, exactly the r3 reviewer's shape.
			name:   "AllowedParameters",
			mutate: func(p *factProviderStub) { p.capability.AllowedParameters[0] = "mutated_out" },
			check: func(t *testing.T, registry *FactCapabilityRegistry) {
				request := canonicalFactRequest(project, FactStatus)
				request.Requirements[0].Parameters = map[string]string{"window_days": "30"}
				if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
					t.Fatalf("ReadFacts() error = %v, want the ORIGINAL allowed parameter still honoured after the provider mutated its own slice", err)
				}
			},
		},
		{
			// No live consumer of SubjectRoles exists (see doc comment
			// above) -- Capabilities() is the only observable surface.
			name:   "SubjectRoles",
			mutate: func(p *factProviderStub) { p.capability.SubjectRoles[0] = FactRoleMember },
			check: func(t *testing.T, registry *FactCapabilityRegistry) {
				if got := registry.Capabilities()[0].SubjectRoles[0]; got != FactRoleSubject {
					t.Fatalf("SubjectRoles = %v after the provider mutated its own slice post-registration, want the ORIGINAL FactRoleSubject unaffected", got)
				}
			},
		},
		{
			// ReadFacts never reads capability.Tables (grep-verified);
			// Capabilities()/DeriveRequirements is the only live consumer.
			name:   "Tables",
			mutate: func(p *factProviderStub) { p.capability.Tables[SubjectProject][0] = FactTableBreakdown },
			check: func(t *testing.T, registry *FactCapabilityRegistry) {
				if got := registry.Capabilities()[0].Tables[SubjectProject][0]; got != FactTableTimeSeries {
					t.Fatalf("Tables = %v after the provider mutated its own map post-registration, want the ORIGINAL time_series unaffected", got)
				}
			},
		},
		{
			name:   "Obligations",
			mutate: func(p *factProviderStub) { p.capability.Obligations[SubjectProject][0] = ObligationReadiness },
			check: func(t *testing.T, registry *FactCapabilityRegistry) {
				if got := registry.Capabilities()[0].Obligations[SubjectProject][0]; got != ObligationState {
					t.Fatalf("Obligations = %v after the provider mutated its own map post-registration, want the ORIGINAL state unaffected", got)
				}
			},
		},
		{
			// Driven through ObservationKeyAssignment, the live consumer every
			// threshold comparison reads -- the observation-key map is the same
			// shape as Tables and Obligations and needs the same copy.
			name: "ObservationKey",
			mutate: func(p *factProviderStub) {
				p.capability.ObservationKey[SubjectProject][0] = "mutated_out"
				p.capability.ObservationKey[SubjectTeam] = []ObservationKey{"added_after_registration"}
			},
			check: func(t *testing.T, registry *FactCapabilityRegistry) {
				keys := registry.ObservationKeyAssignment()[FactStatus]
				if got := keys[SubjectProject]; len(got) != 1 || got[0] != "project_status_rollup" {
					t.Fatalf("ObservationKey[project] = %v after the provider mutated its own map post-registration, want the ORIGINAL [project_status_rollup]", got)
				}
				if got, present := keys[SubjectTeam]; present {
					t.Fatalf("ObservationKey[team] = %v appeared after registration; the registered declaration must not grow", got)
				}
			},
		},
	}
	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			provider := newProvider()
			registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
			if err != nil {
				t.Fatalf("NewFactCapabilityRegistry: %v", err)
			}
			row.mutate(provider)
			row.check(t, registry)
		})
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
			capability: FactCapability{Kind: kind, Name: string(kind), Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
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
			capability: FactCapability{Kind: kind, Name: string(kind), Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
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
		capability: FactCapability{Kind: kind, Name: "ops-" + string(kind), Version: "v1", SupportedSubjectKinds: []SubjectKind{subject.Kind}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
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
		capability: FactCapability{Kind: FactStatus, Name: "ops-status", Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, RequiresEvidence: true, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}},
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
