package v1

import (
	"fmt"
	"strings"
)

// This file is CHAOS-4690's structured-coverage wire surface: one
// ContextFabricCoverageDetail per coverage observation that composes a
// reason string today, carried BESIDE the legacy composed strings during
// the expand phase (CHAOS-4656 optional-first). The composed
// degraded_reasons[] entries are a DERIVATION of the degrading details
// (internal/contextfabric's coverage normalizer), so the two cannot drift
// while both ship.
//
// Design of record: .remember/context-fabric/drafts/chaos-4690-design-2026-08-31.md
// (settled, sol-xhigh r4 NO STRUCTURAL OBJECTIONS; the residual model-
// phrasing latitude on Phrasing was RULED by chris 2026-08-31 14:59 PDT,
// option A).

// ContextFabricCoverageDetailCode is the closed cause vocabulary for one
// coverage detail. ONE detail per coverage observation, always — matching
// the ONE legacy reason string the observation carries. For a multi-cause
// observation the code names the MOST SPECIFIC cause, in fixed precedence
//
//	fact_scope_unexpanded > fact_read_failed > fact_provider_reported > fact_narrowed
//
// and the remaining causes ride as fields on the same detail (Narrowed +
// SkippedKinds are allowed on all four fact read codes and REQUIRED on
// fact_narrowed, which is used only when narrowing is the SOLE cause).
type ContextFabricCoverageDetailCode string

const (
	// ContextFabricCoverageDetailFactUnconfigured: no provider is
	// registered for a planned fact kind (fact_registry.go's
	// "canonical fact capability is not configured" branch).
	ContextFabricCoverageDetailFactUnconfigured ContextFabricCoverageDetailCode = "fact_unconfigured"
	// ContextFabricCoverageDetailFactScopeUnexpanded: a CHAOS-4099 scope
	// gap — the requirement's facts were not directly reachable and scope
	// expansion did not reach them (fact_planner.go's unexpandedReason).
	ContextFabricCoverageDetailFactScopeUnexpanded ContextFabricCoverageDetailCode = "fact_scope_unexpanded"
	// ContextFabricCoverageDetailFactReadFailed: the provider read errored
	// and was classified (fact_registry.go's classifyFactReadError branch).
	ContextFabricCoverageDetailFactReadFailed ContextFabricCoverageDetailCode = "fact_read_failed"
	// ContextFabricCoverageDetailFactProviderReported: the provider itself
	// reported a non-available state with its own reason (stale, truncated,
	// no_data, not_applicable, ...). Degrading exactly when the string path
	// degrades (factStateDegrades) — a SourceNoData reason is a real detail
	// and is NON-degrading.
	ContextFabricCoverageDetailFactProviderReported ContextFabricCoverageDetailCode = "fact_provider_reported"
	// ContextFabricCoverageDetailFactPruned: the planner proved no resolved
	// subject kind fits this capability and never ran it (prunedReason).
	// Never degrading.
	ContextFabricCoverageDetailFactPruned ContextFabricCoverageDetailCode = "fact_pruned"
	// ContextFabricCoverageDetailFactNarrowed: the planner skipped
	// unsupported-kind subjects and the read itself then had nothing
	// further to say (the standalone narrowedReason shape). Sole-cause code
	// by the precedence rule above.
	ContextFabricCoverageDetailFactNarrowed ContextFabricCoverageDetailCode = "fact_narrowed"

	// The four graph-reader degradations (falkorgraph/reader.go) plus its
	// one non-degrading disclosure.
	ContextFabricCoverageDetailGraphEndpointLookupFailed         ContextFabricCoverageDetailCode = "graph_endpoint_lookup_failed"
	ContextFabricCoverageDetailGraphExactNameCandidatesTruncated ContextFabricCoverageDetailCode = "graph_exact_name_candidates_truncated"
	ContextFabricCoverageDetailGraphCohortDeniedByAuthorization  ContextFabricCoverageDetailCode = "graph_cohort_denied_by_authorization"
	ContextFabricCoverageDetailGraphUnknownRelationshipType      ContextFabricCoverageDetailCode = "graph_unknown_relationship_type"
	// ContextFabricCoverageDetailGraphValidityUnbounded: elements admitted
	// at a requested historical time carrying no validity window (the
	// context-fabric:graph-validity-windows source row). Never degrading.
	ContextFabricCoverageDetailGraphValidityUnbounded ContextFabricCoverageDetailCode = "graph_validity_unbounded"

	// ContextFabricCoverageDetailReuseAuxiliaryRefsStripped: this answer
	// was served from the stored-result reuse path, and the authorization
	// recheck could not prove every evidence reference the stored payload
	// carried is still visible to this caller. The unprovable ones were
	// REMOVED before the answer was served, together with any item they
	// left without evidence; Count is the total number of removals.
	//
	// Always degrading. It is not a fact-read or graph-read limitation --
	// it is this answer being narrower than the one originally stored, and
	// a caller who is not told that would reasonably read the reused answer
	// as the whole of what was found. The alternative to disclosing it is
	// refusing the reuse outright, which is what a missing TOP-LEVEL
	// citation still does: a narrowed answer is useful, an answer whose own
	// cited evidence vanished is a different answer.
	ContextFabricCoverageDetailReuseAuxiliaryRefsStripped ContextFabricCoverageDetailCode = "reuse_auxiliary_refs_stripped"
	// ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt: the turn
	// ENDED before this requirement was attempted at all.
	//
	// Every other code in this vocabulary describes something that happened
	// TO a read -- a fact was unconfigured, a read failed, a set was pruned
	// or narrowed. This one exists because no read happened: a terminal veto
	// stopped the turn while the plan already described the requirement, so
	// the requirement is neither served nor unservable. It was simply never
	// reached.
	//
	// Without it that state had NO truthful expression. The outcome token for
	// it, `not_attempted`, is not lossless, so a row carrying it must name a
	// cause -- and the nearest existing code, `fact_pruned`, would assert
	// that a fact was pruned when nothing was read. Borrowing it would have
	// replaced a false `satisfied` with a false `fact_pruned`, which is not a
	// repair.
	ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt ContextFabricCoverageDetailCode = "answer_terminated_before_attempt"
)

