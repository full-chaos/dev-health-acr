package v1

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
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
	return c.validate(contextFabricWriteBounds)
}

func (c ContextFabricSubjectCandidate) validateStored() error {
	return c.validate(contextFabricLegacyBounds)
}

func (c ContextFabricSubjectCandidate) validate(bounds contextFabricBounds) error {
	if !stringLengthBetween(c.ReceiptID, 8, 256) || !validResolutionState(c.State) || c.Confidence < 0 || c.Confidence > 1 {
		return fmt.Errorf("subject candidate identity, state, or confidence violates v1 bounds")
	}
	if err := c.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if len(c.MatchedTerms) > bounds.matchedTerms || len(c.MatchReasons) < 1 || len(c.MatchReasons) > bounds.matchReasons || !uniqueTrimmedStrings(c.MatchedTerms, bounds.matchedTermLength) || !uniqueTrimmedStrings(c.MatchReasons, bounds.matchReasonLength) {
		return fmt.Errorf("subject candidate match metadata violates v1 bounds")
	}
	if !validMatchMechanisms(c.MatchMechanisms) {
		return fmt.Errorf("subject candidate match mechanisms violate v1 bounds")
	}
	if !optionalEvidenceRefs(c.EvidenceRefIDs, bounds.candidateEvidenceRefs) {
		return fmt.Errorf("subject candidate evidence references violate v1 bounds")
	}
	return nil
}

func (r ContextFabricSubjectResolution) Validate() error {
	return r.validate(contextFabricWriteBounds)
}

