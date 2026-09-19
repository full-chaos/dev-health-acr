package contextfabric_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemMembershipS1InfoIsCertifiedFromTheConfiguredJSONLogger(t *testing.T) {
	var logBytes bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBytes, nil))
	ctx := observability.WithRequestID(context.Background(), "req_57520000000000000000000000000000")
	telemetry := contextfabric.NewSlogWorkItemMembershipTelemetry(logger)
	telemetry.RecordWorkItemMembershipS1(ctx, storage.Principal{OrgID: "org_5752"}, contextfabric.WorkItemMembershipS1Event{
		State:                    contextfabric.WorkItemMembershipCensusExact,
		Reason:                   contextfabric.WorkItemMembershipUnmeasuredNone,
		PopulationMeasured:       true,
		PopulationComplete:       true,
		CappedPopulation:         0,
		AuthorizedPopulation:     0,
		DeniedPopulation:         0,
		ServedMembers:            0,
		CensusLimit:              contextfabric.WorkItemMembershipCensusLimit,
		FutureBoundaryCount:      0,
		TransitionAssertionCount: 0,
		MaxExecutionTimeSeconds:  5,
		MaxRowsToRead:            8192,
		MaxMemoryUsage:           contextfabric.WorkItemMembershipMaxMemoryUsage,
		MaxResultRows:            contextfabric.WorkItemMembershipServeLimit + 1,
	})
	if bytes.Contains(logBytes.Bytes(), []byte("question")) {
		t.Fatalf("S1 Info record contains question text: %s", logBytes.Bytes())
	}

	parsed, err := certify.Parse(logBytes.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on configured slog.JSONHandler output: %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{
		Event: eventspec.WorkItemMembershipS1,
		Want: map[string]any{
			"org_id":                     "org_5752",
			"state":                      "exact",
			"reason":                     "",
			"population_measured":        true,
			"population_complete":        true,
			"capped_population":          0,
			"authorized_population":      0,
			"denied_population":          0,
			"served_members":             0,
			"census_limit":               2000,
			"future_boundary_count":      0,
			"transition_assertion_count": 0,
			"max_execution_time_seconds": 5,
			"max_rows_to_read":           8192,
			"max_memory_usage":           int(contextfabric.WorkItemMembershipMaxMemoryUsage),
			"max_result_rows":            201,
			"request_id":                 "req_57520000000000000000000000000000",
		},
	}); err != nil {
		t.Fatalf("certify S1 Info line: %v", err)
	}
}

// TestWorkItemMembershipS1UnmeasuredReasonsAreCertified pins CHAOS-5991's two
// new closed-vocabulary reasons on the REAL emitted Info line (never a hand
// list): read_limit_exceeded (the S1 census's own query-resource budget was
// exceeded -- the exact shape a real project's census hit live) and
// cancelled (the caller's context ended in flight). Each is asserted
// distinctly from the pre-existing generic s1_error.
func TestWorkItemMembershipS1UnmeasuredReasonsAreCertified(t *testing.T) {
	for _, reason := range []contextfabric.WorkItemMembershipUnmeasuredReason{
		contextfabric.WorkItemMembershipUnmeasuredReadLimitExceeded,
		contextfabric.WorkItemMembershipUnmeasuredCancelled,
	} {
		t.Run(string(reason), func(t *testing.T) {
			var logBytes bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logBytes, nil))
			contextfabric.NewSlogWorkItemMembershipTelemetry(logger).RecordWorkItemMembershipS1(
				context.Background(), storage.Principal{OrgID: "org_5752"}, contextfabric.WorkItemMembershipS1Event{
					State:                   contextfabric.WorkItemMembershipCensusUnmeasured,
					Reason:                  reason,
					PopulationMeasured:      false,
					CensusLimit:             contextfabric.WorkItemMembershipCensusLimit,
					MaxExecutionTimeSeconds: 5,
					MaxRowsToRead:           2_000_000,
					MaxMemoryUsage:          contextfabric.WorkItemMembershipMaxMemoryUsage,
					MaxResultRows:           contextfabric.WorkItemMembershipServeLimit + 1,
				},
			)
			if bytes.Contains(logBytes.Bytes(), []byte("Exception")) || bytes.Contains(logBytes.Bytes(), []byte("code:")) {
				t.Fatalf("S1 Info record leaked a ClickHouse exception shape: %s", logBytes.Bytes())
			}
			parsed, err := certify.Parse(logBytes.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			if _, err := certify.Certify(parsed, certify.Assertion{
				Event: eventspec.WorkItemMembershipS1,
				Want: map[string]any{
					"org_id":                     "org_5752",
					"state":                      "unmeasured",
					"reason":                     string(reason),
					"population_measured":        false,
					"population_complete":        false,
					"capped_population":          0,
					"authorized_population":      0,
					"denied_population":          0,
					"served_members":             0,
					"census_limit":               2000,
					"future_boundary_count":      0,
					"transition_assertion_count": 0,
					"max_execution_time_seconds": 5,
					"max_rows_to_read":           2_000_000,
					"max_memory_usage":           int(contextfabric.WorkItemMembershipMaxMemoryUsage),
					"max_result_rows":            201,
				},
			}); err != nil {
				t.Fatalf("certify S1 %s Info line: %v", reason, err)
			}
		})
	}
}

