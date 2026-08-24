package graphrank

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// --- extractor unit tests: handleGraphExtractors' own three parsers ---

// TestWorkItemHandleValue pins identity.Registry's own "<kind>.v2:" scheme
// as workItemHandleValue's ONLY recognized input shape -- see
// handleGraphExtractors' own doc comment for why this cannot be a generic
// parser shared with pullRequestHandleValue.
//
// codex xhigh review (CHAOS-4119 round 1, MEDIUM finding): work_items.
// work_item_id is itself provider-prefixed in real producer data
// ("linear:CHAOS-9001", devhealthsource/tables.go's queryWorkItems feeds
// this verbatim into identity.Derive) -- a bare "CHAOS-9001" segment, as
// the round-1 version of this test used, never exercised that shape. This
// test now derives from the REAL producer shape and pins the alias-cut
// rule (embed_fields.go's ticketKeyAlias) as workItemHandleValue's own
// required behavior.
func TestWorkItemHandleValue(t *testing.T) {
	t.Parallel()
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{"repo_1", "linear:CHAOS-9001"}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive setup failed: omitted=%v err=%v", omitted, err)
	}
	if value, ok := workItemHandleValue(id); !ok || value != "CHAOS-9001" {
		t.Fatalf("workItemHandleValue(%q) = (%q, %v), want (CHAOS-9001, true) -- the linear: provider prefix must be cut, mirroring ticketKeyAlias", id, value, ok)
	}
	// A pull_request-scheme id (belongs to a DIFFERENT kind's canonical
	// scheme) must never parse -- proves the kind-scoped prefix check is
	// load-bearing, not merely a formality.
	if _, ok := workItemHandleValue("pull_request:repo_1:532"); ok {
		t.Fatal("workItemHandleValue(pull_request-scheme id) = ok, want false")
	}
	if _, ok := workItemHandleValue("not-a-canonical-id"); ok {
		t.Fatal("workItemHandleValue(malformed id) = ok, want false")
	}
	// A work_item_id with NO colon at all derives no alias (ticketKeyAlias's
	// own documented contract: "" for a colon-less id) -- ok=false, never
	// the still-unprefixed raw value offered as if it were already a
	// ticket key.
	noColonID, _, err := identity.Derive(identity.KindWorkItem, []string{"repo_1", "CHAOS-9001"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive setup failed: %v", err)
	}
	if _, ok := workItemHandleValue(noColonID); ok {
		t.Fatal("workItemHandleValue(colon-less work_item_id) = ok, want false -- ticketKeyAlias derives no alias here either")
	}
	// codex xhigh review (CHAOS-4119 round 1, LOW finding): an empty
	// repo_id segment is a malformed wrapper, not a legitimate id, even
	// though identity.Segments itself decodes it without error.
	emptyRepoID, _, err := identity.Derive(identity.KindWorkItem, []string{"", "linear:CHAOS-9001"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive setup failed: %v", err)
	}
	if _, ok := workItemHandleValue(emptyRepoID); ok {
		t.Fatal("workItemHandleValue(empty repo_id) = ok, want false")
	}
}

func TestCIRunHandleValue(t *testing.T) {
	t.Parallel()
	id, omitted, err := identity.Derive(identity.KindCIPipelineRun, []string{"repo_1", "554433"}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive setup failed: omitted=%v err=%v", omitted, err)
	}
	if value, ok := ciRunHandleValue(id); !ok || value != "554433" {
		t.Fatalf("ciRunHandleValue(%q) = (%q, %v), want (554433, true)", id, value, ok)
	}
	if _, ok := ciRunHandleValue("ci_pipeline_run:repo_1:554433"); ok {
		t.Fatal("ciRunHandleValue(non-v2-scheme id) = ok, want false")
	}
	// codex xhigh review (CHAOS-4119 round 1, LOW finding): empty repo_id.
	emptyRepoID, _, err := identity.Derive(identity.KindCIPipelineRun, []string{"", "554433"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive setup failed: %v", err)
	}
	if _, ok := ciRunHandleValue(emptyRepoID); ok {
		t.Fatal("ciRunHandleValue(empty repo_id) = ok, want false")
	}
}

// TestPullRequestHandleValue pins queryPullRequests' own pre-identity.Registry
// CanonicalID scheme (devhealthsource/tables.go) as the ONE shape
// pullRequestHandleValue recognizes -- the exact asymmetry
// handleGraphExtractors' own doc comment flags: pull_request predates the
// "<kind>.v2:" migration and is not a member of identity.Registry at all, so
// identity.Segments can never recover it.
func TestPullRequestHandleValue(t *testing.T) {
	t.Parallel()
	if value, ok := pullRequestHandleValue("pull_request:repo_9:532"); !ok || value != "532" {
		t.Fatalf("pullRequestHandleValue(pull_request:repo_9:532) = (%q, %v), want (532, true)", value, ok)
	}
	// Missing a segment.
	if _, ok := pullRequestHandleValue("pull_request:532"); ok {
		t.Fatal("pullRequestHandleValue(missing segment) = ok, want false")
	}
	// A work_item-scheme (v2) id must never parse as a pull_request --
	// proves the "pull_request" literal prefix check is load-bearing.
	if _, ok := pullRequestHandleValue("work_item.v2:cmVwb18x:Q0hBT1MtOTAwMQ"); ok {
		t.Fatal("pullRequestHandleValue(work_item-scheme id) = ok, want false")
	}
	// codex xhigh review (CHAOS-4119 round 1, LOW finding): a malformed
	// wrapper with an empty repo_id segment must never parse.
	if _, ok := pullRequestHandleValue("pull_request::532"); ok {
		t.Fatal("pullRequestHandleValue(empty repo_id) = ok, want false")
	}
}

// TestHandleGraphValueMaxLength (codex xhigh review, CHAOS-4119 round 1, LOW
// finding) pins the safety bound: a forged/corrupt canonical id whose
// grammar-eligible tail is implausibly long must never reach an offer, even
// though ci_run_id's (`^\d{4,}$`) and pull_request_number's (`^\d+$`) own
// valuePattern set no upper bound of their own -- ContextFabricHandleOption's
// wire Label bound (1-200) would otherwise be the first thing to fail,
// turning a graceful non-offer into an internal validation error.
func TestHandleGraphValueMaxLength(t *testing.T) {
	t.Parallel()
	overlong := ""
	for i := 0; i < handleGraphValueMaxLength+1; i++ {
		overlong += "9"
	}
	if _, ok := pullRequestHandleValue("pull_request:repo_1:" + overlong); ok {
		t.Fatalf("pullRequestHandleValue(%d-digit value) = ok, want false", len(overlong))
	}
	id, _, err := identity.Derive(identity.KindCIPipelineRun, []string{"repo_1", overlong}, nil)
	if err != nil {
		t.Fatalf("identity.Derive setup failed: %v", err)
	}
	if _, ok := ciRunHandleValue(id); ok {
		t.Fatalf("ciRunHandleValue(%d-digit value) = ok, want false", len(overlong))
	}
}

// --- handleOfferMaterial-level tests: the graph-derived-source pass itself ---

// workItemCandidate builds a work_item SubjectCandidate from workItemID --
// the RAW, potentially provider-prefixed work_items.work_item_id value a
// real producer would pass into identity.Derive (e.g. "linear:CHAOS-9001"),
// never the already-cut ticket-key alias -- so every caller exercises the
// SAME producer shape TestWorkItemHandleValue pins.
func workItemCandidate(t *testing.T, repoID, workItemID, label string) contextfabric.SubjectCandidate {
	t.Helper()
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive(work_item) setup failed: omitted=%v err=%v", omitted, err)
	}
	return contextfabric.SubjectCandidate{
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: id, Label: label},
	}
}

