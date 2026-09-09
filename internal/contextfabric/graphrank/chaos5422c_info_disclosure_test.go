package graphrank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// emittedDecisionSummary drives the production entry point through the REAL slog
// JSON handler at Info and returns the one decision_summary line. A recording
// tracer proves a field was set; only the handler proves it is emitted, and a
// mutation that deleted these fields from the emitter once survived a
// capture-based version of this very assertion.
func emittedDecisionSummary(t *testing.T, backend *fakeGraphBackend, hints []contextfabric.SubjectHint,
	confirmed *contextfabric.ConfirmedExpectedKind, frame *contextfabric.QuestionFrame,
	anchorKind contextfabric.SubjectKind, max int) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	req := testRequest()
	req.Options.MaxSubjectCandidates = max
	req.RequestedScope.SubjectHints = hints
	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("platform"),
		deps, confirmed, nil, frame, anchorKind); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	var line map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			t.Fatalf("emitted a line that is not JSON: %v", err)
		}
		if entry["stage"] == "decision_summary" {
			line = entry
		}
	}
	if line == nil {
		t.Fatalf("no decision_summary line was EMITTED at Info:\n%s", buf.String())
	}
	return line
}

func emittedWithheldIDs(t *testing.T, line map[string]any) []string {
	t.Helper()
	raw, present := line["offer_pool_anchor_kind_withheld_ids"]
	if !present {
		t.Fatalf("offer_pool_anchor_kind_withheld_ids was ABSENT from the emitted line; an absent key and an "+
			"empty list are different facts and only one of them means \"this call refused nothing\". line=%v", line)
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("offer_pool_anchor_kind_withheld_ids = %#v, want a JSON array", raw)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, fmt.Sprint(item))
	}
	return ids
}

// THE REGRESSION THAT WAS INVISIBLE AT INFO, and this is the pin that makes it
// visible. Adversarial review of the earlier change answered the standing
// question with exactly this: a build that refuses the WRONG subject while
// refusing the SAME NUMBER of them reads identically on the folded line,
// because that line carried counts and no identities.
//
// Two resolutions, each refusing exactly ONE subject, differing only in WHICH.
// The counts are identical by construction — that is the point — so an
// assertion on counts alone cannot tell them apart, and the test says so by
// checking that they match before checking that the ids do not.
func TestTheEmittedLineNamesWhichSubjectWasRefusedNotOnlyHowMany(t *testing.T) {
	t.Parallel()
	anchor := candidateNode(contextfabric.SubjectRepository, "repository.v2:github:platform", "platform", 0.95, "*")

	refusedOne := func(t *testing.T, memberID string) map[string]any {
		t.Helper()
		member := candidateNode(contextfabric.SubjectTeam, memberID, "platform owners", 0.4, "*")
		backend := &fakeGraphBackend{
			searchResults:    map[string][]CandidateNode{"platform": {anchor, member}},
			enableSearchKind: true,
			searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
				"platform": {contextfabric.SubjectRepository: {anchor}, contextfabric.SubjectTeam: {member}},
			},
		}
		return emittedDecisionSummary(t, backend, nil, confirmedTeamKind(), contestFrame("platform"),
			contextfabric.SubjectRepository, 20)
	}

	first := refusedOne(t, "team.v2:github:platform-owners")
	second := refusedOne(t, "team.v2:github:platform-admins")

	if first["offer_pool_anchor_kind_withheld"] != second["offer_pool_anchor_kind_withheld"] {
		t.Fatalf("the two fixtures refused different NUMBERS of subjects (%v vs %v), so this test would pass on "+
			"the counts alone and would not measure the regression it exists for",
			first["offer_pool_anchor_kind_withheld"], second["offer_pool_anchor_kind_withheld"])
	}
	firstIDs, secondIDs := emittedWithheldIDs(t, first), emittedWithheldIDs(t, second)
	t.Logf("same count=%v, first ids=%v, second ids=%v", first["offer_pool_anchor_kind_withheld"], firstIDs, secondIDs)
	if len(firstIDs) != 1 || len(secondIDs) != 1 {
		t.Fatalf("each fixture must refuse exactly one subject; got %v and %v", firstIDs, secondIDs)
	}
	if firstIDs[0] == secondIDs[0] {
		t.Errorf("both resolutions emitted the same refused id %q while refusing DIFFERENT subjects — the "+
			"wrong-subject regression is still invisible at Info", firstIDs[0])
	}
	if firstIDs[0] != "team.v2:github:platform-owners" || secondIDs[0] != "team.v2:github:platform-admins" {
		t.Errorf("emitted ids %v and %v do not name the subjects actually refused", firstIDs, secondIDs)
	}
}

