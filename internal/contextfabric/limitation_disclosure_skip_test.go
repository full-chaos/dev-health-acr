package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// skipRecorder captures what appendBoundedLimitations reports when it has to
// leave a disclosure out, and the Warn line written for it, by installing its
// own sink for one test; the tests that use it must not run in parallel.
type skipRecorder struct {
	mu          sync.Mutex
	recognisers []string
	logs        bytes.Buffer
}

func installSkipRecorder(t *testing.T) *skipRecorder {
	t.Helper()
	rec := &skipRecorder{}
	logger := slog.New(slog.NewTextHandler(&rec.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	restore := SetLimitationDisclosureSkipSink(logger, func(recogniser string) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.recognisers = append(rec.recognisers, recogniser)
	})
	t.Cleanup(restore)
	return rec
}

func (r *skipRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.recognisers...)
}

// A full list of service-looking entries leaves no model caveat to displace, so
// the disclosure cannot be added. That is a disclosure the reader never sees:
// it must be a Warn and a counted event, not silence.
func TestSkippedLeverDisclosureIsWarnedAndCounted(t *testing.T) {
	rec := installSkipRecorder(t)
	full := gapLookalikes(contractsv1.ContextFabricLimitationsMaxCount)
	sentence := retrievalDegradedLimitation

	composed, displaced := appendBoundedLimitations(full, []string{sentence})

	if !slices.Equal(composed, full) {
		t.Fatalf("the skipped addition changed the list that is served")
	}
	if displaced != 0 || len(composed) != len(full) || hasRetrievalDegradedLimitation(composed) {
		t.Fatalf("premise changed: displaced=%d len=%d degraded=%t; the test is about the skip branch", displaced, len(composed), hasRetrievalDegradedLimitation(composed))
	}
	want := serviceLimitationRecogniserID(sentence)
	if got := rec.seen(); len(got) != 1 || got[0] != want {
		t.Fatalf("skip recorder saw %v, want [%s]", got, want)
	}
	logged := rec.logs.String()
	for _, fragment := range []string{
		"level=WARN",
		`msg="context fabric limitation disclosure skipped"`,
		"cf_limitation_skipped_recogniser=" + want,
		"cf_limitations_len=100",
	} {
		if !strings.Contains(logged, fragment) {
			t.Errorf("skip Warn line lacks %q; logs=%s", fragment, logged)
		}
	}
	if strings.Contains(logged, sentence) {
		t.Errorf("the Warn line carries the sentence text; it must name the recogniser only: %s", logged)
	}
}

// An addition that fits, or that displaces a model caveat, is not a skip.
func TestDisclosureThatFitsOrDisplacesIsNotReportedAsSkipped(t *testing.T) {
	rec := installSkipRecorder(t)
	appendBoundedLimitations(modelLimitations(3), []string{retrievalDegradedLimitation})
	appendBoundedLimitations(modelLimitations(contractsv1.ContextFabricLimitationsMaxCount), []string{retrievalDegradedLimitation})
	if got := rec.seen(); len(got) != 0 {
		t.Fatalf("skip recorder saw %v for additions that were served", got)
	}
	if strings.Contains(rec.logs.String(), "disclosure skipped") {
		t.Fatalf("skip Warn written for additions that were served: %s", rec.logs.String())
	}
}

// Through the engine: a synthesizer that hands back a list full of
// service-looking entries (the AnswerSynthesizer port does not vet them; the
// service's own runtime strips them) makes the claim-depth lever's disclosure
// unplaceable. The answer is still served, and the lost sentence is signalled.
func TestEngineSignalsALeverDisclosureThatCouldNotBePlaced(t *testing.T) {
	rec := installSkipRecorder(t)
	calls := 0
	engine := withFullOutcomeRows(chaos6743Engine(t, &calls, &recordingTelemetry{}, chaos6743ProdShape))
	base := engine.synthesizer
	engine.synthesizer = synthesizerFunc(func(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
		result, err := base.Synthesize(ctx, principal, input)
		if err != nil {
			return result, err
		}
		result.Limitations = gapLookalikes(contractsv1.ContextFabricLimitationsMaxCount)
		return result, nil
	})

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6743Request())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a served answer", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result invalid: %v", err)
	}
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricClaimDepthLimitation(limitation) {
			t.Fatalf("premise changed: the claim-depth disclosure was placed, so this fixture no longer reaches the skip branch")
		}
	}
	found := false
	for _, recogniser := range rec.seen() {
		found = found || recogniser == "claim_depth"
	}
	if !found {
		t.Fatalf("the claim-depth disclosure is absent from the served answer and no skip was signalled; recorder saw %v", rec.seen())
	}
}
