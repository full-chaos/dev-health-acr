//go:build unix

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Permanent regressions for gpt-6.1-sol code review r1
// (private/reviews/sol-capture-r1/REPORT.md). Each asserts the FIXED
// behaviour of a finding sol reproduced on the pre-fix code.

// H1: a JSON-escaped (\uXXXX) credential echo is caught after decoding;
// nothing content-derived is persisted, and the run stops.
func TestSolR1UnicodeCredentialEcho(t *testing.T) {
	f := newFixture(t)
	answer := strings.Replace(okAnswer, "recorder", testKey, 1)
	body := completion(answer)
	var escaped strings.Builder
	for _, r := range testKey {
		escaped.WriteString(`\u` + fmt.Sprintf("%04x", r))
	}
	body = strings.ReplaceAll(body, testKey, escaped.String())
	if strings.Contains(body, testKey) {
		t.Fatal("fixture was not escaped")
	}
	f.server.fallback = scripted{status: 200, body: body}
	res, err := f.run(f.config("run1"))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeCredentialEcho)
	mustEqual(t, "server", f.server.count(), 1)
	assertNoKeyAnywhere(t, f.root)
	assertNoKeyDecoded(t, f.root)
}

// H1 (class): percent-encoded and base64-embedded echoes are caught too.
func TestSolR1EncodedCredentialForms(t *testing.T) {
	for name, form := range map[string]string{
		"percent":        strings.ReplaceAll(testKey, "-", "%2D"),
		"base64 offset1": base64.StdEncoding.EncodeToString([]byte("x" + testKey + "y")),
		"base64 url":     base64.RawURLEncoding.EncodeToString([]byte("ab" + testKey)),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.server.fallback = scripted{status: 200, body: completion(`{"note":"` + form + `"}`)}
			res, err := f.run(f.config("run1"))
			if err != nil {
				t.Fatal(err)
			}
			mustEqual(t, "stop", res.StopReason, outcomeCredentialEcho)
			assertNoKeyDecoded(t, f.root)
		})
	}
}

// assertNoKeyDecoded decodes every string field (JSON-unescaped, base64,
// percent) in every persisted file and fails on the key.
func assertNoKeyDecoded(t *testing.T, root string) {
	t.Helper()
	s := newCredScanner(testKey)
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(path, "profile.json") {
			return nil
		}
		data, _ := os.ReadFile(path)
		for _, line := range splitJSONLines(data) {
			if s.scan(line) {
				t.Fatalf("key reachable in %s", path)
			}
		}
		return nil
	})
}

// H2: an interrupted recovery is never resent under the same index; a
// published recovery artifact is completed, not re-captured.
func TestSolR1RecoveryCrashNoReplay(t *testing.T) {
	for _, point := range []string{"after_send", "artifact_after_link", "before_pair_terminal"} {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t)
			f.profileMap["synthesis_max_resynthesis_attempts"] = 1
			f.writeProfile()
			initial := f.config("run1")
			initial.fault = func(p string) error {
				if p == "after_pair_reserved" {
					return errors.New("synthetic initial crash")
				}
				return nil
			}
			if _, err := f.run(initial); err == nil {
				t.Fatal("initial crash did not fire")
			}
			resume := f.config("run1")
			resume.Resume = true
			if _, err := f.run(resume); err != nil {
				t.Fatal(err)
			}
			rec := f.config("run1")
			rec.Resume, rec.RecoverRow, rec.RecoverReplicate, rec.RecoverIndex = true, "row-a", 0, 1
			rec.RecoverBy, rec.RecoverReason = "human:chris", "recover original crash"
			rec.fault = func(p string) error {
				if p == point {
					return errors.New("synthetic recovery crash")
				}
				return nil
			}
			f.server.push(scripted{status: 200, body: completion(rejectedAnswer)})
			if _, err := f.run(rec); err == nil {
				t.Fatal("recovery crash did not fire")
			}
			crashed := f.server.count()
			rec.fault = nil
			_, retryErr := f.run(rec)
			mustEqual(t, "new requests on retry", f.server.count()-crashed, 0)
			l := f.ledger()
			st := l.pairs[pairKey{f.sealDigest, "row-a", 0}]
			switch point {
			case "after_send":
				if retryErr == nil || !strings.Contains(retryErr.Error(), "used or out of order") || !st.Recoveries[1].Uncertain {
					t.Fatalf("an unpublished recovery must become uncertain and its index unusable: %v", retryErr)
				}
			default:
				if retryErr != nil || st.recoveryTerminal() == nil || st.recoveryTerminal().Outcome != "invalid_output" {
					t.Fatalf("a published recovery must be completed as captured: %v", retryErr)
				}
			}
		})
	}
}

