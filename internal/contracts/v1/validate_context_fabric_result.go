package v1

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

func (s ContextFabricSubjectRef) Validate() error {
	if !validContextFabricSubjectKind(s.Kind) || !stringLengthBetween(s.CanonicalID, 1, 256) || !stringLengthBetween(s.Label, 1, 512) {
		return fmt.Errorf("subject reference violates v1 bounds")
	}
	if strings.TrimSpace(s.CanonicalID) != s.CanonicalID || strings.TrimSpace(s.Label) != s.Label {
		return fmt.Errorf("subject reference must be trimmed")
	}
	return nil
}

func (c ContextFabricSubjectCandidate) Validate() error {
	if !stringLengthBetween(c.ReceiptID, 8, 256) || !validResolutionState(c.State) || c.Confidence < 0 || c.Confidence > 1 {
		return fmt.Errorf("subject candidate identity, state, or confidence violates v1 bounds")
	}
	if err := c.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if len(c.MatchedTerms) > 100 || len(c.MatchReasons) < 1 || len(c.MatchReasons) > 100 || !uniqueTrimmedStrings(c.MatchedTerms, 512) || !uniqueTrimmedStrings(c.MatchReasons, 1024) {
		return fmt.Errorf("subject candidate match metadata violates v1 bounds")
	}
	if !validMatchMechanisms(c.MatchMechanisms) {
		return fmt.Errorf("subject candidate match mechanisms violate v1 bounds")
	}
	if !optionalEvidenceRefs(c.EvidenceRefIDs, 500) {
		return fmt.Errorf("subject candidate evidence references violate v1 bounds")
	}
	return nil
}

func (r ContextFabricSubjectResolution) Validate() error {
	if r.Candidates == nil || r.Committed == nil || len(r.Candidates) > 50 || len(r.Committed) > 250 {
		return fmt.Errorf("subject resolution arrays violate v1 bounds")
	}
	seenReceipts := make(map[string]struct{}, len(r.Candidates))
	for _, candidate := range r.Candidates {
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("candidates: %w", err)
		}
		if _, exists := seenReceipts[candidate.ReceiptID]; exists {
			return fmt.Errorf("candidate receipt IDs must be unique")
		}
		seenReceipts[candidate.ReceiptID] = struct{}{}
	}
	if !uniqueSubjects(r.Committed) {
		return fmt.Errorf("committed subjects must be valid and unique")
	}
	if !stringLengthBetween(r.ClarificationPrompt, 0, 2000) || strings.TrimSpace(r.ClarificationPrompt) != r.ClarificationPrompt {
		return fmt.Errorf("clarification prompt violates v1 bounds")
	}
	return nil
}

// Validate enforces the current contract bounds (write path).
func (c ContextFabricCohort) Validate() error {
	return c.validate(ContextFabricCohortMember.Validate)
}

// validateStored enforces the legacy bounds for an already-persisted row.
func (c ContextFabricCohort) validateStored() error {
	return c.validate(ContextFabricCohortMember.validateStored)
}

func (c ContextFabricCohort) validate(validateMember func(ContextFabricCohortMember) error) error {
	if !validContextFabricSubjectKind(c.Kind) || (c.Kind != ContextFabricSubjectTeam && c.Kind != ContextFabricSubjectProject) || c.Members == nil || len(c.Members) > 250 || len(c.Exclusions) > 250 || !stringLengthBetween(strings.TrimSpace(c.Rationale), 1, 4000) || (c.Complete && c.Truncated) {
		return fmt.Errorf("cohort violates v1 bounds")
	}
	seen := make(map[string]struct{}, len(c.Members))
	lastRank := 0
	for _, member := range c.Members {
		if err := validateMember(member); err != nil {
			return fmt.Errorf("members: %w", err)
		}
		if member.Subject.Kind != c.Kind || member.Rank <= lastRank {
			return fmt.Errorf("cohort member kind or rank is invalid")
		}
		key := subjectKey(member.Subject)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("cohort members must be unique")
		}
		seen[key] = struct{}{}
		lastRank = member.Rank
	}
	for _, exclusion := range c.Exclusions {
		if err := exclusion.Validate(); err != nil {
			return fmt.Errorf("exclusions: %w", err)
		}
		if exclusion.Subject.Kind != c.Kind {
			return fmt.Errorf("cohort exclusion kind is invalid")
		}
		if _, exists := seen[subjectKey(exclusion.Subject)]; exists {
			return fmt.Errorf("cohort exclusion duplicates a member")
		}
	}
	return nil
}

