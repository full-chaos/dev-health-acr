package hosted_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type recordingTwoTurnSaver struct {
	saved []contextfabric.InvestigationResult
}

func (r *recordingTwoTurnSaver) Save(_ context.Context, _ storage.Principal, result contextfabric.InvestigationResult, _ contextfabric.SourceWatermarkSnapshot, _ contextfabric.RebuildEpoch, _ string, _ contextfabric.ReuseRetrievalIdentity, _ contextfabric.ReusePromptVersions, _ contextfabric.ReuseVersionAuthorities, _ int64, _ string, _ contextfabric.SemanticStateWrite) error {
	r.saved = append(r.saved, result)
	return nil
}

func TestSeedAnchorNegativeResult_PassesWriteValidationWithProducerCompleteness(t *testing.T) {
	entry := twoTurnOracleEntry{NegativeKind: "repository", NegativeAnchorCanonicalID: "repo:probe"}
	terms := anchorTermIndex{anchorTermIndexKey(entry.NegativeKind, entry.NegativeAnchorCanonicalID): "probe-term"}
	saver := &recordingTwoTurnSaver{}

	id, err := seedAnchorNegativeResult(context.Background(), saver, storage.Principal{OrgID: "org"}, 0, entry, contractsv1.ContextFabricAnchorOptionReceiptPrefix+"probe000000000000000000", terms, "tok")
	if err != nil {
		t.Fatalf("seedAnchorNegativeResult: %v", err)
	}
	if len(saver.saved) != 1 || saver.saved[0].ResultID != id {
		t.Fatalf("saved = %d results, want exactly the seeded %q", len(saver.saved), id)
	}
	got := saver.saved[0]
	if want := contextfabric.ComputeAnswerCompleteness(got); got.Completeness.TerminalStatus != want.TerminalStatus || got.Completeness.State != want.State {
		t.Fatalf("seeded completeness %+v is not the production producer's %+v", got.Completeness, want)
	}
}
