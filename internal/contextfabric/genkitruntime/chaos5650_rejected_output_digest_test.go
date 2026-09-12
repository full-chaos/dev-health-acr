package genkitruntime

import (
	"context"
	"errors"
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

// TestRejectedSynthesisReceiptDigestsEveryPrimaryRejectionPath sweeps the
// PRIMARY leg's rejection paths: the stamp sits before both of them
// (toDomain's own required-field rule and ValidateAgainst's rules), so
// neither may store an empty digest.
//
// Named for the primary leg on purpose. An earlier name claimed to sweep
// EVERY rejection path while every case here builds Config{} with no
// fallback wired, so neither fallback-failure branch was touched -- a test
// asserting completeness it did not have. The fallback legs are swept by
// TestRejectedReceiptDigestDescribesTheLegItReports below; the two together
// are the whole class.
func TestRejectedSynthesisReceiptDigestsEveryPrimaryRejectionPath(t *testing.T) {
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

// fallbackDigestFixture is a fallback ModelRuntime whose own leg FAILED,
// carrying the digest of the draft IT refused -- what a real fallback
// genkitruntime.Runtime returns on its own rejection now that the stamp
// fires on the invalid_output path.
func fallbackDigestFixture(digest string) erroringFallbackRuntime {
	receipt := validReceipt(contextfabric.ModelOperationSynthesize)
	receipt.OutputDigest = digest
	receipt.Outcome = "invalid_output"
	return erroringFallbackRuntime{
		receipt: receipt,
		err: contextfabric.NewSynthesisRejection(
			contextfabric.RejectionReasonDriverInvalid,
			errors.New("fallback draft refused")),
	}
}

// TestRejectedReceiptDigestDescribesTheLegItReports pins the invariant the
// digest has to satisfy to be worth carrying at all: it describes the SAME
// leg as the outcome sitting beside it.
//
// Both "both legs failed" returns set the fallback's outcome and diagnostics
// without merging its receipt, so the receipt carried the PRIMARY's digest
// while reporting the FALLBACK's refusal — stale where the primary had
// rejected a draft of its own, absent where it had failed in transport. A
// digest naming a draft other than the one whose refusal is reported is
// worse than no digest: the field exists to answer "the same bad draft every
// attempt, or a different one each time", and a stale value answers it
// wrongly. Reproduced on both branches before the fix.
func TestRejectedReceiptDigestDescribesTheLegItReports(t *testing.T) {
	const fallbackDigest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

	primaryRejects := func() synthesisOutput {
		o := validSynthesisOutput()
		o.Drivers[0].Category = "not_a_category"
		return o
	}

	cases := []struct {
		name       string
		generator  *generatorStub
		fallback   contextfabric.ModelRuntime
		wantDigest string
		why        string
	}{
		{
			name:       "primary rejected a draft, fallback also rejected one",
			generator:  &generatorStub{synthesis: primaryRejects()},
			fallback:   fallbackDigestFixture(fallbackDigest),
			wantDigest: fallbackDigest,
			why:        "the primary's own rejected-draft digest is stale here; the receipt reports the fallback's refusal",
		},
		{
			name:       "primary failed in transport, fallback rejected a draft",
			generator:  &generatorStub{synthesisErr: errors.New("primary transport failure")},
			fallback:   fallbackDigestFixture(fallbackDigest),
			wantDigest: fallbackDigest,
			why:        "the primary drafted nothing, so without this the rejection has no draft handle at all",
		},
		{
			name:      "primary rejected a draft, fallback failed in transport",
			generator: &generatorStub{synthesis: primaryRejects()},
			// A transport-failed leg drafted nothing, so its own receipt
			// carries an EMPTY digest and a terminal outcome of its own. Built
			// explicitly rather than from validReceipt: that helper is a
			// SUCCESS receipt (Outcome "success", a non-empty digest), and
			// using it here would have described a fallback that both failed
			// and succeeded. The outcome assertion below caught exactly that.
			fallback: erroringFallbackRuntime{
				receipt: func() contextfabric.ModelExecutionReceipt {
					r := validReceipt(contextfabric.ModelOperationSynthesize)
					r.Outcome, r.OutputDigest = "unavailable", ""
					return r
				}(),
				err: errors.New("fallback transport failure"),
			},
			// Empty, and that is the point: the receipt reports a leg that
			// drafted nothing, so an empty digest is the truthful answer and
			// the primary's stale value would be a false one. This is the cell
			// that requires the assignment to be UNCONDITIONAL rather than
			// mergeFallbackReceipt's non-empty guard.
			wantDigest: "",
			why:        "the digest must follow the reported leg even when the primary has a more 'interesting' one",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime := mustRuntime(t, testCase.generator, Config{Fallback: testCase.fallback})
			_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if err == nil {
				t.Fatal("SynthesizeAnswer() = nil error, want both legs to have failed")
			}
			if receipt.Outcome != "invalid_output" && receipt.Outcome != "unavailable" {
				t.Fatalf("receipt.Outcome = %q, want the fallback leg's own terminal outcome", receipt.Outcome)
			}
			if receipt.OutputDigest != testCase.wantDigest {
				t.Fatalf("receipt.OutputDigest = %q, want %q — %s", receipt.OutputDigest, testCase.wantDigest, testCase.why)
			}
		})
	}
}

// TestFallbackSuccessStillCarriesTheFallbacksDigest is the control on the
// other side of the same composition: when the fallback SUCCEEDS the receipt
// must carry its digest too, not the primary's rejected one. This path goes
// through mergeFallbackReceipt and was already correct — asserted so a later
// change to the failing branches cannot quietly break it.
func TestFallbackSuccessStillCarriesTheFallbacksDigest(t *testing.T) {
	bad := validSynthesisOutput()
	bad.Drivers[0].Category = "not_a_category"
	runtime := mustRuntime(t, &generatorStub{synthesis: bad}, Config{
		Fallback: fallbackRuntime{draft: validDraft()},
	})
	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want the fallback to have answered", err)
	}
	want := validReceipt(contextfabric.ModelOperationSynthesize).OutputDigest
	if receipt.OutputDigest != want {
		t.Fatalf("receipt.OutputDigest = %q, want the fallback's %q", receipt.OutputDigest, want)
	}
	if !receipt.FallbackUsed {
		t.Fatal("receipt.FallbackUsed = false, want true")
	}
}

