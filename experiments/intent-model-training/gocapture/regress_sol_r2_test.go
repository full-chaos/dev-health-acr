//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gpt-6.1-sol code review r2 High (private/reviews/sol-capture-r2/REPORT.md):
// harness time inside production's deadlines suppressed a production redraw
// and the resulting invalid_output was scored as an incumbent failure. Any
// non-ok outcome of an invocation with material harness overhead must be
// harness_interference: not scored, capture incomplete.
func TestSolR2SlowLedgerSuppressedRedrawIsInterference(t *testing.T) {
	var controlDescriptor string
	for _, slow := range []bool{false, true} {
		name := "control"
		if slow {
			name = "slow_ledger"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.profileMap["acr_request_timeout"] = "1200ms"
			f.profileMap["model_timeout"] = "1s"
			f.profileMap["synthesis_max_resynthesis_attempts"] = 3
			f.writeProfile()
			f.server.push(scripted{status: 200, body: completion(rejectedAnswer)})
			cfg := f.config("run1")
			if slow {
				cfg.ledgerHook = func(l *approvalLedger) {
					n := 0
					l.failAppend = func() error {
						n++
						if n == 3 { // run_started, pair_reserved, then the first http_reserved
							time.Sleep(300 * time.Millisecond)
						}
						return nil
					}
				}
			}
			res, err := f.run(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var a artifact
			for _, candidate := range f.artifacts("run1") {
				if candidate.RowID == "row-a" && candidate.Replicate == 0 {
					a = candidate
				}
			}
			var report captureReport
			data, err := os.ReadFile(filepath.Join(f.runDir("run1"), "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			first := a.Attempts[0]
			if first.ObservedDescriptorSHA256 != first.ExpectedDescriptorSHA256 {
				t.Fatal("descriptor mismatch")
			}
			if !slow {
				controlDescriptor = first.ObservedDescriptorSHA256
				if a.Outcome != "ok" || !a.ProductionReturned || len(a.Attempts) != 2 || !report.Complete {
					t.Fatalf("control must redraw and succeed: outcome=%s attempts=%d complete=%v", a.Outcome, len(a.Attempts), report.Complete)
				}
				return
			}
			if controlDescriptor != first.ObservedDescriptorSHA256 {
				t.Fatal("control and treatment sent different requests")
			}
			if a.Outcome != outcomeInterference || !a.RunnerOwned || a.ProductionReturned {
				t.Fatalf("slow ledger: outcome=%s runner_owned=%v (want harness_interference, not scored)", a.Outcome, a.RunnerOwned)
			}
			if report.Complete {
				t.Fatal("slow ledger: report.complete must be false")
			}
			if a.HarnessOverheadMS < 300 || report.MaxHarnessOverheadMS < 300 || report.InterferenceThresholdMS != 50 {
				t.Fatalf("overhead must be recorded: artifact=%d report max=%d threshold=%d", a.HarnessOverheadMS, report.MaxHarnessOverheadMS, report.InterferenceThresholdMS)
			}
			mustEqual(t, "stop", res.StopReason, outcomeInterference)
		})
	}
}

// An ok outcome reached only after a timed-out attempt, with material
// harness overhead, is not the incumbent's served answer either: the
// overhead may have caused the retry.
func TestSolR2RetryAfterTimeoutWithOverheadIsInterference(t *testing.T) {
	f := newFixture(t)
	f.profileMap["acr_request_timeout"] = "5s"
	f.profileMap["model_timeout"] = "1s"
	f.profileMap["model_max_attempts"] = 2
	f.profileMap["model_max_transport_retries"] = 0
	f.writeProfile()
	f.server.push(scripted{status: 200, body: completion(okAnswer), delay: 1200 * time.Millisecond})
	cfg := f.config("run1")
	cfg.ledgerHook = func(l *approvalLedger) {
		n := 0
		l.failAppend = func() error {
			n++
			if n == 3 {
				time.Sleep(100 * time.Millisecond)
			}
			return nil
		}
	}
	res, err := f.run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var a artifact
	for _, c := range f.artifacts("run1") {
		if c.RowID == "row-a" && c.Replicate == 0 {
			a = c
		}
	}
	if len(a.Attempts) != 2 || a.Attempts[0].TransportError == "" {
		t.Fatalf("fixture must time out once, then answer: %+v", a.Attempts)
	}
	if a.Outcome != outcomeInterference || !a.RunnerOwned || a.RawText != nil {
		t.Fatalf("outcome=%s runner_owned=%v overhead=%d", a.Outcome, a.RunnerOwned, a.HarnessOverheadMS)
	}
	mustEqual(t, "stop", res.StopReason, outcomeInterference)
}