func TestWorkItemMembershipGateInfoIsCertifiedFromTheConfiguredJSONLogger(t *testing.T) {
	var logBytes bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBytes, nil))
	ctx := observability.WithRequestID(context.Background(), "req_57520000000000000000000000000001")
	telemetry := contextfabric.NewSlogWorkItemMembershipTelemetry(logger)
	telemetry.RecordWorkItemMembershipGate(ctx, storage.Principal{OrgID: "org_5752"}, contextfabric.WorkItemMembershipGateEvent{
		Outcome:       "refused",
		InFlight:      contextfabric.DefaultWorkItemMembershipMaxInFlight,
		Queued:        contextfabric.DefaultWorkItemMembershipQueueCapacity,
		MaxInFlight:   contextfabric.DefaultWorkItemMembershipMaxInFlight,
		QueueCapacity: contextfabric.DefaultWorkItemMembershipQueueCapacity,
	})

	parsed, err := certify.Parse(logBytes.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on configured slog.JSONHandler output: %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{
		Event: eventspec.WorkItemMembershipGate,
		Want: map[string]any{
			"org_id":         "org_5752",
			"outcome":        "refused",
			"in_flight":      contextfabric.DefaultWorkItemMembershipMaxInFlight,
			"queued":         contextfabric.DefaultWorkItemMembershipQueueCapacity,
			"max_in_flight":  contextfabric.DefaultWorkItemMembershipMaxInFlight,
			"queue_capacity": contextfabric.DefaultWorkItemMembershipQueueCapacity,
			"request_id":     "req_57520000000000000000000000000001",
		},
	}); err != nil {
		t.Fatalf("certify gate Info line: %v", err)
	}
}

func TestWorkItemMembershipS1ExplicitZeroCertifiesWithoutRequestID(t *testing.T) {
	var logBytes bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBytes, nil))
	contextfabric.NewSlogWorkItemMembershipTelemetry(logger).RecordWorkItemMembershipS1(
		context.Background(), storage.Principal{OrgID: "org_5752"}, contextfabric.WorkItemMembershipS1Event{
			State:                   contextfabric.WorkItemMembershipCensusExact,
			Reason:                  contextfabric.WorkItemMembershipUnmeasuredNone,
			PopulationMeasured:      true,
			PopulationComplete:      true,
			CensusLimit:             contextfabric.WorkItemMembershipCensusLimit,
			MaxExecutionTimeSeconds: 5,
			MaxRowsToRead:           8192,
			MaxMemoryUsage:          contextfabric.WorkItemMembershipMaxMemoryUsage,
			MaxResultRows:           contextfabric.WorkItemMembershipServeLimit + 1,
		},
	)
	parsed, err := certify.Parse(logBytes.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	if _, err := certify.Certify(parsed, certify.Assertion{
		Event: eventspec.WorkItemMembershipS1,
		Want: map[string]any{
			"org_id":                     "org_5752",
			"state":                      "exact",
			"reason":                     "",
			"population_measured":        true,
			"population_complete":        true,
			"capped_population":          0,
			"authorized_population":      0,
			"denied_population":          0,
			"served_members":             0,
			"census_limit":               2000,
			"future_boundary_count":      0,
			"transition_assertion_count": 0,
			"max_execution_time_seconds": 5,
			"max_rows_to_read":           8192,
			"max_memory_usage":           int(contextfabric.WorkItemMembershipMaxMemoryUsage),
			"max_result_rows":            201,
		},
	}); err != nil {
		t.Fatalf("certify S1 explicit zero without request id: %v", err)
	}
}
