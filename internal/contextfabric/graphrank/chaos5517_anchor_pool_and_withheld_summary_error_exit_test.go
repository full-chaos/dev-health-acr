package graphrank

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

// TestAnchorPoolAndWithheldSummaryFireExactlyOnceOnErrorExit pins AnchorPool
// and AnchorKindWithheldSummary against going missing on an error exit.
// AnchorPool's own emission sits inside resolveSubjects, downstream of its
// two earliest error returns (empty OrgID, a canceled/expired ctx);
// AnchorKindWithheldSummary's own emission sits in
// ResolveSubjectsWithCommitBasis, gated on `err == nil` after resolveSubjects
// returns. Both are declared MultiplicityExactlyOnePerRequest -- driving
// ResolveSubjectsWithCommitBasis through a real NewSlogResolutionTracer with
// an empty OrgID (resolveSubjects' own first check) must still produce
// exactly one line of each, via the exactlyOnceRequestFold anchor_offer/
// kind_coverage_floor already use, set up alongside them before
// resolveSubjects is ever called -- see that call site's own doc comment for
// the full account.
func TestAnchorPoolAndWithheldSummaryFireExactlyOnceOnErrorExit(t *testing.T) {
	var buf bytes.Buffer
	deps := ResolveDeps{ResolutionTracer: NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	)}
	request := contextfabric.InvestigationRequest{RequestID: "req-exact-one-error"}
	_, _, _, _, err := ResolveSubjectsWithCommitBasis(
		context.Background(), storage.Principal{}, request, contextfabric.InterpretedQuestion{}, deps,
		nil, nil, nil, "",
	)
	if err == nil {
		t.Fatal("ResolveSubjectsWithCommitBasis() unexpectedly succeeded")
	}
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []eventspec.Event{eventspec.AnchorPool, eventspec.AnchorKindWithheldSummary} {
		if got := len(log.LinesWithMsg(ev.Msg)); got != 1 {
			t.Errorf("%s lines on error exit = %d, want exactly one", ev.ID, got)
		}
	}
}