// Cohort member inclusion-reason bounds.
//
// The CURRENT bounds match this shape's published JSON Schema, which is the
// wire-contract source of truth. The LEGACY bounds are what the Go
// validator alone used to accept, before CHAOS-3746 round 2 found it was
// looser than the contract it claimed to enforce.
//
// Both exist because investigation results are IMMUTABLE and already
// persisted. Tightening a validator that runs on the read path would have
// made previously accepted rows unreadable after deploy -- API 500s and MCP
// retrieval failures for data nobody can migrate, since the payloads cannot
// be rewritten. So writes enforce the contract and reads tolerate the
// history: a row written by an older binary stays readable, and no new row
// can be created at the legacy size.
const (
	contextFabricCohortInclusionReasonsMaxCount    = 32
	contextFabricCohortInclusionReasonMaxLength    = 1000
	contextFabricLegacyCohortInclusionReasonsCount = 50
	contextFabricLegacyCohortInclusionReasonLength = 1024

	// Limitations and warnings carry the same split, for the same reason
	// and found the same way: the Go bound (250 x 4000) was looser than
	// the published schema (100 x 2000), so a result could pass Go
	// validation and still be a schema-invalid document on the wire. It
	// surfaced only when the EMITTED JSON was validated against the
	// published tool schema (codex round-3 P2-4) -- Go-side agreement is
	// not the same as the wire document being valid.
	contextFabricNarrativeMaxCount  = ContextFabricLimitationsMaxCount
	contextFabricNarrativeMaxLength = ContextFabricLimitationMaxLength
	// The historical Go-only maxima, kept solely so immutable rows written
	// before the correction stay readable.
	contextFabricLegacyNarrativeMaxCount  = 250
	contextFabricLegacyNarrativeMaxLength = 4000
)

// Validate enforces the CURRENT contract bounds. This is the write path and
// the path every freshly produced result takes.
func (m ContextFabricCohortMember) Validate() error {
	return m.validate(contextFabricCohortInclusionReasonsMaxCount, contextFabricCohortInclusionReasonMaxLength)
}

// validateStored enforces the LEGACY bounds, for revalidating a row that
// was already persisted. See the bounds block above for why the read path
// deliberately accepts more than the write path.
func (m ContextFabricCohortMember) validateStored() error {
	return m.validate(contextFabricLegacyCohortInclusionReasonsCount, contextFabricLegacyCohortInclusionReasonLength)
}

func (m ContextFabricCohortMember) validate(maxReasons, maxReasonLength int) error {
	if err := m.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if m.Rank < 1 || len(m.InclusionReasons) < 1 || len(m.InclusionReasons) > maxReasons || !uniqueTrimmedStrings(m.InclusionReasons, maxReasonLength) || !optionalEvidenceRefs(m.EvidenceRefIDs, 500) {
		return fmt.Errorf("cohort member violates v1 bounds")
	}
	return nil
}

func (e ContextFabricCohortExclusion) Validate() error {
	if err := e.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if !stringLengthBetween(strings.TrimSpace(e.Reason), 1, 2000) {
		return fmt.Errorf("cohort exclusion reason violates v1 bounds")
	}
	return nil
}

