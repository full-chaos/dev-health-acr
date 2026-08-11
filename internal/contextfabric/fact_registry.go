package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const canonicalFactRegistryVersion = "context-fabric-facts.v1"

type FactCapability struct {
	Kind                  FactKind
	Name                  string
	Version               string
	SupportedSubjectKinds []SubjectKind
	AllowedParameters     []string
	RequiresEvidence      bool
	Timeout               time.Duration
}

func (c FactCapability) Validate() error {
	if !validFactCapabilityKind(c.Kind) || strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Name) != c.Name || strings.TrimSpace(c.Version) == "" || strings.TrimSpace(c.Version) != c.Version {
		return fmt.Errorf("fact capability identity is invalid")
	}
	if len(c.SupportedSubjectKinds) == 0 || len(c.SupportedSubjectKinds) > 32 || !uniqueSubjectKinds(c.SupportedSubjectKinds) {
		return fmt.Errorf("fact capability subject kinds are invalid")
	}
	if len(c.AllowedParameters) > 32 || !uniqueBoundedParameters(c.AllowedParameters) {
		return fmt.Errorf("fact capability parameters are invalid")
	}
	if c.Timeout < 0 {
		return fmt.Errorf("fact capability timeout must not be negative")
	}
	return nil
}

type FactQuery struct {
	Kind       FactKind
	Subjects   []SubjectRef
	Cohort     *Cohort
	Time       TimeContext
	Parameters map[string]string
}

type FactProviderResult struct {
	Facts      []CanonicalFact
	State      SourceState
	ObservedAt *time.Time
	Watermark  string
	Reason     string
	Version    string
	Truncated  bool
}

type FactProvider interface {
	Capability() FactCapability
	ReadFacts(context.Context, storage.Principal, FactQuery) (FactProviderResult, error)
}

type FactReadFailure struct {
	State  SourceState
	Reason string
}

func (e *FactReadFailure) Error() string {
	if e == nil || strings.TrimSpace(e.Reason) == "" {
		return "canonical fact read failed"
	}
	return e.Reason
}

type FactRegistryOptions struct {
	DefaultTimeout time.Duration
	Now            func() time.Time
}

type registeredFactProvider struct {
	capability FactCapability
	provider   FactProvider
}

type FactCapabilityRegistry struct {
	providers      map[FactKind]registeredFactProvider
	defaultTimeout time.Duration
	now            func() time.Time
}

func NewFactCapabilityRegistry(providers []FactProvider, options FactRegistryOptions) (*FactCapabilityRegistry, error) {
	if options.DefaultTimeout <= 0 {
		options.DefaultTimeout = 5 * time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	registry := &FactCapabilityRegistry{
		providers:      make(map[FactKind]registeredFactProvider, len(providers)),
		defaultTimeout: options.DefaultTimeout,
		now:            options.Now,
	}
	for _, provider := range providers {
		if provider == nil {
			return nil, errors.New("fact provider must not be nil")
		}
		capability := provider.Capability()
		if err := capability.Validate(); err != nil {
			return nil, fmt.Errorf("fact capability: %w", err)
		}
		if _, exists := registry.providers[capability.Kind]; exists {
			return nil, fmt.Errorf("duplicate fact capability %q", capability.Kind)
		}
		registry.providers[capability.Kind] = registeredFactProvider{capability: capability, provider: provider}
	}
	return registry, nil
}

func (r *FactCapabilityRegistry) Capabilities() []FactCapability {
	if r == nil {
		return nil
	}
	capabilities := make([]FactCapability, 0, len(r.providers))
	for _, registered := range r.providers {
		capability := registered.capability
		capability.SupportedSubjectKinds = append([]SubjectKind(nil), capability.SupportedSubjectKinds...)
		capability.AllowedParameters = append([]string(nil), capability.AllowedParameters...)
		capabilities = append(capabilities, capability)
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Kind < capabilities[j].Kind })
	return capabilities
}

func (r *FactCapabilityRegistry) ReadFacts(ctx context.Context, principal storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
	if r == nil {
		return CanonicalFactBundle{}, errors.New("fact capability registry is required")
	}
	if strings.TrimSpace(principal.OrgID) == "" {
		return CanonicalFactBundle{}, errors.New("authenticated organization is required")
	}
	if err := ctx.Err(); err != nil {
		return CanonicalFactBundle{}, err
	}
	if err := validateCanonicalFactRequest(request); err != nil {
		return CanonicalFactBundle{}, err
	}

	bundle := CanonicalFactBundle{
		Facts:      []CanonicalFact{},
		Coverage:   Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		Version:    canonicalFactRegistryVersion,
		Versions:   map[FactKind]string{},
		Watermarks: map[FactKind]string{},
	}
	allowedSubjects := canonicalFactSubjectSet(request.Subjects, request.Cohort)
	for _, requirement := range request.Requirements {
		registered, ok := r.providers[requirement.Kind]
		if !ok {
			appendFactCoverage(&bundle, requirement.Kind, SourceUnconfigured, nil, "", "canonical fact capability is not configured")
			continue
		}
		query, err := buildFactQuery(request, requirement, registered.capability, allowedSubjects)
		if err != nil {
			return CanonicalFactBundle{}, fmt.Errorf("fact capability %s: %w", requirement.Kind, err)
		}
		result, err := r.readProvider(ctx, principal, registered, query)
		if err != nil {
			if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
				return CanonicalFactBundle{}, context.Canceled
			}
			state, reason := classifyFactReadError(err)
			appendFactCoverage(&bundle, requirement.Kind, state, nil, "", reason)
			continue
		}
		if err := mergeFactProviderResult(&bundle, registered.capability, query, result, allowedSubjects); err != nil {
			return CanonicalFactBundle{}, fmt.Errorf("fact capability %s: %w", requirement.Kind, err)
		}
	}
	sortCanonicalFacts(bundle.Facts)
	return bundle, nil
}