func pullRequestCandidate(repoID string, number int, label string) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		Subject: contractsv1.ContextFabricSubjectRef{
			Kind:        contractsv1.ContextFabricSubjectPullRequest,
			CanonicalID: fmt.Sprintf("pull_request:%s:%d", repoID, number),
			Label:       label,
		},
	}
}

// TestHandleOfferMaterial_GraphDerivedOffersPoolCandidatesNotInQuestion is
// CHAOS-4119's core fix: neither ticket key nor PR number below appears
// anywhere in the question text (BindHandles finds nothing), and no
// explicit handle was requested -- the ONLY source that can offer either is
// the graph-derived pool pass.
func TestHandleOfferMaterial_GraphDerivedOffersPoolCandidatesNotInQuestion(t *testing.T) {
	t.Parallel()
	workItem := workItemCandidate(t, "repo_1", "linear:CHAOS-9001", "Outage work item")
	pr := pullRequestCandidate("repo_9", 532, "Outage PR")
	pool := []contextfabric.SubjectCandidate{workItem, pr}

	material, diag := handleOfferMaterial("how is the outage going", nil, nil, pool)
	if len(material.HandleOptions) != 2 {
		t.Fatalf("material.HandleOptions = %+v, want 2 (one per pool candidate)", material.HandleOptions)
	}
	var sawWorkItem, sawPR bool
	for _, opt := range material.HandleOptions {
		if opt.OfferSource != contractsv1.ContextFabricStructureOfferEngine {
			t.Errorf("HandleOptions offer_source = %q, want engine", opt.OfferSource)
		}
		switch opt.Kind {
		case contractsv1.ContextFabricSubjectWorkItem:
			sawWorkItem = true
			if opt.PatternID != "work_item_ticket_key" || opt.Value != "CHAOS-9001" || opt.SourceColumn != "work_items.work_item_id" {
				t.Errorf("work_item HandleOption = %+v, want pattern_id=work_item_ticket_key value=CHAOS-9001 source_column=work_items.work_item_id", opt)
			}
		case contractsv1.ContextFabricSubjectPullRequest:
			sawPR = true
			// The PR-kind extraction path this ticket adds: the v1 plain-colon
			// CanonicalID scheme, not identity.Registry's "<kind>.v2:" scheme.
			if opt.PatternID != "pull_request_number" || opt.Value != "532" || opt.SourceColumn != "git_pull_requests.number" {
				t.Errorf("pull_request HandleOption = %+v, want pattern_id=pull_request_number value=532 source_column=git_pull_requests.number", opt)
			}
		}
	}
	if !sawWorkItem || !sawPR {
		t.Fatalf("material.HandleOptions = %+v, want both work_item and pull_request offered", material.HandleOptions)
	}
	if diag.GraphDerivedCount != 2 {
		t.Errorf("diag.GraphDerivedCount = %d, want 2", diag.GraphDerivedCount)
	}
	if diag.CountBeforeGraphSource != 0 {
		t.Errorf("diag.CountBeforeGraphSource = %d, want 0 (no explicit/BindHandles source fired)", diag.CountBeforeGraphSource)
	}
	if diag.GraphDerivedRejectedCount != 0 {
		t.Errorf("diag.GraphDerivedRejectedCount = %d, want 0", diag.GraphDerivedRejectedCount)
	}
}

