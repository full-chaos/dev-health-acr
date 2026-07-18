package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/full-chaos/dev-health-acr/internal/evaldemo"
)

func main() {
	config, output, err := parseConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	result, runErr := evaldemo.Run(context.Background(), config)
	if runErr != nil && !errors.Is(runErr, evaldemo.ErrInvalidated) {
		fmt.Fprintln(os.Stderr, "evaluation failed")
		os.Exit(2)
	}
	if err := writeResult(result, output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if runErr != nil {
		if errors.Is(runErr, evaldemo.ErrInvalidated) {
			fmt.Fprintln(os.Stderr, "evaluation invalidated")
			os.Exit(1)
		}
	}
}

func parseConfig() (evaldemo.Config, string, error) {
	repeat := flag.Int("repeat", 1, "number of identical fixture runs")
	scenario := flag.String("scenario", string(evaldemo.ScenarioDefault), "default, corrupt-hash, empty-evidence, or mismatched-task")
	corpusDir := flag.String("corpus", "testdata/evaluation/v1", "public evaluation corpus directory")
	output := flag.String("out", "", "optional JSON evidence file")
	flag.Parse()
	if *repeat < 1 {
		return evaldemo.Config{}, "", errors.New("--repeat must be positive")
	}
	return evaldemo.Config{CorpusDir: *corpusDir, Repeat: *repeat, Scenario: evaldemo.Scenario(*scenario)}, *output, nil
}

func writeResult(result evaldemo.Result, output string) error {
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode evaluation result: %w", err)
	}
	encoded = append(encoded, '\n')
	if output != "" {
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return fmt.Errorf("create evidence directory: %w", err)
		}
		if err := os.WriteFile(output, encoded, 0o644); err != nil {
			return fmt.Errorf("write evaluation evidence: %w", err)
		}
	}
	if _, err := os.Stdout.Write(encoded); err != nil {
		return fmt.Errorf("write evaluation output: %w", err)
	}
	return nil
}
