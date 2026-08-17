package contextfabric

import (
	"context"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3859 phase 1 (CAPTURE ONLY, chris-ratified 2026-08-16): ~85-90% of
// corpus questions end at clarification_required with candidate options.
// When a caller resolves one via PriorSubjectReceipts, that resolution is a
// labeled (question -> subject) pair at real production distribution -- the
// training signal CHAOS-3860's learning-loop epic depends on. This file
// defines what gets captured and the port Engine calls; it does NOT feed
// anything back into ranking, thresholds, or retrieval (no learned aliases,
// no threshold consumption) -- those are separately ratified follow-on
// phases per the ticket's own explicit boundary.

// ClarificationOfferedCandidate is one candidate from the ORIGINAL
// clarification_required result's SubjectResolution.Candidates, captured
// alongside which one (if any) the caller went on to select. Field-for-field
// this mirrors SubjectCandidate's identity-bearing fields (ReceiptID,
// Subject, State, Confidence) plus a Rank this type adds: the candidate's
// 0-indexed position in the ORIGINAL Candidates slice, captured explicitly
// rather than left to be re-derived later by re-sorting on Confidence --
// ties or a future non-confidence ranking change would silently reorder a
// re-derived rank, but this field is what the pipeline ACTUALLY presented
// the caller, in the order it actually presented it.
type ClarificationOfferedCandidate struct {
	ReceiptID          string  `json:"receipt_id"`
	SubjectKind        string  `json:"subject_kind"`
	SubjectCanonicalID string  `json:"subject_canonical_id"`
	SubjectLabel       string  `json:"subject_label"`
	State              string  `json:"state"`
	Confidence         float64 `json:"confidence"`
	Rank               int     `json:"rank"`
}

// ClarificationSelectionEvent is one observed "the caller resolved a prior
// clarification_required result to this specific candidate" fact --
// Engine.resolvePriorSubjectHints builds one every time a PriorSubjectReceipt
// it is expanding actually matches a candidate in the named prior result.
type ClarificationSelectionEvent struct {
	OrgID      string
	CapturedAt time.Time
	// QuestionHash is QuestionHash(priorResult.Question) -- the SAME
	// canonicalizing hash contextfabric.QuestionHash/CanonicalizeQuestion
	// already computes for answer-reuse keys (answer_reuse.go), reused
	// here rather than a second, independently-maintained hash. This is
	// the "question phrasing" half of the (question -> subject) pair the
	// ticket asks for; raw question TEXT is deliberately never part of
	// this event -- see the doc comment on Sink.RecordSelection in
	// pgclarification for why (no repo privacy doc mandates hash-only,
	// but the ticket's own literal field list says "question hash/
	// features", not text, and this event follows that instruction
	// exactly).
	QuestionHash string
	// PriorResultID names the clarification_required InvestigationResult
	// OfferedCandidates and Selected were both read from -- the join key
	// back to that immutable row (its own Versions, GeneratedAt, etc.)
	// for anything this capture-only phase does not itself duplicate.
	PriorResultID string
	// OfferedCandidates is the COMPLETE candidate set the prior result
	// offered -- every entry of its SubjectResolution.Candidates, not
	// only the one that was picked. A training signal needs the negative
	// examples (the candidates NOT chosen) as much as the positive one.
	OfferedCandidates []ClarificationOfferedCandidate
	// Selected is the single entry of OfferedCandidates whose ReceiptID
	// matched the caller's PriorSubjectReceipt -- echoed separately so a
	// consumer never has to re-scan OfferedCandidates to find it.
	Selected ClarificationOfferedCandidate
	// SelectionProvenance is a BEST-EFFORT human-vs-agent proxy -- see
	// clarificationSelectionProvenance's doc comment. No field in this
	// codebase today records a caller-asserted, trustworthy "a human, not
	// an agent, picked this" fact (CHAOS-3859's own inventory confirmed
	// this gap); this is the closest available signal, not a precise
	// classification, and is documented as such at both ends.
	SelectionProvenance string
	// The remaining fields are the deployment-CURRENT pipeline/gate
	// config active at the MOMENT this selection was observed (not
	// necessarily the config active when the candidates were originally
	// offered -- that snapshot is recoverable by joining PriorResultID
	// back to the immutable investigation_results row's own Versions and
	// save-time reuse-key columns, CHAOS-3833/3862). Reusing the exact
	// CHAOS-3833/3862 reuse-key types rather than inventing a parallel
	// shape: these are already the extensible surface "the sweep knobs
	// may soon vary" describes, and Engine already carries them as its
	// own fields.
	ProjectionVersion  string
	ModelIdentities    []string
	RetrievalIdentity  ReuseRetrievalIdentity
	PromptVersions     ReusePromptVersions
	VersionAuthorities ReuseVersionAuthorities
}

// ClarificationSelectionSink is notified once per successfully-matched
// PriorSubjectReceipt (CHAOS-3859 capture phase). Optional dependency: nil
// means capture is off, matching every other optional Context Fabric
// dependency's "absent means degrade, never fail" convention (ReuseGate,
// Telemetry, ...).
//
// RecordSelection MUST return promptly and carries no error return by
// design -- Engine calls it SYNCHRONOUSLY from inside Investigate's hot
// path (resolvePriorSubjectHints), exactly like
// EngineTelemetry.RecordAnswerReuse, and capture must NEVER break or delay
// an investigation. An implementation backed by durable storage (a database
// round trip) MUST do the actual write on its own background worker with
// its own bounded timeout, never inline in this call -- see
// pgclarification.Sink for the reference bounded-queue implementation this
// contract is written against. A caller-visible error here would be a
// capture-only feature reaching back into the answer path it is supposed to
// be purely downstream of.
type ClarificationSelectionSink interface {
	RecordSelection(ctx context.Context, event ClarificationSelectionEvent)
}

// clarificationSelectionProvenance derives a BEST-EFFORT human-vs-agent
// proxy from the two signals that exist today, neither of which was
// designed for this purpose:
//
//   - storage.Principal.AuthenticationMethod is auth-derived (never
//     caller-asserted): AuthenticationMethodWebAssertion means a Dev
//     Health web session authenticated this call -- the strongest
//     available "a human is driving a browser" signal this codebase has.
//   - ConsumerInfo.Surface is caller-supplied for most callers, but the
//     MCP sidecar hardcodes it to the literal "mcp" specifically so a
//     tool argument can never spoof it (internal/sidecar/
//     api_client_investigation.go) -- so "mcp" specifically IS
//     spoof-resistant, even though Surface as a whole is not.
//
// Neither signal was built to answer "did a human or an agent pick this
// candidate" -- an agent can run inside a web session, and a human can
// paste MCP tool output by hand. This function documents that limitation
// rather than hiding it: the returned label names WHICH signal produced
// it, not a bare "human"/"agent" claim.
func clarificationSelectionProvenance(principal storage.Principal, consumer ConsumerInfo) string {
	surface := strings.TrimSpace(consumer.Surface)
	switch {
	case principal.AuthenticationMethod == storage.AuthenticationMethodWebAssertion:
		return "web_assertion"
	case surface == "mcp":
		return "credential_mcp"
	case surface != "":
		return "credential_" + surface
	default:
		return "credential_unknown_surface"
	}
}
