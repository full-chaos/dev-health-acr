package contextfabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CanonicalizeQuestion reduces a caller's question text to the form
// answer-reuse hashing binds to (CHAOS-3782, TRD §19.7.2, AC-3782-5). The
// transform is deliberately narrow and fully deterministic:
//
//  1. Leading/trailing whitespace is trimmed.
//  2. Every internal run of whitespace (spaces, tabs, newlines) collapses to
//     one ASCII space.
//  3. The text is lowercased with strings.ToLower -- a simple byte/rune
//     case fold, not a locale-aware collation. This is a known, accepted
//     limitation: two questions that differ only by a locale-specific
//     case rule (e.g. Turkish dotless i) are not guaranteed to canonicalize
//     identically. Documented here rather than silently assumed.
//  4. Trailing punctuation is stripped, together with any whitespace that
//     stripping exposes, repeated to a fixed point -- so "done?", "done ?",
//     and "done?!" all canonicalize to "done".
//
// Nothing else changes: no stemming, no synonym folding, no internal
// punctuation removal, no stop-word dropping. AC-3782-5's second half --
// "two questions that differ in any word do not [hash the same]" -- depends
// on this staying narrow. Widening it (e.g. folding "backend" and "back
// end" together) would be a reuse-correctness change, not a cosmetic one,
// and needs its own acceptance criterion.
func CanonicalizeQuestion(question string) string {
	collapsed := collapseInternalWhitespace(strings.TrimSpace(question))
	lowered := strings.ToLower(collapsed)
	return stripTrailingPunctuation(lowered)
}

// QuestionHash returns the reuse-key hash of question: the SHA-256 digest,
// hex-encoded, of CanonicalizeQuestion(question). Two questions produce the
// same hash if and only if they canonicalize identically.
func QuestionHash(question string) string {
	sum := sha256.Sum256([]byte(CanonicalizeQuestion(question)))
	return hex.EncodeToString(sum[:])
}

func collapseInternalWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastWasSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !lastWasSpace {
				b.WriteByte(' ')
			}
			lastWasSpace = true
			continue
		}
		lastWasSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// stripTrailingPunctuation removes trailing Unicode punctuation and any
// whitespace it exposes, to a fixed point, so multiple trailing marks
// (optionally interspersed with whitespace, e.g. "done ? !") reduce the
// same as one ("done?!") or none ("done").
func stripTrailingPunctuation(s string) string {
	for {
		next := strings.TrimRightFunc(s, unicode.IsPunct)
		next = strings.TrimRightFunc(next, unicode.IsSpace)
		if next == s {
			return next
		}
		s = next
	}
}

// maxReuseSubjectRecheckCount bounds how many distinct subjects tryReuse
// will re-authorize for one candidate. It matches the existing
// RequestedScope.SubjectHints wire bound (see Investigate's
// maxSubjectHints) -- a candidate naming more distinct subjects than a
// single request could ever legally hint at is treated as impossible to
// fully recheck, and answer reuse fails closed rather than checking only
// a subset.
const maxReuseSubjectRecheckCount = 50

// reuseRecheckOptions is the InvestigationOptions the condition-6 recheck
// uses for ResolveSubjects/DiscoverContext -- the contract's own MAXIMUM
// bound for every Max* field (ContextFabricInvestigationOptions.Validate,
// internal/contracts/v1/validate_context_fabric_request.go), never the
// live caller's own (possibly smaller) Options.
//
// Why this matters (flagged in review): graphrank truncates its final
// admitted set to Options.MaxRelationshipPaths/MaxEvidenceRefs/
// MaxCohortMembers/MaxSubjectCandidates (see falkorgraph/reader.go and
// graphrank/discover.go, resolve.go). If the recheck used the CURRENT
// request's Options and those happened to be smaller than whatever
// Options generated the stored candidate, the recheck's discovered set
// could legitimately shrink relative to the original -- with the
// watermark and authorization both completely unchanged -- and the
// containment check below would then reject a candidate that is
// genuinely still fresh and visible. That is a spurious cache miss, not
// a wrong answer (the safe direction), but if it fires often it craters
// the reuse rate silently. Using the contract's ceiling here means the
// recheck's set can only be as large as or larger than what ANY legal
// original investigation could have produced, so it stops truncation
// itself from ever being the reason a recheck fails.
var reuseRecheckOptions = InvestigationOptions{
	MaxSubjectCandidates: 50, MaxCohortMembers: 250, MaxRelationshipPaths: 250,
	MaxDrivers: 50, MaxEvidenceRefs: 500, MaxSerializedBytes: 1 << 20,
	AllowClarification: true,
}

// AnswerReuseOutcome classifies why one Investigate call did or did not
// reuse a stored result (CHAOS-3782, AC-3782-8). A closed set of fixed
// labels -- content-safe by construction, never free text -- so a
// dashboard can tell "reuse rate looks low because of authorization
// churn" apart from "reuse rate looks low because containment keeps
// failing" (the latter usually means the recheck's own bounds, not real
// staleness, are the problem -- see reuseRecheckOptions).
type AnswerReuseOutcome string