// A CALL THAT REFUSED NOTHING EMITS AN EMPTY LIST, never an absent key.
func TestARefusalFreeCallStillEmitsTheIDList(t *testing.T) {
	t.Parallel()
	line := emittedDecisionSummary(t, contestBackend("platform"), nil, nil, nil, "", 20)
	if got := line["offer_pool_anchor_kind_withheld"]; got != float64(0) {
		t.Fatalf("this fixture must refuse nothing; withheld=%v", got)
	}
	if ids := emittedWithheldIDs(t, line); len(ids) != 0 {
		t.Errorf("a call that refused nothing emitted ids %v", ids)
	}
}

// THE CAP, and the count beside it. A large crowd must not turn one Info line
// into an unbounded array, and the count must stay TRUE so an operator reading
// 40 beside 25 ids knows the list is a sample.
func TestTheIDListIsCappedWhileTheCountStaysTrue(t *testing.T) {
	t.Parallel()
	const crowd = traceSummaryIDCap + 15
	anchor := candidateNode(contextfabric.SubjectRepository, "repository.v2:github:platform", "platform", 0.95, "*")
	members := make([]CandidateNode, 0, crowd)
	for i := 0; i < crowd; i++ {
		members = append(members, candidateNode(contextfabric.SubjectTeam,
			fmt.Sprintf("team.v2:github:platform-%03d", i), fmt.Sprintf("platform team %03d", i), 0.4, "*"))
	}
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{"platform": append([]CandidateNode{anchor}, members...)},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"platform": {contextfabric.SubjectRepository: {anchor}, contextfabric.SubjectTeam: members},
		},
	}
	line := emittedDecisionSummary(t, backend, nil, confirmedTeamKind(), contestFrame("platform"),
		contextfabric.SubjectRepository, 20)
	ids := emittedWithheldIDs(t, line)
	count := line["offer_pool_anchor_kind_withheld"]
	t.Logf("crowd=%d emitted count=%v len(ids)=%d", crowd, count, len(ids))
	if count != float64(crowd) {
		t.Errorf("emitted count = %v, want the TRUE %d — the count must never be truncated with the list, or "+
			"an operator cannot tell a sample from the population", count, crowd)
	}
	if len(ids) != traceSummaryIDCap {
		t.Errorf("emitted %d ids, want exactly %d regardless of the crowd", len(ids), traceSummaryIDCap)
	}
	// WHICH ids, not merely how many. The cap takes the FIRST of the sorted
	// order, so the sample is deterministic and two runs of the same
	// resolution log the same one. Asserting only "ascending" would accept a
	// build that logged the LAST 25 — also ascending, and a different sample
	// every time the population shifts.
	for i, id := range ids {
		want := fmt.Sprintf("team.v2:github:platform-%03d", i)
		if id != want {
			t.Fatalf("ids[%d] = %q, want %q — the cap must take the first ids of the deterministic order, "+
				"so the sample does not move under an operator. ids=%v", i, id, want, ids)
		}
	}
}

// ITEM 3: the unenumerated fallback's THREE facts, asserted explicitly. Nothing
// in production reads the authorship field on this path, so without this a
// mutation flipping it survives everything.
func TestTheUnenumeratedFallbackAssertsAllThreeFacts(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"workbench", "a_source_no_producer_emits", "prior_subject_receipt_v2"} {
		attributes := hintsource.Lookup(source)
		if attributes.EngineMinted {
			t.Errorf("Lookup(%q).EngineMinted = true — a source this engine never minted is not engine-minted, "+
				"and calling it so attributes a caller's string to this engine", source)
		}
		if !attributes.ContestExempt.Exempt() {
			t.Errorf("Lookup(%q).ContestExempt = false — an unenumerated source is caller-authored by "+
				"definition, and refusing it would break naming a subject", source)
		}
		if !attributes.ShortCircuitEligible.Eligible() {
			t.Errorf("Lookup(%q).ShortCircuitEligible = false — refusing the short circuit for an ordinary "+
				"caller hint changes how every caller-named subject resolves", source)
		}
	}
}
