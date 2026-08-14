package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ErrNoInvestigationSubjects (CHAOS-3810/CHAOS-3811) classifies the one
// failure this ticket exists to make impossible: a canonical fact read
// attempted with neither a discovered subject nor a cohort.
//
// It is the sentinel form of validateCanonicalFactRequest's "canonical fact
// request requires discovered subjects or a cohort" rejection, which used to
// travel to the route as a bare, unclassified error -- straight into
// writeContextFabricError's final fallthrough, i.e. a 500 internal_error with
// retryable=false and a log line carrying nothing but failure_class. That was
// the observable symptom of the resolution defect: every real-corpus
// investigation 500'd.
//
// Engine.Investigate now converts an unresolved/ambiguous resolution into its
// contract outcome BEFORE the fact read (see resolveTerminalStatus), so this
// sentinel should be unreachable from the investigation path. It stays, and
// is asserted at the fact-read call site, because "unreachable" is a claim
// about today's control flow: if a future path reaches the fact read with no
// subjects, it must fail as a NAMED, classified condition rather than
// rediscovering the 500 fallthrough.
var ErrNoInvestigationSubjects = errors.New("context fabric investigation has no discovered subjects or cohort")

// ResultVersionProvider is optionally implemented by an AnswerSynthesizer
// that can report the STATIC half of the version set it would stamp on a
// synthesized result -- everything that does not come from a model receipt
// (service/backend/projection/query/canonical-service versions).
//
// It exists for the terminal results Engine composes WITHOUT a model call
// (clarification_required / no_match): those results still describe a real
// graph read and must not misreport the backend they read as "unwired" just
// because no synthesis ran. Receipt-derived fields (InterpretationVersion,
// SynthesisVersion, ModelIdentity) are deliberately NOT part of this
// interface -- no model produced the result, so Engine fills them with the
// same "unwired" placeholder it already uses for an unwired ModelIdentity.
//
// Optional by design: a synthesizer that does not implement it (every test
// double, and any future implementation) simply yields "unwired" for the
// static fields too. A missing version string is a diagnosability loss, never
// a correctness one, so this must not be a required port method.
type ResultVersionProvider interface {
	StaticResultVersions() VersionSet
}

// The fixed, non-interpolated limitations a terminal result carries. Same
// discipline as retrievalDegradedLimitation: answer-facing prose that names
// no subject, no term, no mechanism, and no error text.
// The terminal limitations. Every one is a fixed, non-interpolated string
// (same discipline as retrievalDegradedLimitation), which is why a count
// distinction needs its own constant rather than a formatted number.
//
// Each pair exists because of CHAOS-3810 codex round-2 F1: a single
// uncommitted candidate is a real, reachable state -- one candidate that
// misses the 0.72 lone-candidate gate is left uncommitted by
// ResolveFromMergedCandidates -- and plural prose beside exactly one listed
// candidate is the same contradiction round-1 P2 fixed for absence prose.
// Unlike statusSentence, which is shared with the model-authored path and
// must stay cause-free, these are written by the engine, which KNOWS the
// cause and names it.
const (
	clarificationRequiredLimitationOne = "One authorized subject matched the question but could not be confirmed as its subject, so no canonical facts were read until it is confirmed."
	clarificationRequiredLimitation    = "The question matched more than one authorized subject, so no canonical facts were read until the intended subject is confirmed."
	// noMatchLimitation states ABSENCE, so it may only be used when the
	// resolution genuinely produced no candidate at all.
	noMatchLimitation = "No authorized subject in this organization's graph matched the question, so no canonical facts were read."
	// The ambiguous-and-clarification-unavailable pair (CHAOS-3810 codex
	// round-1 P2): a no_match result reached WITH candidates attached must
	// not claim nothing matched while the candidates it names sit in the
	// same payload.
	ambiguousNoClarificationLimitationOne = "One authorized subject matched the question but could not be confirmed, and this request did not allow clarification, so no subject was confirmed and no canonical facts were read. That candidate is listed in this result."
	ambiguousNoClarificationLimitation    = "The question matched more than one authorized subject and this request did not allow clarification, so no subject was confirmed and no canonical facts were read. The matching candidates are listed in this result."
)

// fallbackClarificationPrompt is used only when a GraphReader marked a
// resolution ambiguous, the caller allowed clarification, and the backend
// supplied no prompt of its own. It names no candidate (the machine-readable
// candidate list already carries them, receipt-bound and authorization-
// checked), so it is safe to emit for any resolution.
//
// It exists so "ambiguous + AllowClarification" ALWAYS converts to
// clarification_required. Without it the contract's own requirement that a
// clarification result carry a prompt
// (ContextFabricInvestigationResult.Validate) would silently downgrade such a
// resolution to no_match -- telling a caller that nothing matched when in
// fact several things did.
const (
	fallbackClarificationPromptOne = "One authorized subject matched this question but could not be confirmed. Confirm it, using the candidate receipt in this result."
	fallbackClarificationPrompt    = "Several authorized subjects matched this question. Confirm which one you mean, using the candidate receipts in this result."
)