const (
	// AnswerReuseHit: a stored result was served; zero model calls made.
	AnswerReuseHit AnswerReuseOutcome = "hit"
	// AnswerReuseMissNoCandidate: no gate candidate matched, or the
	// candidate failed one of AnswerReuseGate's own conditions
	// (1/2/3/4/5/7) -- ReuseGate is nil, the question hash/org/contract/
	// projection/model-identity didn't match anything, a source
	// watermark advanced, or the candidate fell outside the staleness/
	// invalidation window.
	AnswerReuseMissNoCandidate AnswerReuseOutcome = "miss_no_candidate"
	// AnswerReuseMissAuthorization: a candidate was found, but a subject
	// it names no longer resolves under current authorization (or the
	// recheck itself could not be completed).
	AnswerReuseMissAuthorization AnswerReuseOutcome = "miss_authorization"
	// AnswerReuseMissEvidenceContainment: the subject recheck passed, but
	// an evidence reference the candidate cites was not present in a
	// freshly discovered evidence set (or the recheck itself could not
	// be completed).
	AnswerReuseMissEvidenceContainment AnswerReuseOutcome = "miss_evidence_containment"
)

// tryReuse implements CHAOS-3782 (TRD §19.7). It is called from
// Investigate BEFORE QuestionInterpreter.Interpret -- see the call site's
// comment for why that ordering is what makes AC-3782-1's zero-model-call
// guarantee hold. ok=false covers every way a reuse attempt can fail to
// pan out (no ReuseGate configured, no candidate found, a candidate that
// failed the gate's own conditions 1-5/7, or a candidate that failed the
// condition-6 authorization recheck here) -- Investigate always falls
// through to a fresh investigation in that case; ok=false is never an
// error.
func (e *Engine) tryReuse(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InvestigationResult, bool) {
	if e.reuseGate == nil {
		return InvestigationResult{}, false
	}
	key := ReuseKey{
		QuestionHash:      QuestionHash(request.Question),
		ContractVersion:   InvestigationResultSchemaV1,
		ProjectionVersion: e.reuseProjectionVersion,
		ModelIdentity:     e.reuseModelIdentity,
	}
	candidate, ok, err := e.reuseGate.FindReusable(ctx, principal, key)
	if err != nil || !ok {
		e.recordReuseOutcome(ctx, principal, AnswerReuseMissNoCandidate)
		return InvestigationResult{}, false
	}
	if err := ctx.Err(); err != nil {
		return InvestigationResult{}, false
	}
	if holds, missReason := e.reuseAuthorizationStillHolds(ctx, principal, request, candidate); !holds {
		e.recordReuseOutcome(ctx, principal, missReason)
		return InvestigationResult{}, false
	}
	// The candidate is served EXACTLY as stored -- same ResultID,
	// RequestID, and GeneratedAt (AC-3782-2: those name the reused
	// result's own identifier and generation time, not this call's).
	// Reused is per-serving metadata, set only on this in-memory copy;
	// nothing about the stored row is touched.
	candidate.Reused = true
	e.recordReuseOutcome(ctx, principal, AnswerReuseHit)
	return candidate, true
}

func (e *Engine) recordReuseOutcome(ctx context.Context, principal storage.Principal, outcome AnswerReuseOutcome) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordAnswerReuse(ctx, principal, outcome)
}