// TestHandleOfferMaterial_GraphDerivedRejectsGrammarInvalidValue is the
// fail-closed proof: a pull_request candidate whose recovered value does
// not match pull_request_number's own valuePattern (`^\d+$`) must NEVER be
// offered -- offering it would let VerifyHandle's own redemption-time
// grammar check reject a handle the engine itself offered.
func TestHandleOfferMaterial_GraphDerivedRejectsGrammarInvalidValue(t *testing.T) {
	t.Parallel()
	malformed := contextfabric.SubjectCandidate{
		Subject: contractsv1.ContextFabricSubjectRef{
			Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:repo_1:not-a-number", Label: "Weird PR",
		},
	}
	material, diag := handleOfferMaterial("no handles here", nil, nil, []contextfabric.SubjectCandidate{malformed})
	if len(material.HandleOptions) != 0 {
		t.Fatalf("material.HandleOptions = %+v, want empty (grammar-invalid value must never be offered)", material.HandleOptions)
	}
	if diag.GraphDerivedCount != 0 {
		t.Errorf("diag.GraphDerivedCount = %d, want 0", diag.GraphDerivedCount)
	}
	if diag.GraphDerivedRejectedCount != 1 {
		t.Errorf("diag.GraphDerivedRejectedCount = %d, want 1", diag.GraphDerivedRejectedCount)
	}
}