func (r *FactCapabilityRegistry) readProvider(ctx context.Context, principal storage.Principal, registered registeredFactProvider, query FactQuery) (FactProviderResult, error) {
	timeout := registered.capability.Timeout
	if timeout <= 0 {
		timeout = r.defaultTimeout
	}
	providerCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := registered.provider.ReadFacts(providerCtx, principal, query)
	if err != nil {
		if errors.Is(providerCtx.Err(), context.DeadlineExceeded) {
			return FactProviderResult{}, &FactReadFailure{State: SourceUnavailable, Reason: "canonical fact capability timed out"}
		}
		return FactProviderResult{}, err
	}
	return result, nil
}

func buildFactQuery(request CanonicalFactRequest, requirement FactRequirement, capability FactCapability, allowed map[string]SubjectRef) (FactQuery, error) {
	subjects := requirement.Subjects
	if len(subjects) == 0 {
		subjects = request.Subjects
	}
	if len(subjects) == 0 && request.Cohort != nil {
		subjects = make([]SubjectRef, 0, len(request.Cohort.Members))
		for _, member := range request.Cohort.Members {
			subjects = append(subjects, member.Subject)
		}
	}
	if len(subjects) == 0 {
		return FactQuery{}, errors.New("fact capability requires at least one discovered subject")
	}
	seen := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		key := canonicalFactSubjectKey(subject)
		if _, ok := allowed[key]; !ok {
			return FactQuery{}, fmt.Errorf("subject %q is outside the discovered investigation set", subject.CanonicalID)
		}
		if !supportsSubjectKind(capability.SupportedSubjectKinds, subject.Kind) {
			return FactQuery{}, fmt.Errorf("subject kind %q is not supported", subject.Kind)
		}
		if _, exists := seen[key]; exists {
			return FactQuery{}, errors.New("fact query subjects must be unique")
		}
		seen[key] = struct{}{}
	}
	for key := range requirement.Parameters {
		if !containsString(capability.AllowedParameters, key) {
			return FactQuery{}, fmt.Errorf("parameter %q is not allowed", key)
		}
	}
	parameters := make(map[string]string, len(requirement.Parameters))
	for key, value := range requirement.Parameters {
		parameters[key] = value
	}
	return FactQuery{Kind: requirement.Kind, Subjects: append([]SubjectRef(nil), subjects...), Cohort: request.Cohort, Time: request.Question.TimeContext, Parameters: parameters}, nil
}

func mergeFactProviderResult(bundle *CanonicalFactBundle, capability FactCapability, query FactQuery, result FactProviderResult, allowed map[string]SubjectRef) error {
	if !validFactSourceState(result.State) {
		return fmt.Errorf("provider returned invalid source state %q", result.State)
	}
	if result.ObservedAt != nil && result.ObservedAt.IsZero() {
		return errors.New("provider returned an invalid observed timestamp")
	}
	if strings.TrimSpace(result.Version) == "" {
		result.Version = capability.Version
	}
	if strings.TrimSpace(result.Version) == "" || strings.TrimSpace(result.Version) != result.Version {
		return errors.New("provider returned an invalid version")
	}
	if result.Truncated {
		result.State = SourceTruncated
		if strings.TrimSpace(result.Reason) == "" {
			result.Reason = "canonical fact result was truncated"
		}
	}
	if stateRejectsFacts(result.State) && len(result.Facts) > 0 {
		return fmt.Errorf("source state %q cannot return facts", result.State)
	}
	for index := range result.Facts {
		fact := result.Facts[index]
		if fact.Kind != capability.Kind || fact.Kind != query.Kind {
			return fmt.Errorf("provider returned fact kind %q for capability %q", fact.Kind, capability.Kind)
		}
		if _, ok := allowed[canonicalFactSubjectKey(fact.Subject)]; !ok {
			return fmt.Errorf("provider returned subject %q outside the investigation set", fact.Subject.CanonicalID)
		}
		if fact.Source == "" {
			fact.Source = capability.Name
		}
		if fact.SourceVersion == "" {
			fact.SourceVersion = result.Version
		}
		if fact.SourceState == "" {
			fact.SourceState = result.State
		}
		if err := fact.Validate(capability.RequiresEvidence && (fact.SourceState == SourceAvailable || fact.SourceState == SourceStale)); err != nil {
			return fmt.Errorf("provider fact: %w", err)
		}
		bundle.Facts = append(bundle.Facts, fact)
	}
	bundle.Versions[capability.Kind] = result.Version
	if result.Watermark != "" {
		bundle.Watermarks[capability.Kind] = result.Watermark
	}
	appendFactCoverage(bundle, capability.Kind, result.State, result.ObservedAt, result.Watermark, result.Reason)
	return nil
}

