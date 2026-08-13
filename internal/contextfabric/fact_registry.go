package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const canonicalFactRegistryVersion = "context-fabric-facts.v1"

// maxCanonicalFactsPerBundle bounds how many CanonicalFacts one
// CanonicalFactBundle may carry, across ALL providers that contributed to
// it (CHAOS-3755 adversarial review finding H7). The bundle becomes model
// input, so an unbounded one is both a cost and a prompt-size hazard.
//
// A request may name up to 64 fact requirements, so a purely per-provider
// limit still multiplies out; this is the ceiling on the total. It sits
// deliberately above any single provider's per-query limit so an ordinary
// multi-kind investigation is never truncated by it -- this is a backstop
// against pathological fanout, not a routine budget.
const maxCanonicalFactsPerBundle = 2000

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
	// Grain is the temporal precision THIS provider answered at
	// (CHAOS-3781 round-1 F1). It exists because the providers are not
	// uniform: a daily rollup can only speak for a day, while a provider
	// deriving state from an immutable event timestamp answers at the
	// exact requested instant.
	//
	// Composing one answer-level grain from a single assumption was
	// observably wrong -- a pull request merged at 14:00Z, serialized
	// under a day grain, reads as though the answer only knew about
	// midnight. Each provider now reports its own, and the engine takes
	// the COARSEST among those that actually contributed.
	//
	// Empty means the provider did not answer on a temporal axis (the
	// current axis, or a degradation), and contributes nothing to the
	// composed grain.
	Grain TemporalGrain
	// OmittedCount is how many rows the provider DROPPED rather than
	// reported -- today, rows whose source value could not be represented
	// (CHAOS-3781 round-4 R4-2).
	//
	// It exists because omitting a row while reporting complete coverage
	// is a measurement that fails toward "fine": the answer looks whole,
	// the caller has no way to know something was withheld, and the
	// omission is invisible precisely when it matters. A count above zero
	// makes the result Truncated, which the existing vocabulary already
	// defines as "fewer rows than exist" and which degrades coverage to
	// partial while KEEPING the rows that were fine.
	OmittedCount int
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
	sort.Slice(capabilities, func(i, j int) bool { return factKindOrder(capabilities[i].Kind) < factKindOrder(capabilities[j].Kind) })
	return capabilities
}