// H3: a terminal stop survives restart; resume sends nothing.
func TestSolR1ResumeKeepsTerminalStop(t *testing.T) {
	for _, mode := range []string{"credential_echo", "two_unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			if mode == "credential_echo" {
				f.server.push(scripted{status: 200, body: completion(testKey)})
			} else {
				f.server.push(scripted{status: 503, body: `{}`, headers: retryFast}, scripted{status: 503, body: `{}`, headers: retryFast})
			}
			first, err := f.run(f.config("run1"))
			if err != nil {
				t.Fatal(err)
			}
			before := f.server.count()
			resume := f.config("run1")
			resume.Resume = true
			second, err := f.run(resume)
			if err != nil {
				t.Fatal(err)
			}
			mustEqual(t, "new requests", f.server.count()-before, 0)
			if second.StopReason != first.StopReason || !second.Incomplete {
				t.Fatalf("resume must report the durable stop: %+v vs %+v", second, first)
			}
		})
	}
}

// H3 (crash variant): a stop-class terminal completed from a published
// artifact during resume is honoured before any send.
func TestSolR1ReconciledTerminalStops(t *testing.T) {
	f := newFixture(t)
	f.server.push(scripted{status: 200, body: completion(testKey)})
	cfg := f.config("run1")
	cfg.fault = func(p string) error {
		if p == "before_pair_terminal" {
			return errors.New("crash after publishing a credential-echo artifact")
		}
		return nil
	}
	if _, err := f.run(cfg); err == nil {
		t.Fatal("crash not surfaced")
	}
	before := f.server.count()
	resume := f.config("run1")
	resume.Resume = true
	res, err := f.run(resume)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeCredentialEcho)
	mustEqual(t, "new requests", f.server.count()-before, 0)
}

// H4: pacing happens between invocations and never consumes a deadline.
func TestSolR1PacingDoesNotConsumeDeadline(t *testing.T) {
	f := newFixture(t)
	f.profileMap["acr_request_timeout"] = "200ms"
	f.profileMap["model_timeout"] = "1s"
	f.profileMap["synthesis_max_resynthesis_attempts"] = 1
	f.writeProfile()
	cfg := f.config("run1")
	cfg.MinInterval = 300 * time.Millisecond
	res, err := f.run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range f.artifacts("run1") {
		if a.Outcome != "ok" {
			t.Fatalf("pacing produced a %s outcome", a.Outcome)
		}
	}
	mustEqual(t, "stop", res.StopReason, "")
	mustEqual(t, "server", f.server.count(), 6)
}

// M5: a session closed before exposure is refused under the lock; no send.
func TestSolR1SessionClosedBeforeExposure(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("run1")
	cfg.fault = func(p string) error {
		if p == "runjson_after_link" {
			f.appendOpenings(map[string]any{"kind": "session_closed", "session_id": f.sessionID, "closed_by": "human:chris", "at": "2026-09-30T01:00:00Z"})
		}
		return nil
	}
	_, err := f.run(cfg)
	if err == nil || !strings.Contains(err.Error(), "session is closed") {
		t.Fatalf("closure before exposure: %v", err)
	}
	mustEqual(t, "server", f.server.count(), 0)
}

// M5: a session closed mid-run stops admission of the next invocation.
func TestSolR1SessionClosedMidRun(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("run1")
	closedAfter := 0
	cfg.fault = func(p string) error {
		if p == "before_pair_terminal" {
			closedAfter++
			if closedAfter == 2 {
				f.appendOpenings(map[string]any{"kind": "session_closed", "session_id": f.sessionID, "closed_by": "evaluate", "at": "2026-09-30T01:00:00Z"})
			}
		}
		return nil
	}
	if _, err := f.run(cfg); err == nil {
		t.Fatal("a closed session admitted another invocation")
	}
	mustEqual(t, "server", f.server.count(), 2)
}