func (p ContextFabricRelationshipPath) Validate() error {
	if !stringLengthBetween(p.PathID, 8, 256) || len(p.Nodes) < 2 || len(p.Nodes) > 51 || len(p.Edges) != len(p.Nodes)-1 || !stringLengthBetween(strings.TrimSpace(p.WhyRelevant), 1, 4000) || !boundedEvidenceRefs(p.EvidenceRefIDs, 500, false) {
		return fmt.Errorf("relationship path violates v1 bounds")
	}
	if !uniqueSubjects(p.Nodes) {
		return fmt.Errorf("relationship path nodes must be valid and unique")
	}
	for index, edge := range p.Edges {
		if err := edge.Validate(); err != nil {
			return fmt.Errorf("edges: %w", err)
		}
		if edge.From != p.Nodes[index] || edge.To != p.Nodes[index+1] {
			return fmt.Errorf("relationship path edge continuity is invalid")
		}
	}
	return nil
}

func (e ContextFabricRelationshipEdge) Validate() error {
	if !validDerivationMethod(e.Derivation) || !validEpistemicStatus(e.EpistemicStatus) || !boundedEvidenceRefs(e.EvidenceRefIDs, 500, false) {
		return fmt.Errorf("relationship edge violates v1 bounds")
	}
	if !validContextFabricRelationshipType(e.Type) {
		return fmt.Errorf("%w: %q", ErrContextFabricUnknownRelationshipType, e.Type)
	}
	if err := e.From.Validate(); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	if err := e.To.Validate(); err != nil {
		return fmt.Errorf("to: %w", err)
	}
	if err := validateTimeRange(e.ObservedAt, e.ValidFrom, e.ValidTo); err != nil {
		return err
	}
	return nil
}

func (d ContextFabricDriverJudgment) Validate() error {
	if !stringLengthBetween(d.DriverID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength) || !validDriverStanding(d.Standing) || !validDriverCategory(ContextFabricDriverCategory(d.Category)) || !stringLengthBetween(strings.TrimSpace(d.Title), 1, ContextFabricDriverTitleMaxLength) || !stringLengthBetween(strings.TrimSpace(d.Summary), 1, ContextFabricDriverSummaryMaxLength) || !validDerivationMethod(d.Derivation) || !validEpistemicStatus(d.EpistemicStatus) || d.Confidence < 0 || d.Confidence > 1 || !stringLengthBetween(d.Qualification, 0, ContextFabricDriverQualificationMaxLength) {
		return fmt.Errorf("driver judgment violates v1 bounds")
	}
	if len(d.AffectedSubjects) < ContextFabricDriverAffectedSubjectsMinCount || len(d.AffectedSubjects) > ContextFabricDriverAffectedSubjectsMaxCount || !uniqueSubjects(d.AffectedSubjects) || len(d.PathIDs) > ContextFabricDriverPathIDsMaxCount || !uniqueTrimmedStrings(d.PathIDs, ContextFabricIdentifierRefMaxLength) || !boundedEvidenceRefs(d.EvidenceRefIDs, ContextFabricEvidenceRefIDsMaxCount, true) {
		return fmt.Errorf("driver subject, path, or evidence references violate v1 bounds")
	}
	if len(d.ClaimedFactIDs) > ContextFabricDriverClaimedFactIDsMaxCount || !uniqueTrimmedStrings(d.ClaimedFactIDs, ContextFabricIdentifierRefMaxLength) {
		return fmt.Errorf("driver claimed fact references violate v1 bounds")
	}
	if d.Standing != ContextFabricDriverWithheld && len(d.PathIDs) == 0 && len(d.EvidenceRefIDs) == 0 {
		return fmt.Errorf("non-withheld driver lacks evidence closure")
	}
	// Category->FactKind closure is a stronger requirement than plain
	// evidence closure above: a driver whose Category names a
	// canonical-fact-shaped judgment (see
	// ContextFabricDriverCategoryRequiresClaimedFact) must cite a
	// ClaimedFactID even if it is withheld or already has evidence/path
	// references -- an evidence ref proves something was cited, not that
	// the cited value agrees with the canonical fact. Full cross-reference
	// (does the ID resolve, does its Kind match) happens at the result
	// level in validateDrivers, where the ClaimedFacts list is available;
	// this only enforces structural presence.
	if _, required := ContextFabricDriverCategoryRequiresClaimedFact(ContextFabricDriverCategory(d.Category)); required && len(d.ClaimedFactIDs) == 0 {
		return fmt.Errorf("driver category %q requires a claimed fact for value-level closure", d.Category)
	}
	if d.Standing == ContextFabricDriverWithheld && strings.TrimSpace(d.Qualification) == "" {
		return fmt.Errorf("withheld driver requires a qualification")
	}
	return nil
}