// capabilityIndex exposes the registered capabilities to the fact planner
// keyed by kind. It returns the declared FactCapability values directly
// rather than the registeredFactProvider wrappers, so the planner can only
// read what a provider DECLARES about itself and can never reach the
// provider to call it -- planning stays pure by construction.
func (r *FactCapabilityRegistry) capabilityIndex() map[FactKind]FactCapability {
	index := make(map[FactKind]FactCapability, len(r.providers))
	for kind, registered := range r.providers {
		index[kind] = registered.capability
	}
	return index
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
	allowedSubjects := investigationScopeSubjectSet(request)
	// CHAOS-3783: decide the whole fan-out up front, before any provider is
	// touched, so a capability that provably cannot contribute is never
	// queried and never silently missing. See planFactReads for the rule and
	// for why an empty subject-kind intersection is a proof rather than a
	// guess.
	// Requirements are unique by kind (validateCanonicalFactRequest), so
	// this index is a lossless way to get back from a plan entry -- which
	// deliberately carries only a kind, never the request's prose -- to the
	// requirement's provider Parameters.
	// Self-found, same class as codex round-8 F1: the parameter allowlist
	// was the LAST buildFactQuery check reachable only when a capability is
	// not pruned. A requirement carrying a disallowed parameter key whose
	// capability then prunes never reached buildFactQuery, so an invalid
	// request returned success with pruned coverage.
	//
	// It runs as a pre-pass here, before the plan loop, rather than inside
	// it: validity must be decided before ANY short-circuit, and a check
	// placed ahead of the loop cannot be skipped by a future branch added
	// inside it. It lives in ReadFacts rather than
	// validateCanonicalFactRequest because AllowedParameters is declared by
	// the capability, which only the registry knows.
	//
	// An unregistered kind is deliberately not validated here: there is no
	// capability to declare an allowlist against, and that kind already
	// degrades to SourceUnconfigured without ever building a query. This
	// check governs exactly the requests buildFactQuery would have judged.
	capabilities := r.capabilityIndex()
	for _, requirement := range request.Requirements {
		capability, registered := capabilities[requirement.Kind]
		if !registered {
			continue
		}
		for key := range requirement.Parameters {
			if !containsString(capability.AllowedParameters, key) {
				return CanonicalFactBundle{}, fmt.Errorf("fact capability %s: parameter %q is not allowed", requirement.Kind, key)
			}
		}
	}
	requirementsByKind := make(map[FactKind]FactRequirement, len(request.Requirements))
	for _, requirement := range request.Requirements {
		requirementsByKind[requirement.Kind] = requirement
	}
	for _, planned := range planFactReads(newFactPlanInput(request), capabilities) {
		requirement := requirementsByKind[planned.Kind]
		registered, ok := r.providers[planned.Kind]
		if !ok {
			appendFactCoverage(&bundle, planned.Kind, SourceUnconfigured, nil, "", "canonical fact capability is not configured")
			continue
		}
		if planned.Pruned {
			// The whole point of the issue's "record every pruning decision"
			// constraint: a pruned capability is visible in Coverage with a
			// reason, exactly like one that ran and failed. It contributes
			// no facts and -- unlike every degrading state -- does not mark
			// the bundle partial, because nothing is actually missing from
			// the answer. factStateDegrades(SourcePruned) is false for that
			// reason.
			appendFactCoverage(&bundle, planned.Kind, SourcePruned, nil, "", planned.Reason)
			continue
		}
		query, err := buildFactQuery(request, requirement, registered.capability, allowedSubjects, planned.Subjects)
		if err != nil {
			return CanonicalFactBundle{}, fmt.Errorf("fact capability %s: %w", planned.Kind, err)
		}
		result, err := r.readProvider(ctx, principal, registered, query)
		if err != nil {
			if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
				return CanonicalFactBundle{}, context.Canceled
			}
			state, reason := classifyFactReadError(err)
			// Codex round-1 F2: a narrowed capability that then FAILS still
			// had its subject list cut, and the observation has to say both
			// things. Recording only the failure would silently drop the
			// record that subjects were dropped -- the unexplained absence
			// the empty-states rule forbids.
			appendFactCoverage(&bundle, planned.Kind, state, nil, "", withNarrowingNote(planned, reason))
			continue
		}
		// Coverage source names must be unique
		// (ContextFabricCoverage.Validate), so the subjects this capability
		// could not be asked about cannot get an observation of their own --
		// the narrowing rides on the capability's own observation instead.
		// Prefixed, never replacing: whatever the provider said about its
		// own read still has to survive.
		result.Reason = withNarrowingNote(planned, result.Reason)
		if err := mergeFactProviderResult(&bundle, registered.capability, query, result, allowedSubjects); err != nil {
			return CanonicalFactBundle{}, fmt.Errorf("fact capability %s: %w", planned.Kind, err)
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

// buildFactQuery turns one planned read into the query a provider receives.
//
// planned is the subject list planFactReads already narrowed to the kinds
// this capability declares it supports; it is nil only for a requirement the
// planner passed through untouched (an unregistered kind, or an
// investigation with no subjects at all). The subject-kind check below is
// therefore no longer reachable in the ordinary path -- the planner prunes
// or narrows first -- but it stays as the invariant that proves it: if a
// planning bug ever let an unsupported subject through, failing here is
// correct, because a provider must never be asked a question its capability
// says it cannot answer.
func buildFactQuery(request CanonicalFactRequest, requirement FactRequirement, capability FactCapability, allowed map[string]SubjectRef, planned []SubjectRef) (FactQuery, error) {
	subjects := planned
	if len(subjects) == 0 {
		subjects = factQuerySubjects(request, requirement)
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
	// Registry-level fanout cap (CHAOS-3755 adversarial review finding
	// H7). Each provider bounds its OWN query, but nothing bounded the
	// SUM across providers: a request may name many fact kinds, and every
	// one of them could legitimately return up to its own per-query limit,
	// so the bundle -- which becomes model input -- could still grow
	// without any server-side ceiling. This is the second, independent
	// bound: it holds even for a provider that ignores or lacks a query
	// limit of its own (a future non-ClickHouse provider, or one whose
	// LIMIT is dropped by a refactor), because it is enforced here at the
	// merge point every provider result must pass through.
	//
	// Over-budget facts are DROPPED and the source is marked truncated
	// rather than the read failing: a partial, explicitly-truncated answer
	// is the honest outcome, and Coverage already carries truncation
	// outward so the model and the caller both see it.
	if remaining := maxCanonicalFactsPerBundle - len(bundle.Facts); remaining < len(result.Facts) {
		if remaining < 0 {
			remaining = 0
		}
		result.Facts = result.Facts[:remaining]
		result.Truncated = true
	}
	// R4-2: an omission is a truncation of the result set, in the exact
	// sense the existing state already names -- so it routes through the
	// same branch rather than minting a new state. Done here, in the
	// registry, so no provider can count omissions and forget to degrade.
	if result.OmittedCount > 0 {
		result.Truncated = true
		if strings.TrimSpace(result.Reason) == "" {
			result.Reason = "canonical fact rows were omitted"
		}
		result.Reason = fmt.Sprintf("%s (omitted %d)", result.Reason, result.OmittedCount)
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
		// Codex round-1 F3: the RESULT state was validated above, but until
		// now an individual fact's own SourceState was not. That gap is not
		// cosmetic -- the evidence requirement immediately below is keyed on
		// this exact field, so a fact carrying any state other than
		// available/stale silently skips the RequiresEvidence check. A
		// provider could therefore return an evidence-free fact inside a
		// perfectly ordinary available result just by stamping the fact
		// itself with an impossible state.
		//
		// Two things are rejected. A state outside the provider-legal set
		// catches SourcePruned, which is a planner verdict a provider must
		// never mint (a provider claiming to have pruned itself has by
		// definition already run). A state that rejects facts catches the
		// rest of the bypass: no_data, unavailable, unconfigured,
		// unauthorized, conflicted, and not_applicable all mean "there is no
		// fact here", so a fact wearing one is self-contradicting.
		if !validFactSourceState(fact.SourceState) {
			return fmt.Errorf("provider returned fact with invalid source state %q", fact.SourceState)
		}
		if stateRejectsFacts(fact.SourceState) {
			return fmt.Errorf("provider returned fact with source state %q, which cannot carry facts", fact.SourceState)
		}
		// Codex round-2 R2-1: the evidence requirement is now keyed on the
		// capability ALONE, not on the fact's state.
		//
		// The old form excluded SourceTruncated, which is legitimately
		// fact-bearing -- the registry itself mints that state when the
		// bundle cap trims a result -- so an evidence-requiring provider
		// could return an evidence-FREE truncated fact and have it accepted.
		// Truncation says "there are more facts than these", never "these
		// facts need less grounding".
		//
		// Naming states here is also redundant now: the two guards above
		// leave exactly available, stale, and truncated reachable
		// (validFactSourceState minus stateRejectsFacts), and all three are
		// fact-bearing. So the state test could only ever weaken the
		// requirement, never strengthen it.
		if err := fact.Validate(capability.RequiresEvidence); err != nil {
			return fmt.Errorf("provider fact: %w", err)
		}
		bundle.Facts = append(bundle.Facts, fact)
	}
	bundle.Versions[capability.Kind] = result.Version
	if result.Watermark != "" {
		bundle.Watermarks[capability.Kind] = result.Watermark
	}
	appendFactCoverage(bundle, capability.Kind, result.State, result.ObservedAt, result.Watermark, result.Reason)
	// F1: only a provider that actually CONTRIBUTED counts toward the
	// composed grain. A degraded or empty provider reporting a grain
	// would let a source that answered nothing coarsen the whole answer.
	//
	// Round-2 F3: "contributing" means FACTS RETAINED, not
	// State == SourceAvailable. The narrower test silently dropped the
	// grain of a truncated or stale provider whose facts this bundle
	// KEPT -- so a mix of an instant-grain provider and a truncated
	// day-grain one composed to instant, overstating the answer's
	// precision on exactly the data it was built from.
	//
	// factsRetained is the same predicate the retention branch above
	// uses, called from one place so the two cannot drift apart again.
	if factsRetained(result.State, len(result.Facts)) {
		bundle.TemporalGrain = coarsestGrain(bundle.TemporalGrain, result.Grain)
	}
	return nil
}

// maxCoverageReasonLength mirrors ContextFabricSourceObservation's own
// 2000-character bound on Reason. Every coverage reason this package emits
// funnels through appendFactCoverage, so clamping here is what keeps a
// long provider reason -- or, since CHAOS-3783, a provider reason with a
// planner narrowing note prefixed onto it -- from pushing the finished
// result past its own contract and failing validation for the whole
// investigation. Truncating an explanation is strictly better than losing
// the answer that carried it.
const maxCoverageReasonLength = 2000

// maxCoverageDegradedReasonLength is ContextFabricCoverage's own bound on one
// DegradedReasons entry. It happens to equal the Reason bound, but they are
// separate contract limits on separate strings and a degraded entry is a
// LONGER string than the reason it is built from (it carries a "<kind>: "
// prefix), so they are clamped independently rather than sharing one
// constant by coincidence.
const maxCoverageDegradedReasonLength = 2000

// coarsestGrain returns whichever of two grains speaks for the LARGER
// span, because an answer is only as precise as its least precise
// contributing source. day is coarser than instant; an empty grain (a
// provider that did not answer temporally) never coarsens anything.
func coarsestGrain(current, candidate TemporalGrain) TemporalGrain {
	if candidate == "" {
		return current
	}
	if current == GrainDay || candidate == GrainDay {
		return GrainDay
	}
	if current == "" {
		return candidate
	}
	return current
}

func appendFactCoverage(bundle *CanonicalFactBundle, kind FactKind, state SourceState, observedAt *time.Time, watermark, reason string) {
	if strings.TrimSpace(reason) == "" && state != SourceAvailable {
		reason = "canonical fact capability returned " + string(state)
	}
	bundle.Coverage.Sources = append(bundle.Coverage.Sources, SourceObservation{
		Source: "canonical_fact:" + string(kind), State: state, ObservedAt: observedAt, Watermark: watermark,
		Reason: clampCoverageText(reason, maxCoverageReasonLength),
	})
	if factStateDegrades(state) {
		bundle.Coverage.Partial = true
		// Codex round-2 R2-3: clamp the COMPOSED string, not its ingredients.
		// Clamping reason first and then prefixing "<kind>: " pushed the
		// result back over the bound by exactly the prefix length, producing
		// a DegradedReasons entry the contract validator rejects -- which
		// fails the whole investigation, the outcome this clamping exists to
		// prevent. A narrowed provider that fails with a long reason is the
		// live path: the narrowing note and the failure reason are
		// concatenated before they ever reach here.
		bundle.Coverage.DegradedReasons = append(
			bundle.Coverage.DegradedReasons,
			clampCoverageText(string(kind)+": "+reason, maxCoverageDegradedReasonLength),
		)
	}
}

// clampCoverageText bounds one coverage string to the contract's limit.
//
// The bound is a RUNE count (contractsv1.stringLengthBetween uses
// utf8.RuneCountInString), so this truncates by runes -- a byte slice would
// both mis-measure multi-byte text and be able to cut a rune in half.
//
// It also normalizes whitespace, because DegradedReasons entries must
// satisfy strings.TrimSpace(value) == value: truncating mid-string can
// easily land on a space, which would fail validation for a reason that has
// nothing to do with length.
//
// Leading whitespace is removed BEFORE truncating, which is what makes the
// result provably non-empty: the retained prefix then starts with a
// non-space rune, so trimming its tail can never empty it. Trimming only
// after truncating would leave a value whose first `maximum` runes are all
// spaces collapsing to "", and an empty reason on a non-available source is
// itself a contract violation.
func clampCoverageText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if maximum <= 0 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return strings.TrimRightFunc(string([]rune(value)[:maximum]), unicode.IsSpace)
}

func validateCanonicalFactRequest(request CanonicalFactRequest) error {
	if len(request.Requirements) == 0 || len(request.Requirements) > 64 {
		return errors.New("canonical fact request requires bounded fact requirements")
	}
	allowed := investigationScopeSubjectSet(request)
	if len(allowed) == 0 {
		return errors.New("canonical fact request requires discovered subjects or a cohort")
	}
	// Codex round-8 F1: subject UNIQUENESS is decided here too, before any
	// pruning short-circuit -- the same family as the round-5 scope fix.
	//
	// buildFactQuery has always rejected a duplicated subject, and both the
	// v1 schema (uniqueItems) and ContextFabricSubjectResolution's validator
	// reject one on the wire. But when every requirement prunes, no provider
	// is queried, so buildFactQuery never runs and its rejection never
	// fires: an invalid request returned SUCCESS with pruned coverage. An
	// invalid request is an error even when nothing would have run --
	// validity is a property of the request, not of how much work it happens
	// to imply.
	//
	// Checked in two places because those are the two lists that can carry a
	// duplicate: the investigation-wide scope (used whenever a requirement
	// names no subjects of its own) and each explicit requirement list.
	//
	// Relationship to buildFactQuery's own uniqueness check, stated
	// precisely: this is equivalent for every request buildFactQuery would
	// have judged, and STRICTER for a duplicate confined to subjects that
	// narrowing drops. buildFactQuery sees the narrowed list; this sees the
	// raw one. Duplicates share a kind, so narrowing keeps or drops both
	// together and the two agree whenever the duplicate is among supported
	// subjects. Being stricter is the contract-correct direction: the v1
	// schema's uniqueItems forbids a duplicate outright, whether or not any
	// capability would have queried it.
	if duplicate, found := firstDuplicateSubject(investigationScopeSubjects(request)); found {
		return fmt.Errorf("canonical fact request subjects must be unique: %q appears more than once", duplicate.CanonicalID)
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
		// Codex round-5 R5-1: an explicit requirement.Subjects list is
		// checked for SCOPE here, before the planner runs.
		//
		// The list is a caller ASSERTION -- "read this kind for exactly
		// these subjects" -- and naming a subject outside the investigation
		// set is an error about the caller's request, not a statement about
		// what a capability can answer. buildFactQuery has always rejected
		// it; but once pruning was introduced, a wholly-unsupported explicit
		// list was pruned BEFORE that check could run, so an out-of-scope
		// request quietly became a success with zero facts and a pruned
		// coverage entry. Pruning must never swallow a scope violation.
		//
		// The ordering is the fix: validate the assertion, then plan. The
		// planner's fail-open rule already says an explicit Subjects list is
		// honored unchanged, and honoring a list includes honoring its
		// errors. buildFactQuery keeps its own identical check as the
		// defensive invariant behind this one.
		for _, subject := range requirement.Subjects {
			if _, ok := allowed[canonicalFactSubjectKey(subject)]; !ok {
				return fmt.Errorf("fact capability %s: subject %q is outside the discovered investigation set", requirement.Kind, subject.CanonicalID)
			}
		}
		if duplicate, found := firstDuplicateSubject(requirement.Subjects); found {
			return fmt.Errorf("fact capability %s: fact query subjects must be unique: %q appears more than once", requirement.Kind, duplicate.CanonicalID)
		}
	}
	return nil
}

// firstDuplicateSubject reports the first subject that appears twice in
// subjects, keyed the same way scope membership is (kind + canonical ID), so
// "duplicate" means exactly what "in scope" means and the two cannot drift.
func firstDuplicateSubject(subjects []SubjectRef) (SubjectRef, bool) {
	seen := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		key := canonicalFactSubjectKey(subject)
		if _, exists := seen[key]; exists {
			return subject, true
		}
		seen[key] = struct{}{}
	}
	return SubjectRef{}, false
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
			return factKindOrder(facts[i].Kind) < factKindOrder(facts[j].Kind)
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

func factKindOrder(kind FactKind) int {
	order := map[FactKind]int{
		FactIdentity: 0, FactMembership: 1, FactStatus: 2, FactActualCompletion: 3,
		FactWork: 4, FactBlockers: 5, FactRequiredChildren: 6, FactPullRequests: 7,
		FactReviews: 8, FactContinuousIntegration: 9, FactDeployments: 10,
		FactIncidents: 11, FactMetrics: 12, FactHealth: 13, FactWorkload: 14,
		FactInvestment: 15, FactReadiness: 16, FactOperationalDeficiencies: 17,
		FactSourceHealth: 18, FactEvidence: 19,
	}
	if value, ok := order[kind]; ok {
		return value
	}
	return len(order)
}

// stateRejectsFacts lists the states that cannot coexist with facts.
// SourcePruned joins them (CHAOS-3783): the provider was never called, so a
// fact attributed to a pruned capability would have no origin at all.
// factsRetained reports whether this provider's facts actually reached the
// bundle -- the single definition of "contributed", shared by the
// fact-retention check and the temporal-grain composition so a state that
// keeps facts can never be one that skips reporting its grain (round-2
// F3). A provider that returned no facts contributes nothing either way.
func factsRetained(state SourceState, factCount int) bool {
	return factCount > 0 && !stateRejectsFacts(state)
}

func stateRejectsFacts(state SourceState) bool {
	switch state {
	case SourceUnavailable, SourceUnconfigured, SourceUnauthorized, SourceNoData, SourceConflicted, SourceNotApplicable, SourcePruned:
		return true
	default:
		return false
	}
}

// factStateDegrades lists the states that make an answer partial.
//
// SourcePruned is deliberately NOT among them (CHAOS-3783). Every other
// non-available state means something the answer wanted is missing -- the
// source was unreachable, unconfigured, truncated, or in conflict. A prune
// means the planner proved the source had nothing to contribute to THIS
// question, so nothing is missing and the answer is not degraded. Marking it
// partial would train every consumer to treat a correctly-scoped
// investigation as a compromised one, and would make Coverage.Partial
// useless as a signal exactly as pruning became routine.
func factStateDegrades(state SourceState) bool {
	switch state {
	case SourceStale, SourceUnavailable, SourceUnconfigured, SourceUnauthorized, SourceTruncated, SourceConflicted:
		return true
	default:
		return false
	}
}

// validFactSourceState bounds what a PROVIDER may return. SourcePruned is
// absent on purpose: it is a planner verdict, minted only by ReadFacts, and
// a provider claiming to have pruned itself has by definition already run.
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