// reuseAuthorizationStillHolds implements TRD §19.7.3 condition 6 --
// current authorization for every subject and evidence reference in the
// stored result -- using only GraphReader's two existing methods
// (ResolveSubjects, DiscoverContext). No new port or graph query surface
// is needed: both calls are graph reads, never model calls, so this stays
// inside AC-3782-1's zero-model-call bound. On failure it returns
// (false, reason) so tryReuse's telemetry can tell an authorization
// rejection apart from a containment rejection (AC-3782-8) -- see
// AnswerReuseOutcome's doc comment for why that split matters
// diagnostically. Both legs use reuseRecheckOptions, the contract's
// ceiling bounds, not the live caller's own Options -- see that var's doc
// comment for why using the caller's (possibly smaller) Options here
// would risk a truncation-induced false miss.
//
// Subject leg: every subject the candidate ever names is re-resolved
// through ResolveSubjects with an exact SubjectHint, the same mechanism
// resolvePriorSubjectHints already relies on to re-authorize a prior
// turn's committed subject -- a subject that no longer resolves (deleted,
// or the caller's authorization narrowed) fails the recheck.
//
// Evidence-ref leg: DiscoverContext is re-run for the now-reauthorized
// subjects, using the candidate's OWN stored Interpretation (already
// computed -- no model call). If every condition-3 watermark is truly
// unchanged, the freshly discovered evidence set must contain everything
// the candidate cites; if authorization narrowed since the candidate was
// generated, DiscoverContext silently omits what is no longer visible,
// and the containment check below correctly fails closed.
func (e *Engine) reuseAuthorizationStillHolds(ctx context.Context, principal storage.Principal, request InvestigationRequest, candidate InvestigationResult) (bool, AnswerReuseOutcome) {
	subjects := reuseSubjectsToRecheck(candidate)
	if len(subjects) > maxReuseSubjectRecheckCount {
		return false, AnswerReuseMissAuthorization
	}
	recheckRequest := request
	recheckRequest.Options = reuseRecheckOptions
	if len(subjects) > 0 {
		hints := make([]SubjectHint, 0, len(subjects))
		for _, subject := range subjects {
			hints = append(hints, SubjectHint{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "answer_reuse_authorization_recheck"})
		}
		recheckRequest.RequestedScope.SubjectHints = hints
	}

	resolution, err := e.graph.ResolveSubjects(ctx, principal, recheckRequest, candidate.Interpretation)
	if err != nil {
		return false, AnswerReuseMissAuthorization
	}
	committed := make(map[string]struct{}, len(resolution.Committed))
	for _, subject := range resolution.Committed {
		committed[subjectKeyForModel(subject)] = struct{}{}
	}
	for _, subject := range subjects {
		if _, ok := committed[subjectKeyForModel(subject)]; !ok {
			return false, AnswerReuseMissAuthorization
		}
	}

	evidenceRefs := reuseEvidenceRefsToRecheck(candidate)
	if len(evidenceRefs) == 0 {
		return true, AnswerReuseHit
	}
	graphContext, err := e.graph.DiscoverContext(ctx, principal, GraphDiscoveryRequest{
		Request: recheckRequest, Interpretation: candidate.Interpretation, Resolution: resolution,
	})
	if err != nil {
		return false, AnswerReuseMissEvidenceContainment
	}
	visible := make(map[string]struct{}, len(graphContext.EvidenceRefIDs))
	for _, ref := range graphContext.EvidenceRefIDs {
		visible[ref] = struct{}{}
	}
	for _, ref := range evidenceRefs {
		if _, ok := visible[ref]; !ok {
			return false, AnswerReuseMissEvidenceContainment
		}
	}
	return true, AnswerReuseHit
}

// reuseSubjectsToRecheck collects every distinct subject named anywhere in
// candidate -- committed and candidate subject resolutions, cohort
// members AND exclusions (an exclusion still discloses a subject's
// identity and the reason it was excluded, so it needs the same
// recheck), claimed facts, driver/finding affected subjects, and
// relationship path nodes.
func reuseSubjectsToRecheck(candidate InvestigationResult) []SubjectRef {
	seen := make(map[string]struct{})
	subjects := make([]SubjectRef, 0, len(candidate.SubjectResolution.Committed))
	add := func(subject SubjectRef) {
		key := subjectKeyForModel(subject)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		subjects = append(subjects, subject)
	}
	for _, subject := range candidate.SubjectResolution.Committed {
		add(subject)
	}
	for _, sc := range candidate.SubjectResolution.Candidates {
		add(sc.Subject)
	}
	if candidate.Cohort != nil {
		for _, member := range candidate.Cohort.Members {
			add(member.Subject)
		}
		for _, exclusion := range candidate.Cohort.Exclusions {
			add(exclusion.Subject)
		}
	}
	for _, claim := range candidate.ClaimedFacts {
		add(claim.Subject)
	}
	for _, driver := range candidate.Drivers {
		for _, subject := range driver.AffectedSubjects {
			add(subject)
		}
	}
	for _, findings := range [][]Finding{candidate.RemainingWork, candidate.ReadinessGaps, candidate.Conflicts} {
		for _, finding := range findings {
			for _, subject := range finding.Subjects {
				add(subject)
			}
		}
	}
	for _, path := range candidate.Paths {
		for _, subject := range path.Nodes {
			add(subject)
		}
	}
	return subjects
}

// reuseEvidenceRefsToRecheck collects every distinct evidence reference ID
// named anywhere in candidate -- the top-level closure set plus every
// nested occurrence (subject candidates, cohort members, drivers,
// findings, paths). Deduplicated so DiscoverContext's fresh evidence set
// only needs one containment check per unique ID.
func reuseEvidenceRefsToRecheck(candidate InvestigationResult) []string {
	seen := make(map[string]struct{}, len(candidate.EvidenceRefIDs))
	refs := make([]string, 0, len(candidate.EvidenceRefIDs))
	add := func(ids []string) {
		for _, id := range ids {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			refs = append(refs, id)
		}
	}
	add(candidate.EvidenceRefIDs)
	for _, sc := range candidate.SubjectResolution.Candidates {
		add(sc.EvidenceRefIDs)
	}
	if candidate.Cohort != nil {
		for _, member := range candidate.Cohort.Members {
			add(member.EvidenceRefIDs)
		}
	}
	for _, driver := range candidate.Drivers {
		add(driver.EvidenceRefIDs)
	}
	for _, findings := range [][]Finding{candidate.RemainingWork, candidate.ReadinessGaps, candidate.Conflicts} {
		for _, finding := range findings {
			add(finding.EvidenceRefIDs)
		}
	}
	for _, path := range candidate.Paths {
		add(path.EvidenceRefIDs)
	}
	return refs
}
