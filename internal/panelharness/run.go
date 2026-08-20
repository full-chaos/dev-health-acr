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
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	startedAt := now()

	group, groupCtx := errgroup.WithContext(ctx)
	results := make([][]panelistMemberResult, len(cfg.Panelists))
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
				// run) propagates.
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
		entry, ok := findConfirmedEntry(turn2Result.ConfirmedStructure, member, receiptID)
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
				Accepted:               entry.Provenance == contractsv1.ContextFabricStructureClarificationConfirmed,
				ConfirmedResultID:      turn2Result.ResultID,
			},
		})
	}
	return memberResults, nil
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
func findConfirmedEntry(confirmed []contractsv1.ContextFabricConfirmedStructureEntry, member, receiptID string) (contractsv1.ContextFabricConfirmedStructureEntry, bool) {
	for _, entry := range confirmed {
		if string(entry.Member) == member && entry.ReceiptID == receiptID {
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
