package evaldemo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var fixtureTime = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func Run(ctx context.Context, config Config) (Result, error) {
	config = normalizedConfig(config)
	if err := validateConfig(config); err != nil {
		return Result{}, fmt.Errorf("validate evaluation configuration: %w", err)
	}
	corpus, err := loadCorpus(config)
	if err != nil {
		if config.Scenario == ScenarioCorruptHash {
			return invalidated(config.Scenario, "corpus_integrity_failed"), fmt.Errorf("%w: verify public corpus: %w", ErrInvalidated, err)
		}
		return Result{}, fmt.Errorf("verify public corpus: %w", err)
	}
	result := newResult(corpus)
	store, err := contextpacket.NewEvaluationStore(corpus, "org-fixture")
	if err != nil {
		return Result{}, fmt.Errorf("create fixture store: %w", err)
	}
	assembler := contextpacket.NewAssembler(store, contextpacket.Options{
		Now: func() time.Time { return fixtureTime }, ServiceVersion: "evaluation-demo", MinimumSidecarVersion: "0.1.0",
	})
	principal := storage.Principal{OrgID: "org-fixture", RepositoryScopes: []string{corpus.Scenario.Repository.Slug}}
	for index := 0; index < config.Repeat; index++ {
		repeat, runErr := runRepeat(ctx, config.Scenario, corpus, store, assembler, principal, index+1)
		if runErr != nil {
			if runErr.invalidated {
				return invalidated(config.Scenario, runErr.reason), fmt.Errorf("%w: %s", ErrInvalidated, runErr.reason)
			}
			return Result{}, fmt.Errorf("run fixture repeat: %s", runErr.reason)
		}
		result.Repeats = append(result.Repeats, repeat)
	}
	if !sameRepeatInputs(result.Repeats) {
		return Result{}, errors.New("repeat input hashes differ")
	}
	metrics := calculateMetrics(result.Repeats[0].Tasks)
	result.Metrics = &metrics
	return result, nil
}

func normalizedConfig(config Config) Config {
	if config.Repeat == 0 {
		config.Repeat = 1
	}
	if config.Scenario == "" {
		config.Scenario = ScenarioDefault
	}
	return config
}

func validateConfig(config Config) error {
	if config.CorpusDir == "" || config.Repeat < 1 {
		return errors.New("corpus directory and positive repeat count are required")
	}
	switch config.Scenario {
	case ScenarioDefault, ScenarioCorruptHash, ScenarioEmptyEvidence, ScenarioMismatchedTask:
		return nil
	default:
		return fmt.Errorf("unknown scenario %q", config.Scenario)
	}
}

func newResult(corpus evalfixture.Corpus) Result {
	return Result{
		SchemaVersion: SchemaVersion,
		Result:        ResultComplete,
		Disclosure:    "Deterministic fixture measurement only; no live model or customer data is used, and no agent-outcome claim is made.",
		Fixture: Fixture{
			CorpusVersion: corpus.CorpusVersion,
			ScenarioID:    corpus.Scenario.ScenarioID,
			Repository:    corpus.Scenario.Repository.Slug,
		},
		Repeats: []Repeat{},
	}
}

func invalidated(scenario Scenario, reason string) Result {
	return Result{
		SchemaVersion: SchemaVersion,
		Result:        ResultInvalidated,
		Disclosure:    "Deterministic fixture measurement only; no live model or customer data is used, and no agent-outcome claim is made.",
		Repeats:       []Repeat{},
		Invalidation:  &Invalidation{Scenario: scenario, Reason: reason},
	}
}

type scenarioError struct {
	reason      string
	invalidated bool
}

func (e scenarioError) Error() string { return e.reason }

func runRepeat(ctx context.Context, scenario Scenario, corpus evalfixture.Corpus, store *contextpacket.EvaluationStore, assembler *contextpacket.Assembler, principal storage.Principal, index int) (Repeat, *scenarioError) {
	repeat := Repeat{Index: index, Tasks: make([]TaskRun, 0, len(corpus.Tasks))}
	for taskIndex, task := range corpus.Tasks {
		request := taskRequest(task, scenario, taskIndex)
		inputHash, err := hashRequest(request)
		if err != nil {
			return Repeat{}, &scenarioError{reason: "request_hash_failed"}
		}
		packet, err := assembler.Assemble(ctx, principal, request)
		if err != nil {
			return Repeat{}, &scenarioError{reason: "packet_assembly_failed"}
		}
		citations, err := citationsForPacket(ctx, store, principal, packet)
		if err != nil {
			return Repeat{}, &scenarioError{reason: "packet_citation_resolution_failed"}
		}
		if scenario != ScenarioDefault && !packetMatchesTask(packet, citations, task) {
			return Repeat{}, &scenarioError{reason: scenarioReason(scenario), invalidated: true}
		}
		repeat.Tasks = append(repeat.Tasks, TaskRun{
			TaskID: task.TaskID, InputHash: inputHash, ExpectedEvidenceIDs: append([]string{}, task.ExpectedEvidenceIDs...), Cold: ColdRun{PlanStatus: "no_acr_context", Claims: []string{}},
			Context: ContextRun{PacketStatus: string(packet.Status), ContextPacketID: packet.ContextPacketID, Citations: citations, EstimatedTokens: packet.Budget.EstimatedTokens},
		})
	}
	return repeat, nil
}

func taskRequest(task evalfixture.Task, scenario Scenario, taskIndex int) contractsv1.ContextPacketRequest {
	asOf := fixtureTime
	request := contractsv1.ContextPacketRequest{
		SchemaVersion: contractsv1.ContextPacketRequestSchema,
		RequestID:     "eval-demo-" + task.TaskID,
		Goal:          task.Goal,
		Repository:    contractsv1.RepositoryRef{Slug: "example-org/widget-service"},
		Scope:         contractsv1.RequestedScope{Branch: task.Scope.Branch, CommitSHA: task.Scope.CommitSHA, AsOf: &asOf},
		Options:       contractsv1.PacketOptions{MaxItems: 10, MaxOutputTokens: 500, MaxSerializedBytes: 8192},
		Client:        contractsv1.ClientInfo{Name: "evaluation-demo", Version: "1.0", SidecarVersion: "0.1.0"},
	}
	if scenario == ScenarioEmptyEvidence {
		emptyAsOf := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		request.Scope.AsOf = &emptyAsOf
	}
	if scenario == ScenarioMismatchedTask && taskIndex == 0 {
		request.Scope = contractsv1.RequestedScope{Branch: "unmatched", TaskRef: "missing-task", AsOf: request.Scope.AsOf}
	}
	return request
}

func hashRequest(request contractsv1.ContextPacketRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
