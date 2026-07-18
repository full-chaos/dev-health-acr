package evaldemo

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func citationsForPacket(ctx context.Context, store *contextpacket.EvaluationStore, principal storage.Principal, packet contractsv1.ContextPacket) ([]Citation, error) {
	citations := make([]Citation, 0, len(packet.Items))
	seen := map[string]bool{}
	for _, item := range packet.Items {
		for _, evidenceRefID := range item.EvidenceRefIDs {
			if seen[evidenceRefID] {
				continue
			}
			expanded, err := store.ResolveEvidence(ctx, principal, evidenceRefID)
			if err != nil {
				return nil, fmt.Errorf("resolve evidence: %w", err)
			}
			seen[evidenceRefID] = true
			citations = append(citations, Citation{
				EvidenceRefID: expanded.Evidence.EvidenceRefID,
				EntityID:      expanded.Evidence.Source.EntityID,
				SafeURI:       expanded.Evidence.Source.SafeURI,
				Excerpt:       expanded.Excerpt,
			})
		}
	}
	return citations, nil
}

func packetMatchesTask(packet contractsv1.ContextPacket, citations []Citation, task evalfixture.Task) bool {
	if string(packet.Status) != string(task.ExpectedStatus) {
		return false
	}
	actual := make([]string, 0, len(citations))
	for _, citation := range citations {
		actual = append(actual, citation.EvidenceRefID)
	}
	return sameIDs(actual, task.ExpectedEvidenceIDs)
}

func sameIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := map[string]bool{}
	for _, value := range left {
		seen[value] = true
	}
	for _, value := range right {
		if !seen[value] {
			return false
		}
	}
	return true
}

func scenarioReason(scenario Scenario) string {
	switch scenario {
	case ScenarioEmptyEvidence:
		return "expected_evidence_missing"
	case ScenarioMismatchedTask:
		return "task_input_does_not_match_fixture"
	default:
		return "packet_does_not_match_fixture"
	}
}

func sameRepeatInputs(repeats []Repeat) bool {
	if len(repeats) < 2 {
		return true
	}
	first := repeats[0].Tasks
	for _, repeat := range repeats[1:] {
		if !reflect.DeepEqual(first, repeat.Tasks) {
			return false
		}
	}
	return true
}

func calculateMetrics(tasks []TaskRun) Metrics {
	contextItems, contextTokens, groundedItems := 0, 0, 0
	for _, task := range tasks {
		contextItems += len(task.Context.Citations)
		contextTokens += task.Context.EstimatedTokens
		for _, citation := range task.Context.Citations {
			if containsID(task.ExpectedEvidenceIDs, citation.EvidenceRefID) {
				groundedItems++
			}
		}
	}
	return Metrics{
		Cold: SurfaceMetrics{
			FactualErrorRate:   notApplicableRatio("no observed claims are emitted without ACR context"),
			FileTestRecall:     notApplicableRatio("the public corpus has no file or test identifier ground truth"),
			CitationPrecision:  notApplicableRatio("no citations are emitted without ACR context"),
			IrrelevantItemRate: notApplicableRatio("no packet items are emitted without ACR context"),
			PlanLatency:        CostMetric{Value: len(tasks), Unit: "logical_steps", Note: "One deterministic planning step per task; not wall-clock latency."},
			TokenCost:          CostMetric{Value: 0, Unit: "estimated_context_tokens"},
		},
		Context: SurfaceMetrics{
			FactualErrorRate:   ratio(contextItems-groundedItems, contextItems),
			FileTestRecall:     notApplicableRatio("the public corpus has no file or test identifier ground truth"),
			CitationPrecision:  ratio(groundedItems, contextItems),
			IrrelevantItemRate: ratio(contextItems-groundedItems, contextItems),
			PlanLatency:        CostMetric{Value: len(tasks) + contextItems, Unit: "logical_steps", Note: "One planning step plus one cited-packet-item inspection per task; not wall-clock latency."},
			TokenCost:          CostMetric{Value: contextTokens, Unit: "estimated_context_tokens", Note: "Uses the real packet budget estimator."},
		},
	}
}

func containsID(ids []string, want string) bool {
	return slices.Contains(ids, want)
}

func ratio(numerator, denominator int) RatioMetric {
	if denominator == 0 {
		return notApplicableRatio("no applicable denominator")
	}
	value := float64(numerator) / float64(denominator)
	return RatioMetric{Numerator: numerator, Denominator: denominator, Value: &value, Unit: "ratio"}
}

func notApplicableRatio(note string) RatioMetric {
	return RatioMetric{Unit: "ratio", Note: note}
}
