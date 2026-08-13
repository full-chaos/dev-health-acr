package v1

import (
	"fmt"
	"strings"
)

// Validate checks the wire-shape bounds and internal referential integrity
// of a bounded consumer projection.
//
// It deliberately does NOT re-check the semantics the canonical result
// already proved (evidence closure, claimed-fact category rules, driver
// ordering against a graph). A projection is derived from a result that
// passed ContextFabricInvestigationResult.Validate, so re-deriving those
// rules here would duplicate the authority and invite the two copies to
// disagree. What this proves is that the projection is a well-formed,
// self-consistent document: every referenced claim and evidence ref it
// carries actually appears in it, and every declared drop is coherent.
func (p ContextFabricAnswerProjection) Validate() error {
	if p.SchemaVersion != ContextFabricAnswerProjectionSchema {
		return fmt.Errorf("answer projection schema version must be %q", ContextFabricAnswerProjectionSchema)
	}
	if !stringLengthBetween(p.ResultID, 8, 256) || !stringLengthBetween(p.RequestID, 8, 256) {
		return fmt.Errorf("answer projection identifiers violate v1 bounds")
	}
	if p.GeneratedAt.IsZero() {
		return fmt.Errorf("answer projection generated_at is required")
	}
	if !validInvestigationStatus(p.Status) {
		return fmt.Errorf("answer projection status is not a member of the closed vocabulary")
	}
	if !stringLengthBetween(p.Question, 1, 8000) {
		return fmt.Errorf("answer projection question violates v1 bounds")
	}
	if !stringLengthBetween(p.DirectJudgment, 0, 4000) || !stringLengthBetween(p.CurrentState, 0, 4000) {
		return fmt.Errorf("answer projection judgment text violates v1 bounds")
	}
	if len(p.StrongestPressures) > ContextFabricProjectedPressuresMaxCount || !uniqueTrimmedStrings(p.StrongestPressures, 2000) {
		return fmt.Errorf("answer projection strongest pressures violate v1 bounds")
	}
	if p.CommittedSubjects == nil || len(p.CommittedSubjects) > ContextFabricProjectedSubjectsMaxCount || !uniqueSubjects(p.CommittedSubjects) {
		return fmt.Errorf("answer projection committed subjects violate v1 bounds")
	}
	if err := p.validateClarification(); err != nil {
		return err
	}
	if p.Cohort != nil {
		if err := p.Cohort.Validate(); err != nil {
			return fmt.Errorf("cohort: %w", err)
		}
	}
	claims, err := p.validateFacts()
	if err != nil {
		return err
	}
	if err := p.validateDrivers(claims); err != nil {
		return err
	}
	if err := p.validateCoverage(); err != nil {
		return err
	}
	if len(p.Limitations) > ContextFabricProjectedNarrativeMaxCount || !uniqueTrimmedStrings(p.Limitations, 2000) {
		return fmt.Errorf("answer projection limitations violate v1 bounds")
	}
	if len(p.Warnings) > ContextFabricProjectedNarrativeMaxCount || !uniqueTrimmedStrings(p.Warnings, 2000) {
		return fmt.Errorf("answer projection warnings violate v1 bounds")
	}
	if !boundedEvidenceRefs(p.EvidenceRefIDs, ContextFabricProjectedEvidenceMaxCount, true) {
		return fmt.Errorf("answer projection evidence references violate v1 bounds")
	}
	if err := p.validateReceipts(); err != nil {
		return err
	}
	if err := p.Versions.Validate(); err != nil {
		return fmt.Errorf("versions: %w", err)
	}
	return p.ProjectionBudget.Validate()
}

func (p ContextFabricAnswerProjection) validateClarification() error {
	if p.Clarification == nil {
		return nil
	}
	clarification := *p.Clarification
	if !stringLengthBetween(clarification.Prompt, 0, 2000) || strings.TrimSpace(clarification.Prompt) != clarification.Prompt {
		return fmt.Errorf("answer projection clarification prompt violates v1 bounds")
	}
	if clarification.Candidates == nil || len(clarification.Candidates) > ContextFabricProjectedCandidatesMaxCount {
		return fmt.Errorf("answer projection clarification candidates violate v1 bounds")
	}
	seen := make(map[string]struct{}, len(clarification.Candidates))
	for _, candidate := range clarification.Candidates {
		if !stringLengthBetween(candidate.ReceiptID, 8, 256) || !validResolutionState(candidate.State) {
			return fmt.Errorf("answer projection clarification candidate violates v1 bounds")
		}
		if candidate.Confidence < 0 || candidate.Confidence > 1 {
			return fmt.Errorf("answer projection clarification candidate confidence violates v1 bounds")
		}
		if err := candidate.Subject.Validate(); err != nil {
			return fmt.Errorf("clarification candidate subject: %w", err)
		}
		if len(candidate.MatchReasons) > 100 || !uniqueTrimmedStrings(candidate.MatchReasons, 1024) {
			return fmt.Errorf("answer projection clarification candidate reasons violate v1 bounds")
		}
		if _, exists := seen[candidate.ReceiptID]; exists {
			return fmt.Errorf("answer projection clarification candidate receipt IDs must be unique")
		}
		seen[candidate.ReceiptID] = struct{}{}
	}
	return nil
}