// contextFabricCoverageDetailCodes is the closed vocabulary in published
// order — same unexported-array discipline as contextFabricFactKinds.
var contextFabricCoverageDetailCodes = [...]ContextFabricCoverageDetailCode{
	ContextFabricCoverageDetailFactUnconfigured,
	ContextFabricCoverageDetailFactScopeUnexpanded,
	ContextFabricCoverageDetailFactReadFailed,
	ContextFabricCoverageDetailFactProviderReported,
	ContextFabricCoverageDetailFactPruned,
	ContextFabricCoverageDetailFactNarrowed,
	ContextFabricCoverageDetailGraphEndpointLookupFailed,
	ContextFabricCoverageDetailGraphExactNameCandidatesTruncated,
	ContextFabricCoverageDetailGraphCohortDeniedByAuthorization,
	ContextFabricCoverageDetailGraphUnknownRelationshipType,
	ContextFabricCoverageDetailGraphValidityUnbounded,
	ContextFabricCoverageDetailReuseAuxiliaryRefsStripped,
	ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt,
}

// ContextFabricCoverageDetailCodeCount is the vocabulary size as a
// compile-time constant.
const ContextFabricCoverageDetailCodeCount = len(contextFabricCoverageDetailCodes)

// ContextFabricCoverageDetailCodeVocabulary returns the closed code
// vocabulary in published order (array return = caller gets a copy).
func ContextFabricCoverageDetailCodeVocabulary() [ContextFabricCoverageDetailCodeCount]ContextFabricCoverageDetailCode {
	return contextFabricCoverageDetailCodes
}

func validCoverageDetailCode(code ContextFabricCoverageDetailCode) bool {
	for _, candidate := range contextFabricCoverageDetailCodes {
		if candidate == code {
			return true
		}
	}
	return false
}

// The three fact-scope vocabularies, mirrored onto the wire from
// internal/contextfabric's own closed declarations (FactScopeExpansionOutcome
// / FactScopePolicy / FactScopeBasis). internal/contextfabric carries a
// parity test asserting the two sets are EQUAL in both directions, so a new
// domain member cannot ship without its wire mirror (and its display label,
// context_fabric_display_labels.go).
var contextFabricFactScopeOutcomes = [...]string{
	"not_needed", "policy_unavailable", "attempted_empty",
	"target_kind_mismatch", "expanded", "expanded_partial", "failed",
	"matched_unauthorized",
}

var contextFabricFactScopePolicies = [...]string{
	"none",
	"project_work_item_repository_v1",
	"project_work_item_pull_request_v1",
	"project_work_item_pull_request_review_v1",
	"team_primary_attribution_repository_v1",
	"team_primary_attribution_pull_request_v1",
	"team_primary_attribution_pull_request_review_v1",
}

