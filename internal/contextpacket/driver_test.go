package contextpacket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

func TestFixtureDriver_assembles_every_fixed_corpus_task(t *testing.T) {
	corpus, err := evalfixture.VerifyCorpus(repopath.Path(t, "testdata", "evaluation", "v1"))
	if err != nil {
		t.Fatalf("verify corpus: %v", err)
	}
	store, err := contextpacket.NewEvaluationStore(corpus, "org-fixture")
	if err != nil {
		t.Fatalf("create fixture store: %v", err)
	}
	assembler := contextpacket.NewAssembler(store, fixedOptions())
	for _, task := range corpus.Tasks {
		t.Run(task.TaskID, func(t *testing.T) {
			request := fixtureRequest(task.TaskID, task.Scope.Branch, task.Scope.CommitSHA)
			request.Goal = task.Goal
			packet, assembleErr := assembler.Assemble(context.Background(), fixturePrincipal(), request)
			if assembleErr != nil {
				t.Fatalf("assemble task: %v", assembleErr)
			}
			if string(packet.Status) != string(task.ExpectedStatus) {
				t.Fatalf("status = %q, want %q", packet.Status, task.ExpectedStatus)
			}
			if got, want := itemEvidenceIDs(packet), rankedEvidenceIDs(task.TaskID); !equalStrings(got, want) {
				t.Fatalf("evidence = %v, want %v", got, want)
			}
			first, marshalErr := json.Marshal(packet)
			if marshalErr != nil {
				t.Fatalf("marshal first packet: %v", marshalErr)
			}
			secondPacket, assembleErr := fixtureAssembler(t).Assemble(context.Background(), fixturePrincipal(), request)
			if assembleErr != nil {
				t.Fatalf("assemble packet again: %v", assembleErr)
			}
			second, marshalErr := json.Marshal(secondPacket)
			if marshalErr != nil {
				t.Fatalf("marshal second packet: %v", marshalErr)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("driver output is not byte-identical\nfirst=%s\nsecond=%s", first, second)
			}
		})
	}
}

func rankedEvidenceIDs(taskID string) []string {
	switch taskID {
	case "task-001-checkout-flake-exact-commit":
		return []string{"ev-ci-checkout-001", "ev-commit-checkout-001"}
	case "task-002-auth-refactor-branch":
		return []string{"ev-pr-auth-002", "ev-commit-auth-002"}
	case "task-003-unindexed-branch-empty":
		return []string{}
	default:
		return nil
	}
}