// M6: symlinks anywhere below the data root are refused; derive ids are
// validated and must be ledger-bound.
func TestSolR1PathConfinement(t *testing.T) {
	f := newFixture(t)
	escape := filepath.Join(f.root, "escape")
	if err := os.Mkdir(escape, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, filepath.Join(f.paths.Root, "heldout", "H1", "captures")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(f.config("run1")); err == nil {
		t.Fatal("a symlinked captures directory was accepted")
	}
	mustEqual(t, "server", f.server.count(), 0)
	entries, _ := os.ReadDir(escape)
	mustEqual(t, "files written through the symlink", len(entries), 0)

	g := newFixture(t)
	if _, err := g.run(g.config("run1")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../../../../escape/run1", "..", "run1/../run1", ".hidden", "unknown-run"} {
		if err := cmdDerive([]string{"--data-root", g.paths.Root, "--session", g.sessionID, "--approval-id", "appr-test", "--run-id", id}); err == nil {
			t.Fatalf("derive accepted run id %q", id)
		}
	}
	if err := cmdDerive([]string{"--data-root", g.paths.Root, "--session", g.sessionID, "--approval-id", "appr-test", "--run-id", "run1"}); err != nil {
		t.Fatalf("derive refused the real run: %v", err)
	}
}

// M7: the pacing boundary is durable across a restart.
func TestSolR1IntervalAcrossRestart(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("run1")
	cfg.MinInterval = time.Second
	reservations := 0
	cfg.fault = func(p string) error {
		if p == "after_pair_reserved" {
			reservations++
			if reservations == 2 {
				return errors.New("crash before the second pair's send")
			}
		}
		return nil
	}
	if _, err := f.run(cfg); err == nil {
		t.Fatal("initial crash did not fire")
	}
	resume := f.config("run1")
	resume.Resume, resume.MinInterval = true, cfg.MinInterval
	resume.fault = func(p string) error {
		if p == "after_send" {
			return errors.New("stop right after the resumed send")
		}
		return nil
	}
	if _, err := f.run(resume); err == nil {
		t.Fatal("resumed crash did not fire")
	}
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	if len(f.server.times) != 2 {
		t.Fatalf("server count=%d", len(f.server.times))
	}
	if gap := f.server.times[1].Sub(f.server.times[0]); gap < 950*time.Millisecond {
		t.Fatalf("the interval reset across restart: gap=%s", gap)
	}
}

// M8: out-of-range tuning is refused before any write or exposure.
func TestSolR1MalformedRangeRefusedBeforeWrites(t *testing.T) {
	for name, edit := range map[string]func(m map[string]any){
		"model_timeout 25ms": func(m map[string]any) { m["model_timeout"] = "25ms" },
		"model_timeout 3m":   func(m map[string]any) { m["model_timeout"] = "3m" },
		"retries 6":          func(m map[string]any) { m["model_max_transport_retries"] = 6 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			edit(f.profileMap)
			f.writeProfile()
			if _, err := f.run(f.config("run1")); err == nil {
				t.Fatal("accepted")
			}
			if _, err := os.Stat(f.runDir("run1")); err == nil {
				t.Fatal("a run directory was written")
			}
			ledger, _ := os.ReadFile(f.paths.Ledger)
			openings, _ := os.ReadFile(f.paths.Openings)
			if strings.Contains(string(ledger), `"run_started"`) || strings.Contains(string(openings), `"exposure"`) {
				t.Fatal("a ledger or exposure write happened before the refusal")
			}
			mustEqual(t, "server", f.server.count(), 0)
		})
	}
}