func (f ContextFabricFinding) Validate() error {
	if !stringLengthBetween(f.FindingID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength) || !stringLengthBetween(strings.TrimSpace(f.Kind), 1, ContextFabricFindingKindMaxLength) || !stringLengthBetween(strings.TrimSpace(f.Summary), 1, ContextFabricFindingSummaryMaxLength) || len(f.Subjects) > ContextFabricFindingSubjectsMaxCount || !uniqueSubjects(f.Subjects) || !boundedEvidenceRefs(f.EvidenceRefIDs, ContextFabricEvidenceRefIDsMaxCount, false) {
		return fmt.Errorf("finding violates v1 bounds")
	}
	if len(f.ClaimedFactIDs) > ContextFabricDriverClaimedFactIDsMaxCount || !uniqueTrimmedStrings(f.ClaimedFactIDs, ContextFabricIdentifierRefMaxLength) {
		return fmt.Errorf("finding claimed fact references violate v1 bounds")
	}
	// See the matching comment in ContextFabricDriverJudgment.Validate --
	// Finding.Kind is the category-equivalent field for findings.
	if _, required := ContextFabricDriverCategoryRequiresClaimedFact(ContextFabricDriverCategory(f.Kind)); required && len(f.ClaimedFactIDs) == 0 {
		return fmt.Errorf("finding kind %q requires a claimed fact for value-level closure", f.Kind)
	}
	return nil
}

func (c ContextFabricClaimedFact) Validate() error {
	if !stringLengthBetween(c.ClaimID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength) || !validFactKind(c.Kind) || !stringLengthBetween(c.Field, 1, ContextFabricClaimedFieldMaxLength) || strings.TrimSpace(c.Field) != c.Field {
		return fmt.Errorf("claimed fact identity violates v1 bounds")
	}
	if err := c.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if err := c.Value.Validate(); err != nil {
		return fmt.Errorf("value: %w", err)
	}
	return nil
}

func (o ContextFabricSourceObservation) Validate() error {
	if !stringLengthBetween(strings.TrimSpace(o.Source), 1, 128) || !validSourceState(o.State) || !stringLengthBetween(o.Watermark, 0, 512) || !stringLengthBetween(o.Reason, 0, 2000) {
		return fmt.Errorf("source observation violates v1 bounds")
	}
	if o.ObservedAt != nil && o.ObservedAt.IsZero() {
		return fmt.Errorf("source observation timestamp is invalid")
	}
	if o.State != ContextFabricSourceAvailable && strings.TrimSpace(o.Reason) == "" {
		return fmt.Errorf("non-available source requires a reason")
	}
	return nil
}

// Validate enforces the current contract (write path).
func (c ContextFabricCoverage) Validate() error {
	return c.validate(contextFabricNarrativeMaxCount)
}

// validateStored accepts the legacy source and degraded-reason counts for
// an already-persisted row. Same immutability argument as the cohort and
// narrative bounds.
func (c ContextFabricCoverage) validateStored() error {
	return c.validate(contextFabricLegacyNarrativeMaxCount)
}

