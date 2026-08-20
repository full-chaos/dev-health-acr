// Package structurepriorcuration is CHAOS-3977 P5's own batch curation job
// (pivot-intent design brief, DESIGN-FINAL, §3.2): capture -> candidate
// priors. NEVER in any serve path -- cmd/acr-projector's "priors curate"
// subcommand is the sole production caller, run offline per org.
//
// Deterministic and re-runnable from the event log (design brief §3.2): the
// exported Curate function is PURE (no I/O, no clock, no randomness) so the
// SAME input rows plus the SAME frozenQuestionHashes always produce the
// SAME output, and a curation-RULE change is a version bump
// (contextfabric.CurationRuleVersionV1), never an in-place edit of an
// already-published version.
//
// SCOPE (v1, disclosed -- see this repository's PR description): curation
// reads ONLY acr.context_fabric_structure_selections (CHAOS-3927 P4's own
// capture table, shipped) and promotes ONLY expected_kind entries.
//   - subject_anchor/subject_handle rows ARE read and support-counted
//     (Support fields below), but never promoted into a published version's
//     entry set in v1: the capture schema records each selection's
//     AppliedValue only (structure_capture.go's own StructureOfferedOption
//     shape) -- for subject_handle that value is the handle's literal text
//     alone, with no Kind/PatternID captured alongside it, which the
//     runtime offer-time grammar check (contextfabric.HandleGrammarChecker)
//     needs and this package cannot safely guess. Design brief §3.5 names
//     the fix: "curation harvests from BOTH the sink table and a backfill
//     scan over persisted results' ConfirmedStructure + StructureNeeds
//     offer sets" -- the backfill scan (which WOULD carry Kind/PatternID,
//     read off the stored HandleOption) is a named v2 follow-on, not
//     implemented here. subject_anchor has its own, separate scope trim --
//     see priors_consult.go's own doc comment.
//   - window entries are never produced at all in v1: design brief §2.4
//     names a WindowSelectionEvent (3900 W3) as the capture source for
//     window confirmations, and grepping this repository at P5's own start
//     found zero implementation of it (no migration, no sink, no table) --
//     the runtime consultation mechanism (Engine.resolveWindowPriorProposal)
//     ships now, dark, and starts curating real proposals the moment W3's
//     own capture lands; until then it always degrades to "no proposal",
//     harmlessly.
//   - sink-only, no persisted-result backfill (codex adversarial review,
//     medium finding, ACKNOWLEDGED -- not narrowed to just the
//     handle/anchor trims above): ReadSelections consumes ONLY
//     context_fabric_structure_selections, which is explicitly lossy by
//     design (background-queued, drops on a full queue -- structure_capture.go's
//     own doc comment). Design brief §3.5's own loss bound requires
//     curation to ALSO backfill from persisted results' own
//     ConfirmedStructure + StructureNeeds offer sets (the durable,
//     authoritative record) -- not implemented here. This means even
//     v1's ONE promoted member (expected_kind) can under-count a real
//     human confirmation the sink dropped, not only the handle/anchor
//     members already trimmed above. The v1 promotion gate (>=1
//     human_panel event) fails toward NOT promoting on a drop, never
//     toward wrongly promoting -- so this is a reach gap (a real prior
//     that should exist doesn't get curated), not a correctness gap (no
//     wrong prior is ever curated from a drop). The backfill scan is a
//     named v2 follow-on for ALL members, not handle/anchor alone.
package structurepriorcuration

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SelectionEvent is one row read from acr.context_fabric_structure_selections
// -- curation's own read-only input shape, deliberately separate from
// contextfabric.StructureSelectionEvent (Engine's in-memory capture event,
// which carries far more than curation needs and would couple this package
// to the engine's own capture-time dependencies).
type SelectionEvent struct {
	OrgID                string
	QuestionHash         string
	Member               string
	SelectedAppliedValue string
	SelectionMode        string
	SelectionProvenance  string
}

