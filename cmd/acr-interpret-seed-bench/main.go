// Command acr-interpret-seed-bench is CHAOS-4631's Shape-distribution
// measurement tool. It calls the real configured Context Fabric model
// (the same ACR_CONTEXT_FABRIC_MODEL_* environment modelprovider.ConfigFromEnv
// reads for a production deployment) with genkitruntime's CHAOS-4631
// measurement entry point, N distinct derived seeds per acceptance
// question, and prints a Shape-distribution + cost table.
//
// This tool makes real, billable model calls. It is not part of `make
// verify`, not run in CI, and ships no telemetry of its own -- it is a
// manual measurement instrument, run against a real provider credential by
// a human deciding when to run it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretseedbench"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func main() {
	samples := flag.Int("n", 5, "samples per question (distinct derived seeds 0..n-1)")
	orgID := flag.String("org-id", "acr-interpret-seed-bench", "principal org id (interpret makes no DB call; any non-empty value works)")
	out := flag.String("out", "", "optional path to write raw per-sample JSON")
	flag.Parse()

	if err := run(*samples, *orgID, *out); err != nil {
		fmt.Fprintln(os.Stderr, "acr-interpret-seed-bench:", err)
		os.Exit(1)
	}
}

func run(n int, orgID, outPath string) error {
	ctx := context.Background()
	cfg, err := modelprovider.ConfigFromEnv(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load model configuration: %w", err)
	}
	runtime, err := modelprovider.NewGenkitRuntime(ctx, cfg)
	if err != nil {
		return fmt.Errorf("build model runtime: %w", err)
	}
	principal := storage.Principal{OrgID: orgID}

	results, err := interpretseedbench.Run(ctx, runtime, principal, interpretseedbench.AcceptanceQuestions, n)
	if err != nil {
		return fmt.Errorf("run measurement: %w", err)
	}

	if outPath != "" {
		encoded, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return fmt.Errorf("encode results: %w", err)
		}
		if err := os.WriteFile(outPath, encoded, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
	}

	printDistribution(results)
	printCost(results)
	return nil
}

func printDistribution(results []interpretseedbench.Sample) {
	dist := interpretseedbench.ShapeDistribution(results)
	fmt.Println("Shape distribution (question | shape | count):")
	for _, q := range interpretseedbench.AcceptanceQuestions {
		byShape := dist[q.ID]
		shapes := make([]string, 0, len(byShape))
		for shape := range byShape {
			shapes = append(shapes, shape)
		}
		sort.Strings(shapes)
		for _, shape := range shapes {
			label := shape
			if label == "" {
				label = "(error)"
			}
			fmt.Printf("  %-10s %-24s %d\n", q.ID, label, byShape[shape])
		}
	}
}

func printCost(results []interpretseedbench.Sample) {
	fmt.Println("Cost per turn-1 (question | samples | avg input | avg output | avg total | avg ms):")
	for _, summary := range interpretseedbench.CostSummaries(results, interpretseedbench.AcceptanceQuestions) {
		fmt.Printf("  %-10s %8d %12.1f %12.1f %12.1f %10.1f\n",
			summary.QuestionID, summary.Samples, summary.AvgInputTokens, summary.AvgOutputTokens, summary.AvgTotalTokens, summary.AvgDurationMS)
	}
}
