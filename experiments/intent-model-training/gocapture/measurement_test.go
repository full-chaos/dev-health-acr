package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Measurement correctness (coordinator scope, 2026-09-30):
// 1. every attempt's wire model, seed, messages, response format and
//    decoding equal the expected descriptor, or the attempt is refused unsent;
// 2. the complete 3-per-row grid, each replicate from sample 0, no
//    selective redraws;
// 3. the 720-attempt cap is never exceeded.

func mutateBody(t *testing.T, body []byte, edit func(map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Item 1: a wrong model and a wrong seed are each refused before sending.
func TestWireProofRefusesWrongModelAndSeed(t *testing.T) {
	control := newTransportFixture(t)
	if err := control.send(control.body, http.MethodPost, control.url()); err != nil {
		t.Fatalf("the production request was refused: %v", err)
	}
	mustEqual(t, "control sent", control.fx.server.count(), 1)

	cases := map[string]func(tf *transportFixture) func(map[string]any){
		"wrong model": func(*transportFixture) func(map[string]any) {
			return func(m map[string]any) { m["model"] = "gpt-5.6-other" }
		},
		"wrong seed": func(tf *transportFixture) func(map[string]any) {
			return func(m map[string]any) {
				m["seed"] = json.Number(strconv.FormatInt(interpretSeed(tf.inv.question, 0)+1, 10))
			}
		},
		"missing seed": func(*transportFixture) func(map[string]any) { return func(m map[string]any) { delete(m, "seed") } },
		"temperature added": func(*transportFixture) func(map[string]any) {
			return func(m map[string]any) { m["temperature"] = json.Number("0") }
		},
		"response format": func(*transportFixture) func(map[string]any) {
			return func(m map[string]any) { m["response_format"] = map[string]any{"type": "text"} }
		},
		"system message": func(*transportFixture) func(map[string]any) {
			return func(m map[string]any) {
				m["messages"].([]any)[0].(map[string]any)["content"] = "another system prompt"
			}
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			tf := newTransportFixture(t)
			wrong := mutateBody(t, tf.body, edit(tf))
			err := tf.send(wrong, http.MethodPost, tf.url())
			if err == nil {
				t.Fatal("the attempt was sent")
			}
			mustEqual(t, "latch", tf.inv.latch, outcomeContractFailure)
			mustEqual(t, "server requests", tf.fx.server.count(), 0)
			if len(tf.inv.attempts) != 1 || tf.inv.attempts[0].Sent ||
				tf.inv.attempts[0].ObservedDescriptorSHA256 == tf.inv.attempts[0].ExpectedDescriptorSHA256 {
				t.Fatalf("the refused attempt must be recorded unsent, with differing descriptors: %+v", tf.inv.attempts)
			}
		})
	}
}

// Item 2: exactly replicates 0, 1, 2 per sealed row; each replicate starts
// at sample 0 (seed d=0 on its first attempt); each pair is reserved once.
func TestGridEachReplicateFromSampleZero(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	questions := map[string]string{}
	for id, raw := range fixtureRequests {
		var q struct {
			Question string `json:"question"`
		}
		_ = json.Unmarshal([]byte(raw), &q)
		questions[id] = q.Question
	}
	seen := map[string]int{}
	for _, a := range f.artifacts("run1") {
		seen[a.RowID+"#"+strconv.Itoa(a.Replicate)]++
		first := a.Attempts[0]
		seed, ok := seedOf(first.ObservedDescriptor)
		if !ok || first.Draw != 0 || seed != strconv.FormatInt(interpretSeed(questions[a.RowID], 0), 10) {
			t.Fatalf("%s#%d did not start at sample 0 (draw %d, seed %s)", a.RowID, a.Replicate, first.Draw, seed)
		}
	}
	for id := range fixtureRequests {
		for r := 0; r < 3; r++ {
			mustEqual(t, id+"#"+strconv.Itoa(r), seen[id+"#"+strconv.Itoa(r)], 1)
		}
	}
	mustEqual(t, "pairs", len(seen), 6)
	// A second run id on the same seal cannot add or redraw any pair.
	if _, err := f.run(f.config("run2")); err == nil {
		t.Fatal("a second run id recaptured the seal")
	}
	mustEqual(t, "server", f.server.count(), 6)
}

// Item 3: 720 across runs and sets; attempt 721 is refused before sending,
// and a reload keeps the spending.
func TestCap720NeverExceeded(t *testing.T) {
	f := newFixture(t)
	l := f.ledger()
	cap := 720
	if err := l.append(ledgerRecord{Kind: "run_started", RunID: "run1", Set: "H1", SealDigest: f.sealDigest, RunConfigSHA256: "c1", RunCapHTTPAttempts: &cap}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 360; i++ {
		if _, err := l.reserveHTTP(pairKey{f.sealDigest, "row-a", 0}, "run1", 0, "x"); err != nil {
			t.Fatal(err)
		}
	}
	l.close()
	l2, err := f.tryLedger()
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.append(ledgerRecord{Kind: "run_started", RunID: "run2", Set: "H2", SealDigest: "seal-h2", RunConfigSHA256: "c2", RunCapHTTPAttempts: &cap}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 360; i++ {
		if _, err := l2.reserveHTTP(pairKey{"seal-h2", "row-a", 0}, "run2", 0, "x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l2.reserveHTTP(pairKey{"seal-h2", "row-a", 0}, "run2", 0, "x"); !errors.Is(err, errBudget) {
		t.Fatalf("attempt 721 was not refused: %v", err)
	}
	l2.close()
	l3, err := f.tryLedger()
	if err != nil {
		t.Fatal(err)
	}
	defer l3.close()
	mustEqual(t, "spent after reload", l3.spent, 720)
	if _, err := l3.reserveHTTP(pairKey{"seal-h2", "row-a", 0}, "run2", 0, "x"); !errors.Is(err, errBudget) {
		t.Fatal("the cap reset on reload")
	}
}

// The ledger itself refuses records that would break the cap or the
// no-redraw rule, independently of the runner.
func TestLedgerRefusesOutOfRuleRecords(t *testing.T) {
	f := newFixture(t)
	l := f.ledger()
	cap, rep, seq, idx := 720, 0, 5, 2
	_ = l.append(ledgerRecord{Kind: "run_started", RunID: "run1", Set: "H1", SealDigest: f.sealDigest, RunConfigSHA256: "c", RunCapHTTPAttempts: &cap})
	if err := l.append(ledgerRecord{Kind: "http_reserved", RunID: "run1", SealDigest: f.sealDigest, RowID: "row-a", Replicate: &rep, AttemptSeq: &seq}); err == nil {
		t.Fatal("an out-of-sequence reservation was accepted")
	}
	if err := l.append(ledgerRecord{Kind: "pair_reserved", RunID: "run1", SealDigest: f.sealDigest, RowID: "row-a", Replicate: &rep}); err != nil {
		t.Fatal(err)
	}
	if err := l.append(ledgerRecord{Kind: "pair_reserved", RunID: "run1", SealDigest: f.sealDigest, RowID: "row-a", Replicate: &rep}); err == nil {
		t.Fatal("a pair was reserved twice")
	}
	if err := l.append(ledgerRecord{Kind: "pair_uncertain", RunID: "run1", SealDigest: f.sealDigest, RowID: "row-a", Replicate: &rep}); err != nil {
		t.Fatal(err)
	}
	if err := l.append(ledgerRecord{Kind: "recovery_authorized", RunID: "run1", SealDigest: f.sealDigest, RowID: "row-a", Replicate: &rep, RecoveryIndex: &idx, AuthorizedBy: "human:chris", Reason: "r"}); err == nil {
		t.Fatal("recovery index 2 before 1 was accepted")
	}
	one := 1
	if err := l.append(ledgerRecord{Kind: "recovery_authorized", RunID: "run1", SealDigest: f.sealDigest, RowID: "row-a", Replicate: &rep, RecoveryIndex: &one, AuthorizedBy: "human:chris", Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := l.append(ledgerRecord{Kind: "recovery_reserved", RunID: "run1", SealDigest: f.sealDigest, RowID: "row-a", Replicate: &rep, RecoveryIndex: &one}); err != nil {
		t.Fatal(err)
	}
	if err := l.append(ledgerRecord{Kind: "recovery_authorized", RunID: "run1", SealDigest: f.sealDigest, RowID: "row-a", Replicate: &rep, RecoveryIndex: &idx, AuthorizedBy: "human:chris", Reason: "r"}); err == nil {
		t.Fatal("a new recovery was authorized while recovery 1 is unreconciled")
	}
}

// A deadline caused by the harness's own time is not the incumbent's
// failure: the pair is harness_interference and the capture is incomplete.
func TestHarnessInterferenceIsIncomplete(t *testing.T) {
	f := newFixture(t)
	f.profileMap["acr_request_timeout"] = "1s"
	f.profileMap["model_max_attempts"] = 1
	f.writeProfile()
	f.server.fallback = scripted{status: 200, body: completion(okAnswer), delay: 1500 * time.Millisecond}
	cfg := f.config("run1")
	cfg.ledgerHook = func(l *approvalLedger) {
		l.failAppend = func() error { time.Sleep(80 * time.Millisecond); return nil }
	}
	res, err := f.run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeInterference)
	var report captureReport
	data, _ := os.ReadFile(filepath.Join(f.runDir("run1"), "report.json"))
	_ = json.Unmarshal(data, &report)
	if report.Complete {
		t.Fatal("a harness-caused deadline was reported as a complete capture")
	}
}