// TestHandleOfferMaterial_GraphDerivedSkipsKindsOutsideTheClosedSet proves
// handleGraphExtractors' own closed-vocabulary membership: a candidate of a
// kind with NO handle grammar entry (e.g. repository) is silently skipped,
// never a panic, never a diagnostics count.
func TestHandleOfferMaterial_GraphDerivedSkipsKindsOutsideTheClosedSet(t *testing.T) {
	t.Parallel()
	repo := contextfabric.SubjectCandidate{
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:repo_1", Label: "dev-health-acr"},
	}
	material, diag := handleOfferMaterial("no handles here", nil, nil, []contextfabric.SubjectCandidate{repo})
	if len(material.HandleOptions) != 0 {
		t.Fatalf("material.HandleOptions = %+v, want empty (repository has no handle grammar)", material.HandleOptions)
	}
	if diag.GraphDerivedCount != 0 || diag.GraphDerivedRejectedCount != 0 {
		t.Errorf("diag = %+v, want both zero -- a kind outside the closed set is skipped, not rejected", diag)
	}
}

// TestHandleOfferMaterial_GraphDerivedDedupsAgainstBindHandlesMatch proves
// the shared seen-map dedup extends to the graph-derived source: a value
// BindHandles' own question-text scan already offered must not ALSO appear
// via the pool -- one option, counted toward CountBeforeGraphSource (the
// text match "wins" because it is processed first), never GraphDerivedCount.
func TestHandleOfferMaterial_GraphDerivedDedupsAgainstBindHandlesMatch(t *testing.T) {
	t.Parallel()
	workItem := workItemCandidate(t, "repo_1", "linear:CHAOS-9001", "Outage work item")
	material, diag := handleOfferMaterial("what is the status of CHAOS-9001?", nil, nil, []contextfabric.SubjectCandidate{workItem})
	if len(material.HandleOptions) != 1 {
		t.Fatalf("material.HandleOptions = %+v, want exactly 1 (deduped)", material.HandleOptions)
	}
	if diag.CountBeforeGraphSource != 1 {
		t.Errorf("diag.CountBeforeGraphSource = %d, want 1 (the BindHandles match)", diag.CountBeforeGraphSource)
	}
	if diag.GraphDerivedCount != 0 {
		t.Errorf("diag.GraphDerivedCount = %d, want 0 (the pool candidate's own identical value was a duplicate, not a NEW option)", diag.GraphDerivedCount)
	}
}

// TestHandleOfferMaterial_GraphDerivedCountReflectsPostCapTruncation proves
// the cap-adjustment logic: when explicit+BindHandles ALONE already fill
// structureOfferMaxOptions, every graph-derived candidate is truncated away,
// and GraphDerivedCount must read 0 (what actually reached the wire), not
// the pre-cap raw count.
func TestHandleOfferMaterial_GraphDerivedCountReflectsPostCapTruncation(t *testing.T) {
	t.Parallel()
	question := "compare"
	for i := 0; i < structureOfferMaxOptions; i++ {
		question += fmt.Sprintf(" PR %d", 1000+i)
	}
	pool := []contextfabric.SubjectCandidate{pullRequestCandidate("repo_1", 9999, "Unrelated PR")}
	material, diag := handleOfferMaterial(question, nil, nil, pool)
	if len(material.HandleOptions) != structureOfferMaxOptions {
		t.Fatalf("len(material.HandleOptions) = %d, want %d (capped)", len(material.HandleOptions), structureOfferMaxOptions)
	}
	if diag.CountBeforeGraphSource != structureOfferMaxOptions {
		t.Errorf("diag.CountBeforeGraphSource = %d, want %d", diag.CountBeforeGraphSource, structureOfferMaxOptions)
	}
	if diag.GraphDerivedCount != 0 {
		t.Errorf("diag.GraphDerivedCount = %d, want 0 -- the pool candidate was truncated away by the pre-existing cap, same as any other overflow entry", diag.GraphDerivedCount)
	}
}

// --- end-to-end causal fixture through ResolveSubjects ---