func (c ContextFabricCoverage) validate(maxSourcesAndReasons int) error {
	// DegradedReasons has no non-nil requirement, unlike Sources: the JSON
	// Schema's Coverage.required is ["sources", "partial"] only --
	// degraded_reasons is genuinely optional there, and its Go tag is
	// `omitempty`, so an empty (non-nil) slice a caller sets in Go
	// legitimately round-trips through JSON as an OMITTED field and comes
	// back nil. A validator that rejected nil here would spuriously
	// reject its own valid output the moment anything re-decodes it from
	// JSON (as InvestigationResultStore implementations now do on every
	// Get -- CHAOS-3755 finding M2) even though nothing was ever actually
	// invalid.
	if c.Sources == nil || len(c.Sources) > maxSourcesAndReasons || len(c.DegradedReasons) > maxSourcesAndReasons || !uniqueTrimmedStrings(c.DegradedReasons, 2000) {
		return fmt.Errorf("coverage violates v1 bounds")
	}
	seen := make(map[string]struct{}, len(c.Sources))
	for _, source := range c.Sources {
		if err := source.Validate(); err != nil {
			return fmt.Errorf("sources: %w", err)
		}
		if _, exists := seen[source.Source]; exists {
			return fmt.Errorf("coverage source names must be unique")
		}
		seen[source.Source] = struct{}{}
	}
	return nil
}

func (v ContextFabricVersionSet) Validate() error {
	values := []string{v.ServiceVersion, v.ContractVersion, v.Backend, v.ProjectionVersion, v.QueryVersion, v.InterpretationVersion, v.SynthesisVersion, v.CanonicalServiceVersion}
	for _, value := range values {
		if !validVersion(value) {
			return fmt.Errorf("version metadata violates v1 bounds")
		}
	}
	// ModelIdentity (CHAOS-3782) is OPTIONAL, unlike every sibling above --
	// Codex round-2 finding #2: a row persisted before this field existed
	// (migration 0009, pre-0011) has no model identity captured at all,
	// and the immutable store must keep reading it back regardless --
	// Get() calls Validate() on every read, so a REQUIRED field here would
	// permanently break every pre-CHAOS-3782 stored result. Empty means
	// "unknown," distinct from "unwired" (the placeholder a FRESH
	// investigation gets when no model ran -- see Engine.Investigate --
	// which is itself a non-empty string and so still validates here).
	// This also has no reuse-eligibility consequence to special-case: a
	// row with no ModelIdentity also has no question_hash (it predates
	// answer reuse entirely, or was saved with reuse disabled), and
	// FindReusable already excludes every such row on that basis alone.
	if v.ModelIdentity != "" && !validModelIdentity(v.ModelIdentity) {
		return fmt.Errorf("version metadata violates v1 bounds")
	}
	if !stringLengthBetween(v.BackendVersion, 0, 256) || strings.TrimSpace(v.BackendVersion) != v.BackendVersion {
		return fmt.Errorf("backend_version violates v1 bounds")
	}
	return nil
}

func (q ContextFabricInterpretedQuestion) Validate() error {
	if !validInvestigationShape(q.Shape) || !stringLengthBetween(strings.TrimSpace(q.RequestedJudgment), 1, ContextFabricRequestedJudgmentMaxLength) || len(q.SubjectTerms) > ContextFabricSubjectTermsMaxCount || len(q.ComparisonTerms) > ContextFabricComparisonTermsMaxCount || !uniqueTrimmedStrings(q.SubjectTerms, ContextFabricSubjectOrComparisonTermMaxLength) || !uniqueTrimmedStrings(q.ComparisonTerms, ContextFabricSubjectOrComparisonTermMaxLength) || len(q.FactRequirements) > ContextFabricFactRequirementsMaxCount || !stringLengthBetween(q.ClarificationReason, 0, ContextFabricClarificationReasonMaxLength) {
		return fmt.Errorf("interpreted question violates v1 bounds")
	}
	if err := q.TimeContext.Validate(); err != nil {
		return fmt.Errorf("time_context: %w", err)
	}
	seen := make(map[ContextFabricFactKind]struct{}, len(q.FactRequirements))
	for _, requirement := range q.FactRequirements {
		if err := requirement.Validate(); err != nil {
			return fmt.Errorf("fact_requirements: %w", err)
		}
		if _, exists := seen[requirement.Kind]; exists {
			return fmt.Errorf("fact_requirements kinds must be unique")
		}
		seen[requirement.Kind] = struct{}{}
	}
	if q.ClarificationNeeded && strings.TrimSpace(q.ClarificationReason) == "" {
		return fmt.Errorf("clarification_needed requires a reason")
	}
	return nil
}