func (r ContextFabricSubjectResolution) validate(bounds contextFabricBounds) error {
	if r.Candidates == nil || r.Committed == nil || len(r.Candidates) > 50 || len(r.Committed) > 250 {
		return fmt.Errorf("subject resolution arrays violate v1 bounds")
	}
	seenReceipts := make(map[string]struct{}, len(r.Candidates))
	for _, candidate := range r.Candidates {
		if err := candidate.validate(bounds); err != nil {
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
	// CommitDecisionDigests (CHAOS-4087) is additive-optional: nil is
	// valid (a resolution engine that predates this field, or a resolution
	// with nothing committed) and is the ONLY way to omit it -- once
	// present, its own doc comment's "one entry per committed subject"
	// claim is a hard invariant engine.go's own stamping logic always
	// satisfies, so a mismatch here is a real defect, not a forward-compat
	// gap to tolerate.
	if r.CommitDecisionDigests != nil {
		if len(r.CommitDecisionDigests) != len(r.Committed) || len(r.CommitDecisionDigests) > 250 {
			return fmt.Errorf("commit decision digests must have exactly one entry per committed subject")
		}
		committedSet := make(map[string]struct{}, len(r.Committed))
		for _, subject := range r.Committed {
			committedSet[subjectKey(subject)] = struct{}{}
		}
		// seenDigestSubjects (codex R1 finding): the count-and-membership
		// checks above accept two digests for the SAME committed subject and
		// none for another, since neither check requires the digest subjects
		// to be DISTINCT. Combined with r.Committed's own uniqueSubjects
		// guarantee above, rejecting a repeat here plus the count match
		// above forces a true bijection -- every committed subject gets
		// EXACTLY one digest, never a silently-skipped one reading as
		// fail-closed-unrecorded because a duplicate consumed its slot.
		seenDigestSubjects := make(map[string]struct{}, len(r.CommitDecisionDigests))
		for _, digest := range r.CommitDecisionDigests {
			if err := digest.Validate(); err != nil {
				return fmt.Errorf("commit decision digests: %w", err)
			}
			key := subjectKey(digest.Subject)
			if _, ok := committedSet[key]; !ok {
				return fmt.Errorf("commit decision digest names a subject not in committed")
			}
			if _, ok := seenDigestSubjects[key]; ok {
				return fmt.Errorf("commit decision digest names the same committed subject more than once")
			}
			seenDigestSubjects[key] = struct{}{}
		}
	}
	if !stringLengthBetween(r.ClarificationPrompt, 0, 2000) || strings.TrimSpace(r.ClarificationPrompt) != r.ClarificationPrompt {
		return fmt.Errorf("clarification prompt violates v1 bounds")
	}
	// PriorSubjectReceiptDispositions (CHAOS-3478/CHAOS-3813) is
	// additive-optional exactly like CommitDecisionDigests above: nil is
	// valid and the ONLY way to omit it. Once present, bounded to the same
	// 20-receipt cap validateStructureReceiptField already enforces on the
	// request side (a response can never echo more than the request could
	// have carried) and each entry must itself be well-formed.
	if r.PriorSubjectReceiptDispositions != nil {
		if len(r.PriorSubjectReceiptDispositions) > 20 {
			return fmt.Errorf("prior subject receipt dispositions exceed v1 bounds")
		}
		for _, entry := range r.PriorSubjectReceiptDispositions {
			if err := entry.Validate(); err != nil {
				return fmt.Errorf("prior subject receipt dispositions: %w", err)
			}
		}
	}
	return nil
}

// Validate enforces CHAOS-4087's own bounds on one commit-decision digest
// entry: a structurally valid Subject, a recognized (possibly empty, the
// fail-closed "unrecorded" reading) CommitGate, and an IdentityProven value
// that gate could actually have produced (codex R1 finding: an
// unconstrained bool let a statistical gate like lone_floor claim a proven
// identity, which a trusting consumer would misclassify).
func (d ContextFabricCommitDecisionDigest) Validate() error {
	if err := d.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if !validCommitGate(d.CommitGate) {
		return fmt.Errorf("commit gate is not a recognized value")
	}
	if !validCommitDecisionDigestIdentityProven(d.CommitGate, d.IdentityProven) {
		return fmt.Errorf("identity proven is not consistent with the commit gate")
	}
	return nil
}

// Validate enforces the current contract bounds (write path).
func (c ContextFabricCohort) Validate() error {
	return c.validate(contextFabricWriteBounds)
}

// validateStored enforces the legacy bounds for an already-persisted row.
func (c ContextFabricCohort) validateStored() error {
	return c.validate(contextFabricLegacyBounds)
}

func (c ContextFabricCohort) validate(bounds contextFabricBounds) error {
	if !validContextFabricSubjectKind(c.Kind) || (c.Kind != ContextFabricSubjectTeam && c.Kind != ContextFabricSubjectProject) || c.Members == nil || len(c.Members) > 250 || len(c.Exclusions) > 250 || !boundedText(c.Rationale, 1, 4000, bounds) || (c.Complete && c.Truncated) {
		return fmt.Errorf("cohort violates v1 bounds")
	}
	seen := make(map[string]struct{}, len(c.Members))
	lastRank := 0
	// attentionRanks (codex round-2 finding): RankCohort assigns AttentionRank
	// as a DENSE 1..N ranking over every member it ranks -- checked here,
	// across all members, since a per-member validate() call cannot see
	// its siblings.
	attentionRanks := make(map[int]struct{}, len(c.Members))
	rankedCount := 0
	for _, member := range c.Members {
		if err := member.validate(bounds); err != nil {
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
		if member.RankingComputed {
			if _, exists := attentionRanks[member.AttentionRank]; exists {
				return fmt.Errorf("cohort member attention ranks must be unique")
			}
			attentionRanks[member.AttentionRank] = struct{}{}
			rankedCount++
		}
	}
	for rank := 1; rank <= rankedCount; rank++ {
		if _, ok := attentionRanks[rank]; !ok {
			return fmt.Errorf("cohort member attention ranks must be a dense 1..N sequence over every ranked member")
		}
	}
	for _, exclusion := range c.Exclusions {
		if err := exclusion.validate(bounds); err != nil {
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

// contextFabricBounds carries every numeric bound where the Go validator
// and the published JSON Schema disagreed historically.
//
// Two sets exist because investigation results are IMMUTABLE. The published
// schema is the wire-contract source of truth, so WRITES must enforce it or
// the service emits documents that violate its own contract. But a row
// written by an older, looser binary cannot be rewritten, so a read path
// enforcing the corrected bound would make that row permanently unreadable
// -- an API 500 and an MCP retrieval failure for data that was correct when
// it was written.
//
// So: strict at write and at model-output acceptance, lenient at every read
// of persisted data. The looseness cannot grow, because no NEW row can be
// created at a legacy size.
//
// Every NUMERIC field here corresponds to a schema bound that
// TestSchemaAndGoBoundsAgree checks, so this struct cannot quietly drift from
// the contract again. closedFindingKinds is the one non-numeric member: a
// vocabulary gate rather than a size, carried here because bounds is already
// the channel that distinguishes a write from a stored read, and adding a
// second leniency channel would leave two places to consult.
type contextFabricBounds struct {
	// closedFindingKinds enforces the driver-category vocabulary on
	// ContextFabricFinding.Kind. TRUE on writes, FALSE on stored reads
	// (codex round-12): the write path never enforced this, so a stored row
	// may legitimately carry an out-of-vocabulary kind, and those rows are
	// immutable -- tightening the read path would make real answers
	// permanently unreadable to fix a defect that only new writes can
	// introduce.
	closedFindingKinds bool
	// rawTextLength measures a bounded text field's length on the RAW value
	// rather than the trimmed one. TRUE on writes, FALSE on stored reads
	// (codex round-13 F2). Every bounded text field was measured after
	// TrimSpace, so a value padded past the schema maximum -- raw 130,
	// trimmed 128 -- validated while being schema-invalid.
	//
	// Reads stay on the trimmed basis because the pre-branch write validator
	// ALSO trimmed (merge-base 81ac259b, validate_context_fabric_result.go:181,
	// a form dating to cd9b338/CHAOS-3770), so padded rows were legally
	// writable for the whole life of these fields and may exist in immutable
	// storage. Rejecting them on read would break reading data the service
	// itself accepted.
	rawTextLength bool

	cohortInclusionReasons      int
	cohortInclusionReasonLength int
	narrativeCount              int
	narrativeLength             int
	coverageEntries             int
	matchedTerms                int
	matchedTermLength           int
	matchReasons                int
	matchReasonLength           int
	pathEvidenceRefs            int
	pathWhyRelevantLength       int
	factParameterValueLength    int
	judgmentLength              int
	deterministicAnswerLength   int
	nestedEvidenceRefs          int
	interpretationTerms         int
	candidateEvidenceRefs       int
	cohortExclusionReasonLength int
	memberEvidenceRefs          int
	// cohortMemberRankingBasis/-Length bound CHAOS-4398's new RankingBasis
	// field. Both bound sets share ONE value: RankingBasis is a brand-new
	// field with no legacy persisted rows to stay lenient for (unlike every
	// other pair above, which predates this bounds split).
	cohortMemberRankingBasis       int
	cohortMemberRankingBasisLength int
	// cohortMemberDrivers/-ThresholdLabels bound CHAOS-4398 PR2's new
	// Drivers field. cohortMemberDrivers is 5 (one per signal family, never
	// more); cohortMemberDriverThresholdLabels is 4 (investment_mix's own
	// maximum sub-label count -- the only family with any today).
	cohortMemberDrivers               int
	cohortMemberDriverThresholdLabels int
	// cohortMemberDriversRequired is true on the write path only: PR1
	// (RankingBasis, no Drivers) already shipped and persisted real rows,
	// so a row read back via validateStored must tolerate RankingBasis
	// naming a family with no matching Drivers entry -- Drivers is PR2's
	// own brand-new field, unlike RankingBasis, which had no prior shape
	// to stay lenient for. A freshly produced row (Validate(), write
	// bounds) has no such excuse: RankCohort always computes both
	// together, so the write path enforces the full correspondence +
	// Sum(WeightContributed)==Score invariant.
	cohortMemberDriversRequired bool
	// cohortMemberOutcomeRequired (CHAOS-4398 PR3) is the SAME write-only
	// split as cohortMemberDriversRequired above, for Outcome/
	// MissingSignals -- PR1/PR2-era persisted rows never had them.
	cohortMemberOutcomeRequired bool
	// cohortMemberMissingSignals bounds MissingSignals -- 5 (one per
	// signal family, never more), same one-value-for-both-bound-sets
	// reasoning as cohortMemberDrivers.
	cohortMemberMissingSignals int
	// cohortMemberDriverConcentrationRequired (CHAOS-4398 PR3, codex R1)
	// is the SAME write-only split as cohortMemberOutcomeRequired above:
	// PR2 already shipped and persisted real investment_mix driver rows
	// before Concentration/ConcentrationMethod existed, so a row read back
	// via validateStored must tolerate an investment_mix driver with
	// neither field. A freshly produced row (Validate(), write bounds) has
	// no such excuse -- investmentMixSignal always sets both together.
	cohortMemberDriverConcentrationRequired bool
}

// contextFabricRelationshipPathMaxNodes is the Go-enforced ceiling on path
// length. The published schema advertised 64 while Go rejected anything
// above this, so the contract promised what the service refused; the schema
// moved to this number rather than the reverse, because nothing the service
// actually produces changes -- the contract simply becomes honest.
const contextFabricRelationshipPathMaxNodes = 51

// contextFabricWriteBounds matches the published JSON Schema exactly.
var contextFabricWriteBounds = contextFabricBounds{
	closedFindingKinds:                      true,
	rawTextLength:                           true,
	cohortInclusionReasons:                  32,
	cohortInclusionReasonLength:             1000,
	narrativeCount:                          ContextFabricLimitationsMaxCount,
	narrativeLength:                         ContextFabricLimitationMaxLength,
	coverageEntries:                         100,
	matchedTerms:                            32,
	matchedTermLength:                       512,
	matchReasons:                            32,
	matchReasonLength:                       1000,
	pathEvidenceRefs:                        200,
	pathWhyRelevantLength:                   2000,
	factParameterValueLength:                ContextFabricFactRequirementParameterValueMaxLength,
	judgmentLength:                          ContextFabricDirectJudgmentMaxLength,
	deterministicAnswerLength:               ContextFabricDeterministicAnswerMaxLength,
	nestedEvidenceRefs:                      ContextFabricNestedEvidenceRefIDsMaxCount,
	interpretationTerms:                     ContextFabricSubjectTermsMaxCount,
	candidateEvidenceRefs:                   100,
	cohortExclusionReasonLength:             1000,
	memberEvidenceRefs:                      100,
	cohortMemberRankingBasis:                16,
	cohortMemberRankingBasisLength:          128,
	cohortMemberDrivers:                     5,
	cohortMemberDriverThresholdLabels:       4,
	cohortMemberDriversRequired:             true,
	cohortMemberOutcomeRequired:             true,
	cohortMemberMissingSignals:              5,
	cohortMemberDriverConcentrationRequired: true,
}

// contextFabricLegacyBounds is what the Go validator alone used to accept.
// It exists ONLY so already-persisted rows stay readable.
var contextFabricLegacyBounds = contextFabricBounds{
	closedFindingKinds:                false,
	rawTextLength:                     false,
	cohortInclusionReasons:            50,
	cohortInclusionReasonLength:       1024,
	narrativeCount:                    250,
	narrativeLength:                   4000,
	coverageEntries:                   250,
	matchedTerms:                      100,
	matchedTermLength:                 512,
	matchReasons:                      100,
	matchReasonLength:                 1024,
	pathEvidenceRefs:                  500,
	pathWhyRelevantLength:             4000,
	factParameterValueLength:          1024,
	judgmentLength:                    8000,
	deterministicAnswerLength:         16000,
	nestedEvidenceRefs:                ContextFabricEvidenceRefIDsMaxCount,
	interpretationTerms:               100,
	candidateEvidenceRefs:             500,
	cohortExclusionReasonLength:       2000,
	memberEvidenceRefs:                500,
	cohortMemberRankingBasis:          16,
	cohortMemberRankingBasisLength:    128,
	cohortMemberDrivers:               5,
	cohortMemberDriverThresholdLabels: 4,
	cohortMemberDriversRequired:       false,
	cohortMemberOutcomeRequired:       false,
	cohortMemberMissingSignals:        5,
}

// Validate enforces the CURRENT contract bounds. This is the write path and
// the path every freshly produced result takes.
func (m ContextFabricCohortMember) Validate() error {
	return m.validate(contextFabricWriteBounds)
}

// validateStored enforces the LEGACY bounds, for revalidating a row that
// was already persisted. See the bounds block above for why the read path
// deliberately accepts more than the write path.
func (m ContextFabricCohortMember) validateStored() error {
	return m.validate(contextFabricLegacyBounds)
}

func (m ContextFabricCohortMember) validate(bounds contextFabricBounds) error {
	if err := m.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if m.Rank < 1 || len(m.InclusionReasons) < 1 || len(m.InclusionReasons) > bounds.cohortInclusionReasons || !uniqueTrimmedStrings(m.InclusionReasons, bounds.cohortInclusionReasonLength) || !optionalEvidenceRefs(m.EvidenceRefIDs, bounds.memberEvidenceRefs) {
		return fmt.Errorf("cohort member violates v1 bounds")
	}
	// RankingComputed is the explicit disambiguator (subject-model-and-
	// cohort-answers.md §4): false means ranking has not run, and every
	// other ranking field must then be absent/zero -- a Score/AttentionRank/
	// DataCompleteness present without RankingComputed would be exactly the
	// "was this really scored, or does it just look like it" ambiguity a
	// bare omitempty float64 could never resolve. True means all three are
	// REQUIRED together; RankingBasis alone may still be empty (zero
	// available signal families -- "nothing contributed", not a producer
	// bug).
	if !m.RankingComputed {
		if m.Score != nil || m.AttentionRank != 0 || m.DataCompleteness != "" || len(m.RankingBasis) > 0 || len(m.Drivers) > 0 || m.Outcome != "" || len(m.MissingSignals) > 0 {
			return fmt.Errorf("cohort member ranking fields set without ranking_computed")
		}
		return nil
	}
	// Score may be nil: the design's own zero-signal-family exception
	// (subject-model-and-cohort-answers.md §5b) -- an empty weight
	// denominator cannot be honestly turned into a number, and scoring it
	// 0 would render the least-observed member as the healthiest. A
	// present Score must still be a FINITE value in [0,100] -- codex
	// review: `< 0`/`> 100` alone both evaluate false for NaN, which would
	// otherwise pass through unrejected.
	if m.Score != nil && (math.IsNaN(*m.Score) || math.IsInf(*m.Score, 0) || *m.Score < 0 || *m.Score > 100) {
		return fmt.Errorf("cohort member score violates v1 bounds")
	}
	if m.AttentionRank < 1 {
		return fmt.Errorf("cohort member attention rank violates v1 bounds")
	}
	if !validContextFabricCohortDataCompleteness(m.DataCompleteness) || m.DataCompleteness == "" {
		return fmt.Errorf("cohort member data completeness is not a recognized value")
	}
	if m.Score == nil && (len(m.RankingBasis) > 0 || len(m.Drivers) > 0) {
		return fmt.Errorf("cohort member nil score must be paired with an empty ranking basis and no drivers")
	}
	// Outcome/MissingSignals (CHAOS-4398 PR3, design doc §8) are REQUIRED
	// whenever RankingComputed on the WRITE path -- a PR1/PR2-era
	// persisted row (Outcome never existed yet) must stay readable on the
	// legacy path, the SAME cohortMemberDriversRequired-style split
	// Drivers already uses; gated on Outcome's own presence too, so a
	// PR3-written row re-validated via the legacy path is still checked
	// fully (it was written under the strict rule, so it always passes).
	if bounds.cohortMemberOutcomeRequired || m.Outcome != "" {
		if !validContextFabricCohortMemberOutcome(m.Outcome) || m.Outcome == "" {
			return fmt.Errorf("cohort member outcome is not a recognized value")
		}
		// Score is present iff Outcome is qualified/provisional, nil iff
		// insufficient_evidence/not_applicable -- Outcome is the field a
		// consumer reads to know WHY a member has no Score, not
		// DataCompleteness (a pure data-availability measure, independent
		// of Outcome: a degraded-completeness member CAN still be
		// qualified/provisional with a real Score, e.g. investment_mix +
		// health alone clears the threshold with only 2 of 5 families).
		scoredOutcome := m.Outcome == ContextFabricCohortOutcomeQualified || m.Outcome == ContextFabricCohortOutcomeProvisional
		if (m.Score != nil) != scoredOutcome {
			return fmt.Errorf("cohort member score presence does not match outcome")
		}
		// MissingSignals is empty iff Outcome is qualified (nothing
		// missing); non-empty for every other Outcome (qualification
		// requires ALL 5 families, so anything short of qualified has left
		// at least one out).
		if (len(m.MissingSignals) == 0) != (m.Outcome == ContextFabricCohortOutcomeQualified) {
			return fmt.Errorf("cohort member missing_signals presence does not match outcome")
		}
	}
	if len(m.MissingSignals) > bounds.cohortMemberMissingSignals || !uniqueTrimmedStrings(m.MissingSignals, bounds.cohortMemberRankingBasisLength) {
		return fmt.Errorf("cohort member missing signals violate v1 bounds")
	}
	for _, entry := range m.MissingSignals {
		if _, isFamily := contextFabricCohortMemberDriverWeights[entry]; !isFamily {
			return fmt.Errorf("cohort member missing signal is not a recognized family name")
		}
	}
	if len(m.RankingBasis) > bounds.cohortMemberRankingBasis || !uniqueTrimmedStrings(m.RankingBasis, bounds.cohortMemberRankingBasisLength) {
		return fmt.Errorf("cohort member ranking basis violates v1 bounds")
	}
	for _, entry := range m.RankingBasis {
		if !validContextFabricCohortRankingBasisLabel(entry) {
			return fmt.Errorf("cohort member ranking basis is not a recognized closed-vocabulary value")
		}
	}
	if err := m.validateDrivers(bounds); err != nil {
		return err
	}
	return nil
}

// validateDrivers enforces CHAOS-4398 PR2's traceability invariant: Drivers
// is present iff RankingComputed && Score != nil (the nil-Score case is
// already forced empty above), one entry per available signal family with
// NO duplicates, its signal set is EXACTLY the family-name subset of
// RankingBasis (never a driver for a family RankingBasis never named, never
// a RankingBasis family with no matching driver), and -- the whole point --
// Sum(WeightContributed) reconstructs *Score to within float64 rounding.
// This is what makes "no narration a human can't trace to a number" an
// enforced contract property, not a convention.
func (m ContextFabricCohortMember) validateDrivers(bounds contextFabricBounds) error {
	// A PR1-era persisted row (RankingBasis populated, Drivers never
	// existed yet) must stay readable on the legacy path -- see
	// contextFabricBounds.cohortMemberDriversRequired's own doc comment.
	// Nothing further to check: there is nothing present to be internally
	// inconsistent. Gated on Score alone (codex R1 finding 1), not on
	// RankingBasis being non-empty: RankCohort's own construction makes
	// Score!=nil and a non-empty RankingBasis the SAME condition (both
	// derive from "at least one signal family was available"), but the
	// write-path check must not rely on a caller reproducing that
	// coincidence -- a member with a finite Score and an (incorrectly)
	// empty RankingBasis must still be rejected for missing Drivers.
	if len(m.Drivers) == 0 {
		if bounds.cohortMemberDriversRequired && m.Score != nil {
			return fmt.Errorf("cohort member has a score but no drivers")
		}
		return nil
	}
	if len(m.Drivers) > bounds.cohortMemberDrivers {
		return fmt.Errorf("cohort member drivers violate v1 bounds")
	}
	// basisFamilies/basisSubLabels split RankingBasis into its two entry
	// shapes: bare family names (e.g. "health.compounding_risk") and
	// investment_mix's own threshold sub-labels (e.g.
	// "investment_mix.reactive_share_high") -- codex R1 finding 3 found
	// the sub-label half was never cross-checked at all.
	basisFamilies := make(map[string]struct{}, len(m.RankingBasis))
	basisSubLabels := make(map[string]struct{}, len(m.RankingBasis))
	for _, entry := range m.RankingBasis {
		if _, isFamily := contextFabricCohortMemberDriverWeights[entry]; isFamily {
			basisFamilies[entry] = struct{}{}
		} else {
			basisSubLabels[entry] = struct{}{}
		}
	}

	seenSignals := make(map[string]struct{}, len(m.Drivers))
	var availableWeight float64
	for _, driver := range m.Drivers {
		if err := driver.validate(bounds); err != nil {
			return fmt.Errorf("cohort member drivers: %w", err)
		}
		if _, dup := seenSignals[driver.Signal]; dup {
			return fmt.Errorf("cohort member drivers must not repeat a signal family")
		}
		seenSignals[driver.Signal] = struct{}{}
		if _, inBasis := basisFamilies[driver.Signal]; !inBasis {
			return fmt.Errorf("cohort member driver names a signal family not present in ranking_basis")
		}
		// Codex R1 finding 4: only investment_mix's mix-shift sub-signal
		// makes a real prior-window comparison
		// (cohort_ranking.go's investmentMixSignal) -- every other family
		// always reads a single point in time.
		if driver.Window == ContextFabricCohortMemberDriverWindowCurrentVsPrior && driver.Signal != investmentMixSignalName {
			return fmt.Errorf("cohort member driver window current_vs_prior is only valid for investment_mix")
		}
		availableWeight += driver.Weight
	}
	// Present-but-partial Drivers (some basis families covered, others
	// not) is never valid, on EITHER path -- unlike total absence, this
	// shape could only come from a genuinely broken producer, never a
	// pre-PR2 row (which has none at all).
	for family := range basisFamilies {
		if _, hasDriver := seenSignals[family]; !hasDriver {
			return fmt.Errorf("cohort member ranking_basis names a signal family with no matching driver")
		}
	}

	// Second pass (needs availableWeight from the first): codex R1 finding
	// 2 -- checking only the AGGREGATE Sum(WeightContributed)==Score lets
	// compensating errors through (e.g. Value=0 with a nonzero
	// WeightContributed, offset by another driver's own error). Each
	// driver's WeightContributed must equal what scoreMember's formula
	// actually computes for it: 100*Weight*Value/availableWeight.
	claimedSubLabels := make(map[string]struct{}, len(m.RankingBasis))
	var weightContributedSum float64
	for _, driver := range m.Drivers {
		if availableWeight > 0 {
			expected := 100 * driver.Weight * driver.Value / availableWeight
			if math.Abs(driver.WeightContributed-expected) > 1e-6 {
				return fmt.Errorf("cohort member driver weight_contributed does not match its formula value")
			}
		}
		// Codex R3 finding 3: investmentMixSignal's Value is BY
		// CONSTRUCTION the sum of exactly the sub-weights of whichever
		// ThresholdLabels fired -- a driver claiming a label without the
		// matching contribution in Value (or vice versa) is fabricated.
		// Codex R3 finding 1: mix_shift_* is mutually exclusive (at most
		// one fires) and REQUIRES Window==current_vs_prior when present
		// (the reverse is not required: a prior-window comparison can
		// legitimately find no shift worth a label).
		var subWeightSum float64
		var mixShiftLabels int
		for _, label := range driver.ThresholdLabels {
			if _, claimed := basisSubLabels[label]; !claimed {
				return fmt.Errorf("cohort member driver threshold label is not present in ranking_basis")
			}
			claimedSubLabels[label] = struct{}{}
			subWeightSum += contextFabricInvestmentMixSubWeights[label]
			if _, isMixShift := contextFabricMixShiftLabels[label]; isMixShift {
				mixShiftLabels++
			}
		}
		if mixShiftLabels > 1 {
			return fmt.Errorf("cohort member driver claims more than one mutually-exclusive mix_shift label")
		}
		if mixShiftLabels == 1 && driver.Window != ContextFabricCohortMemberDriverWindowCurrentVsPrior {
			return fmt.Errorf("cohort member driver claims a mix_shift label without window current_vs_prior")
		}
		if driver.Signal == investmentMixSignalName && math.Abs(driver.Value-subWeightSum) > 1e-6 {
			return fmt.Errorf("cohort member driver value does not match the sum of its claimed threshold labels")
		}
		weightContributedSum += driver.WeightContributed
	}
	for label := range basisSubLabels {
		if _, claimed := claimedSubLabels[label]; !claimed {
			return fmt.Errorf("cohort member ranking_basis names a threshold label no driver claims")
		}
	}

	if m.Score != nil && math.Abs(weightContributedSum-*m.Score) > 1e-6 {
		return fmt.Errorf("cohort member drivers do not sum to score")
	}

	// Codex R3 finding 4: DataCompleteness must agree with how many
	// families actually contributed a driver -- mirrors scoreMember's own
	// switch (cohort_ranking.go): complete iff all 5, degraded iff <=2,
	// partial otherwise. Write-path only (bounds.cohortMemberDriversRequired):
	// a PR1-era row's DataCompleteness reflects its own RankingBasis
	// family count from before Drivers existed, which this len(m.Drivers)
	// check cannot see on the legacy read path.
	if bounds.cohortMemberDriversRequired {
		var want ContextFabricCohortDataCompleteness
		switch {
		case len(m.Drivers) == len(contextFabricCohortMemberDriverWeights):
			want = ContextFabricCohortDataComplete
		case len(m.Drivers) <= 2:
			want = ContextFabricCohortDataDegraded
		default:
			want = ContextFabricCohortDataPartial
		}
		if m.DataCompleteness != want {
			return fmt.Errorf("cohort member data_completeness does not match its driver count")
		}
	}
	return nil
}

// investmentMixSignalName mirrors internal/contextfabric/cohort_ranking.go's
// RankingSignalInvestmentMix constant (same cross-package literal-mirroring
// discipline as contextFabricCohortMemberDriverWeights above) -- the only
// signal whose driver may legally claim Window ==
// ContextFabricCohortMemberDriverWindowCurrentVsPrior.
const investmentMixSignalName = "investment_mix"

// Validate enforces the current contract bounds (write path) for one
// cohort member driver.
func (d ContextFabricCohortMemberDriver) Validate() error {
	return d.validate(contextFabricWriteBounds)
}

func (d ContextFabricCohortMemberDriver) validate(bounds contextFabricBounds) error {
	expectedWeight, knownSignal := contextFabricCohortMemberDriverWeights[d.Signal]
	if !knownSignal {
		return fmt.Errorf("cohort member driver signal is not a recognized closed-vocabulary value")
	}
	if d.Weight != expectedWeight {
		return fmt.Errorf("cohort member driver weight does not match its signal's formula weight")
	}
	if math.IsNaN(d.Value) || math.IsInf(d.Value, 0) || d.Value < 0 || d.Value > 1 {
		return fmt.Errorf("cohort member driver value violates v1 bounds")
	}
	if math.IsNaN(d.WeightContributed) || math.IsInf(d.WeightContributed, 0) || d.WeightContributed < 0 {
		return fmt.Errorf("cohort member driver weight_contributed violates v1 bounds")
	}
	if !validContextFabricCohortMemberDriverWindow(d.Window) {
		return fmt.Errorf("cohort member driver window is not a recognized value")
	}
	if len(d.ThresholdLabels) > bounds.cohortMemberDriverThresholdLabels || !uniqueTrimmedStrings(d.ThresholdLabels, bounds.cohortMemberRankingBasisLength) {
		return fmt.Errorf("cohort member driver threshold labels violate v1 bounds")
	}
	for _, label := range d.ThresholdLabels {
		if !validContextFabricCohortRankingBasisLabel(label) || !strings.HasPrefix(label, d.Signal+".") {
			return fmt.Errorf("cohort member driver threshold label is not a recognized value for its signal")
		}
	}
	// Concentration/ConcentrationMethod (CHAOS-4398 PR3) are present iff
	// Signal is investment_mix -- every other family never computes a
	// concentration measure. investmentMixSignal always sets both together
	// whenever the family is available (cohort_ranking.go), so a driver
	// entry (which only exists when its family WAS available) always
	// carries both for investment_mix, never one without the other.
	if d.Signal == investmentMixSignalName {
		if d.Concentration == nil {
			if bounds.cohortMemberDriverConcentrationRequired {
				return fmt.Errorf("cohort member investment_mix driver is missing concentration")
			}
		} else {
			if math.IsNaN(*d.Concentration) || math.IsInf(*d.Concentration, 0) || *d.Concentration < 0 || *d.Concentration > 1 {
				return fmt.Errorf("cohort member driver concentration violates v1 bounds")
			}
			if !validContextFabricCohortMemberDriverConcentrationMethod(d.ConcentrationMethod) {
				return fmt.Errorf("cohort member driver concentration_method is not a recognized value")
			}
		}
		if d.Concentration == nil && d.ConcentrationMethod != "" {
			return fmt.Errorf("cohort member driver concentration_method requires concentration")
		}
		if bounds.cohortMemberDriverConcentrationRequired && d.ConcentrationMethod == "" {
			return fmt.Errorf("cohort member investment_mix driver is missing concentration_method")
		}
	} else if d.Concentration != nil || d.ConcentrationMethod != "" {
		return fmt.Errorf("cohort member driver concentration is only valid for investment_mix")
	}
	return nil
}

func (e ContextFabricCohortExclusion) Validate() error {
	return e.validate(contextFabricWriteBounds)
}

func (e ContextFabricCohortExclusion) validate(bounds contextFabricBounds) error {
	if err := e.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if !boundedText(e.Reason, 1, bounds.cohortExclusionReasonLength, bounds) {
		return fmt.Errorf("cohort exclusion reason violates v1 bounds")
	}
	return nil
}

func (p ContextFabricRelationshipPath) Validate() error {
	return p.validate(contextFabricWriteBounds)
}

func (p ContextFabricRelationshipPath) validateStored() error {
	return p.validate(contextFabricLegacyBounds)
}

func (p ContextFabricRelationshipPath) validate(bounds contextFabricBounds) error {
	if !stringLengthBetween(p.PathID, 8, 256) || len(p.Nodes) < 2 || len(p.Nodes) > contextFabricRelationshipPathMaxNodes || len(p.Edges) != len(p.Nodes)-1 || !boundedText(p.WhyRelevant, 1, bounds.pathWhyRelevantLength, bounds) || !boundedEvidenceRefs(p.EvidenceRefIDs, bounds.pathEvidenceRefs, false) {
		return fmt.Errorf("relationship path violates v1 bounds")
	}
	if !uniqueSubjects(p.Nodes) {
		return fmt.Errorf("relationship path nodes must be valid and unique")
	}
	for index, edge := range p.Edges {
		if err := edge.validate(bounds); err != nil {
			return fmt.Errorf("edges: %w", err)
		}
		if edge.From != p.Nodes[index] || edge.To != p.Nodes[index+1] {
			return fmt.Errorf("relationship path edge continuity is invalid")
		}
	}
	return nil
}

func (e ContextFabricRelationshipEdge) Validate() error {
	return e.validate(contextFabricWriteBounds)
}

func (e ContextFabricRelationshipEdge) validate(bounds contextFabricBounds) error {
	if !validDerivationMethod(e.Derivation) || !validEpistemicStatus(e.EpistemicStatus) || !boundedEvidenceRefs(e.EvidenceRefIDs, bounds.memberEvidenceRefs, false) {
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
	return d.validate(contextFabricWriteBounds)
}

func (d ContextFabricDriverJudgment) validate(bounds contextFabricBounds) error {
	if !stringLengthBetween(d.DriverID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength) || !validDriverStanding(d.Standing) || !validDriverCategory(ContextFabricDriverCategory(d.Category)) || !boundedText(d.Title, 1, ContextFabricDriverTitleMaxLength, bounds) || !boundedText(d.Summary, 1, ContextFabricDriverSummaryMaxLength, bounds) || !validDerivationMethod(d.Derivation) || !validEpistemicStatus(d.EpistemicStatus) || d.Confidence < 0 || d.Confidence > 1 || !stringLengthBetween(d.Qualification, 0, ContextFabricDriverQualificationMaxLength) {
		return fmt.Errorf("driver judgment violates v1 bounds")
	}
	if len(d.AffectedSubjects) < ContextFabricDriverAffectedSubjectsMinCount || len(d.AffectedSubjects) > ContextFabricDriverAffectedSubjectsMaxCount || !uniqueSubjects(d.AffectedSubjects) || len(d.PathIDs) > ContextFabricDriverPathIDsMaxCount || !uniqueTrimmedStrings(d.PathIDs, ContextFabricIdentifierRefMaxLength) || !boundedEvidenceRefs(d.EvidenceRefIDs, bounds.nestedEvidenceRefs, true) {
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
	return f.validate(contextFabricWriteBounds)
}

func (f ContextFabricFinding) validate(bounds contextFabricBounds) error {
	if !stringLengthBetween(f.FindingID, ContextFabricModelMintedIDMinLength, ContextFabricModelMintedIDMaxLength) || !boundedText(f.Kind, 1, ContextFabricFindingKindMaxLength, bounds) || !boundedText(f.Summary, 1, ContextFabricFindingSummaryMaxLength, bounds) || len(f.Subjects) > ContextFabricFindingSubjectsMaxCount || !uniqueSubjects(f.Subjects) || !boundedEvidenceRefs(f.EvidenceRefIDs, bounds.nestedEvidenceRefs, false) {
		return fmt.Errorf("finding violates v1 bounds")
	}
	if len(f.ClaimedFactIDs) > ContextFabricDriverClaimedFactIDsMaxCount || !uniqueTrimmedStrings(f.ClaimedFactIDs, ContextFabricIdentifierRefMaxLength) {
		return fmt.Errorf("finding claimed fact references violate v1 bounds")
	}
	// Finding.Kind is the category-equivalent field for findings and is
	// governed by the SAME closed vocabulary as DriverJudgment.Category --
	// the synthesis prompt has always said so, and
	// ContextFabricDriverCategoryRequiresClaimedFact has always read it that
	// way. Nothing enforced it (codex round-12), so a model could return
	// kind "source_disagreement" with valid evidence and produce a result
	// that validated. Closed on writes, deliberately not on stored reads --
	// see contextFabricBounds.closedFindingKinds.
	if bounds.closedFindingKinds && !validDriverCategory(ContextFabricDriverCategory(f.Kind)) {
		return fmt.Errorf("finding kind %q is not in the closed v1 vocabulary", f.Kind)
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
	if err := validateClaimedFactRows(c.Rows); err != nil {
		return fmt.Errorf("rows: %w", err)
	}
	return nil
}

func (o ContextFabricSourceObservation) Validate() error {
	return o.validate(true)
}

func (o ContextFabricSourceObservation) validateStored() error {
	return o.validate(false)
}

// validate optionally requires the source name to be exactly trimmed.
//
// The canonical validator only trimmed FOR LENGTH, so " work_items " passed
// while the projection -- which requires an exactly-trimmed name -- rejected
// it (codex round-5 R5-5). Writes now reject the padding; stored rows keep
// being accepted, and the projection trims on copy, so a legacy padded row
// stays readable and projectable.
func (o ContextFabricSourceObservation) validate(requireTrimmed bool) error {
	if requireTrimmed && strings.TrimSpace(o.Source) != o.Source {
		return fmt.Errorf("source observation name must be trimmed")
	}
	if !stringLengthBetween(strings.TrimSpace(o.Source), 1, 128) || !validSourceState(o.State) || !stringLengthBetween(o.Watermark, 0, 512) || !stringLengthBetween(o.Reason, 0, ContextFabricSourceObservationReasonMaxLength) {
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
	return c.validate(contextFabricWriteBounds)
}

// validateStored accepts the legacy source and degraded-reason counts for
// an already-persisted row. Same immutability argument as the cohort and
// narrative bounds.
func (c ContextFabricCoverage) validateStored() error {
	return c.validate(contextFabricLegacyBounds)
}

func (c ContextFabricCoverage) validate(bounds contextFabricBounds) error {
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
	if c.Sources == nil || len(c.Sources) > bounds.coverageEntries || len(c.DegradedReasons) > bounds.coverageEntries || !uniqueTrimmedStrings(c.DegradedReasons, ContextFabricCoverageDegradedReasonMaxLength) {
		return fmt.Errorf("coverage violates v1 bounds")
	}
	seen := make(map[string]struct{}, len(c.Sources))
	for _, source := range c.Sources {
		validateSource := source.Validate
		if bounds.coverageEntries != contextFabricWriteBounds.coverageEntries {
			validateSource = source.validateStored
		}
		if err := validateSource(); err != nil {
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

// rawBoundedText is boundedText's write-path rule for contracts that have no
// stored-read counterpart: a REQUEST is validated once, at the door, and is
// never re-read from storage under legacy bounds.
//
// Requests are checked raw so a padded question is refused where the client
// can still act on it. Accepting it at the door and rejecting it later, when
// the engine echoed it into the result, is the compose-then-reject failure
// round 7 fixed -- and the request contract must not be the thing that
// manufactures it (codex round-13 F2 follow-up).
func rawBoundedText(value string, minimum, maximum int) bool {
	return boundedText(value, minimum, maximum, contextFabricWriteBounds)
}

// boundedText enforces a text field's length against the maximum the schema
// publishes. Writes measure the RAW value; stored reads measure the trimmed
// one -- see contextFabricBounds.rawTextLength for why the two differ.
//
// The minimum is always applied to the trimmed value: a field of nothing but
// whitespace is empty in every sense, and always was.
func boundedText(value string, minimum, maximum int, bounds contextFabricBounds) bool {
	if bounds.rawTextLength && utf8.RuneCountInString(value) > maximum {
		return false
	}
	return stringLengthBetween(strings.TrimSpace(value), minimum, maximum)
}

func (q ContextFabricInterpretedQuestion) Validate() error {
	return q.validate(contextFabricWriteBounds)
}

func (q ContextFabricInterpretedQuestion) validate(bounds contextFabricBounds) error {
	if !validInvestigationShape(q.Shape) || !boundedText(q.RequestedJudgment, 1, ContextFabricRequestedJudgmentMaxLength, bounds) || len(q.SubjectTerms) > bounds.interpretationTerms || len(q.ComparisonTerms) > bounds.interpretationTerms || !uniqueTrimmedStrings(q.SubjectTerms, ContextFabricSubjectOrComparisonTermMaxLength) || !uniqueTrimmedStrings(q.ComparisonTerms, ContextFabricSubjectOrComparisonTermMaxLength) || len(q.FactRequirements) > ContextFabricFactRequirementsMaxCount || !stringLengthBetween(q.ClarificationReason, 0, ContextFabricClarificationReasonMaxLength) {
		return fmt.Errorf("interpreted question violates v1 bounds")
	}
	if err := q.TimeContext.Validate(); err != nil {
		return fmt.Errorf("time_context: %w", err)
	}
	seen := make(map[ContextFabricFactKind]struct{}, len(q.FactRequirements))
	for _, requirement := range q.FactRequirements {
		if err := requirement.validate(bounds); err != nil {
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
	// CHAOS-3900 W1: the empty value is legitimate ("the model made no
	// pick" -- see ContextFabricWindowClass's own doc comment), so only a
	// NON-empty, out-of-vocabulary value is rejected.
	if q.WindowClass != "" && !ValidContextFabricWindowClass(q.WindowClass) {
		return fmt.Errorf("window_class is invalid")
	}
	if q.WindowConfidence != "" && !ValidContextFabricWindowConfidence(q.WindowConfidence) {
		return fmt.Errorf("window_confidence is invalid")
	}
	return nil
}

func (r ContextFabricFactRequirement) Validate() error {
	return r.validate(contextFabricWriteBounds)
}

func (r ContextFabricFactRequirement) validate(bounds contextFabricBounds) error {
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
		if !stringLengthBetween(key, 1, ContextFabricFactRequirementParameterKeyMaxLength) || !stringLengthBetween(value, 0, bounds.factParameterValueLength) || strings.TrimSpace(key) != key || strings.TrimSpace(value) != value {
			return fmt.Errorf("fact requirement parameter violates v1 bounds")
		}
	}
	return nil
}

// Validate enforces the CURRENT contract on a result being produced or
// accepted. Use it for every write and for anything a caller is about to
// receive fresh.
func (r ContextFabricInvestigationResult) Validate() error {
	return r.validate(contextFabricWriteBounds)
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
	return r.validate(contextFabricLegacyBounds)
}

// ValidateV2 enforces the CHAOS-4042 context_fabric_investigation_result.v2
// contract on a result being produced or accepted -- identical to Validate()
// in every field shape (see validateAgainstSchemaVersion), except
// schema_version must equal ContextFabricInvestigationResultSchemaV2, not
// the v1 constant. A result carrying the v1 schema_version is REJECTED here,
// and a result carrying the v2 schema_version is REJECTED by Validate(): the
// two entrypoints are deliberately never interchangeable, so nothing that
// goes through the v1 path can ever admit v2 (membership) semantics, or vice
// versa (the ruling's "a v1 offer must never acquire v2 membership
// semantics" / "do not reinterpret old persisted v1 receipts").
func (r ContextFabricInvestigationResult) ValidateV2() error {
	return r.validateAgainstSchemaVersion(contextFabricWriteBounds, ContextFabricInvestigationResultSchemaV2)
}

// ValidateStoredV2 is ValidateV2's ValidateStored counterpart -- see
// ValidateStored's own doc comment for the legacy-bounds rationale, which
// applies identically here.
func (r ContextFabricInvestigationResult) ValidateStoredV2() error {
	return r.validateAgainstSchemaVersion(contextFabricLegacyBounds, ContextFabricInvestigationResultSchemaV2)
}

// ValidateStoredResult (CHAOS-4042 PR3) dispatches a read-back WIRE result
// to ValidateStoredV2 or ValidateStored by its OWN r.SchemaVersion --
// mirrors contextfabric.ValidateStoredResult (model.go), which does the
// identical dispatch for the internal type, but cannot be reused here: this
// package cannot import contextfabric (contextfabric imports contracts/v1,
// not the reverse). Every caller that revalidates a result already read
// back off the wire (MCP tool responses, the sidecar's own transport
// defense-in-depth check) must use this instead of calling
// r.ValidateStored() directly -- codex xhigh review finding, confirmed
// real: all three of internal/sidecar/api_client_investigation.go,
// internal/contracts/v1/validate_mcp_investigation.go (x2) called
// ValidateStored() unconditionally, which hardcodes the v1 schema_version
// constant and REJECTS any v2-stamped result outright. Silent while offer
// minting stays dark (PR2's own flag), but a flip-blocker: the instant a
// v2 result exists, every one of those three call sites would 500/fail a
// retrieval that ValidateStoredV2 would have accepted. Any other
// SchemaVersion value fails closed, matching this package's existing
// "unrecognized value fails loudly" discipline -- never a silent pass.
func ValidateStoredResult(r ContextFabricInvestigationResult) error {
	switch r.SchemaVersion {
	case ContextFabricInvestigationResultSchemaV2:
		return r.ValidateStoredV2()
	case ContextFabricInvestigationResultSchema:
		return r.ValidateStored()
	default:
		return fmt.Errorf("investigation result schema_version %q is not a recognized major", r.SchemaVersion)
	}
}

func (r ContextFabricInvestigationResult) validate(bounds contextFabricBounds) error {
	return r.validateAgainstSchemaVersion(bounds, ContextFabricInvestigationResultSchema)
}

// validateAgainstSchemaVersion is the shared field-shape check both the v1
// and v2 (CHAOS-4042) entrypoints run -- every field below is IDENTICAL
// between the two majors (the ruling's "JSON fields may remain identical");
// only the expected schema_version const differs, which is why this exists
// as a parameter rather than two near-duplicate ~150-line functions that
// could silently drift from each other.
func (r ContextFabricInvestigationResult) validateAgainstSchemaVersion(bounds contextFabricBounds, expectedSchemaVersion string) error {
	if r.SchemaVersion != expectedSchemaVersion || !stringLengthBetween(r.ResultID, 8, 256) || !stringLengthBetween(r.RequestID, 8, 256) || r.GeneratedAt.IsZero() || !boundedText(r.Question, 1, 8000, bounds) || !validInvestigationStatus(r.Status) {
		return fmt.Errorf("result identity or status violates v1 bounds")
	}
	if err := r.Interpretation.validate(bounds); err != nil {
		return fmt.Errorf("interpretation: %w", err)
	}
	if err := r.SubjectResolution.validate(bounds); err != nil {
		return fmt.Errorf("subject_resolution: %w", err)
	}
	if r.Cohort != nil {
		if err := r.Cohort.validate(bounds); err != nil {
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
	if !stringLengthBetween(r.DirectJudgment, 0, bounds.judgmentLength) || !stringLengthBetween(r.CurrentState, 0, bounds.judgmentLength) || !stringLengthBetween(r.DeterministicAnswer, 1, bounds.deterministicAnswerLength) ||
		r.StrongestPressures == nil || len(r.StrongestPressures) > ContextFabricStrongestPressuresMaxCount || !uniqueTrimmedStrings(r.StrongestPressures, ContextFabricStrongestPressureMaxLength) ||
		r.Drivers == nil || len(r.Drivers) > ContextFabricDriversMaxCount ||
		r.RemainingWork == nil || len(r.RemainingWork) > ContextFabricRemainingWorkMaxCount ||
		r.ReadinessGaps == nil || len(r.ReadinessGaps) > ContextFabricReadinessGapsMaxCount ||
		r.Paths == nil || len(r.Paths) > 250 ||
		r.Conflicts == nil || len(r.Conflicts) > ContextFabricConflictsMaxCount ||
		r.Limitations == nil || len(r.Limitations) > bounds.narrativeCount || !uniqueTrimmedStrings(r.Limitations, bounds.narrativeLength) ||
		r.EvidenceRefIDs == nil || !boundedEvidenceRefs(r.EvidenceRefIDs, ContextFabricEvidenceRefIDsMaxCount, true) ||
		r.Warnings == nil || len(r.Warnings) > bounds.narrativeCount || !uniqueTrimmedStrings(r.Warnings, bounds.narrativeLength) {
		return fmt.Errorf("result answer fields violate v1 bounds")
	}
	// LimitationsDisplaced is a COHERENCE rule, not only a range check. A
	// bare "count >= 0" would admit a result claiming losses it never
	// took, which is the same lie as hiding one -- a reader cannot check
	// this number against anything else in the document, so the contract
	// has to. A displacement only ever happens when a SERVICE-AUTHORED
	// disclosure had to be forced into a list that was already full, so a
	// positive count requires exactly that shape: the list at its cap,
	// carrying one of them.
	//
	// Legacy rows are unaffected: they carry zero, which makes the rule
	// vacuous for every result written before this field existed.
	if r.LimitationsDisplaced < 0 || r.LimitationsDisplaced > bounds.narrativeCount {
		return fmt.Errorf("result displaced-limitation count violates v1 bounds")
	}
	if r.LimitationsDisplaced > 0 {
		// "The list was full" is evaluated against the cap the ROW WAS
		// WRITTEN UNDER, not today's (round-17 finding 2). A displacement
		// only ever happens at a cap, so on the write path this is exactly
		// the current cap; on the lenient stored path it is anything from
		// the current cap up to the legacy ceiling, which is what a
		// 250-era displaced row legitimately looks like. Demanding the
		// current cap on both paths made a perfectly good stored answer
		// unreadable -- the same mistake the padded-text bounds made
		// before rawTextLength split them.
		//
		// The two ends are derived, not restated: the floor is the write
		// cap and the ceiling is whichever cap this validation is running
		// under, so on the write path the pair collapses to equality.
		full := len(r.Limitations) >= ContextFabricLimitationsMaxCount && len(r.Limitations) <= bounds.narrativeCount
		// ANY service-authored disclosure, not the retrieval-degradation
		// one by name (CHAOS-4098). This rule was written when
		// degradation was the only displacer; CHAOS-3781's historical
		// disclosures, CHAOS-4085's commit retraction and CHAOS-4098's
		// clarification override each displace too, and each produced a
		// result this rule REJECTED whenever the model returned a full
		// limitation list -- ErrInvalidResult for a displacement that
		// happened exactly as designed. Deriving the question from
		// ContextFabricServiceAuthoredLimitations instead of naming one
		// constant means a fifth disclosure cannot reintroduce it.
		//
		// This ADMITS documents the old form rejected and rejects none it
		// accepted, so it is not a tightening and needs no new major: the
		// field's own meaning (how many model caveats were dropped) is
		// unchanged, and every previously valid row carries the
		// degradation disclosure, which is still in the derived set.
		if !full || !HasContextFabricServiceAuthoredLimitation(r.Limitations) {
			return fmt.Errorf("result claims displaced limitations without the full list and disclosure that would require one")
		}
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
	if err := validateDrivers(r.Drivers, claimed, bounds); err != nil {
		return err
	}
	if err := validateFindings("remaining_work", r.RemainingWork, claimed, bounds); err != nil {
		return err
	}
	if err := validateFindings("readiness_gaps", r.ReadinessGaps, claimed, bounds); err != nil {
		return err
	}
	if err := validatePaths(r.Paths, bounds); err != nil {
		return err
	}
	if err := validateFindings("conflicts", r.Conflicts, claimed, bounds); err != nil {
		return err
	}
	if err := r.Coverage.validate(bounds); err != nil {
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
	// CHAOS-3900 W1: EffectiveEvidenceWindow is legal only when a window
	// could genuinely be in play -- the current axis, mirroring
	// EvidenceWindow's own axis-legality rule on the wire request.
	if r.EffectiveEvidenceWindow != nil {
		if r.Interpretation.TimeContext.Axis != ContextFabricTemporalCurrent {
			return fmt.Errorf("%w: axis %q", ErrEvidenceWindowAxisInvalid, r.Interpretation.TimeContext.Axis)
		}
		if err := r.EffectiveEvidenceWindow.validate(); err != nil {
			return fmt.Errorf("effective_evidence_window: %w", err)
		}
	}
	if r.WindowClarification != nil {
		if err := r.WindowClarification.Validate(); err != nil {
			return fmt.Errorf("window_clarification: %w", err)
		}
	}
	if r.StructureNeeds != nil {
		if err := r.StructureNeeds.Validate(); err != nil {
			return fmt.Errorf("structure_needs: %w", err)
		}
	}
	// One entry per CARRIED member (design brief §2.1) -- bounded by the
	// closed frame-member vocabulary's own size, not an offer-list cap.
	if len(r.ConfirmedStructure) > ContextFabricStructureNeedKindCount {
		return fmt.Errorf("confirmed_structure exceeds v1 bounds")
	}
	seenConfirmedMembers := make(map[ContextFabricStructureNeedKind]struct{}, len(r.ConfirmedStructure))
	for i, entry := range r.ConfirmedStructure {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("confirmed_structure[%d]: %w", i, err)
		}
		if _, exists := seenConfirmedMembers[entry.Member]; exists {
			return fmt.Errorf("confirmed_structure[%d]: member %q already carried -- one entry per member", i, entry.Member)
		}
		seenConfirmedMembers[entry.Member] = struct{}{}
	}
	// Bounded by ContextFabricStructureNeedKindCount members (kind/anchor/
	// handle/window) times each member's own mint-time offer-set cap
	// (contextFabricStructureNeedsMaxOptions) -- the design brief's own
	// "copies at most the offer set's own mint-time bound PER MEMBER"
	// rule, not a single flat cap across every member.
	if len(r.StructureOfferSnapshot) > ContextFabricStructureNeedKindCount*contextFabricStructureNeedsMaxOptions {
		return fmt.Errorf("structure_offer_snapshot exceeds v1 bounds")
	}
	for i, entry := range r.StructureOfferSnapshot {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("structure_offer_snapshot[%d]: %w", i, err)
		}
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