var contextFabricFactScopeBases = [...]string{
	"direct", "activity_proxy", "attributed_primary_team",
}

// ContextFabricFactScopeOutcomeVocabulary, -PolicyVocabulary and
// -BasisVocabulary return copies of the mirrored fact-scope vocabularies,
// for the domain-side parity tests.
func ContextFabricFactScopeOutcomeVocabulary() [len(contextFabricFactScopeOutcomes)]string {
	return contextFabricFactScopeOutcomes
}

func ContextFabricFactScopePolicyVocabulary() [len(contextFabricFactScopePolicies)]string {
	return contextFabricFactScopePolicies
}

func ContextFabricFactScopeBasisVocabulary() [len(contextFabricFactScopeBases)]string {
	return contextFabricFactScopeBases
}

func stringInVocabulary(value string, vocabulary []string) bool {
	for _, candidate := range vocabulary {
		if candidate == value {
			return true
		}
	}
	return false
}

// Bounds for the three detail strings. DetailID is engine-minted and
// ordinal; Label is the deterministic fail-closed floor and the ONLY place
// a quantity is put into words; Phrasing is the optional synthesis-authored
// sentence (guarded in internal/contextfabric — ref closure, digit ban,
// whole-set discard); Raw restates the legacy composed string and shares
// its bound.
const (
	ContextFabricCoverageDetailIDMaxLength       = 64
	ContextFabricCoverageDetailLabelMaxLength    = 160
	ContextFabricCoverageDetailPhrasingMaxLength = 400
	ContextFabricCoverageDetailRawMaxLength      = ContextFabricCoverageDegradedReasonMaxLength
	// contextFabricCoverageDetailKindsMaxCount bounds the two subject-kind
	// arrays (supported_kinds/skipped_kinds) — comfortably above the closed
	// SubjectKind vocabulary size, so a legal detail can never hit it.
	contextFabricCoverageDetailKindsMaxCount = 32
)

// ContextFabricCoverageDetail is one structured coverage reason — the
// CHAOS-4690 replacement for parsing coverage.degraded_reasons[] /
// coverage.sources[].reason strings in a consumer. Additive and optional:
// absent on every result written before this field existed.
type ContextFabricCoverageDetail struct {
	// DetailID is the engine-minted ordinal id ("cov-01", ...) assigned
	// after the deterministic coverage merge; the ref key synthesis
	// phrasing closes over.
	DetailID string `json:"detail_id"`
	// Source matches the sibling SourceObservation.Source string, so a
	// consumer associates detail and source chip without parsing either.
	Source string                          `json:"source"`
	Code   ContextFabricCoverageDetailCode `json:"code"`
	// Degrading mirrors the PRODUCER's own branch (for canonical-fact rows,
	// exactly contextfabric.factStateDegrades) — never a second
	// code→degrading table that could disagree with the string path. The
	// degrading details' Raw strings, in order, ARE degraded_reasons[].
	Degrading bool `json:"degrading"`

	// Closed-vocabulary parameters, present per producer branch. Which
	// fields a code requires/allows is enforced by the write path
	// (validateCoverageDetail's per-code table).
	FactKind       ContextFabricFactKind      `json:"fact_kind,omitempty"`
	SourceState    ContextFabricSourceState   `json:"source_state,omitempty"`
	ScopeOutcome   string                     `json:"scope_outcome,omitempty"`
	OriginKind     ContextFabricSubjectKind   `json:"origin_kind,omitempty"`
	SupportedKinds []ContextFabricSubjectKind `json:"supported_kinds,omitempty"`
	SkippedKinds   []ContextFabricSubjectKind `json:"skipped_kinds,omitempty"`
	Policy         string                     `json:"policy,omitempty"`
	Basis          string                     `json:"basis,omitempty"`
	Count          *int                       `json:"count,omitempty"`
	Narrowed       bool                       `json:"narrowed,omitempty"`

	// Label is REQUIRED: the server-composed terse plain-language phrase
	// (ComposeCoverageDetailLabel) — what renders when no Phrasing exists.
	Label string `json:"label"`
	// Phrasing is OPTIONAL: the synthesis-authored user-language sentence
	// for THIS detail, guard-verified upstream. Rendered beside/under the
	// Label, never replacing it.
	Phrasing string `json:"phrasing,omitempty"`
	// Raw is the exact legacy composed string this detail structurally
	// restates. Collapsed-Details rendering only; also the derivation
	// anchor for degraded_reasons[].
	Raw string `json:"raw,omitempty"`
}

