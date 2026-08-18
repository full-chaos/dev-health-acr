package falkorgraph

import (
	"context"
	"strings"
	"testing"
)

// CHAOS-3890 (audit H5): fulltextSearchNodesForResolution computes whether
// the CHAOS-3838 domain-lexicon expansion fired, how many batches it ran,
// how many genuinely new candidates it contributed, and whether expansion
// itself flipped this call's own truncated signal to true -- then discards
// all of it once the function returns. truncated feeds
// ResolveFromMergedCandidates' searchTruncated, which can block a
// downstream auto-commit, so a caller investigating an unexpected
// non-commit had no operational signal to confirm "expansion caused this"
// against. These tests pin RecordLexiconExpansion as that signal, per
// request, without changing fulltextSearchNodesForResolution's own
// returned candidates/truncated value at all (PURE ADDITIVE).

// TestLexiconExpansion_DoesNotFireReportsFiredFalse is the overwhelming
// common-case control: no lexicon phrase matched, so the ONE-round-trip
// fast path (queries.go's own comment) still reports its outcome, at
// fired=false with zero batches/candidates, rather than silently emitting
// nothing.
func TestLexiconExpansion_DoesNotFireReportsFiredFalse(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, nil
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	if _, _, err := adapter.fulltextSearchNodesForResolution(context.Background(), "k", "org-1", "horizontal scaling readiness", 10, temporalFilter{}); err != nil {
		t.Fatalf("fulltextSearchNodesForResolution() error = %v", err)
	}

	if len(telemetry.lexiconExpansions) != 1 {
		t.Fatalf("lexiconExpansions = %#v, want exactly 1", telemetry.lexiconExpansions)
	}
	got := telemetry.lexiconExpansions[0]
	if got.fired {
		t.Fatal("fired = true, want false -- no lexicon phrase in this text")
	}
	if got.batchCount != 0 || got.addedCandidates != 0 || got.truncatedByExpansion {
		t.Fatalf("expansion record = %#v, want all-zero/false alongside fired=false", got)
	}
}

// TestLexiconExpansion_FiresAndAddsCandidatesWithoutTruncating is the
// positive case: the "pr" group matches, one kind-agnostic batch runs, and
// it contributes a genuinely new candidate the base query never saw --
// with plenty of budget, so truncated never flips.
func TestLexiconExpansion_FiresAndAddsCandidatesWithoutTruncating(t *testing.T) {
	baseRow := fulltextRow("pull_request", "pr_1", "PR base hit", "PR base hit", nil)
	expansionRow := fulltextRow("pull_request", "pr_2", "Pull request expansion hit", "Pull request expansion hit", nil)

	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		query, _ := params["query"].(string)
		switch {
		case query == "pr":
			// The BASE query: exactly the tokenized caller text.
			return []row{baseRow}, nil
		case strings.Contains(query, "pull request"):
			// The lexicon-expansion batch's own widened query -- contains
			// the multi-word synonym's quoted phrase clause.
			return []row{expansionRow}, nil
		default:
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	candidates, truncated, err := adapter.fulltextSearchNodesForResolution(context.Background(), "k", "org-1", "pr", 10, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodesForResolution() error = %v", err)
	}
	if truncated {
		t.Fatal("truncated = true, want false -- plenty of budget for one added candidate")
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v, want exactly 2 (base + expansion)", candidates)
	}

	if len(telemetry.lexiconExpansions) != 1 {
		t.Fatalf("lexiconExpansions = %#v, want exactly 1", telemetry.lexiconExpansions)
	}
	got := telemetry.lexiconExpansions[0]
	if !got.fired {
		t.Fatal("fired = false, want true -- the \"pr\" lexicon group matched")
	}
	if got.batchCount != 1 {
		t.Fatalf("batchCount = %d, want 1 (one kind-agnostic batch)", got.batchCount)
	}
	if got.addedCandidates != 1 {
		t.Fatalf("addedCandidates = %d, want 1 (exactly the expansion-only row)", got.addedCandidates)
	}
	if got.truncatedByExpansion {
		t.Fatal("truncatedByExpansion = true, want false -- nothing here exceeded budget")
	}
}

// TestLexiconExpansion_FiringFlipsTruncatedIsObservable is the CHAOS-3890
// audit's own named hazard: expansion can flip truncated=true even when
// the base query alone did not truncate -- and that fact, previously
// computed then discarded, must now be reported so a downstream
// non-commit traced back to searchTruncated is confirmable against this
// signal.
func TestLexiconExpansion_FiringFlipsTruncatedIsObservable(t *testing.T) {
	baseRow := fulltextRow("pull_request", "pr_1", "PR base hit", "PR base hit", nil)
	// Three genuinely new expansion rows against a tight limit=1: the
	// batch's own limit+1 sentinel (fetchBudget = limit(1) + len(seen)(1)
	// = 2) sees 3 > 2 rows and reports its own batchTruncated=true.
	overflow := []row{
		fulltextRow("pull_request", "pr_2", "Pull request overflow A", "Pull request overflow A", nil),
		fulltextRow("pull_request", "pr_3", "Pull request overflow B", "Pull request overflow B", nil),
		fulltextRow("pull_request", "pr_4", "Pull request overflow C", "Pull request overflow C", nil),
	}

	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		query, _ := params["query"].(string)
		switch {
		case query == "pr":
			return []row{baseRow}, nil
		case strings.Contains(query, "pull request"):
			return overflow, nil
		default:
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)

	_, truncated, err := adapter.fulltextSearchNodesForResolution(context.Background(), "k", "org-1", "pr", 1, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodesForResolution() error = %v", err)
	}
	if !truncated {
		t.Fatal("truncated = false, want true -- the expansion batch's own overflow must truncate this call")
	}

	if len(telemetry.lexiconExpansions) != 1 {
		t.Fatalf("lexiconExpansions = %#v, want exactly 1", telemetry.lexiconExpansions)
	}
	got := telemetry.lexiconExpansions[0]
	if !got.fired {
		t.Fatal("fired = false, want true")
	}
	if !got.truncatedByExpansion {
		t.Fatal("truncatedByExpansion = false, want true -- the base query alone did not truncate; expansion is the reason truncated flipped true, and that must be visible per request rather than discarded")
	}
}