func (r ContextFabricFactRequirement) Validate() error {
	if !validFactKind(r.Kind) || len(r.Subjects) > 250 || !uniqueSubjects(r.Subjects) || len(r.Parameters) > ContextFabricFactRequirementParametersMaxCount {
		return fmt.Errorf("fact requirement violates v1 bounds")
	}
	// Sorted, not a bare map range (CHAOS-3784 round-5 R5-1): Go
	// randomizes map iteration order per range, so ranging r.Parameters
	// directly would make WHICH parameter this rejects on -- and so which
	// one DiagnoseContextFabricFactRequirementBound (bound_diagnosis.go,
	// which already sorts) can correctly attribute -- nondeterministic
	// when more than one parameter violates a bound. Sorting here does not
	// change the accept/reject set (the same requirements are still
	// rejected either way); it only makes the first-reported violation,
	// among several, deterministic and equal to what diagnosis reports
	// (CHAOS-3784 round-3 R3-2's same fix, applied to this validator).
	keys := make([]string, 0, len(r.Parameters))
	for key := range r.Parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := r.Parameters[key]
		if !stringLengthBetween(key, 1, ContextFabricFactRequirementParameterKeyMaxLength) || !stringLengthBetween(value, 0, ContextFabricFactRequirementParameterValueMaxLength) || strings.TrimSpace(key) != key || strings.TrimSpace(value) != value {
			return fmt.Errorf("fact requirement parameter violates v1 bounds")
		}
	}
	return nil
}

// isStoredValidation reports whether the supplied cohort validator is the
// lenient stored-read one. Deriving the narrative mode from it keeps a
// single decision -- "is this a read of persisted data?" -- rather than
// letting two independently-set flags disagree.
func isStoredValidation(validateCohort func(ContextFabricCohort) error) bool {
	return reflect.ValueOf(validateCohort).Pointer() == reflect.ValueOf(ContextFabricCohort.validateStored).Pointer()
}

// Validate enforces the CURRENT contract on a result being produced or
// accepted. Use it for every write and for anything a caller is about to
// receive fresh.
func (r ContextFabricInvestigationResult) Validate() error {
	return r.validate(ContextFabricCohort.Validate)
}

// ValidateStored revalidates a result read back from durable storage.
//
// It differs from Validate in exactly one way: bounds that were legitimately
// looser in an older binary are still accepted, because investigation
// results are IMMUTABLE. A stored row cannot be rewritten to satisfy a
// tightened bound, so a read path that enforced the new bound would make
// previously valid data permanently unreadable -- an API 500 and an MCP
// retrieval failure for a row that was correct when it was written.
//
// Writes stay strict, so the looseness cannot grow: no NEW row can be
// created at the legacy size. See the cohort inclusion-reason bounds block
// for the specific allowance and its origin.
func (r ContextFabricInvestigationResult) ValidateStored() error {
	return r.validate(ContextFabricCohort.validateStored)
}