// Validate checks one projected cohort. Total must be at least the number
// of retained members: it reports the canonical size before projection, so
// a value below the retained count would understate a cohort the caller can
// already see in full.
func (c ContextFabricProjectedCohort) Validate() error {
	if !validContextFabricSubjectKind(c.Kind) {
		return fmt.Errorf("projected cohort kind is not a member of the closed vocabulary")
	}
	if c.Members == nil || len(c.Members) > ContextFabricProjectedCohortMaxCount {
		return fmt.Errorf("projected cohort members violate v1 bounds")
	}
	if c.Total < len(c.Members) {
		return fmt.Errorf("projected cohort total must not understate retained members")
	}
	if !stringLengthBetween(strings.TrimSpace(c.Rationale), 1, 4000) {
		return fmt.Errorf("projected cohort rationale violates v1 bounds")
	}
	seen := make(map[string]struct{}, len(c.Members))
	lastRank := 0
	for _, member := range c.Members {
		if err := member.Subject.Validate(); err != nil {
			return fmt.Errorf("member subject: %w", err)
		}
		if member.Subject.Kind != c.Kind {
			return fmt.Errorf("projected cohort member kind must match the cohort kind")
		}
		// Ranks must stay strictly increasing. The projection may drop a
		// trailing member, never reorder one: a reordered cohort is a
		// different judgment about which subject matters most.
		if member.Rank <= lastRank {
			return fmt.Errorf("projected cohort member ranks must be strictly increasing")
		}
		lastRank = member.Rank
		if len(member.InclusionReasons) < 1 || len(member.InclusionReasons) > 32 || !uniqueTrimmedStrings(member.InclusionReasons, 1000) {
			return fmt.Errorf("projected cohort member inclusion reasons violate v1 bounds")
		}
		// Optional and omitempty, exactly like the canonical
		// CohortMember.EvidenceRefIDs it mirrors: nil and empty both mean
		// "none", and demanding non-nil would make the projection
		// unable to survive its own round trip.
		if !optionalEvidenceRefs(member.EvidenceRefIDs, ContextFabricProjectedEvidenceMaxCount) {
			return fmt.Errorf("projected cohort member evidence references violate v1 bounds")
		}
		key := subjectKey(member.Subject)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("projected cohort members must be unique")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// validateFacts checks KeyFacts bounds and ClaimID uniqueness, returning the
// ClaimID set that validateDrivers cross-checks driver references against.
func (p ContextFabricAnswerProjection) validateFacts() (map[string]struct{}, error) {
	if p.KeyFacts == nil || len(p.KeyFacts) > ContextFabricProjectedFactsMaxCount {
		return nil, fmt.Errorf("answer projection key facts violate v1 bounds")
	}
	claims := make(map[string]struct{}, len(p.KeyFacts))
	for _, fact := range p.KeyFacts {
		if !stringLengthBetween(fact.ClaimID, 8, 256) || !validFactKind(fact.Kind) {
			return nil, fmt.Errorf("answer projection key fact identity violates v1 bounds")
		}
		if !stringLengthBetween(fact.Field, 1, ContextFabricClaimedFieldMaxLength) || strings.TrimSpace(fact.Field) != fact.Field {
			return nil, fmt.Errorf("answer projection key fact field violates v1 bounds")
		}
		if err := fact.Subject.Validate(); err != nil {
			return nil, fmt.Errorf("key fact subject: %w", err)
		}
		if err := fact.Value.Validate(); err != nil {
			return nil, fmt.Errorf("key fact value: %w", err)
		}
		if _, exists := claims[fact.ClaimID]; exists {
			return nil, fmt.Errorf("answer projection key fact claim IDs must be unique")
		}
		claims[fact.ClaimID] = struct{}{}
	}
	return claims, nil
}

// validateDrivers proves every retained driver is well formed and that its
// claimed-fact references resolve inside this document. A driver citing a
// claim the projection dropped would be unverifiable by the consumer that
// received it, which defeats the point of carrying facts at value level.
func (p ContextFabricAnswerProjection) validateDrivers(claims map[string]struct{}) error {
	if p.PrincipalDrivers == nil || len(p.PrincipalDrivers) > ContextFabricProjectedDriversMaxCount {
		return fmt.Errorf("answer projection drivers violate v1 bounds")
	}
	seen := make(map[string]struct{}, len(p.PrincipalDrivers))
	for _, driver := range p.PrincipalDrivers {
		if !stringLengthBetween(driver.DriverID, 8, 256) || !validDriverStanding(driver.Standing) {
			return fmt.Errorf("answer projection driver identity or standing violates v1 bounds")
		}
		if !validDriverCategory(ContextFabricDriverCategory(driver.Category)) {
			return fmt.Errorf("answer projection driver category is not a member of the closed vocabulary")
		}
		if !stringLengthBetween(driver.Title, 1, 512) || !stringLengthBetween(driver.Summary, 1, 4000) {
			return fmt.Errorf("answer projection driver text violates v1 bounds")
		}
		if !stringLengthBetween(driver.Qualification, 0, 2000) {
			return fmt.Errorf("answer projection driver qualification violates v1 bounds")
		}
		if driver.Confidence < 0 || driver.Confidence > 1 {
			return fmt.Errorf("answer projection driver confidence violates v1 bounds")
		}
		if !boundedEvidenceRefs(driver.EvidenceRefIDs, ContextFabricProjectedEvidenceMaxCount, true) {
			return fmt.Errorf("answer projection driver evidence references violate v1 bounds")
		}
		for _, claimID := range driver.ClaimedFactIDs {
			if _, ok := claims[claimID]; !ok {
				return fmt.Errorf("answer projection driver references claimed fact %q that the projection does not carry", claimID)
			}
		}
		if _, exists := seen[driver.DriverID]; exists {
			return fmt.Errorf("answer projection driver IDs must be unique")
		}
		seen[driver.DriverID] = struct{}{}
	}
	return nil
}

func (p ContextFabricAnswerProjection) validateCoverage() error {
	if p.CoverageSummary == nil || len(p.CoverageSummary) > ContextFabricProjectedCoverageMaxCount {
		return fmt.Errorf("answer projection coverage violates v1 bounds")
	}
	seen := make(map[string]struct{}, len(p.CoverageSummary))
	for _, entry := range p.CoverageSummary {
		if !stringLengthBetween(entry.Source, 1, 128) || strings.TrimSpace(entry.Source) != entry.Source {
			return fmt.Errorf("answer projection coverage source violates v1 bounds")
		}
		if !validSourceState(entry.State) {
			return fmt.Errorf("answer projection coverage state is not a member of the closed vocabulary")
		}
		// Matches the canonical SourceObservation.reason bound. A tighter
		// bound here would reject a legitimate canonical result: the
		// explanation for a missing source is precisely what a reader
		// needs to judge a partial answer, so it is not agent-noise to
		// be trimmed.
		if !stringLengthBetween(entry.Reason, 0, 2000) {
			return fmt.Errorf("answer projection coverage reason violates v1 bounds")
		}
		if _, exists := seen[entry.Source]; exists {
			return fmt.Errorf("answer projection coverage sources must be unique")
		}
		seen[entry.Source] = struct{}{}
	}
	return nil
}

func (p ContextFabricAnswerProjection) validateReceipts() error {
	if p.SubjectReceipts == nil || len(p.SubjectReceipts) > ContextFabricProjectedReceiptsMaxCount {
		return fmt.Errorf("answer projection subject receipts violate v1 bounds")
	}
	seen := make(map[string]struct{}, len(p.SubjectReceipts))
	for _, receipt := range p.SubjectReceipts {
		if !stringLengthBetween(receipt.ResultID, 8, 256) || !stringLengthBetween(receipt.ReceiptID, 8, 256) {
			return fmt.Errorf("answer projection subject receipt violates v1 bounds")
		}
		// Every receipt must name the result this projection came from.
		// A receipt pointing at some other result would invite a caller
		// to bind a follow-up turn to a subject this answer never
		// resolved.
		if receipt.ResultID != p.ResultID {
			return fmt.Errorf("answer projection subject receipts must reference this result")
		}
		if _, exists := seen[receipt.ReceiptID]; exists {
			return fmt.Errorf("answer projection subject receipt IDs must be unique")
		}
		seen[receipt.ReceiptID] = struct{}{}
	}
	return nil
}

// Validate proves the declared drops are coherent: no negative count, and
// Truncated set if and only if something was actually dropped. The
// if-and-only-if matters in both directions. A projection claiming
// truncation it did not perform teaches a caller to distrust complete
// answers; one that dropped content without saying so is the silent
// truncation this contract exists to prevent.
func (b ContextFabricProjectionBudget) Validate() error {
	counts := []int{b.DriversOmitted, b.WithheldDriversOmitted, b.CohortMembersOmitted, b.FactsOmitted, b.CandidatesOmitted, b.EvidenceRefsOmitted, b.LimitationsOmitted, b.WarningsOmitted, b.CoverageOmitted, b.ValuesClamped}
	dropped := b.FullResultOmitted
	for _, count := range counts {
		if count < 0 {
			return fmt.Errorf("answer projection budget counts must not be negative")
		}
		if count > 0 {
			dropped = true
		}
	}
	if b.Truncated != dropped {
		return fmt.Errorf("answer projection budget truncated flag must match the declared drops")
	}
	return nil
}
