package panelharness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"golang.org/x/sync/errgroup"
)

// Panelist is one configured panel member: a real, credentialed Client
// (the "3860 guard" -- real per-model credentials, never a shared or
// synthetic principal) paired with a Selector that decides which offered
// receipts, if any, this panelist's model is confident enough to confirm.
type Panelist struct {
	CanonicalModelIdentity string
	Client                 *Client
	Selector               Selector
}

// DefaultMaxClarificationTurns bounds how many Investigate calls runPanelist
// will drive one panelist through (CHAOS-4146(a)) before giving up and
// recording a turn_exhausted outcome -- turn 1 (the initial ask) counts
// toward this bound like every later clarification round. A caller may
// override via RunConfig.MaxClarificationTurns; this is only the default
// applied when that field is zero.
const DefaultMaxClarificationTurns = 4

// RunConfig configures one full panel run: one org, one question, driven
// through every configured panelist independently and in parallel.
type RunConfig struct {
	// OrgID is a CALLER-ASSERTED label carried onto the manifest -- it is
	// NOT independently verified against any panelist's own credential
	// (codex adversarial review, round 1, HIGH: this package has no
	// "whoami"/introspection call to cross-check a bearer token's actual
	// org scope, and neither ContextFabricInvestigationResult nor any
	// other hosted-API response body echoes org_id back to the caller --
	// the hosted side enforces org scoping server-side via the
	// credential's own storage.Principal, which this package never
	// observes). The real enforcement boundary is OPERATIONAL: every
	// panelist's credential must be minted against THIS SAME org via
	// `acr-api credentials create --org-id <this-org>` (see
	// cmd/acr-panel-harness's own doc comment) -- a token from a
	// different org will simply run this harness against ITS OWN org's
	// data while the manifest silently records the asserted OrgID,
	// mismatched. Closing this gap for real needs a hosted-side
	// introspection endpoint this package does not have and is not
	// authorized to add unilaterally; flagged to the orchestrator as an
	// architectural fork, not solved here.
	OrgID     string
	Question  string
	Panelists []Panelist
	// BaseRequest supplies every field of the investigation request OTHER
	// than Question/SchemaVersion/RequestID/Consumer, which Run/Client.Investigate
	// always overwrite per-panelist-per-turn: RequestedScope, TimeContext,
	// Options, and (every turn after the first) the Prior*Receipts fields
	// this package itself populates from each panelist's own selections.
	BaseRequest contractsv1.ContextFabricInvestigationRequest
	// Now returns the current time for StartedAt/CompletedAt -- injectable
	// for deterministic tests, defaulting to time.Now in Run.
	Now func() time.Time
	// MaxClarificationTurns overrides DefaultMaxClarificationTurns when
	// positive; zero (the common case) uses the default.
	MaxClarificationTurns int
	// CaseIndex/RunTag/CorpusPath/CorpusSHA256 (CHAOS-4146(c), schema v2)
	// are the batch corpus driver's own provenance -- stamped verbatim
	// onto the manifest and every PanelMemberRun when set. All four are
	// the zero value for a single ad-hoc (-question) run; nil is
	// CaseIndex's own "not a corpus-driven run" state, never a real
	// index of zero silently collapsed into "absent" (this is why it is
	// a pointer, not a bare int).
	CaseIndex    *int
	RunTag       string
	CorpusPath   string
	CorpusSHA256 string
}

// panelistMemberResult is one panelist's outcome for one member, carried
// internally between runPanelist and the member-grouping step in Run.
type panelistMemberResult struct {
	member    string
	selection PanelistSelection
}