// M9: the profile's attempt limit reaches production (real runtime retries).
//
// The timeouts set the interference threshold (the smaller timeout / 20).
// With a 1s model timeout it was 50 ms, and the harness time of the three
// attempts of one invocation measured 34 to 55 ms under the race detector
// (median 40, 35 invocations), so the pair turned runner-owned on a slow
// run. A 5s model timeout puts the threshold at 250 ms. The mechanism is
// unchanged: every attempt still ends on the model timeout.
func TestSolR1RuntimeRetriesOnModelTimeout(t *testing.T) {
	f := newFixture(t)
	f.profileMap["acr_request_timeout"] = "20s"
	f.profileMap["model_timeout"] = "5s"
	f.profileMap["model_max_attempts"] = 3
	f.profileMap["model_max_transport_retries"] = 0
	f.profileMap["synthesis_max_resynthesis_attempts"] = 1
	f.writeProfile()
	f.server.fallback = scripted{status: 200, body: completion(okAnswer), delay: 5500 * time.Millisecond}
	cfg := f.config("run1")
	if _, err := f.run(cfg); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "reserved = server", f.ledger().spent, f.server.count())
	for _, a := range f.artifacts("run1") {
		mustEqual(t, "HTTP attempts per invocation", len(a.Attempts), 3)
		mustEqual(t, "runtime receipt attempts", a.Receipt.Attempts, 3)
		if a.ProductionReturned || a.RawText != nil || a.RunnerOwned {
			t.Fatalf("a timeout was promoted or treated as runner-owned: outcome %s, harness overhead %d ms, interference threshold 250 ms", a.Outcome, a.HarnessOverheadMS)
		}
	}
}

// M9: SDK retries are charged and real; the terminal proof holds.
func TestSolR1SDKRetriesOn500(t *testing.T) {
	f := newFixture(t)
	f.profileMap["model_max_attempts"] = 2
	f.profileMap["model_max_transport_retries"] = 2
	f.writeProfile()
	f.server.fallback = scripted{status: 500, body: `{"error":{"message":"down"}}`, headers: retryFast}
	res, err := f.run(f.config("run1"))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, "consecutive_model_unavailable")
	mustEqual(t, "server", f.server.count(), 6)
	mustEqual(t, "reserved", f.ledger().spent, 6)
	for _, a := range f.artifacts("run1") {
		mustEqual(t, "HTTP attempts per invocation", len(a.Attempts), 3)
		if a.ProductionReturned || a.RawText != nil || a.RunnerOwned {
			t.Fatal("failed retries promoted or treated as runner-owned")
		}
	}
}

// M9: a failed HTTP reservation write latches write_failure; nothing is sent.
func TestSolR1ReservationWriteFailureLatches(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("run1")
	n := 0
	cfg.ledgerHook = func(l *approvalLedger) {
		l.failAppend = func() error {
			n++
			if n == 3 { // run_started, pair_reserved, then the first http_reserved
				return os.ErrPermission
			}
			return nil
		}
	}
	res, err := f.run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeWriteFailure)
	mustEqual(t, "server", f.server.count(), 0)
	for _, a := range f.artifacts("run1") {
		if !a.RunnerOwned || a.Outcome != outcomeWriteFailure {
			t.Fatalf("outcome %s", a.Outcome)
		}
	}
}

// M9: a deadline outcome with a genuine provider delay is a scored-zero
// model failure, not runner-owned (the harness overhead is small).
//
// With a 300ms request timeout the interference threshold was 15 ms, and
// the harness time of one invocation measured 10 to 16 ms under the race
// detector (median 12, 31 invocations). A 2s request timeout puts the
// threshold at 100 ms. The provider delay still exceeds the deadline.
func TestSolR1DeadlineTerminalProof(t *testing.T) {
	f := newFixture(t)
	f.profileMap["acr_request_timeout"] = "2s"
	f.writeProfile()
	f.server.fallback = scripted{status: 200, body: completion(okAnswer), delay: 2600 * time.Millisecond}
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.artifacts("run1") {
		if a.Outcome != "deadline" {
			t.Fatalf("outcome = %s, want deadline (harness overhead %d ms, interference threshold 100 ms)", a.Outcome, a.HarnessOverheadMS)
		}
		if a.ProductionReturned || a.RawText != nil || a.RunnerOwned {
			t.Fatal("deadline promoted or treated as runner-owned")
		}
	}
}

