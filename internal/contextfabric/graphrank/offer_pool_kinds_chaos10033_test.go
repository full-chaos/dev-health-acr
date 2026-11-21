package graphrank

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

const offeredByKindKey = "offered_by_kind"

func offerPoolLines(t *testing.T, buf *bytes.Buffer) (summary map[string]any, others []map[string]any) {
	t.Helper()
	for _, raw := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		if line["msg"] == eventspec.OfferPoolSummary.Msg {
			summary = line
			continue
		}
		others = append(others, line)
	}
	return summary, others
}

// CHAOS-10033: the summary carries the offered kinds with counts, kind names
// and ints only; withheld candidates are not offered and are not counted.
func TestOfferPoolSummaryCarriesOfferedKindCounts(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	repository := vectorOfferCandidate("repo_exact", 1, contextfabric.ResolutionProposed, contextfabric.MatchExact)
	repository.Subject.Kind = contextfabric.SubjectRepository
	resolveOfferPoolTraced(tracer,
		vectorOfferCandidate("team_vector_only", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
		vectorOfferCandidate("team_exact_a", 1, contextfabric.ResolutionProposed, contextfabric.MatchExact),
		vectorOfferCandidate("team_exact_b", 1, contextfabric.ResolutionProposed, contextfabric.MatchExact),
		repository,
	)
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.OfferPoolSummary,
		Want:  map[string]any{"request_id": "req_offer_pool_identity_0000000000", "pass": 1, "vector_only_excluded": 1},
	}); err != nil {
		t.Fatalf("Certify(OfferPoolSummary): %v", err)
	}
	summary, others := offerPoolLines(t, &buf)
	want := map[string]any{string(contextfabric.SubjectTeam): float64(2), string(contextfabric.SubjectRepository): float64(1)}
	if got := summary[offeredByKindKey]; !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", offeredByKindKey, got, want)
	}
	// Control: no other trace line gains the field.
	for _, line := range others {
		if _, has := line[offeredByKindKey]; has {
			t.Fatalf("line %q carries %s", line["msg"], offeredByKindKey)
		}
	}
}

// The empty-graph early return still emits the field, as an empty map.
func TestOfferPoolSummaryEmptyGraphCarriesEmptyOfferedKinds(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	resolveOfferPoolTraced(tracer)
	summary, _ := offerPoolLines(t, &buf)
	if summary == nil {
		t.Fatal("no offer pool summary line")
	}
	if got, ok := summary[offeredByKindKey].(map[string]any); !ok || len(got) != 0 {
		t.Fatalf("%s = %v, want empty object", offeredByKindKey, summary[offeredByKindKey])
	}
}