func (r ContextFabricInvestigationResult) validate(validateCohort func(ContextFabricCohort) error) error {
	// The narrative bounds follow the same strict-write / lenient-read
	// split as the cohort bounds, keyed off which cohort validator was
	// supplied so the two can never drift apart.
	narrativeCount, narrativeLength := contextFabricNarrativeMaxCount, contextFabricNarrativeMaxLength
	if isStoredValidation(validateCohort) {
		narrativeCount, narrativeLength = contextFabricLegacyNarrativeMaxCount, contextFabricLegacyNarrativeMaxLength
	}
	if r.SchemaVersion != ContextFabricInvestigationResultSchema || !stringLengthBetween(r.ResultID, 8, 256) || !stringLengthBetween(r.RequestID, 8, 256) || r.GeneratedAt.IsZero() || !stringLengthBetween(strings.TrimSpace(r.Question), 1, 8000) || !validInvestigationStatus(r.Status) {
		return fmt.Errorf("result identity or status violates v1 bounds")
	}
	if err := r.Interpretation.Validate(); err != nil {
		return fmt.Errorf("interpretation: %w", err)
	}
	if err := r.SubjectResolution.Validate(); err != nil {
		return fmt.Errorf("subject_resolution: %w", err)
	}
	if r.Cohort != nil {
		if err := validateCohort(*r.Cohort); err != nil {
			return fmt.Errorf("cohort: %w", err)
		}
	}
	// DirectJudgment/CurrentState/DeterministicAnswer are server-composed
	// (RuntimeAnswerSynthesizer.Synthesize's compose* functions render and
	// truncate them to fit these exact lengths -- see
	// internal/contextfabric/model_runtime.go), never validated against the
	// model's own raw prose, so their bounds are not part of the
	// model-facing registry (ContextFabricModelFacingBounds) this file
	// otherwise draws from; likewise Paths, which ACR derives from the
	// graph, not the model.
	if !stringLengthBetween(r.DirectJudgment, 0, 8000) || !stringLengthBetween(r.CurrentState, 0, 8000) || !stringLengthBetween(r.DeterministicAnswer, 1, 16000) ||
		r.StrongestPressures == nil || len(r.StrongestPressures) > ContextFabricStrongestPressuresMaxCount || !uniqueTrimmedStrings(r.StrongestPressures, ContextFabricStrongestPressureMaxLength) ||
		r.Drivers == nil || len(r.Drivers) > ContextFabricDriversMaxCount ||
		r.RemainingWork == nil || len(r.RemainingWork) > ContextFabricRemainingWorkMaxCount ||
		r.ReadinessGaps == nil || len(r.ReadinessGaps) > ContextFabricReadinessGapsMaxCount ||
		r.Paths == nil || len(r.Paths) > 250 ||
		r.Conflicts == nil || len(r.Conflicts) > ContextFabricConflictsMaxCount ||
		r.Limitations == nil || len(r.Limitations) > narrativeCount || !uniqueTrimmedStrings(r.Limitations, narrativeLength) ||
		r.EvidenceRefIDs == nil || !boundedEvidenceRefs(r.EvidenceRefIDs, ContextFabricEvidenceRefIDsMaxCount, true) ||
		r.Warnings == nil || len(r.Warnings) > narrativeCount || !uniqueTrimmedStrings(r.Warnings, narrativeLength) {
		return fmt.Errorf("result answer fields violate v1 bounds")
	}
	if r.Status == ContextFabricInvestigationComplete || r.Status == ContextFabricInvestigationPartial {
		if strings.TrimSpace(r.DirectJudgment) == "" {
			return fmt.Errorf("answer-capable result requires a direct judgment")
		}
	}
	if r.Status == ContextFabricInvestigationClarificationRequired && r.SubjectResolution.ClarificationPrompt == "" {
		return fmt.Errorf("clarification result requires a prompt")
	}
	claimed, err := validateClaimedFacts(r.ClaimedFacts)
	if err != nil {
		return err
	}
	if err := validateDrivers(r.Drivers, claimed); err != nil {
		return err
	}
	if err := validateFindings("remaining_work", r.RemainingWork, claimed); err != nil {
		return err
	}
	if err := validateFindings("readiness_gaps", r.ReadinessGaps, claimed); err != nil {
		return err
	}
	if err := validatePaths(r.Paths); err != nil {
		return err
	}
	if err := validateFindings("conflicts", r.Conflicts, claimed); err != nil {
		return err
	}
	validateCoverage := ContextFabricCoverage.Validate
	if isStoredValidation(validateCohort) {
		validateCoverage = ContextFabricCoverage.validateStored
	}
	if err := validateCoverage(r.Coverage); err != nil {
		return fmt.Errorf("coverage: %w", err)
	}
	if err := r.Versions.Validate(); err != nil {
		return fmt.Errorf("versions: %w", err)
	}
	if r.Temporal != nil {
		if err := r.Temporal.Validate(); err != nil {
			return fmt.Errorf("temporal: %w", err)
		}
		// The label must describe the SAME question the interpretation
		// ran on. A label whose axis or instant disagreed with the
		// interpreted one would let a result claim to speak for a time
		// the investigation never actually bounded its reads by.
		if !sameTimeContext(r.Temporal.Requested, r.Interpretation.TimeContext) {
			return fmt.Errorf("temporal: requested time context must equal the interpreted time context")
		}
	} else if r.Interpretation.TimeContext.Axis != ContextFabricTemporalCurrent {
		// The converse, and the more dangerous direction: a historical
		// investigation carrying no temporal label is exactly an answer
		// about a past time with nothing marking it as one (AC-3781-2).
		// Refuse it at the contract boundary rather than let a
		// composition bug ship a silently unlabeled historical answer.
		return fmt.Errorf("temporal: a non-current time axis requires a temporal label")
	}
	return nil
}