// coverageDetailFieldRule is one row of the code-conditioned field table:
// which parameter fields a code REQUIRES and which it additionally ALLOWS.
// A field neither required nor allowed must be absent (zero) — a detail
// whose fields do not fit its code is unrepresentable on a fresh write.
type coverageDetailFieldRule struct {
	requireFactKind  bool
	allowFactKind    bool
	requireScope     bool // ScopeOutcome + OriginKind + Policy + Basis
	requireSkipped   bool // Narrowed + SkippedKinds
	allowNarrowed    bool // Narrowed + SkippedKinds may ride along
	allowSourceState bool
	allowSupported   bool
	requireCount     bool
	allowCount       bool
}

var coverageDetailFieldRules = map[ContextFabricCoverageDetailCode]coverageDetailFieldRule{
	ContextFabricCoverageDetailFactUnconfigured: {
		requireFactKind: true, allowFactKind: true, allowSourceState: true,
	},
	ContextFabricCoverageDetailFactScopeUnexpanded: {
		requireFactKind: true, allowFactKind: true, requireScope: true,
		allowNarrowed: true, allowSourceState: true, allowSupported: true, allowCount: true,
	},
	ContextFabricCoverageDetailFactReadFailed: {
		requireFactKind: true, allowFactKind: true, allowNarrowed: true,
		allowSourceState: true, allowSupported: true,
	},
	ContextFabricCoverageDetailFactProviderReported: {
		requireFactKind: true, allowFactKind: true, allowNarrowed: true,
		allowSourceState: true, allowSupported: true,
	},
	ContextFabricCoverageDetailFactPruned: {
		requireFactKind: true, allowFactKind: true, allowSourceState: true, allowSupported: true,
	},
	ContextFabricCoverageDetailFactNarrowed: {
		requireFactKind: true, allowFactKind: true, requireSkipped: true,
		allowNarrowed: true, allowSourceState: true, allowSupported: true, allowCount: true,
	},
	ContextFabricCoverageDetailGraphEndpointLookupFailed:         {requireCount: true, allowCount: true},
	ContextFabricCoverageDetailGraphExactNameCandidatesTruncated: {},
	// No fact kind and no count: this code is about the TURN ending, not
	// about any one fact or any countable set. Every other allowance stays
	// off, so a row cannot decorate it with a fact it never read.
	ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt:    {},
	ContextFabricCoverageDetailGraphCohortDeniedByAuthorization: {requireCount: true, allowCount: true},
	ContextFabricCoverageDetailGraphUnknownRelationshipType:     {requireCount: true, allowCount: true},
	ContextFabricCoverageDetailGraphValidityUnbounded:           {requireCount: true, allowCount: true},
	ContextFabricCoverageDetailReuseAuxiliaryRefsStripped:       {requireCount: true, allowCount: true},
}

// coverageDetailCodeDegrades declares, per the settled design, which codes
// are NEVER degrading regardless of producer state (parity with the string
// path: fact_pruned and graph_validity_unbounded never enter
// degraded_reasons; every other code's degrading bit mirrors its producer
// branch). Used only to reject an impossible combination on write.
func coverageDetailCodeMayDegrade(code ContextFabricCoverageDetailCode) bool {
	switch code {
	case ContextFabricCoverageDetailFactPruned, ContextFabricCoverageDetailGraphValidityUnbounded:
		return false
	default:
		return true
	}
}