// TestResolveSubjects_HandleOfferGraphSourceCausalFixture is CHAOS-4119's
// validation-step causal fixture: proves the graph-derived handle source
// fires end to end through ResolveSubjects (not merely handleOfferMaterial
// in isolation), and that it has ZERO effect on the commit-decision and
// candidate-list axes -- the SAME "byte-unchanged" differential-proof
// discipline TestResolveSubjects_KindBoundaryRepairCausalFixture
// (chaos4183_kind_boundary_repair_test.go) established for the kind axis.
func TestResolveSubjects_HandleOfferGraphSourceCausalFixture(t *testing.T) {
	t.Parallel()
	// Two comparable-strength work_item candidates keep the resolution
	// stalled (nothing auto-commits), so offer.CandidateOptions is
	// non-trivial (candidateOfferMaterial only fires when nothing
	// committed) -- exercising both offer axes in the same call.
	workItemA := workItemCandidate(t, "repo_1", "linear:CHAOS-9001", "Outage work item A")
	workItemB := workItemCandidate(t, "repo_1", "linear:CHAOS-9002", "Outage work item B")
	nodeA := candidateNode(contextfabric.SubjectWorkItem, workItemA.Subject.CanonicalID, workItemA.Subject.Label, 0.95, "*")
	nodeB := candidateNode(contextfabric.SubjectWorkItem, workItemB.Subject.CanonicalID, workItemB.Subject.Label, 0.90, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"outage": {nodeA, nodeB}}}

	request := testRequest()
	request.Question = "how is the outage going" // neither ticket key appears in the question text
	resolution, offer, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("outage"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none -- two comparable candidates must stay stalled", resolution.Committed)
	}

	// The fix itself: a ticket key neither candidate's own question
	// mentioned is now offered, sourced from the pool alone.
	if len(offer.HandleOptions) != 2 {
		t.Fatalf("offer.HandleOptions = %+v, want 2 (both work_item candidates' own ticket keys, graph-derived)", offer.HandleOptions)
	}
	seen := map[string]bool{}
	for _, opt := range offer.HandleOptions {
		if opt.Kind != contractsv1.ContextFabricSubjectWorkItem || opt.PatternID != "work_item_ticket_key" {
			t.Fatalf("offer.HandleOptions = %+v, want only work_item_ticket_key options", offer.HandleOptions)
		}
		seen[opt.Value] = true
	}
	if !seen["CHAOS-9001"] || !seen["CHAOS-9002"] {
		t.Fatalf("offer.HandleOptions values = %v, want both CHAOS-9001 and CHAOS-9002", seen)
	}

	// Byte-unchanged proof: resolution.Candidates/Committed and
	// offer.CandidateOptions must be EXACTLY what they were before this
	// ticket -- handleOfferMaterial's poolCandidates argument is read-only
	// (never mutated, never fed back into resolution or candidateOfferMaterial's
	// own separate call, which both run BEFORE handleOfferMaterial at its
	// resolve.go call site) -- so a run that mints two new HandleOptions must
	// still show the SAME two work_item candidates, same confidences, same
	// order, on the commit-decision and candidate-list axes.
	if len(resolution.Candidates) != 2 {
		t.Fatalf("resolution.Candidates = %#v, want exactly 2", resolution.Candidates)
	}
	if resolution.Candidates[0].Subject.CanonicalID != workItemA.Subject.CanonicalID {
		t.Fatalf("resolution.Candidates[0] = %#v, want the higher-confidence work_item A first", resolution.Candidates[0])
	}
	if resolution.Candidates[1].Subject.CanonicalID != workItemB.Subject.CanonicalID {
		t.Fatalf("resolution.Candidates[1] = %#v, want work_item B second", resolution.Candidates[1])
	}
	if len(offer.CandidateOptions) != 2 {
		t.Fatalf("offer.CandidateOptions = %#v, want exactly 2 (the candidate-list axis, unaffected by handle offers)", offer.CandidateOptions)
	}
	for _, opt := range offer.CandidateOptions {
		if opt.Kind != contractsv1.ContextFabricSubjectWorkItem {
			t.Fatalf("offer.CandidateOptions = %#v, want both work_item", offer.CandidateOptions)
		}
	}
}
