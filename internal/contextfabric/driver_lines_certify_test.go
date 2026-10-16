package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	projectStatusDriversLegacyKeys = []string{
		"time", "level", "msg", "org_id", "emitted", "skipped_budget", "skipped_id_clash",
		"missing_kinds", "missing_states", "partial_kinds", "partial_states", "request_id",
	}
	teamBlockersDriversLegacyKeys = []string{
		"time", "level", "msg", "org_id", "emitted", "truncated_served", "skipped_missing", "skipped_zero",
		"skipped_budget", "skipped_id_clash", "skipped_stale", "skipped_duplicate", "request_id",
	}
)

func recordProjectStatusDrivers(ctx context.Context, event contextfabric.ProjectStatusDriverEvent) []byte {
	var buf bytes.Buffer
	telemetry := contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	telemetry.RecordProjectStatusDrivers(ctx, storage.Principal{OrgID: "org_1"}, event)
	return buf.Bytes()
}

func driverLineContext() context.Context {
	return observability.WithRequestID(context.Background(), factScopeCertifyRequestID)
}

func asAnyStrings(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// TestProjectStatusDriversLineCertifiesWhatTheBuilderWrites runs the real
// project status driver builder through every missing and partial state it can
// report, writes the line through the real sink and certifies it, so the
// declared vocabularies are what the producer writes.
func TestProjectStatusDriversLineCertifiesWhatTheBuilderWrites(t *testing.T) {
	t.Parallel()
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:p1", Label: "P1"}
	interpretation := contextfabric.InterpretedQuestion{FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}}}
	kinds := contextfabric.ProjectStatusDriverKindVocabulary()
	build := func(facts []contextfabric.CanonicalFact, coverage contextfabric.Coverage) contextfabric.ProjectStatusDriverEvent {
		_, _, event := contextfabric.ProjectStatusDriversForTest(interpretation, []contextfabric.SubjectRef{project}, facts, coverage, nil, nil, contextfabric.ItemAllocation{})
		if event == nil {
			t.Fatalf("the builder returned no event for a project status question")
		}
		return *event
	}
	coverageIn := func(state contextfabric.SourceState) contextfabric.Coverage {
		var coverage contextfabric.Coverage
		for _, kind := range kinds {
			coverage.Sources = append(coverage.Sources, contextfabric.SourceObservation{Source: "canonical_fact:" + kind, State: state})
		}
		return coverage
	}
	seenMissing := map[string]bool{}

	// Not read: no coverage at all. Every kind is missing in state not_read.
	event := build(nil, contextfabric.Coverage{})
	raw := recordProjectStatusDrivers(driverLineContext(), event)
	pinKeys(t, bytes.NewBuffer(raw), projectStatusDriversLegacyKeys, 1)
	line := certifyRequestLine(t, raw, eventspec.ProjectStatusDrivers, map[string]any{"org_id": "org_1", "emitted": 0})
	for _, state := range line["missing_states"].([]any) {
		seenMissing[state.(string)] = true
	}

	// Every source state in the coverage of a kind with no fact.
	for _, state := range []contextfabric.SourceState{
		contextfabric.SourceAvailable, contextfabric.SourceStale, contextfabric.SourceUnavailable, contextfabric.SourceUnconfigured,
		contextfabric.SourceUnauthorized, contextfabric.SourceNoData, contextfabric.SourceTruncated, contextfabric.SourceConflicted,
		contextfabric.SourceNotApplicable, contextfabric.SourcePruned,
	} {
		event := build(nil, coverageIn(state))
		line := certifyRequestLine(t, recordProjectStatusDrivers(driverLineContext(), event), eventspec.ProjectStatusDrivers, nil)
		for _, got := range line["missing_states"].([]any) {
			seenMissing[got.(string)] = true
		}
	}

	// A fact in an unusable state is named by that state; a usable fact without evidence and one without a value.
	workloadFact := func(state contextfabric.SourceState, evidence []string, fields map[string]contextfabric.FactValue) contextfabric.CanonicalFact {
		return contextfabric.CanonicalFact{Kind: contextfabric.FactWorkload, Subject: project, SourceState: state, EvidenceRefIDs: evidence, Fields: fields}
	}
	backlog := map[string]contextfabric.FactValue{"backlog_size": contextfabric.IntegerFactValue(4)}
	for _, facts := range [][]contextfabric.CanonicalFact{
		{workloadFact(contextfabric.SourceNoData, nil, nil)},
		{workloadFact(contextfabric.SourceAvailable, nil, backlog)},
		{workloadFact(contextfabric.SourceAvailable, []string{"acr:v1:p1"}, map[string]contextfabric.FactValue{})},
	} {
		event := build(facts, contextfabric.Coverage{})
		line := certifyRequestLine(t, recordProjectStatusDrivers(driverLineContext(), event), eventspec.ProjectStatusDrivers, nil)
		for _, got := range line["missing_states"].([]any) {
			seenMissing[got.(string)] = true
		}
	}

	// Partial: a usable fact that is not available, in each state it can be.
	seenPartial := map[string]bool{}
	for _, state := range []contextfabric.SourceState{contextfabric.SourceStale, contextfabric.SourceTruncated} {
		event := build([]contextfabric.CanonicalFact{workloadFact(state, []string{"acr:v1:p1"}, backlog)}, contextfabric.Coverage{})
		if event.Emitted != 1 {
			t.Fatalf("a %s workload fact with evidence and a value must emit one driver, emitted %d", state, event.Emitted)
		}
		line := certifyRequestLine(t, recordProjectStatusDrivers(driverLineContext(), event), eventspec.ProjectStatusDrivers, map[string]any{
			"emitted": 1, "partial_kinds": asAnyStrings([]string{"workload"}), "partial_states": asAnyStrings([]string{string(state)}),
		})
		for _, got := range line["partial_states"].([]any) {
			seenPartial[got.(string)] = true
		}
	}

	// The vocabularies are exactly what the drive wrote (less what only the budget path or the clash path can reach).
	for _, state := range contextfabric.ProjectStatusDriverMissingStateVocabulary() {
		if !seenMissing[state] {
			t.Errorf("declared missing state %q was not written by any driven case", state)
		}
	}
	for _, state := range contextfabric.ProjectStatusDriverPartialStateVocabulary() {
		if !seenPartial[state] {
			t.Errorf("declared partial state %q was not written by any driven case", state)
		}
	}

	// A line without a request id still writes the same keys minus request_id, and unknown values are refused.
	without := recordProjectStatusDrivers(context.Background(), contextfabric.ProjectStatusDriverEvent{})
	pinKeys(t, bytes.NewBuffer(without), projectStatusDriversLegacyKeys[:len(projectStatusDriversLegacyKeys)-1], 1)
	if bytes.Contains(without, []byte("null")) {
		t.Fatalf("an empty event must write empty lists, never null: %s", without)
	}
	certifyRefused(t, recordProjectStatusDrivers(driverLineContext(), contextfabric.ProjectStatusDriverEvent{
		Missing: []contextfabric.FactKind{"future_kind"}, MissingStates: []string{"no_value"},
	}), eventspec.ProjectStatusDrivers, "an unknown missing kind")
	certifyRefused(t, recordProjectStatusDrivers(driverLineContext(), contextfabric.ProjectStatusDriverEvent{
		Missing: []contextfabric.FactKind{contextfabric.FactWorkload}, MissingStates: []string{"future_state"},
	}), eventspec.ProjectStatusDrivers, "an unknown missing state")
}

func TestTeamBlockersDriversLineCertifies(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	telemetry.RecordTeamBlockersDrivers(driverLineContext(), storage.Principal{OrgID: "org_1"}, contextfabric.TeamBlockersDriverEvent{
		Emitted: 1, TruncatedServed: 2, SkippedMissing: 3, SkippedZero: 4, SkippedBudget: 5, SkippedIDClash: 6, SkippedStale: 7, SkippedDuplicate: 8,
	})
	pinKeys(t, &buf, teamBlockersDriversLegacyKeys, 1)
	certifyRequestLine(t, buf.Bytes(), eventspec.TeamBlockersDrivers, map[string]any{
		"org_id": "org_1", "emitted": 1, "truncated_served": 2, "skipped_missing": 3, "skipped_zero": 4,
		"skipped_budget": 5, "skipped_id_clash": 6, "skipped_stale": 7, "skipped_duplicate": 8,
	})
	buf.Reset()
	telemetry.RecordTeamBlockersDrivers(context.Background(), storage.Principal{OrgID: "org_1"}, contextfabric.TeamBlockersDriverEvent{})
	pinKeys(t, &buf, teamBlockersDriversLegacyKeys[:len(teamBlockersDriversLegacyKeys)-1], 1)
}