// M10: canonical JSON equals Python byte for byte, DEL and controls
// included; invalid UTF-8 is refused. Python is executed, not assumed.
func TestSolR1CanonicalJSONPythonParity(t *testing.T) {
	inputs := []string{"\x7f", "a\x00b\x1fc", "café \U0001F600", "<&>\"\\/", "tab\tnl\ncr\rbs\bff\f", "  ", "~ !"}
	var goOut []string
	for _, in := range inputs {
		out, err := cj(map[string]any{"s": in, "k\x7f": 1})
		if err != nil {
			t.Fatal(err)
		}
		goOut = append(goOut, string(out))
	}
	payload, _ := json.Marshal(inputs)
	script := `import json,sys
for s in json.loads(sys.stdin.read()):
    print(json.dumps({"s": s, "k\x7f": 1}, sort_keys=True, separators=(",", ":"), ensure_ascii=True))`
	cmd := exec.Command("python3", "-c", script)
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("python3 unavailable: a missing oracle fails: %v", err)
	}
	pyOut := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i := range inputs {
		if pyOut[i] != goOut[i] {
			t.Fatalf("input %q: go %s python %s", inputs[i], goOut[i], pyOut[i])
		}
	}
	if _, err := cjRaw([]byte{'{', '"', 's', '"', ':', '"', 0xff, '"', '}'}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

// M10: an artifact carrying DEL in production-returned text verifies in
// Python (self_sha256 recomputed from the canonical bytes).
func TestSolR1ArtifactSelfHashPythonParity(t *testing.T) {
	f := newFixture(t)
	f.server.push(scripted{status: 200, body: completion(strings.Replace(okAnswer, "recorder", "recor\x7fder", 1))})
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.runDir("run1"), "raw", artifactName("row-a", 0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := verifyArtifactBytes(data)
	if err != nil || a.RawText == nil || !strings.Contains(*a.RawText, "\x7f") {
		t.Fatalf("fixture did not produce returned DEL text: %v", err)
	}
	script := `import json,sys,hashlib
raw=sys.stdin.buffer.read()
a=json.loads(raw)
claimed=a.pop("self_sha256")
body=json.dumps(a,sort_keys=True,separators=(",",":"),ensure_ascii=True).encode()
a["self_sha256"]=hashlib.sha256(body).hexdigest()
full=json.dumps(a,sort_keys=True,separators=(",",":"),ensure_ascii=True).encode()
print(claimed==a["self_sha256"] and full==raw)`
	cmd := exec.Command("python3", "-c", script)
	cmd.Stdin = strings.NewReader(string(data))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("python3: %v", err)
	}
	mustEqual(t, "python self hash", strings.TrimSpace(string(out)), "True")
}

// Survivors from the earlier mutation run, now each with its own test.
func TestSolR1SeedMustBeANumber(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"seed":"123"}`)
	if _, err := observedDescriptor(body); err == nil || !strings.Contains(err.Error(), "seed is not a number") {
		t.Fatalf("string seed: %v", err)
	}
}

func TestSolR1SuccessWithoutFinalDrawBodyIsTrace(t *testing.T) {
	f := newFixture(t)
	s := &captureSession{cfg: f.config("run1"), tuning: effectiveTuning{RequestTimeout: time.Second}}
	s.cfg.defaults()
	inv := &invocation{attempts: []attemptRecord{
		{AttemptSeq: 1, Draw: 0, Sent: true, Status: 200, ResponseBodyBase64: ptr(base64.StdEncoding.EncodeToString([]byte(completion(okAnswer))))},
		{AttemptSeq: 2, Draw: 1, Sent: true, Status: 503},
	}}
	a := s.buildArtifact(pairKey{"x", "row-a", 0}, captureRow{RowID: "row-a"}, inv, emptyInterpreted, emptyReceipt, nil, 0, 0)
	if a.Outcome != outcomeTrace || !a.RunnerOwned || a.RawText != nil {
		t.Fatalf("a success whose final draw has no 2xx must be trace_inconsistent, got %s", a.Outcome)
	}
}

func TestSolR1ResumeConfigGuardMessage(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("run1")
	cfg.fault = func(p string) error {
		if p == "after_pair_reserved" {
			return errors.New("crash")
		}
		return nil
	}
	_, _ = f.run(cfg)
	changed := f.config("run1")
	changed.Resume, changed.MinInterval = true, 5*time.Second
	_, err := f.run(changed)
	if err == nil || !strings.Contains(err.Error(), "run configuration changed since the run started") {
		t.Fatalf("changed config: %v", err)
	}
}

func ptr(s string) *string { return &s }

var (
	emptyInterpreted = zeroInterpreted()
	emptyReceipt     = zeroReceipt()
)
