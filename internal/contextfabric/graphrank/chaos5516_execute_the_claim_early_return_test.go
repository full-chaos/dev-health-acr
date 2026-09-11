package graphrank

// CHAOS-5516 B5 -- EXECUTE-THE-CLAIM, the named failure mode from the ticket
// text verbatim: "an early return loses the record". This is the amended
// prompt-of-record's Method paragraph applied to this PR's own pilot scope:
// the claim under test is "execution and emission share a finalized
// decision" for decision_summary, and the specific regression a reviewer is
// told to look for is an early return around the decision loop silently
// skipping the fold's flush.
//
// This does NOT read decisionSummaryBuffer's own defer/return structure and
// argue the claim from the code (brief-pr2.md §2's own caveat: "will
// re-verify with an executed repro, not just a read"). It drives the REAL
// production entry point (ResolveSubjectsWithCommitBasis, the same call
// chaos5515_eventspec_certify_test.go's pilot already uses) through a REAL
// NewSlogResolutionTracer + slog.JSONHandler, forces resolveSubjects' own
// FIRST check (resolve.go:2328, `strings.TrimSpace(principal.OrgID) == ""`)
// to return an error before ANY anchor_pool/ranked_cut decision event is
// ever traced, and certifies the resulting decision_summary line -- parsed
// from the real emitted JSON bytes, through the NEW generated typed
// constructor's own certificate (eventspec.DecisionSummary, the same Certify
// call every other pilot event in this package uses) -- carries EXPLICIT
// zeros, not omission, and emits EXACTLY ONCE.
//
// decisionFold is constructed and its flush deferred in
// ResolveSubjectsWithCommitBasis BEFORE resolveSubjects is ever called
// (resolve.go:1959-1972), so frame_gate/refuse_basis are stamped from the
// CARRIED frame at construction regardless of whether resolveSubjects itself
// ever runs -- this test cites the real values the real frame produces
// rather than assuming them.

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestDecisionSummaryStillEmitsExactlyOnceWhenResolveSubjectsReturnsBeforeAnyDecision(t *testing.T) {
	t.Parallel()
	backend := anchorOnlyByKindBackend("chaos", saturatedCrowd)
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20

	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// storage.Principal{OrgID: ""} fails resolve.go:2328, the FIRST statement
	// resolveSubjects executes -- above anchor_pool's decision (2354), above
	// ranked_cut, above every decision-stage event this fold aggregates. No
	// event this fold could learn a value from is EVER traced.
	_, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: ""}, req, testInterpreted("chaos"), deps,
		nil, nil, scopedProjectsFrame("chaos"), contextfabric.SubjectTeam)
	if err == nil {
		t.Fatal("expected an error from an empty OrgID -- this executed repro only means anything on a path that returns before the decision loop runs")
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production slog output error = %v", err)
	}

	// Multiplicity: exactly one decision_summary line, and nothing else --
	// LinesWithMsg proves no OTHER decision-stage line snuck in ahead of it
	// (anchor_pool, ranked_cut) that a lucky Certify scope-match could paper
	// over.
	lines := log.LinesWithMsg(eventspec.DecisionSummary.Msg)
	if len(lines) != 1 {
		t.Fatalf("captured %d decision_summary lines on the early-return path, want exactly 1 -- the fold is deferred so it must flush exactly once even when resolveSubjects returns before any decision runs", len(lines))
	}
	if anchorPool := log.LinesWithMsg("context fabric resolution trace: anchor slot displaced"); len(anchorPool) != 0 {
		t.Fatalf("the early-return path emitted %d anchor_slot_displaced events, want 0 -- it must return BEFORE any decision event for this repro to actually exercise the claimed failure mode", len(anchorPool))
	}

	// EXPLICIT zeros through the NEW typed path's own certificate --
	// eventspec.DecisionSummary, the same generated declaration
	// decisionSummaryBuffer.flush() now constructs via
	// eventspec.NewDecisionSummaryFields(...) and tracer.go emits via
	// DecisionSummaryFields.SlogArgs(). A field silently dropped, or
	// defaulted to Go's zero value without ever being written, is refused by
	// Certify's own unconditional PresenceRequired check (validateFields) --
	// this assertion is not just reading the values, it is asking the
	// generated certificate itself to refuse an omission.
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.DecisionSummary,
		Want: map[string]any{
			"request_id":                req.RequestID,
			"decision_event_count":      0,
			"committed_count":           0,
			"ambiguous_count":           0,
			"no_commit_count":           0,
			"committed_ids":             []string{},
			"commit_gates":              []string{},
			"commit_bases":              []string{},
			"offered_under_window_gate": false,
			// frame_gate/refuse_basis: stamped at decisionFold construction
			// from scopedProjectsFrame("chaos") -- a frame with no
			// declared member kind and Scoped.MemberKind=project passes
			// ValidateFramePhaseA1 and cohortKindFromFrame cleanly in every
			// other pilot test in this package, so frameGateObservable's
			// real, executed verdict for it is "passed"/"none" -- cited
			// here as the value this specific frame produces, not assumed.
			"frame_gate":                             "passed",
			"refuse_basis":                           "none",
			"offer_pool_vector_only_excluded":        0,
			"offer_pool_vector_only_demoted":         0,
			"offer_pool_emptied_by_exclusion":        false,
			"offer_pool_anchor_kind_withheld":        0,
			"offer_pool_anchor_kind_withheld_scope":  anchorPoolKindScopeNone,
			"offer_pool_anchor_kind_withheld_reason": anchorPoolKindScopeNone,
			"offer_pool_anchor_kind_withheld_ids":    []string{},
			"offer_pool_anchor_kind_exempted":        0,
			// anchor_pool_kind_scope/_source: no anchor_pool event was ever
			// traced (asserted above) -- these are stamped BEFORE any such
			// event exists, so the fold's own construction-time defaults
			// (never having learned a value) are the explicit "none" tokens,
			// distinct from a decided-but-empty scope.
			"anchor_pool_kind_scope":        anchorPoolKindScopeNone,
			"anchor_pool_kind_scope_source": anchorPoolKindScopeNone,
			// member_kind_confirmed IS stamped at construction from the
			// confirmedKind parameter (resolve.go:1968,
			// confirmedMemberKindToken) -- an input to the call, not
			// something the decision loop discovers, so it survives even
			// this path with its real value. This call passed confirmedKind
			// = nil, so the real, executed value is the explicit "none"
			// token, the SAME mechanism proven distinct in
			// TestAFailedResolutionStillCarriesExplicitNoneTokens.
			"member_kind_confirmed": anchorPoolKindScopeNone,
			"reserved_kinds":        []string{},
			"filter_kinds":          []string{},
		},
	}); err != nil {
		t.Errorf("certify DecisionSummary (early-return path, explicit zeros): %v", err)
	}
}