// terminalResult composes the model-free result for an investigation that
// resolved no subject to read facts for.
//
// It is deliberately deterministic and calls no model: with no committed
// subject there is no canonical fact to ground an answer in, so there is
// nothing for a synthesizer to say that would not be invention. It is also
// what makes the outcome reachable when the model runtime is down -- an
// ambiguous question must not need a healthy LLM to be told it is ambiguous.
//
// The result is persisted like any other (immutable store), which is what
// lets the caller's follow-up bind one of the offered candidates back through
// PriorSubjectReceipts -- the clarification loop only closes if the candidate
// receipts are retrievable.
func (e *Engine) terminalResult(
	ctx context.Context,
	principal storage.Principal,
	request InvestigationRequest,
	interpretation InterpretedQuestion,
	resolution SubjectResolution,
	graphContext GraphContext,
	watermark SourceWatermarkSnapshot,
	epoch RebuildEpoch,
) (InvestigationResult, error) {
	status, limitation := resolveTerminalStatus(request, &resolution)
	coverage := graphContext.Coverage
	if coverage.Sources == nil {
		coverage.Sources = []SourceObservation{}
	}
	if coverage.DegradedReasons == nil {
		coverage.DegradedReasons = []string{}
	}
	limitations := []string{limitation}
	// Same fold Investigate applies to a synthesized result, through the
	// same bounded wrapper: a retrieval mechanism that was unavailable
	// narrowed THIS resolution, and that is even more load-bearing here --
	// it may be the reason the resolution found nothing to commit.
	//
	// withRetrievalDegradation rather than a raw append (codex round-5 F1):
	// a hand-rolled append here would be a second place the cap is handled,
	// which is the exact shape of round-17 finding 1, and it would drop the
	// displacement count on the floor.
	degradedDisplaced := 0
	if resolution.RetrievalDegraded {
		limitations, degradedDisplaced = withRetrievalDegradation(limitations)
		coverage.Partial = true
	}
	// CHAOS-3781: a terminal answer speaks for a time too, and says so in
	// the same two ways a synthesized one does.
	//
	// The label is not optional decoration -- the result contract REFUSES a
	// non-current axis carrying no temporal label, so a terminal result
	// composed without one cannot validate. Omitting it turned every
	// historical question that resolved no subject into the validation
	// failure (and the 500) that CHAOS-3810 exists to remove, on the one
	// axis where the terminal outcome is most likely: a subject that did
	// not exist at the requested time resolves to nothing by construction.
	//
	// The grain argument is the zero TemporalGrain because this path read
	// no canonical facts at all. That is not a placeholder -- it is the
	// input temporalCoverage already interprets as "no source gave this
	// answer any temporal precision", yielding GrainNone and
	// CoverageComplete=false, which is exactly true of a result composed
	// without a fact read.
	temporallyLimited, temporalDisplaced := appendTemporalLimitations(limitations, interpretation)
	limitations = temporallyLimited
	answer := statusSentence(status, resolution)
	if status == InvestigationClarificationRequired && resolution.ClarificationPrompt != "" {
		answer += " " + resolution.ClarificationPrompt
	}
	result := InvestigationResult{
		SchemaVersion: InvestigationResultSchemaV1,
		ResultID:      e.newResultID(),
		RequestID:     request.RequestID,
		GeneratedAt:   e.now().UTC(),
		Status:        status,
		Question:      request.Question,
		// Reused is explicitly false for the same reason Investigate sets
		// it explicitly on a fresh synthesized result: only tryReuse's own
		// return value may ever carry true.
		Reused:            false,
		Interpretation:    interpretation,
		SubjectResolution: resolution,
		Cohort:            graphContext.Cohort,
		// Every answer-bearing field stays empty by construction, not by
		// omission: an investigation with no committed subject has read no
		// canonical fact, so it has nothing to judge, no driver to rank,
		// and no evidence to cite. Paths and driver candidates the graph
		// may have discovered are deliberately dropped rather than
		// forwarded -- they describe subjects this result never committed
		// to, and a fact-shaped driver could not close to a ClaimedFact
		// bundle that was never read.
		DirectJudgment:       "",
		CurrentState:         "",
		StrongestPressures:   []string{},
		Drivers:              []DriverJudgment{},
		RemainingWork:        []Finding{},
		ReadinessGaps:        []Finding{},
		Paths:                []RelationshipPath{},
		Conflicts:            []Finding{},
		Limitations:          limitations,
		LimitationsDisplaced: degradedDisplaced + temporalDisplaced,
		EvidenceRefIDs:       []string{},
		ClaimedFacts:         []ClaimedFact{},
		Coverage:             coverage,
		Temporal:             composeTemporalLabel(interpretation, coverage, ""),
		Versions:             e.terminalVersions(),
		DeterministicAnswer:  answer,
		Warnings:             []string{},
	}
	if err := result.Validate(); err != nil {
		return InvestigationResult{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
	}
	if e.results != nil {
		// Keyed exactly as Investigate keys its own Save (CHAOS-3781): on the
		// CLAMPED REQUEST context, which is the value tryReuse keyed its
		// lookup with. Investigate replaces request.TimeContext with the
		// clamped value before any of this runs, so the request this function
		// received already carries it -- re-clamping here could only
		// introduce a difference, and a terminal result saved under a key no
		// lookup will ever form is a row the clarification loop cannot reach.
		if err := e.results.Save(ctx, principal, result, watermark, epoch, TimeAxisKeyFor(request.TimeContext)); err != nil {
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
	}
	return result, nil
}

// resolveTerminalStatus decides which contract status an investigation with
// no committed subject carries, and may fill in a clarification prompt the
// backend left empty. It invents no status: the v1 enum is
// complete/partial/degraded/clarification_required/no_match, complete and
// partial both require a direct judgment (which needs canonical facts), and
// degraded describes limited coverage of an answer that still exists -- so
// the only two outcomes available here are the two named below.
//
// AllowClarification=false with ambiguous candidates resolves to no_match,
// NOT to a refusal or a new status. Two facts of the existing contract force
// it: ContextFabricInvestigationResult.Validate rejects a
// clarification_required result that carries no prompt, and a caller that set
// AllowClarification=false has declined the only thing a prompt could ask
// for. The candidates stay attached to the result either way -- they are
// ranked and receipt-bound, so even a no_match caller can bind one through
// PriorSubjectReceipts on a follow-up.
func resolveTerminalStatus(request InvestigationRequest, resolution *SubjectResolution) (InvestigationStatus, string) {
	if len(resolution.Candidates) == 0 {
		return InvestigationNoMatch, noMatchLimitation
	}
	// Exactly one uncommitted candidate is a REACHABLE state, not a
	// theoretical one: ResolveFromMergedCandidates leaves a lone candidate
	// uncommitted whenever it misses the 0.72 gate. Prose that says "more
	// than one" beside a single listed candidate contradicts the payload it
	// travels with (codex round-2 F1).
	single := len(resolution.Candidates) == 1
	if !request.Options.AllowClarification {
		if single {
			return InvestigationNoMatch, ambiguousNoClarificationLimitationOne
		}
		return InvestigationNoMatch, ambiguousNoClarificationLimitation
	}
	if strings.TrimSpace(resolution.ClarificationPrompt) == "" {
		if single {
			resolution.ClarificationPrompt = fallbackClarificationPromptOne
		} else {
			resolution.ClarificationPrompt = fallbackClarificationPrompt
		}
	}
	if single {
		return InvestigationClarificationRequired, clarificationRequiredLimitationOne
	}
	return InvestigationClarificationRequired, clarificationRequiredLimitation
}

// terminalVersions builds the version set for a model-free terminal result:
// the synthesizer's static versions when it can report them (see
// ResultVersionProvider), Engine's own service/contract versions, and the
// "unwired" placeholder for everything only a model receipt could supply.
func (e *Engine) terminalVersions() VersionSet {
	var versions VersionSet
	if provider, ok := e.synthesizer.(ResultVersionProvider); ok {
		versions = provider.StaticResultVersions()
	}
	versions.ServiceVersion = nonEmptyVersion(versions.ServiceVersion, e.serviceVersion)
	versions.ContractVersion = InvestigationResultSchemaV1
	versions.Backend = nonEmptyVersion(versions.Backend, "")
	versions.ProjectionVersion = nonEmptyVersion(versions.ProjectionVersion, "")
	versions.QueryVersion = nonEmptyVersion(versions.QueryVersion, "")
	versions.CanonicalServiceVersion = nonEmptyVersion(versions.CanonicalServiceVersion, "")
	versions.InterpretationVersion = nonEmptyVersion(versions.InterpretationVersion, "")
	versions.SynthesisVersion = nonEmptyVersion(versions.SynthesisVersion, "")
	versions.ModelIdentity = nonEmptyVersion(versions.ModelIdentity, "")
	return versions
}