// ReadSelections reads every acr.context_fabric_structure_selections row
// for orgID -- read-only, never mutates P4's own capture table. v1 reads
// the org's FULL history each curation run (no watermark cursor) --
// deterministic and re-runnable regardless (design brief §3.2), simply not
// incremental; a later changeset MAY add a watermark parameter without
// changing this function's own contract.
func ReadSelections(ctx context.Context, db *sql.DB, orgID string) ([]SelectionEvent, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, fmt.Errorf("structurepriorcuration: organization is required")
	}
	rows, err := db.QueryContext(ctx, `
SELECT org_id, question_hash, member, selected_applied_value, selection_mode, selection_provenance
FROM acr.context_fabric_structure_selections
WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("structurepriorcuration: read selections: %w", err)
	}
	defer rows.Close()
	var out []SelectionEvent
	for rows.Next() {
		var e SelectionEvent
		if err := rows.Scan(&e.OrgID, &e.QuestionHash, &e.Member, &e.SelectedAppliedValue, &e.SelectionMode, &e.SelectionProvenance); err != nil {
			return nil, fmt.Errorf("structurepriorcuration: scan selection: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("structurepriorcuration: read selections: %w", err)
	}
	return out, nil
}

// promotableMembers is v1's own promoted-member allowlist -- see this
// package's own doc comment for why subject_anchor/subject_handle/window
// are excluded.
var promotableMembers = map[string]bool{
	string(contractsv1.ContextFabricStructureNeedExpectedKind): true,
}

// promotableKindValues mirrors contextfabric's own structurePriorKindLabel
// closed switch (priors_consult.go) -- the SAME seven-member set, kept as
// this package's own copy per the established per-package-duplicate
// convention (that function's own doc comment names the precedent). A
// captured value outside this set is silently excluded from promotion --
// it can only mean a since-removed kind or a capture-time bug, never a
// value this package should propose as a runtime offer.
var promotableKindValues = map[string]bool{
	string(contractsv1.ContextFabricSubjectPullRequest):       true,
	string(contractsv1.ContextFabricSubjectPullRequestReview): true,
	string(contractsv1.ContextFabricSubjectCIRun):             true,
	string(contractsv1.ContextFabricSubjectWorkItem):          true,
	string(contractsv1.ContextFabricSubjectRepository):        true,
	string(contractsv1.ContextFabricSubjectProject):           true,
	string(contractsv1.ContextFabricSubjectTeam):              true,
}

type candidateKey struct {
	orgID, questionHash, member, value string
}

type candidate struct {
	key                 candidateKey
	supportHumanPanel   int
	supportAgentReceipt int
}

// Curate is the PURE curation function -- see this package's own doc
// comment for the determinism/re-runnability contract. frozenQuestionHashes
// is the HARD REQUIREMENT gate (this repository's own mission acceptance
// row): any event whose QuestionHash is in this set is excluded BEFORE
// aggregation, unconditionally, regardless of support -- see
// TestCurate_ExcludesFrozenCorpusQuestionHashes for the pin.
//
// Promotion rule (design brief §3.2, pinned): an entry is promotable ONLY
// if its support includes at least one web_assertion+human_panel event (the
// best-effort human proxy this codebase's capture schema can actually
// record -- clarificationSelectionProvenance's own documented limitation,
// unchanged here) OR at least one recorded multi-model-consensus event
// (ConsensusEvidence -- always absent from today's capture schema, so this
// arm never fires until CHAOS-3860/P6 lands it; SupportConsensus stays 0 by
// construction). Agent-only support (agent_receipt, any provenance alone)
// NEVER promotes an entry by itself, matching the design brief's own "single-
// model support alone can propose only at the lowest weight and never
// promotes by itself."
func Curate(events []SelectionEvent, frozenQuestionHashes map[string]bool) []contextfabric.StructurePriorEntry {
	candidates := map[candidateKey]*candidate{}
	var order []candidateKey
	for _, e := range events {
		if !promotableMembers[e.Member] {
			continue
		}
		if frozenQuestionHashes[e.QuestionHash] {
			continue
		}
		if e.Member == string(contractsv1.ContextFabricStructureNeedExpectedKind) && !promotableKindValues[e.SelectedAppliedValue] {
			continue
		}
		key := candidateKey{orgID: e.OrgID, questionHash: e.QuestionHash, member: e.Member, value: e.SelectedAppliedValue}
		c, ok := candidates[key]
		if !ok {
			c = &candidate{key: key}
			candidates[key] = c
			order = append(order, key)
		}
		switch {
		case e.SelectionMode == "human_panel" && e.SelectionProvenance == "web_assertion":
			c.supportHumanPanel++
		case e.SelectionMode == "agent_receipt":
			c.supportAgentReceipt++
		}
	}

	// Group by (org, question_hash, member) for ranking -- a question can
	// have MULTIPLE promoted values for the same member only in principle
	// (a caller confirmed different kinds across different visits); rank
	// orders them by total support, ties broken by EntryID for determinism.
	type groupKey struct{ orgID, questionHash, member string }
	groups := map[groupKey][]*candidate{}
	for _, key := range order {
		c := candidates[key]
		if c.supportHumanPanel < 1 {
			// Design brief §3.2 promotion rule -- agent-only support never
			// promotes alone; SupportConsensus is always 0 today (see this
			// package's own doc comment), so this is currently the WHOLE
			// gate.
			continue
		}
		gk := groupKey{c.key.orgID, c.key.questionHash, c.key.member}
		groups[gk] = append(groups[gk], c)
	}

	var out []contextfabric.StructurePriorEntry
	for gk, members := range groups {
		sort.SliceStable(members, func(i, j int) bool {
			si := members[i].supportHumanPanel + members[i].supportAgentReceipt
			sj := members[j].supportHumanPanel + members[j].supportAgentReceipt
			if si != sj {
				return si > sj
			}
			return members[i].key.value < members[j].key.value
		})
		for rank, c := range members {
			entryID := contextfabric.DeriveStructurePriorEntryID(gk.orgID, contractsv1.ContextFabricStructureNeedKind(gk.member), gk.questionHash, "", "", c.key.value)
			out = append(out, contextfabric.StructurePriorEntry{
				EntryID: entryID, QuestionHash: gk.questionHash,
				Member: contractsv1.ContextFabricStructureNeedKind(gk.member), Value: c.key.value,
				SupportHumanPanel: c.supportHumanPanel, SupportAgentReceipt: c.supportAgentReceipt, SupportConsensus: 0,
				Rank: rank,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].QuestionHash != out[j].QuestionHash {
			return out[i].QuestionHash < out[j].QuestionHash
		}
		if out[i].Member != out[j].Member {
			return out[i].Member < out[j].Member
		}
		return out[i].Rank < out[j].Rank
	})
	return out
}