// TestInterpretRejectionCarriesNoStaleDigest is the SIBLING CELL, executed
// rather than argued: a sibling dismissed by reasoning is an unswept
// sibling.
//
// InterpretQuestion has the same "both legs failed" composition, so the
// question is whether it can carry a stale digest the same way. It cannot:
// interpret stamps its digest only on its SUCCESS path, so on a failed leg
// there is nothing stale to carry and the field is empty. Empty is a
// truthful "no draft was judged" rather than a wrong answer, which is why
// this change does not extend to it — and this cell is what makes that a
// measurement instead of a claim.
func TestInterpretRejectionCarriesNoStaleDigest(t *testing.T) {
	fallbackReceipt := validReceipt(contextfabric.ModelOperationInterpret)
	fallbackReceipt.Outcome = "invalid_output"
	runtime := mustRuntime(t, &generatorStub{interpretErr: errors.New("primary transport failure")}, Config{
		Fallback: erroringFallbackRuntime{
			receipt: fallbackReceipt,
			err:     errors.New("fallback also failed"),
		},
	})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err == nil {
		t.Fatal("InterpretQuestion() = nil error, want both legs to have failed")
	}
	if receipt.OutputDigest != "" {
		t.Fatalf("interpret receipt.OutputDigest = %q on a failed leg, want empty — if interpret ever stamps a digest before its fallback, it inherits the synthesis defect and needs the same fix", receipt.OutputDigest)
	}
}

