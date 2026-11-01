package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The I-48 service plan (Ask Dev Phase 1 live suite). Each interpretation here is UNTESTED
// against the real askdev-1 graph: these tests prove the plan through the real engine over
// the fake graph and fact readers only.

const (
	i48PlanPath = "testdata/i48-service-plan.v1.json"
	i48Subject  = "the status of full-chaos/dev-health-web?"
	i48Q1       = "What is " + i48Subject
	i48Q2       = "Hold: what is " + i48Subject
	i48Q3       = "Long: what is " + i48Subject
	i48Q4       = "Again: what is " + i48Subject
	i48Q5       = "Late: what is " + i48Subject
	// realModelTimeout is the 45 s model timeout of the cluster.
	realModelTimeout = 45 * time.Second
)

func readI48(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(i48PlanPath)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// i48Delays returns the REAL delays of the file by question and kind.
func i48Delays(t *testing.T, raw []byte) map[string]ServiceDelay {
	t.Helper()
	var plan Plan
	if err := json.Unmarshal(raw, &plan); err != nil || plan.Service == nil {
		t.Fatalf("decode i48 plan: %v", err)
	}
	out := map[string]ServiceDelay{}
	for _, q := range plan.Service.Questions {
		out[q.Question] = q.DelayMS
	}
	return out
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// The REAL delays meet the rules, so scaling in the engine test cannot hide a wrong plan.
func TestI48PlanRealDelaysMeetTheRules(t *testing.T) {
	d := i48Delays(t, readI48(t))
	if len(d) != 5 {
		t.Fatalf("the plan holds %d questions, want exactly Q1 to Q5", len(d))
	}
	for _, q := range []string{i48Q1, i48Q2, i48Q3, i48Q4, i48Q5} {
		if _, ok := d[q]; !ok {
			t.Fatalf("question %q is missing from the plan", q)
		}
	}
	total := func(x ServiceDelay) time.Duration { return ms(x.Interpret + x.Synthesize + x.Phrase) }
	if total(d[i48Q1]) < 6*time.Second {
		t.Errorf("Q1 total %v, want 6 s or more", total(d[i48Q1]))
	}
	if d[i48Q2].Interpret < 40000 {
		t.Errorf("Q2 interpret hold %d ms, want 40 s or more", d[i48Q2].Interpret)
	}
	if total(d[i48Q3]) <= 60*time.Second {
		t.Errorf("Q3 total %v, want over 60 s", total(d[i48Q3]))
	}
	if total(d[i48Q4]) != 0 {
		t.Errorf("Q4 delay %v, want none", total(d[i48Q4]))
	}
	if ms(d[i48Q5].Synthesize) <= realModelTimeout {
		t.Errorf("Q5 synthesis delay %v, want over the %v model timeout", ms(d[i48Q5].Synthesize), realModelTimeout)
	}
	// No single call may reach the timeout, except Q5's synthesis, which exists to pass it.
	for q, x := range d {
		for kind, v := range map[string]int{"interpret": x.Interpret, "synthesize": x.Synthesize, "phrase": x.Phrase} {
			if q == i48Q5 && kind == "synthesize" {
				continue
			}
			if ms(v) >= realModelTimeout {
				t.Errorf("%q %s delay %v reaches the %v model timeout", q, kind, ms(v), realModelTimeout)
			}
		}
	}
}

// capI48 is a test-only transform on the loaded plan: every delay is capped at maxDelay ms,
// except Q5's synthesis, which is set to q5Synthesis ms (over the test model timeout).
// The REAL delays are proven by TestI48PlanRealDelaysMeetTheRules; this run need not keep
// their ratio.
func capI48(t *testing.T, raw []byte, maxDelay, q5Synthesis int) []byte {
	t.Helper()
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	for _, q := range serviceQuestions(plan) {
		entry := q.(map[string]any)
		delay := entry["delay_ms"].(map[string]any)
		for k, v := range delay {
			delay[k] = min(int(v.(float64)), maxDelay)
		}
		if entry["question"] == i48Q5 {
			delay["synthesize"] = q5Synthesis
		}
	}
	out, _ := json.Marshal(plan)
	return out
}

// Q1 to Q4 reach the ready state with their delays capped at 200 ms (over 1 s of margin
// against the 1.5 s test model timeout); Q5's synthesis is held to timeout + 1 s, so the
// client timeout cancels it.
func TestI48PlanThroughTheRealEngineWithScaledDelays(t *testing.T) {
	const (
		maxDelay    = 200
		testTimeout = 1500 * time.Millisecond
		q5Synthesis = int((testTimeout + time.Second) / time.Millisecond)
	)
	h := startOracleWithTimeout(t, capI48(t, readI48(t), maxDelay, q5Synthesis), testTimeout)
	engine := h.engine(t)
	for _, q := range []string{i48Q1, i48Q2, i48Q3, i48Q4} {
		t.Run(q, func(t *testing.T) {
			start := time.Now()
			result, err := engine.Investigate(context.Background(), principal(), investigationRequest(q))
			if err != nil {
				t.Fatalf("Investigate: %v", err)
			}
			requireReady(t, result)
			want := time.Duration(0)
			d := i48Delays(t, readI48(t))[q]
			want = ms(min(d.Interpret, maxDelay) + min(d.Synthesize, maxDelay))
			if got := time.Since(start); got < want {
				t.Fatalf("took %v, the scaled delays are %v: the delay did not run", got, want)
			}
		})
	}
	t.Run(i48Q5, func(t *testing.T) {
		result, err := engine.Investigate(context.Background(), principal(), investigationRequest(i48Q5))
		if err == nil && (result.Status == contextfabric.InvestigationComplete || result.Status == contextfabric.InvestigationPartial) {
			t.Fatalf("Q5 was served %q, want a model timeout", result.Status)
		}
		byOp := h.calls(t).ByQuestion[i48Q5]
		if byOp[opInterpret].Answered != 1 || byOp[opSynthesize].Started < 1 || byOp[opSynthesize].Answered != 0 {
			t.Fatalf("Q5 calls = %+v, want interpret answered and synthesis started but never answered", byOp)
		}
		deadline := time.Now().Add(3 * time.Second)
		for h.calls(t).ByQuestion[i48Q5][opSynthesize].Cancelled < 1 {
			if time.Now().After(deadline) {
				t.Fatalf("Q5 synthesis was not cancelled by the client timeout: %+v", h.calls(t).ByQuestion[i48Q5])
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	snap := h.calls(t)
	if snap.Failed != 0 {
		t.Fatalf("snapshot = %+v, want no failed call", snap)
	}
}