func appendFactCoverage(bundle *CanonicalFactBundle, kind FactKind, state SourceState, observedAt *time.Time, watermark, reason string) {
	if strings.TrimSpace(reason) == "" && state != SourceAvailable {
		reason = "canonical fact capability returned " + string(state)
	}
	bundle.Coverage.Sources = append(bundle.Coverage.Sources, SourceObservation{
		Source: "canonical_fact:" + string(kind), State: state, ObservedAt: observedAt, Watermark: watermark, Reason: reason,
	})
	if factStateDegrades(state) {
		bundle.Coverage.Partial = true
		bundle.Coverage.DegradedReasons = append(bundle.Coverage.DegradedReasons, string(kind)+": "+reason)
	}
}

func validateCanonicalFactRequest(request CanonicalFactRequest) error {
	if len(request.Requirements) == 0 || len(request.Requirements) > 64 {
		return errors.New("canonical fact request requires bounded fact requirements")
	}
	allowed := canonicalFactSubjectSet(request.Subjects, request.Cohort)
	if len(allowed) == 0 {
		return errors.New("canonical fact request requires discovered subjects or a cohort")
	}
	seenKinds := make(map[FactKind]struct{}, len(request.Requirements))
	for _, requirement := range request.Requirements {
		if !validFactCapabilityKind(requirement.Kind) {
			return fmt.Errorf("unsupported fact kind %q", requirement.Kind)
		}
		if _, exists := seenKinds[requirement.Kind]; exists {
			return fmt.Errorf("duplicate fact requirement %q", requirement.Kind)
		}
		seenKinds[requirement.Kind] = struct{}{}
	}
	return nil
}

func canonicalFactSubjectSet(subjects []SubjectRef, cohort *Cohort) map[string]SubjectRef {
	result := make(map[string]SubjectRef, len(subjects))
	for _, subject := range subjects {
		result[canonicalFactSubjectKey(subject)] = subject
	}
	if cohort != nil {
		for _, member := range cohort.Members {
			result[canonicalFactSubjectKey(member.Subject)] = member.Subject
		}
	}
	return result
}

func canonicalFactSubjectKey(subject SubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}

func classifyFactReadError(err error) (SourceState, string) {
	var failure *FactReadFailure
	if errors.As(err, &failure) && validFactSourceState(failure.State) {
		return failure.State, failure.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return SourceUnavailable, "canonical fact capability timed out"
	}
	return SourceUnavailable, "canonical fact capability is unavailable"
}

func sortCanonicalFacts(facts []CanonicalFact) {
	sort.SliceStable(facts, func(i, j int) bool {
		if facts[i].Kind != facts[j].Kind {
			return facts[i].Kind < facts[j].Kind
		}
		if facts[i].Subject.Kind != facts[j].Subject.Kind {
			return facts[i].Subject.Kind < facts[j].Subject.Kind
		}
		if facts[i].Subject.CanonicalID != facts[j].Subject.CanonicalID {
			return facts[i].Subject.CanonicalID < facts[j].Subject.CanonicalID
		}
		return facts[i].Source < facts[j].Source
	})
}

func stateRejectsFacts(state SourceState) bool {
	switch state {
	case SourceUnavailable, SourceUnconfigured, SourceUnauthorized, SourceNoData, SourceConflicted, SourceNotApplicable:
		return true
	default:
		return false
	}
}

func factStateDegrades(state SourceState) bool {
	switch state {
	case SourceStale, SourceUnavailable, SourceUnconfigured, SourceUnauthorized, SourceTruncated, SourceConflicted:
		return true
	default:
		return false
	}
}

func validFactSourceState(state SourceState) bool {
	switch state {
	case SourceAvailable, SourceStale, SourceUnavailable, SourceUnconfigured, SourceUnauthorized, SourceNoData, SourceTruncated, SourceConflicted, SourceNotApplicable:
		return true
	default:
		return false
	}
}

func validFactCapabilityKind(kind FactKind) bool {
	switch kind {
	case FactIdentity, FactMembership, FactStatus, FactActualCompletion, FactWork, FactBlockers, FactRequiredChildren, FactPullRequests, FactReviews, FactContinuousIntegration, FactDeployments, FactIncidents, FactMetrics, FactHealth, FactWorkload, FactInvestment, FactReadiness, FactOperationalDeficiencies, FactSourceHealth, FactEvidence:
		return true
	default:
		return false
	}
}

func uniqueSubjectKinds(values []SubjectKind) bool {
	seen := make(map[SubjectKind]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func uniqueBoundedParameters(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != value || value == "" || len(value) > 128 {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func supportsSubjectKind(values []SubjectKind, target SubjectKind) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