// TestBothLegsFailedReceiptIsOneProjectionOfTheReportedLeg is the CLASS SWEEP
// over the receipt's own fields, executed rather than reasoned.
//
// Fixing only the digest would have left the receipt mixed a different way:
// outcome, diagnostics and digest describing the fallback while identity
// still described the primary — so the digest would name a draft made by a
// model the receipt does not name. Each field below is asserted to the leg it
// must describe, including the three that deliberately do NOT follow the
// reported leg, so "deliberate" is a measurement and not a comment.
func TestBothLegsFailedReceiptIsOneProjectionOfTheReportedLeg(t *testing.T) {
	fallback := validReceipt(contextfabric.ModelOperationSynthesize)
	fallback.Provider, fallback.Model, fallback.ModelVersion = "fallback-provider", "fallback-model", "fallback-v9"
	fallback.OutputDigest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	fallback.Outcome = "invalid_output"
	fallback.Attempts = 7
	fallback.Usage = contextfabric.ModelUsage{InputTokens: 111, OutputTokens: 222, TotalTokens: 333}

	primaryRejected := validSynthesisOutput()
	primaryRejected.Drivers[0].Category = "not_a_category"

	for _, leg := range []struct {
		name      string
		generator *generatorStub
	}{
		{"primary rejected a draft", &generatorStub{synthesis: primaryRejected}},
		{"primary failed in transport", &generatorStub{synthesisErr: errors.New("primary transport failure")}},
	} {
		t.Run(leg.name, func(t *testing.T) {
			runtime := mustRuntime(t, leg.generator, Config{
				Fallback: erroringFallbackRuntime{receipt: fallback, err: contextfabric.NewSynthesisRejection(
					contextfabric.RejectionReasonDriverInvalid, errors.New("fallback refused"))},
			})
			_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if err == nil {
				t.Fatal("want both legs to have failed")
			}

			// Projected: these describe the leg the receipt reports.
			if receipt.Outcome != fallback.Outcome {
				t.Errorf("Outcome = %q, want the reported leg's %q", receipt.Outcome, fallback.Outcome)
			}
			if receipt.OutputDigest != fallback.OutputDigest {
				t.Errorf("OutputDigest = %q, want the reported leg's %q", receipt.OutputDigest, fallback.OutputDigest)
			}
			if receipt.Provider != fallback.Provider {
				t.Errorf("Provider = %q, want the reported leg's %q -- otherwise the digest names a draft made by a model the receipt does not name", receipt.Provider, fallback.Provider)
			}
			if receipt.Model != fallback.Model {
				t.Errorf("Model = %q, want the reported leg's %q", receipt.Model, fallback.Model)
			}
			if receipt.ModelVersion != fallback.ModelVersion {
				t.Errorf("ModelVersion = %q, want the reported leg's %q", receipt.ModelVersion, fallback.ModelVersion)
			}

			// NOT projected, each for its own stated reason. Pinned so a later
			// change to any of them has to be deliberate.
			if receipt.Attempts == fallback.Attempts {
				t.Errorf("Attempts = %d, want the PRIMARY's -- the fallback's own count is reported separately as fallback_attempts_total", receipt.Attempts)
			}
			if receipt.FallbackUsed {
				t.Error("FallbackUsed = true, want false -- it means the fallback ANSWERED, and on this branch it did not")
			}
			if receipt.Usage.TotalTokens == fallback.Usage.TotalTokens+28 {
				t.Errorf("Usage.TotalTokens = %d looks summed; this fix deliberately does not change usage accounting", receipt.Usage.TotalTokens)
			}
			if receipt.Usage.TotalTokens != 28 {
				t.Errorf("Usage.TotalTokens = %d, want the primary's 28 -- the fallback's tokens stay unaccounted on a both-failed call, a pre-existing gap this change does not silently alter", receipt.Usage.TotalTokens)
			}
		})
	}
}