// Run drives every configured panelist through the bounded N-turn
// select-and-continue flow in parallel, then assembles the immutable
// PanelRunManifest. It NEVER touches Postgres and NEVER constructs a
// contextfabric.ConsensusEvidence value -- see this package's own doc
// comment (manifest.go) for why that is the ruling's own pinned boundary,
// not an oversight.
func Run(ctx context.Context, cfg RunConfig) (PanelRunManifest, error) {
	if len(cfg.Panelists) == 0 {
		return PanelRunManifest{}, fmt.Errorf("panelharness: at least one panelist is required")
	}
	// codex adversarial review (round 1, HIGH): nothing previously stopped
	// two panelist CONFIGS from pointing at the SAME underlying credential
	// (only their CanonicalModelIdentity strings had to differ) -- one
	// authenticated principal would then be counted as several distinct
	// "panelists" for the required invariant's own distinct-identities
	// check, which trusts the label, not the credential. Fail closed
	// before any network call: TokenFingerprint is a one-way digest, so
	// this never handles or logs a raw token.
	seenFingerprints := make(map[string]string, len(cfg.Panelists)) // fingerprint -> first identity that used it
	for _, panelist := range cfg.Panelists {
		if panelist.Client == nil {
			return PanelRunManifest{}, fmt.Errorf("panelharness: panelist %s has no Client configured", panelist.CanonicalModelIdentity)
		}
		fingerprint := panelist.Client.TokenFingerprint()
		if first, duplicate := seenFingerprints[fingerprint]; duplicate {
			return PanelRunManifest{}, fmt.Errorf("panelharness: panelists %q and %q share the same bearer credential -- every panelist must use its OWN, independently minted token", first, panelist.CanonicalModelIdentity)
		}
		seenFingerprints[fingerprint] = panelist.CanonicalModelIdentity
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	maxTurns := cfg.MaxClarificationTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxClarificationTurns
	}
	startedAt := now()

	group, groupCtx := errgroup.WithContext(ctx)
	results := make([][]panelistMemberResult, len(cfg.Panelists))
	logs := make([]PanelistClarificationLog, len(cfg.Panelists))
	panelistErrors := make([]error, len(cfg.Panelists))
	for i, panelist := range cfg.Panelists {
		group.Go(func() error {
			memberResults, log, err := runPanelist(groupCtx, cfg.OrgID, cfg.Question, cfg.BaseRequest, panelist, maxTurns)
			logs[i] = log
			if err != nil {
				// A single panelist's failure (timeout, credential
				// rejected, no fitting offer) never fails the whole run --
				// it is reported as that panelist simply contributing no
				// selection for any member, which BuildMemberRun's own
				// Complete check already treats honestly (missing
				// panelist => incomplete, never silently ignored). Only a
				// context cancellation (the caller gave up on the WHOLE
				// run) propagates immediately; every other per-panelist
				// error is recorded (panelistErrors) and checked once
				// every panelist has finished, below.
				panelistErrors[i] = err
				if groupCtx.Err() != nil {
					return err
				}
				return nil
			}
			results[i] = memberResults
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return PanelRunManifest{}, fmt.Errorf("panelharness: panel run canceled: %w", err)
	}
	// codex adversarial review (round 1, MEDIUM): a run where EVERY
	// panelist errored (bad credentials, unreachable API, every turn
	// timed out) previously returned a successful, zero-member manifest
	// -- indistinguishable from a run where every panelist genuinely
	// found the question decisive on turn 1 (also zero members, also
	// legitimate). Surface the former as an error instead of silently
	// asserting a member-less manifest is meaningful.
	if allPanelistsFailed(panelistErrors) {
		return PanelRunManifest{}, fmt.Errorf("panelharness: every configured panelist failed (first error: %w)", firstNonNil(panelistErrors))
	}

	byMember := make(map[string][]PanelistSelection)
	memberOrder := make([]string, 0, 4)
	for _, panelistResults := range results {
		for _, entry := range panelistResults {
			if _, seen := byMember[entry.member]; !seen {
				memberOrder = append(memberOrder, entry.member)
			}
			byMember[entry.member] = append(byMember[entry.member], entry.selection)
		}
	}

	panelRunID, err := generatePanelRunID()
	if err != nil {
		return PanelRunManifest{}, err
	}
	members := make([]PanelMemberRun, 0, len(memberOrder))
	for _, member := range memberOrder {
		built := BuildMemberRun(member, len(cfg.Panelists), byMember[member])
		// CaseIndex is denormalized onto every member row (see its own doc
		// comment, manifest.go) -- stamped here, once, rather than
		// threaded through BuildMemberRun's own signature, which several
		// existing tests call directly with the pre-CHAOS-4146(c) shape.
		built.CaseIndex = cfg.CaseIndex
		members = append(members, built)
	}
	// CHAOS-4146(a): keep only the logs of panelists that actually attempted
	// at least one turn -- a nil Client guard failure (rejected before any
	// goroutine ran) never reaches runPanelist, so its zero-value log would
	// otherwise contribute a spurious, empty CanonicalModelIdentity entry.
	clarificationLogs := make([]PanelistClarificationLog, 0, len(logs))
	for _, log := range logs {
		if log.CanonicalModelIdentity == "" {
			continue
		}
		clarificationLogs = append(clarificationLogs, log)
	}
	return PanelRunManifest{
		SchemaVersion:     ManifestSchemaVersion,
		PanelRunID:        panelRunID,
		OrgID:             cfg.OrgID,
		QuestionHash:      contextfabric.QuestionHash(cfg.Question),
		AlgorithmVersion:  AlgorithmVersion,
		StartedAt:         startedAt,
		CompletedAt:       now(),
		Members:           members,
		ClarificationLogs: clarificationLogs,
		CaseIndex:         cfg.CaseIndex,
		RunTag:            cfg.RunTag,
		CorpusPath:        cfg.CorpusPath,
		CorpusSHA256:      cfg.CorpusSHA256,
	}, nil
}

// receiptRef names one structure-offer receipt this panelist chose,
// carrying its own originating result id -- ContextFabricConfirmedStructureEntry's
// own doc comment states receipts are "globally scoped by (PriorResultID,
// ReceiptID): a bare receipt id is only unique within its issuing result,"
// so a multi-turn accumulator cannot share one priorResultID across every
// entry the way the old fixed two-turn flow could. accepted is captured at
// SELECTION time (against the StructureNeeds the offer actually came from),
// never recomputed later once that turn's own response is out of scope.
type receiptRef struct {
	member        string
	priorResultID string
	receiptID     string
	accepted      bool
}

// runPanelist drives one panelist through a bounded (maxTurns) clarification
// loop -- CHAOS-4146(a)'s own generalization of the prior fixed two-turn
// flow: ask, and for as long as the response keeps surfacing an actionable
// StructureNeeds AND the panelist's own Selector keeps choosing among the
// offers, re-ask carrying every receipt confirmed so far. Confirmed contract
// state is NOT retained server-side across calls (canonicalizeStructure
// derives structure fresh from what each request itself carries -- see
// GraphReader.ResolveSubjects' own doc comment), so every subsequent
// request must resend the FULL accumulated set of previously-applied
// receipts, not just the newest turn's -- dropping an earlier turn's
// receipt from a later request would silently un-confirm it. Returns the
// landed PanelistSelection per member this panelist actually confirmed,
// plus this panelist's own turn-by-turn clarification log (returned even on
// error, reflecting whatever turns were actually attempted before failing).
func runPanelist(ctx context.Context, orgID, question string, base contractsv1.ContextFabricInvestigationRequest, panelist Panelist, maxTurns int) ([]panelistMemberResult, PanelistClarificationLog, error) {
	if maxTurns <= 0 {
		maxTurns = DefaultMaxClarificationTurns
	}
	log := PanelistClarificationLog{CanonicalModelIdentity: panelist.CanonicalModelIdentity}

	// codex adversarial review (round 1, MEDIUM), preserved verbatim under
	// the N-turn generalization: base is COPIED into every concurrently
	// running panelist's own request (Run starts one goroutine per
	// panelist, all sharing the same RunConfig.BaseRequest value) -- a
	// struct copy copies each Prior*Receipts field's SLICE HEADER, not its
	// backing array. Every turn's request is built by copying base fresh
	// and re-applying the full accumulated receipt set, so no goroutine
	// ever appends into a backing array another goroutine might also hold.
	clearPriorReceipts(&base)

	confirmedByMember := make(map[string]receiptRef, 4) // member -> latest applied ref, resent every later turn
	appliedValue := make(map[string]string, 4)          // member -> the applied value that ref actually confirmed
	confirmedAtResult := make(map[string]string, 4)     // member -> result id where that confirmation was observed
	// pending is the FULL set of refs the just-sent request carried --
	// every accumulated (already-applied, resent) ref PLUS whatever was
	// newly selected that turn, never just the newest selections alone
	// (codex xhigh review, HIGH: ContextFabricConfirmedStructureEntry's own
	// doc comment says the response carries "one entry PER carried member,
	// INCLUDING vetoed ones" -- an ALREADY-applied member can flip to
	// vetoed_stale/vetoed_conflict on a LATER turn, e.g. a graph epoch
	// flip or a conflicting confirmation elsewhere in the same request;
	// reconciling only the newest selections would silently keep a
	// since-invalidated confirmation in the manifest forever).
	var pending []receiptRef

	request := base
	request.Question = question

	for turn := 1; turn <= maxTurns; turn++ {
		requestID, err := generateRequestID()
		if err != nil {
			return nil, log, err
		}
		result, err := panelist.Client.Investigate(ctx, requestID, request)
		if err != nil {
			return nil, log, fmt.Errorf("panelharness: panelist %s turn %d: %w", panelist.CanonicalModelIdentity, turn, err)
		}

		for _, ref := range pending {
			entry, ok := findConfirmedEntry(result.ConfirmedStructure, ref.member, ref.priorResultID, ref.receiptID)
			if !ok || entry.Disposition != contractsv1.ContextFabricStructureDispositionApplied {
				// Vetoed, superseded, or otherwise not applied -- dropped
				// from every accumulator, including one this member landed
				// on an EARLIER turn (see pending's own doc comment above).
				// If this member is still needed, the engine's own next
				// StructureNeeds re-offers it fresh.
				delete(confirmedByMember, ref.member)
				delete(appliedValue, ref.member)
				delete(confirmedAtResult, ref.member)
				continue
			}
			confirmedByMember[ref.member] = ref
			appliedValue[ref.member] = entry.AppliedValue
			confirmedAtResult[ref.member] = result.ResultID
		}
		pending = nil

		if result.StructureNeeds == nil || len(result.StructureNeeds.Missing) == 0 {
			// Decisive, or no structure need surfaced at all -- a
			// legitimate terminal outcome, not an error.
			log.Turns = append(log.Turns, ClarificationTurnEvent{Turn: turn, Outcome: ClarificationTurnDecisive})
			break
		}

		offers := projectOffers(*result.StructureNeeds)
		if len(offers) == 0 {
			// CHAOS-4118: a window-only (or otherwise unrepresentable)
			// StructureNeeds has nothing this flow's Selector can act on --
			// see projectOffers/applyPriorReceipts' own doc comments for
			// why window is deliberately excluded.
			log.Turns = append(log.Turns, ClarificationTurnEvent{Turn: turn, Outcome: ClarificationTurnRefusedNoOffers})
			break
		}
		offerKinds := offerKindsSeen(offers)

		selections, err := panelist.Selector.SelectReceipts(ctx, question, *result.StructureNeeds)
		if err != nil {
			return nil, log, fmt.Errorf("panelharness: panelist %s selection (turn %d): %w", panelist.CanonicalModelIdentity, turn, err)
		}
		// Selector's own doc comment: "a member absent from the map, or
		// mapped to an empty string, means the panelist found no offer
		// worth confirming for that member" -- filter blank values out
		// before treating the map as a genuine selection (codex xhigh
		// review, MEDIUM: a map containing only empty/blank values used to
		// be treated as a non-empty, confident selection).
		for member, receiptID := range selections {
			if receiptID == "" {
				delete(selections, member)
			}
		}
		if len(selections) == 0 {
			// Not confident in any offer -- a legitimate, reportable
			// refusal, never a fabricated choice.
			log.Turns = append(log.Turns, ClarificationTurnEvent{Turn: turn, Outcome: ClarificationTurnRefusedNotConfident, OfferKinds: offerKinds})
			break
		}
		if turn == maxTurns {
			// The panelist would continue, but the turn budget is spent --
			// never spend a selection round the loop cannot act on.
			log.Turns = append(log.Turns, ClarificationTurnEvent{Turn: turn, Outcome: ClarificationTurnExhausted, OfferKinds: offerKinds})
			break
		}
		log.Turns = append(log.Turns, ClarificationTurnEvent{Turn: turn, Outcome: ClarificationTurnContinued, OfferKinds: offerKinds})

		newRefs := make([]receiptRef, 0, len(selections))
		for member, receiptID := range selections {
			newRefs = append(newRefs, receiptRef{
				member: member, priorResultID: result.ResultID, receiptID: receiptID,
				// Accepted reports whether this receipt was the
				// TOP-RANKED (rank 0) offer THIS turn presented for
				// member -- captured now, against this turn's own
				// StructureNeeds, matching PanelistSelection's own doc
				// comment. NOT entry.Provenance: every successfully
				// redeemed structure receipt on this flow carries
				// ContextFabricStructureClarificationConfirmed provenance
				// regardless of rank, so that comparison would never
				// actually measure acceptance of the engine's own
				// leading proposal.
				accepted: offerIsTopRanked(*result.StructureNeeds, member, receiptID),
			})
		}

		nextRequest := base
		nextRequest.Question = question
		refsToResend := make([]receiptRef, 0, len(confirmedByMember)+len(newRefs))
		for _, ref := range confirmedByMember {
			refsToResend = append(refsToResend, ref)
		}
		refsToResend = append(refsToResend, newRefs...)
		applyReceiptRefs(&nextRequest, refsToResend)

		// pending is the FULL carried set (see its own doc comment above),
		// not just newRefs -- every one of these must be reconciled against
		// the NEXT turn's own response.
		pending = refsToResend
		request = nextRequest
	}

	memberResults := make([]panelistMemberResult, 0, len(confirmedByMember))
	for member, ref := range confirmedByMember {
		memberResults = append(memberResults, panelistMemberResult{
			member: member,
			selection: PanelistSelection{
				CanonicalModelIdentity: panelist.CanonicalModelIdentity,
				PriorResultID:          ref.priorResultID,
				ReceiptID:              ref.receiptID,
				AppliedValue:           appliedValue[member],
				Accepted:               ref.accepted,
				ConfirmedResultID:      confirmedAtResult[member],
			},
		})
	}
	return memberResults, log, nil
}

// offerKindsSeen returns the distinct closed-vocabulary member kinds
// projectOffers actually offered this turn, in first-seen order -- the
// per-turn "offer kinds seen" telemetry CHAOS-4146(a) requires.
func offerKindsSeen(offers []offerProjection) []string {
	seen := make(map[string]struct{}, len(offers))
	kinds := make([]string, 0, len(offers))
	for _, offer := range offers {
		if _, ok := seen[offer.Member]; ok {
			continue
		}
		seen[offer.Member] = struct{}{}
		kinds = append(kinds, offer.Member)
	}
	return kinds
}

// allPanelistsFailed reports whether every entry in errs is non-nil --
// i.e. every configured panelist errored, with not even one genuinely
// decisive (zero-error, zero-selection) outcome among them.
func allPanelistsFailed(errs []error) bool {
	for _, err := range errs {
		if err == nil {
			return false
		}
	}
	return len(errs) > 0
}

// firstNonNil returns the first non-nil error in errs, or nil if none.
func firstNonNil(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// clearPriorReceipts resets every Prior*Receipts field to nil -- see
// runPanelist's own call-site comment for why this matters even though
// this harness's own CLI never populates them on BaseRequest today.
func clearPriorReceipts(request *contractsv1.ContextFabricInvestigationRequest) {
	request.PriorSubjectReceipts = nil
	request.PriorWindowReceipts = nil
	request.PriorKindReceipts = nil
	request.PriorAnchorReceipts = nil
	request.PriorHandleReceipts = nil
}

// applyReceiptRefs buckets refs into request's own three Prior*Receipts
// fields, matching each closed member value to its namespace exactly
// (design brief §2.1's own receipt prefixes) -- window is deliberately
// absent from this switch (out of this package's scope, see manifest.go).
// Unlike the prior fixed two-turn flow's applyPriorReceipts, each ref
// carries its OWN priorResultID: a multi-turn accumulator resends receipts
// that originated from DIFFERENT turns' results in a single later request,
// and receipts are only unique within their own issuing result (see
// receiptRef's own doc comment).
func applyReceiptRefs(request *contractsv1.ContextFabricInvestigationRequest, refs []receiptRef) {
	for _, ref := range refs {
		receipt := contractsv1.ContextFabricBoundSubjectReceipt{ReceiptID: ref.receiptID, ResultID: ref.priorResultID}
		switch contractsv1.ContextFabricStructureNeedKind(ref.member) {
		case contractsv1.ContextFabricStructureNeedExpectedKind:
			request.PriorKindReceipts = append(request.PriorKindReceipts, receipt)
		case contractsv1.ContextFabricStructureNeedSubjectAnchor:
			request.PriorAnchorReceipts = append(request.PriorAnchorReceipts, receipt)
		case contractsv1.ContextFabricStructureNeedSubjectHandle:
			request.PriorHandleReceipts = append(request.PriorHandleReceipts, receipt)
		}
	}
}

// findConfirmedEntry locates the ConfirmedStructure entry matching member
// and receiptID -- ConfirmedStructure carries one entry per carried
// member, so a linear scan over its (always small, single-digit) length is
// simplest and clearest.
// findConfirmedEntry matches on the FULL (member, priorResultID, receiptID)
// tuple, not receipt/member alone (codex round 1, HIGH): receipts are
// globally scoped by (PriorResultID, ReceiptID) --
// ContextFabricConfirmedStructureEntry's own doc comment states this
// explicitly ("a bare receipt id is only unique within its issuing
// result") -- so matching on member+receiptID alone could attribute a
// response entry to the WRONG prior result if a receipt id ever collided
// across two different stored results. priorResultID here is always
// turn1Result.ResultID, the result THIS package's own turn-1 call actually
// received the offer from.
func findConfirmedEntry(confirmed []contractsv1.ContextFabricConfirmedStructureEntry, member, priorResultID, receiptID string) (contractsv1.ContextFabricConfirmedStructureEntry, bool) {
	for _, entry := range confirmed {
		if string(entry.Member) == member && entry.PriorResultID == priorResultID && entry.ReceiptID == receiptID {
			return entry, true
		}
	}
	return contractsv1.ContextFabricConfirmedStructureEntry{}, false
}

func generatePanelRunID() (string, error) {
	id, err := randomHex(16)
	if err != nil {
		return "", fmt.Errorf("panelharness: generate panel run id: %w", err)
	}
	return "panel_run_" + id, nil
}

func generateRequestID() (string, error) {
	id, err := randomHex(16)
	if err != nil {
		return "", fmt.Errorf("panelharness: generate request id: %w", err)
	}
	return "request_" + id, nil
}

func randomHex(bytesLength int) (string, error) {
	buffer := make([]byte, bytesLength)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
