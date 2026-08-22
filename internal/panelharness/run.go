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
	// Options, and (turn 2 only) the Prior*Receipts fields this package
	// itself populates from each panelist's own selections.
	BaseRequest contractsv1.ContextFabricInvestigationRequest
	// Now returns the current time for StartedAt/CompletedAt -- injectable
	// for deterministic tests, defaulting to time.Now in Run.
	Now func() time.Time
}

// panelistMemberResult is one panelist's outcome for one member, carried
// internally between runPanelist and the member-grouping step in Run.
type panelistMemberResult struct {
	member    string
	selection PanelistSelection
}

// Run drives every configured panelist through the two-turn
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
	startedAt := now()

	group, groupCtx := errgroup.WithContext(ctx)
	results := make([][]panelistMemberResult, len(cfg.Panelists))
	panelistErrors := make([]error, len(cfg.Panelists))
	for i, panelist := range cfg.Panelists {
		group.Go(func() error {
			memberResults, err := runPanelist(groupCtx, cfg.OrgID, cfg.Question, cfg.BaseRequest, panelist)
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
		members = append(members, BuildMemberRun(member, len(cfg.Panelists), byMember[member]))
	}
	return PanelRunManifest{
		SchemaVersion:    ManifestSchemaVersion,
		PanelRunID:       panelRunID,
		OrgID:            cfg.OrgID,
		QuestionHash:     contextfabric.QuestionHash(cfg.Question),
		AlgorithmVersion: AlgorithmVersion,
		StartedAt:        startedAt,
		CompletedAt:      now(),
		Members:          members,
	}, nil
}

// runPanelist drives one panelist through turn 1 (ask the question), the
// select step (this panelist's own Selector chooses among offered
// receipts), and turn 2 (re-ask carrying the chosen receipts) -- CHAOS-3860's
// own stated delta over the CHAOS-3742 trial harness: "driving the
// CLARIFICATION flow rather than stopping at first resolution."
func runPanelist(ctx context.Context, orgID, question string, base contractsv1.ContextFabricInvestigationRequest, panelist Panelist) ([]panelistMemberResult, error) {
	// codex adversarial review (round 1, MEDIUM): base is COPIED into
	// every concurrently-running panelist's own turn1Request/turn2Request
	// (Run starts one goroutine per panelist, all sharing the same
	// RunConfig.BaseRequest value) -- a struct copy copies each
	// Prior*Receipts field's SLICE HEADER, not its backing array. If
	// BaseRequest ever arrived with a non-nil Prior*Receipts slice (this
	// harness's own CLI never sets one, but the exported Run/RunConfig API
	// does not prevent a caller from doing so), every panelist's later
	// append in applyPriorReceipts could write into the SAME shared
	// backing array from multiple goroutines at once -- a data race, and
	// worse, a chance for one panelist's receipts to bleed into another's
	// request. Turn 1 and turn 2's Prior*Receipts are entirely this
	// function's own responsibility to construct (turn 1 asks fresh; turn
	// 2's are built by applyPriorReceipts below), so both are explicitly
	// reset to nil before use, guaranteeing every append starts a FRESH
	// backing array no other goroutine can ever see.
	clearPriorReceipts(&base)
	turn1Request := base
	turn1Request.Question = question
	requestID1, err := generateRequestID()
	if err != nil {
		return nil, err
	}
	turn1Result, err := panelist.Client.Investigate(ctx, requestID1, turn1Request)
	if err != nil {
		return nil, fmt.Errorf("panelharness: panelist %s turn 1: %w", panelist.CanonicalModelIdentity, err)
	}
	if turn1Result.StructureNeeds == nil || len(turn1Result.StructureNeeds.Missing) == 0 {
		// Decisive on turn 1, or no structure need surfaced at all --
		// nothing for this panelist to confirm through this flow. Not an
		// error: a genuinely decisive answer is a legitimate outcome, it
		// just contributes no PanelistSelection.
		return nil, nil
	}
	// CHAOS-4118 (codex xhigh review round 1, confirmed real): windowConfirmationRequiredResult
	// (contextfabric/window.go) now composes a window-only StructureNeeds
	// (Missing=["window"], WindowOptions only) on every turn-1 window-gated
	// response. Window rides its own, separately designed WindowSelectionEvent
	// path and is deliberately excluded from projectOffers/applyPriorReceipts
	// (see their own doc comments) -- this package's select-and-continue flow
	// has never supported it. Guarding only on len(Missing)==0 above would let
	// a window-only disclosure fall through to SelectReceipts below with ZERO
	// projected offers for the panelist to choose from -- a real file-exchange
	// round trip against an external responder, waiting up to its own timeout,
	// for a member this flow can never resolve. Guard on whether THIS
	// package's own Selector flow has anything to act on, not merely on
	// whether Missing is non-empty (also correctly short-circuits the same
	// way for any other member whose own offer-builder returns Missing with
	// no options, not only window's case).
	if len(projectOffers(*turn1Result.StructureNeeds)) == 0 {
		return nil, nil
	}

	selections, err := panelist.Selector.SelectReceipts(ctx, question, *turn1Result.StructureNeeds)
	if err != nil {
		return nil, fmt.Errorf("panelharness: panelist %s selection: %w", panelist.CanonicalModelIdentity, err)
	}
	if len(selections) == 0 {
		// The panelist was not confident in any offer -- a legitimate,
		// reportable outcome (this member simply gets no vote from this
		// panelist), never a fabricated choice.
		return nil, nil
	}

	turn2Request := base
	turn2Request.Question = question
	applyPriorReceipts(&turn2Request, turn1Result.ResultID, selections)
	requestID2, err := generateRequestID()
	if err != nil {
		return nil, err
	}
	turn2Result, err := panelist.Client.Investigate(ctx, requestID2, turn2Request)
	if err != nil {
		return nil, fmt.Errorf("panelharness: panelist %s turn 2: %w", panelist.CanonicalModelIdentity, err)
	}

	memberResults := make([]panelistMemberResult, 0, len(selections))
	for member, receiptID := range selections {
		entry, ok := findConfirmedEntry(turn2Result.ConfirmedStructure, member, turn1Result.ResultID, receiptID)
		if !ok || entry.Disposition != contractsv1.ContextFabricStructureDispositionApplied {
			// Vetoed, superseded, or otherwise not applied -- this
			// panelist's attempted confirmation did not actually land, so
			// it contributes no selection for this member. Reported
			// nowhere but the ordinary hosted-API telemetry this call
			// already produces; not a manifest concern.
			continue
		}
		memberResults = append(memberResults, panelistMemberResult{
			member: member,
			selection: PanelistSelection{
				CanonicalModelIdentity: panelist.CanonicalModelIdentity,
				PriorResultID:          turn1Result.ResultID,
				ReceiptID:              receiptID,
				AppliedValue:           entry.AppliedValue,
				// Accepted reports whether this receipt was the
				// TOP-RANKED (rank 0) offer turn 1 presented for member --
				// PanelistSelection's own doc comment, matching
				// StructureSelectionEvent.Accepted's identical semantics.
				// NOT entry.Provenance: every successfully redeemed
				// structure receipt on this flow carries
				// ContextFabricStructureClarificationConfirmed provenance
				// regardless of rank, so that comparison is always true
				// and would never actually measure acceptance of the
				// engine's own leading proposal.
				Accepted:          offerIsTopRanked(*turn1Result.StructureNeeds, member, receiptID),
				ConfirmedResultID: turn2Result.ResultID,
			},
		})
	}
	return memberResults, nil
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

// applyPriorReceipts buckets selections (member -> receipt id) into
// turn2Request's own three Prior*Receipts fields, matching each closed
// member value to its namespace exactly (design brief §2.1's own receipt
// prefixes) -- window is deliberately absent from this switch (out of this
// package's scope, see manifest.go).
func applyPriorReceipts(request *contractsv1.ContextFabricInvestigationRequest, priorResultID string, selections map[string]string) {
	for member, receiptID := range selections {
		receipt := contractsv1.ContextFabricBoundSubjectReceipt{ReceiptID: receiptID, ResultID: priorResultID}
		switch contractsv1.ContextFabricStructureNeedKind(member) {
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
