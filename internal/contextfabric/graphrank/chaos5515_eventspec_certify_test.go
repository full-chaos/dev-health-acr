package graphrank

// CHAOS-5515 A2/A3 -- the reusable JSON assertion runner
// (internal/contextfabric/eventspec/certify), piloted against the two events
// eventspec.spec.go declares (RankedCutSummary, AnchorSlotDisplaced),
// CONSUMING the same production output chaos5434_anchor_slot_test.go's own
// value pins already assert on -- not replacing them. This file adds
// identity, multiplicity, level and closed-vocabulary certification on top
// of the same fixture and the same real NewSlogResolutionTracer +
// slog.NewJSONHandler production entry point
// TestTheAnchorSlotDecisionAndItsVictimAreVisibleAtProductionLogLevel already
// drives; logLineWithMsg and its value assertions in that file are untouched
// by this one.

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

func TestEventspecCertifiesTheAnchorSlotPilotAtProductionLogLevel(t *testing.T) {
	t.Parallel()
	backend := anchorOnlyByKindBackend("chaos", saturatedCrowd)
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20

	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos"), deps,
		nil, nil, scopedProjectsFrame("chaos"), contextfabric.SubjectTeam); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production slog output error = %v", err)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.RankedCutSummary,
		Want: map[string]any{
			"request_id":            req.RequestID,
			"candidate_count":       92,
			"survived_count":        20,
			"max":                   20,
			"anchor_slot_reserved":  string(contextfabric.SubjectTeam),
			"anchor_slot_source":    anchorPoolKindScopeReceipt,
			"anchor_slot_displaced": 1,
			"pool_truncated_n":      72,
		},
	}); err != nil {
		t.Errorf("certify RankedCutSummary: %v", err)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.AnchorSlotDisplaced,
		Want: map[string]any{
			"request_id":           req.RequestID,
			"anchor_slot_reserved": string(contextfabric.SubjectTeam),
			"anchor_slot_source":   anchorPoolKindScopeReceipt,
			"subject_kind":         string(contextfabric.SubjectProject),
		},
	}); err != nil {
		t.Errorf("certify AnchorSlotDisplaced: %v", err)
	}
}

// TestEventspecCertifiesExplicitZerosWhenNoSlotIsReserved is the sibling
// pass: on a resolution that reserves no anchor slot, RankedCutSummary must
// still certify (explicit zeros, never omission), and AnchorSlotDisplaced
// must certify ABSENT -- the two are how this runner tells "never decided" I
// apart from "decided none".
func TestEventspecCertifiesExplicitZerosWhenNoSlotIsReserved(t *testing.T) {
	t.Parallel()
	backend := anchorOnlyByKindBackend("chaos", saturatedCrowd)
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20

	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos"), deps,
		nil, nil, scopedProjectsFrame("chaos"), ""); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.RankedCutSummary,
		Want: map[string]any{
			"request_id":            req.RequestID,
			"anchor_slot_reserved":  anchorSlotNone,
			"anchor_slot_source":    anchorSlotNone,
			"anchor_slot_displaced": 0,
		},
	}); err != nil {
		t.Errorf("certify RankedCutSummary (no slot reserved): %v", err)
	}

	if err := certify.CertifyAbsent(log, eventspec.AnchorSlotDisplaced); err != nil {
		t.Errorf("certify.CertifyAbsent(AnchorSlotDisplaced): %v", err)
	}
}