// Validate enforces CHAOS-3781's temporal-label bounds (AC-3781-2). See
// ContextFabricTemporalLabel's doc comment for what the type means; this
// enforces the two invariants that make it trustworthy -- the label only
// ever appears on a historical axis, and Effective never widens beyond
// Requested.
func (l ContextFabricTemporalLabel) Validate() error {
	if err := l.Requested.Validate(); err != nil {
		return fmt.Errorf("requested: %w", err)
	}
	if err := l.Effective.Validate(); err != nil {
		return fmt.Errorf("effective: %w", err)
	}
	if l.Requested.Axis == ContextFabricTemporalCurrent {
		return fmt.Errorf("a temporal label is only meaningful on a historical time axis")
	}
	if l.Effective.Axis != l.Requested.Axis {
		return fmt.Errorf("effective axis must equal requested axis")
	}
	if !validContextFabricTemporalGrain(l.Grain) {
		return fmt.Errorf("temporal grain is invalid")
	}
	// Effective is narrower than or equal to Requested, never wider. Both
	// Validate calls above already proved the pointers each axis requires
	// are non-nil, so these dereferences are safe.
	switch l.Requested.Axis {
	case ContextFabricTemporalValidTime, ContextFabricTemporalObservedTime:
		if l.Effective.AsOf.After(*l.Requested.AsOf) {
			return fmt.Errorf("effective as_of cannot be after the requested as_of")
		}
	case ContextFabricTemporalRange:
		if l.Effective.Start.Before(*l.Requested.Start) || l.Effective.End.After(*l.Requested.End) {
			return fmt.Errorf("effective window cannot extend beyond the requested window")
		}
	}
	return nil
}

func validContextFabricTemporalGrain(grain ContextFabricTemporalGrain) bool {
	switch grain {
	case ContextFabricGrainInstant, ContextFabricGrainDay, ContextFabricGrainNone:
		return true
	default:
		return false
	}
}

// sameTimeContext compares two ContextFabricTimeContext values by VALUE,
// including through their *time.Time fields. Go's == on the struct would
// compare the pointers themselves, so two contexts naming the same instant
// through different pointers would compare unequal -- which is the normal
// case here, since the label is composed separately from the
// interpretation it must agree with. time.Time.Equal is used rather than
// == so a UTC and a same-instant non-UTC value still match.
func sameTimeContext(a, b ContextFabricTimeContext) bool {
	return a.Axis == b.Axis &&
		sameOptionalTime(a.AsOf, b.AsOf) &&
		sameOptionalTime(a.Start, b.Start) &&
		sameOptionalTime(a.End, b.End)
}

func sameOptionalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