// Validate enforces the current contract bounds for one coverage detail
// (write path — stored rows are immutable and skip this via validateStored).
func (d ContextFabricCoverageDetail) Validate() error {
	if !stringLengthBetween(d.DetailID, 1, ContextFabricCoverageDetailIDMaxLength) || strings.TrimSpace(d.DetailID) != d.DetailID {
		return fmt.Errorf("coverage detail id violates v1 bounds")
	}
	if !stringLengthBetween(strings.TrimSpace(d.Source), 1, 128) {
		return fmt.Errorf("coverage detail source violates v1 bounds")
	}
	if !validCoverageDetailCode(d.Code) {
		return fmt.Errorf("coverage detail code %q is not in the closed vocabulary", d.Code)
	}
	if d.Degrading && !coverageDetailCodeMayDegrade(d.Code) {
		return fmt.Errorf("coverage detail code %q can never degrade", d.Code)
	}
	if !stringLengthBetween(strings.TrimSpace(d.Label), 1, ContextFabricCoverageDetailLabelMaxLength) {
		return fmt.Errorf("coverage detail label violates v1 bounds")
	}
	if !stringLengthBetween(d.Phrasing, 0, ContextFabricCoverageDetailPhrasingMaxLength) || strings.TrimSpace(d.Phrasing) != d.Phrasing {
		return fmt.Errorf("coverage detail phrasing violates v1 bounds")
	}
	if !stringLengthBetween(d.Raw, 0, ContextFabricCoverageDetailRawMaxLength) {
		return fmt.Errorf("coverage detail raw violates v1 bounds")
	}
	if d.FactKind != "" && !validFactKind(d.FactKind) {
		return fmt.Errorf("coverage detail fact kind %q is invalid", d.FactKind)
	}
	if d.SourceState != "" && !validSourceState(d.SourceState) {
		return fmt.Errorf("coverage detail source state %q is invalid", d.SourceState)
	}
	if d.ScopeOutcome != "" && !stringInVocabulary(d.ScopeOutcome, contextFabricFactScopeOutcomes[:]) {
		return fmt.Errorf("coverage detail scope outcome %q is invalid", d.ScopeOutcome)
	}
	if d.Policy != "" && !stringInVocabulary(d.Policy, contextFabricFactScopePolicies[:]) {
		return fmt.Errorf("coverage detail policy %q is invalid", d.Policy)
	}
	if d.Basis != "" && !stringInVocabulary(d.Basis, contextFabricFactScopeBases[:]) {
		return fmt.Errorf("coverage detail basis %q is invalid", d.Basis)
	}
	if d.OriginKind != "" && !validContextFabricSubjectKind(d.OriginKind) {
		return fmt.Errorf("coverage detail origin kind %q is invalid", d.OriginKind)
	}
	if len(d.SupportedKinds) > contextFabricCoverageDetailKindsMaxCount || len(d.SkippedKinds) > contextFabricCoverageDetailKindsMaxCount {
		return fmt.Errorf("coverage detail kind arrays violate v1 bounds")
	}
	for _, kind := range d.SupportedKinds {
		if !validContextFabricSubjectKind(kind) {
			return fmt.Errorf("coverage detail supported kind %q is invalid", kind)
		}
	}
	for _, kind := range d.SkippedKinds {
		if !validContextFabricSubjectKind(kind) {
			return fmt.Errorf("coverage detail skipped kind %q is invalid", kind)
		}
	}
	if d.Count != nil && *d.Count < 0 {
		return fmt.Errorf("coverage detail count must be non-negative")
	}

	rule, ok := coverageDetailFieldRules[d.Code]
	if !ok {
		return fmt.Errorf("coverage detail code %q has no field rule", d.Code)
	}
	if rule.requireFactKind && d.FactKind == "" {
		return fmt.Errorf("coverage detail code %q requires fact_kind", d.Code)
	}
	if !rule.allowFactKind && d.FactKind != "" {
		return fmt.Errorf("coverage detail code %q forbids fact_kind", d.Code)
	}
	scopeSet := d.ScopeOutcome != "" || d.OriginKind != "" || d.Policy != "" || d.Basis != ""
	if rule.requireScope && (d.ScopeOutcome == "" || d.OriginKind == "" || d.Policy == "" || d.Basis == "") {
		return fmt.Errorf("coverage detail code %q requires scope_outcome/origin_kind/policy/basis together", d.Code)
	}
	if !rule.requireScope && scopeSet {
		return fmt.Errorf("coverage detail code %q forbids scope fields", d.Code)
	}
	narrowedSet := d.Narrowed || len(d.SkippedKinds) > 0
	if rule.requireSkipped && (!d.Narrowed || len(d.SkippedKinds) == 0) {
		return fmt.Errorf("coverage detail code %q requires narrowed with skipped_kinds", d.Code)
	}
	if !rule.requireSkipped && !rule.allowNarrowed && narrowedSet {
		return fmt.Errorf("coverage detail code %q forbids narrowing fields", d.Code)
	}
	if narrowedSet && (!d.Narrowed || len(d.SkippedKinds) == 0) {
		return fmt.Errorf("coverage detail narrowing requires narrowed AND skipped_kinds together")
	}
	if !rule.allowSourceState && d.SourceState != "" {
		return fmt.Errorf("coverage detail code %q forbids source_state", d.Code)
	}
	if !rule.allowSupported && len(d.SupportedKinds) > 0 {
		return fmt.Errorf("coverage detail code %q forbids supported_kinds", d.Code)
	}
	if rule.requireCount && d.Count == nil {
		return fmt.Errorf("coverage detail code %q requires count", d.Code)
	}
	if !rule.requireCount && !rule.allowCount && d.Count != nil {
		return fmt.Errorf("coverage detail code %q forbids count", d.Code)
	}
	return nil
}

