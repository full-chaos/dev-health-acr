package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// synthesizeRejectedReceipt runs SynthesizeAnswer against output, asserts
// it was REJECTED, and returns the receipt the call produced.
func synthesizeRejectedReceipt(t *testing.T, output synthesisOutput) contextfabric.ModelExecutionReceipt {
	t.Helper()
	runtime := mustRuntime(t, &generatorStub{synthesis: output}, Config{})
	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil {
		t.Fatal("SynthesizeAnswer() = nil error, want a rejection")
	}
	if receipt.Outcome != "invalid_output" {
		t.Fatalf("receipt.Outcome = %q, want %q", receipt.Outcome, "invalid_output")
	}
	return receipt
}

// TestRejectedSynthesisReceiptCarriesTheOutputDigest is the F2 pin.
//
// The digest stamp used to sit on the SUCCESS path alone, so every
// invalid_output receipt stored an empty output digest and a rejection had
// no handle to the draft that caused it. That is not a cosmetic gap: the
// draft is model content, so it is never logged or persisted anywhere else,
// and the digest is therefore the ONLY thing that can distinguish "the
// model returned the same bad draft on every attempt" -- a deterministic
// server-side defect -- from "it returned a different bad draft each time"
// -- model nondeterminism. Those two have opposite owners and opposite
// fixes, and on the yardstick of record that question was unanswerable:
// all four rejected attempts of the same turn stored output_digest empty.
//
// A non-empty digest alone would not prove the stamp describes the draft,
// so this asserts the two rejections' digests DIFFER, and that neither
// equals the digest of an accepted draft. A constant, a zero value, or a
// digest of the wrong value all fail.
func TestRejectedSynthesisReceiptCarriesTheOutputDigest(t *testing.T) {
	firstOutput := validSynthesisOutput()
	firstOutput.Drivers[0].Category = "not_a_category"
	first := synthesizeRejectedReceipt(t, firstOutput)

	secondOutput := validSynthesisOutput()
	secondOutput.Drivers[0].Standing = contextfabric.DriverWithheld
	secondOutput.Drivers[0].Qualification = ""
	second := synthesizeRejectedReceipt(t, secondOutput)

	for name, digest := range map[string]string{"first": first.OutputDigest, "second": second.OutputDigest} {
		if digest == "" {
			t.Fatalf("%s rejection: receipt.OutputDigest = %q, want the rejected draft's digest", name, digest)
		}
		if len(digest) != 64 {
			t.Fatalf("%s rejection: receipt.OutputDigest = %q (%d chars), want 64", name, digest, len(digest))
		}
	}
	if first.OutputDigest == second.OutputDigest {
		t.Fatalf("both rejections digested to %q -- the stamp does not describe the draft", first.OutputDigest)
	}

	runtime := mustRuntime(t, &generatorStub{synthesis: validSynthesisOutput()}, Config{})
	_, accepted, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() on a valid output error = %v, want nil", err)
	}
	if accepted.Outcome != "success" {
		t.Fatalf("receipt.Outcome = %q, want success", accepted.Outcome)
	}
	if accepted.OutputDigest == first.OutputDigest || accepted.OutputDigest == second.OutputDigest {
		t.Fatalf("an accepted draft digested to the same value as a rejected one (%q)", accepted.OutputDigest)
	}
}

// TestRejectedSynthesisReceiptStaysValid guards the receipt's own
// invariant: ModelExecutionReceipt.Validate bounds OutputDigest to exactly
// 64 characters when non-empty, and the durable sink writes receipts that
// pass it. Stamping a digest on a path that never carried one must not turn
// a storable receipt into an unstorable one.
func TestRejectedSynthesisReceiptStaysValid(t *testing.T) {
	output := validSynthesisOutput()
	output.Drivers[0].Title = ""
	receipt := synthesizeRejectedReceipt(t, output)
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt.Validate() = %v, want nil", err)
	}
}

// TestRejectedSynthesisReceiptDigestsEveryRejectionPath sweeps the CLASS:
// the stamp sits before both rejection legs (toDomain's own required-field
// rule and ValidateAgainst's rules), so neither may store an empty digest.
// A fix applied to one leg and not its sibling is the defect class this
// repository keeps re-finding.
func TestRejectedSynthesisReceiptDigestsEveryRejectionPath(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(synthesisOutput) synthesisOutput
	}{
		{"toDomain required-field rejection", func(o synthesisOutput) synthesisOutput {
			o.DeterministicAnswer = ""
			return o
		}},
		{"ValidateAgainst driver rejection", func(o synthesisOutput) synthesisOutput {
			o.Drivers[0].Category = "not_a_category"
			return o
		}},
		{"ValidateAgainst finding rejection", func(o synthesisOutput) synthesisOutput {
			o.RemainingWork = []contextfabric.Finding{{
				FindingID: "finding_12345678", Kind: "not_a_category", Summary: "Summary",
				EvidenceRefIDs: []string{"evidence_release_1234"},
			}}
			return o
		}},
		{"ValidateAgainst top-level evidence rejection", func(o synthesisOutput) synthesisOutput {
			o.EvidenceRefIDs = []string{"evidence_not_in_the_allowed_set"}
			return o
		}},
	}
	seen := map[string]string{}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			receipt := synthesizeRejectedReceipt(t, testCase.mutate(validSynthesisOutput()))
			if receipt.OutputDigest == "" {
				t.Fatalf("receipt.OutputDigest is empty on the %s path", testCase.name)
			}
			if other, clash := seen[receipt.OutputDigest]; clash {
				t.Fatalf("%s digested to the same value as %s -- the stamp does not describe the draft", testCase.name, other)
			}
			seen[receipt.OutputDigest] = testCase.name
		})
	}
}

// TestRejectedSynthesisReceiptNamesTheClauseToo pins the two halves of this
// change TOGETHER at the runtime seam: a rejection must carry both the
// clause that refused and a digest of the draft that was refused. Either
// alone leaves the diagnosis short -- the clause says which rule, the
// digest says whether it was the same draft each time.
func TestRejectedSynthesisReceiptNamesTheClauseToo(t *testing.T) {
	output := validSynthesisOutput()
	output.Drivers[0].Category = "not_a_category"
	runtime := mustRuntime(t, &generatorStub{synthesis: output}, Config{})
	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err == nil {
		t.Fatal("SynthesizeAnswer() = nil error, want a rejection")
	}
	clause, ok := contextfabric.SynthesisRejectionClauseOf(err)
	if !ok {
		t.Fatal("SynthesisRejectionClauseOf() ok = false, want the rejection to name its clause")
	}
	if clause != contractsv1.ContextFabricClauseDriverCategory {
		t.Fatalf("clause = %q, want %q", clause, contractsv1.ContextFabricClauseDriverCategory)
	}
	if receipt.OutputDigest == "" {
		t.Fatal("receipt.OutputDigest is empty -- the clause names the rule but nothing identifies the draft")
	}
}