// validateCoverageDetails is the write-path check over the whole Details
// array: per-detail bounds, unique ids, count bound, and the dual-write
// derivation invariant — the degrading details' Raw strings, in order, must
// equal degraded_reasons[] exactly. That invariant is what makes drift
// between the structured form and the composed strings unrepresentable
// while both ship (settled design §3.4).
func validateCoverageDetails(details []ContextFabricCoverageDetail, degradedReasons []string, maxEntries int) error {
	if len(details) > maxEntries {
		return fmt.Errorf("coverage details exceed the entry bound")
	}
	seen := make(map[string]struct{}, len(details))
	degrading := make([]string, 0, len(details))
	for _, detail := range details {
		if err := detail.Validate(); err != nil {
			return err
		}
		if _, exists := seen[detail.DetailID]; exists {
			return fmt.Errorf("coverage detail ids must be unique")
		}
		seen[detail.DetailID] = struct{}{}
		if detail.Degrading {
			if strings.TrimSpace(detail.Raw) == "" {
				return fmt.Errorf("degrading coverage detail requires raw")
			}
			degrading = append(degrading, detail.Raw)
		}
	}
	if len(degrading) != len(degradedReasons) {
		return fmt.Errorf("degrading coverage details (%d) must pair 1:1 with degraded_reasons (%d)", len(degrading), len(degradedReasons))
	}
	for i, raw := range degrading {
		if raw != degradedReasons[i] {
			return fmt.Errorf("coverage detail raw %q does not match degraded_reasons[%d]", raw, i)
		}
	}
	return nil
}

// ContextFabricEvidenceRefClosure returns every distinct evidence ref id
// reachable on the result — the SAME sites synthesis grounding admits
// (result-level refs, drivers, findings, cohort members, paths and their
// edges, candidates). It is the set ContextFabricInvestigationResult.
// EvidenceRefLabels must key exactly on a fresh write, and the set the
// engine composes that map over.
func ContextFabricEvidenceRefClosure(r ContextFabricInvestigationResult) map[string]struct{} {
	closure := make(map[string]struct{})
	add := func(refs []string) {
		for _, ref := range refs {
			closure[ref] = struct{}{}
		}
	}
	add(r.EvidenceRefIDs)
	for _, driver := range r.Drivers {
		add(driver.EvidenceRefIDs)
	}
	for _, findings := range [][]ContextFabricFinding{r.RemainingWork, r.ReadinessGaps, r.Conflicts} {
		for _, finding := range findings {
			add(finding.EvidenceRefIDs)
		}
	}
	for _, path := range r.Paths {
		add(path.EvidenceRefIDs)
		for _, edge := range path.Edges {
			add(edge.EvidenceRefIDs)
		}
	}
	if r.Cohort != nil {
		for _, member := range r.Cohort.Members {
			add(member.EvidenceRefIDs)
		}
	}
	for _, candidate := range r.SubjectResolution.Candidates {
		add(candidate.EvidenceRefIDs)
	}
	return closure
}

// validateEvidenceRefLabels enforces exact key equality between a non-nil
// EvidenceRefLabels map and the result's own evidence-ref closure, plus
// per-label bounds.
func validateEvidenceRefLabels(r ContextFabricInvestigationResult) error {
	closure := ContextFabricEvidenceRefClosure(r)
	if len(r.EvidenceRefLabels) != len(closure) {
		return fmt.Errorf("label map has %d entries, closure has %d", len(r.EvidenceRefLabels), len(closure))
	}
	for ref, label := range r.EvidenceRefLabels {
		if _, ok := closure[ref]; !ok {
			return fmt.Errorf("label for %q names no evidence ref on the result", ref)
		}
		if strings.TrimSpace(label) != label || !stringLengthBetween(label, 1, ContextFabricCoverageDetailLabelMaxLength) {
			return fmt.Errorf("label for %q violates v1 bounds", ref)
		}
	}
	return nil
}
