package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var retryFast = map[string]string{"retry-after-ms": "5", "retry-after": "0"}

// T1 + T2 + T9 (fault-free count): the full grid, wire proof on every row.
func TestHappyPathGridAndWireProof(t *testing.T) {
	f := newFixture(t)
	res, err := f.run(f.config("run1"))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, "")
	arts := f.artifacts("run1")
	mustEqual(t, "artifacts", len(arts), 6)
	rows := f.responses("run1")
	mustEqual(t, "rows", len(rows), 6)
	grid := map[string]bool{}
	for _, r := range rows {
		grid[r.RowID+"#"+strconv.Itoa(r.Draw)] = true
		if !r.ContractMatch || r.RawText == nil || *r.RawText != okAnswer || !r.ProductionReturned || r.Capture.ResponseSHA256 == "" {
			t.Fatalf("row %+v", r)
		}
	}
	for _, id := range []string{"row-a", "row-b"} {
		for d := 0; d < 3; d++ {
			if !grid[id+"#"+strconv.Itoa(d)] {
				t.Fatalf("grid gap %s#%d", id, d)
			}
		}
	}
	sys := helperSystemSHA(t)
	inputs := map[string]string{}
	for _, m := range f.membership {
		inputs[m.RowID] = m.InputSHA256
	}
	for _, a := range arts {
		if len(a.Attempts) != 1 || !a.Attempts[0].Sent {
			t.Fatalf("attempts %+v", a.Attempts)
		}
		at := a.Attempts[0]
		if at.ObservedDescriptorSHA256 != at.ExpectedDescriptorSHA256 {
			t.Fatal("descriptor mismatch on an accepted row")
		}
		var d descriptor
		if err := json.Unmarshal(at.ObservedDescriptor, &d); err != nil {
			t.Fatal(err)
		}
		if d.Messages[0].ContentSHA256 != sys || d.Messages[1].ContentSHA256 != inputs[a.RowID] {
			t.Fatal("wire system/user content differs from the seal")
		}
		body, _ := base64.StdEncoding.DecodeString(*at.RequestBodyBase64)
		if sha256Hex(body) != at.RequestSHA256 {
			t.Fatal("request body hash")
		}
	}
	l := f.ledger()
	mustEqual(t, "server count = reservations", f.server.count(), l.spent)
	var report captureReport
	data, _ := os.ReadFile(filepath.Join(f.runDir("run1"), "report.json"))
	_ = json.Unmarshal(data, &report)
	if !report.Complete || report.Canonical != 6 {
		t.Fatalf("report %+v", report)
	}
}

// T3: a wrong expected system sha is refused unsent and latched; the run stops.
func TestWrongSystemShaRefusedUnsent(t *testing.T) {
	f := newFixture(t)
	f.rewriteSeal(func(seal map[string]any, header map[string]any) {
		seal["expected_system_message_sha256"] = strings.Repeat("0", 64)
		header["expected_system_message_sha256"] = strings.Repeat("0", 64)
	})
	cfg := f.config("run1")
	f.profileMap["model_max_attempts"] = 3
	f.writeProfile()
	res, err := f.run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeContractFailure)
	mustEqual(t, "server requests", f.server.count(), 0)
	arts := f.artifacts("run1")
	mustEqual(t, "artifacts", len(arts), 1)
	for _, a := range arts {
		if a.Outcome != outcomeContractFailure || !a.RunnerOwned || a.RawText != nil {
			t.Fatalf("artifact %+v", a.Outcome)
		}
		for _, at := range a.Attempts {
			if at.Sent {
				t.Fatal("an attempt was sent after the latch")
			}
		}
	}
}

// rewriteSeal edits the seal record and the matching capture-input header.
func (f *fixture) rewriteSeal(edit func(seal, header map[string]any)) {
	sealLines, _ := readJSONLines(f.paths.Seals)
	var seal map[string]any
	_ = json.Unmarshal(sealLines[0], &seal)
	inLines, _ := readJSONLines(f.paths.input("H1"))
	var header map[string]any
	_ = json.Unmarshal(inLines[0], &header)
	edit(seal, header)
	writePrivate(f.t, f.paths.Seals, jsonLines(f.t, seal))
	rest := []any{header}
	for _, l := range inLines[1:] {
		rest = append(rest, json.RawMessage(l))
	}
	writePrivate(f.t, f.paths.input("H1"), jsonLines(f.t, rest...))
}

// T6: every refusal happens before any connection.
func TestRefusalsBeforeAnyConnection(t *testing.T) {
	cases := map[string]func(f *fixture, cfg *runConfig){
		"missing key": func(f *fixture, cfg *runConfig) {
			delete(f.env, "ACR_CONTEXT_FABRIC_MODEL_API_KEY")
			cfg.lookup = lookupOf(f.env)
		},
		"key and key file": func(f *fixture, cfg *runConfig) {
			f.env["ACR_CONTEXT_FABRIC_MODEL_API_KEY_FILE"] = "/nonexistent"
			cfg.lookup = lookupOf(f.env)
		},
		"missing profile":   func(f *fixture, cfg *runConfig) { cfg.ProfilePath = "" },
		"malformed profile": func(f *fixture, cfg *runConfig) { f.profileMap["model_timeout"] = "soon"; f.writeProfile() },
		"profile default":   func(f *fixture, cfg *runConfig) { delete(f.profileMap, "model_max_attempts"); f.writeProfile() },
		"env tuning conflict": func(f *fixture, cfg *runConfig) {
			f.env["ACR_CONTEXT_FABRIC_MODEL_MAX_ATTEMPTS"] = "2"
			cfg.lookup = lookupOf(f.env)
		},
		"fallback set": func(f *fixture, cfg *runConfig) {
			f.env["ACR_CONTEXT_FABRIC_MODEL_FALLBACK"] = "gpt-other"
			cfg.lookup = lookupOf(f.env)
		},
		"model differs": func(f *fixture, cfg *runConfig) {
			f.env["ACR_CONTEXT_FABRIC_MODEL"] = "gpt-other"
			cfg.lookup = lookupOf(f.env)
		},
		"base url": func(f *fixture, cfg *runConfig) {
			f.env["ACR_CONTEXT_FABRIC_MODEL_BASE_URL"] = "http://127.0.0.1:1/v1/"
			cfg.lookup = lookupOf(f.env)
		},
		"insecure": func(f *fixture, cfg *runConfig) {
			delete(f.env, "ACR_CONTEXT_FABRIC_MODEL_ALLOW_INSECURE_BASE_URL")
			cfg.lookup = lookupOf(f.env)
		},
		"relation": func(f *fixture, cfg *runConfig) {
			f.profileMap["source_revision_relation"] = "unknown"
			f.writeProfile()
		},
		"cap zero":       func(f *fixture, cfg *runConfig) { cfg.RunCap = 0 },
		"cap above 720":  func(f *fixture, cfg *runConfig) { cfg.RunCap = 721 },
		"no approval":    func(f *fixture, cfg *runConfig) { cfg.ApprovalID = "appr-none" },
		"profile perms":  func(f *fixture, cfg *runConfig) { _ = os.Chmod(f.profile, 0o644) },
		"resume unknown": func(f *fixture, cfg *runConfig) { cfg.Resume = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			cfg := f.config("run1")
			mutate(f, &cfg)
			_, err := f.run(cfg)
			if err == nil {
				t.Fatal("accepted")
			}
			if want := map[string]string{"base url": "base URL is outside", "cap above 720": "max-http-attempts", "cap zero": "max-http-attempts", "env tuning conflict": "MAX_ATTEMPTS is set and differs", "fallback set": "fallback model is configured", "insecure": "must use https", "key and key file": "conflicting sources", "malformed profile": "model_timeout must be", "missing key": "no credential", "missing profile": "no default profile", "model differs": "model is outside", "no approval": "no approval record", "profile default": "model_max_attempts must be", "profile perms": "0600", "relation": "source_revision_relation", "resume unknown": "never started"}[name]; want == "" || !strings.Contains(err.Error(), want) {
				t.Fatalf("refused for the wrong reason: %v", err)
			}
			mustEqual(t, "server requests", f.server.count(), 0)
			if _, err := os.Stat(f.runDir("run1")); err == nil {
				t.Fatal("a run directory was written before the refusal")
			}
		})
	}
}

// T7: a credential echoed in a 200 body or a 401 body latches, stops and
// suppresses every content-derived field.
func TestCredentialEchoSuppressed(t *testing.T) {
	for _, step := range []scripted{
		{status: 200, body: completion(`{"shape":"open","note":"` + testKey + `"}`)},
		{status: 401, body: `{"error":{"message":"bad key ` + testKey + `"}}`},
		{status: 200, body: completion(base64.StdEncoding.EncodeToString([]byte(testKey)))},
	} {
		f := newFixture(t)
		f.server.push(step)
		res, err := f.run(f.config("run1"))
		if err != nil {
			t.Fatal(err)
		}
		mustEqual(t, "stop", res.StopReason, outcomeCredentialEcho)
		mustEqual(t, "server requests", f.server.count(), 1)
		assertNoKeyAnywhere(t, f.root)
		for _, a := range f.artifacts("run1") {
			if a.RawText != nil || a.ContentAttemptSeq != nil {
				t.Fatal("content kept after a credential echo")
			}
			for _, at := range a.Attempts {
				if at.ResponseBodyBase64 != nil {
					t.Fatal("response body kept after a credential echo")
				}
			}
		}
	}
}

// assertNoKeyAnywhere scans every file, plain and with base64 fields decoded.
func assertNoKeyAnywhere(t *testing.T, root string) {
	t.Helper()
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(path, "profile.json") {
			return nil
		}
		data, _ := os.ReadFile(path)
		if bytes.Contains(data, []byte(testKey)) {
			t.Fatalf("key found in %s", path)
		}
		var generic any
		for _, line := range bytes.Split(data, []byte("\n")) {
			if json.Unmarshal(line, &generic) == nil {
				walkStrings(generic, func(s string) {
					if decoded, err := base64.StdEncoding.DecodeString(s); err == nil && bytes.Contains(decoded, []byte(testKey)) {
						t.Fatalf("key found base64-decoded in %s", path)
					}
				})
			}
		}
		return nil
	})
}

func walkStrings(v any, visit func(string)) {
	switch x := v.(type) {
	case string:
		visit(x)
	case []any:
		for _, i := range x {
			walkStrings(i, visit)
		}
	case map[string]any:
		for _, i := range x {
			walkStrings(i, visit)
		}
	}
}

// T8a: 429 then 200 under SDK retries; the 200 is the content attempt.
func TestRetryThenSuccess(t *testing.T) {
	f := newFixture(t)
	f.profileMap["model_max_transport_retries"] = 1
	f.writeProfile()
	f.server.push(scripted{status: 429, body: `{"error":{"message":"slow down"}}`, headers: retryFast})
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	var first artifact
	for _, a := range f.artifacts("run1") {
		if a.RowID == "row-a" && a.Replicate == 0 {
			first = a
		}
	}
	if len(first.Attempts) != 2 || first.Attempts[0].Status != 429 || first.Attempts[0].ResponseBodyBase64 != nil {
		t.Fatalf("attempts %+v", first.Attempts)
	}
	if first.ContentAttemptSeq == nil || *first.ContentAttemptSeq != first.Attempts[1].AttemptSeq || first.Outcome != "ok" {
		t.Fatal("content attempt must be the 200")
	}
	mustEqual(t, "server = reservations", f.server.count(), f.ledger().spent)
}

// T8b: a validator rejection authorizes draw 1 through the production event;
// the redraw carries seed(d=1).
func TestRedrawAuthorizedByRejectionEvent(t *testing.T) {
	f := newFixture(t)
	f.server.push(scripted{status: 200, body: completion(rejectedAnswer)})
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	var first artifact
	for _, a := range f.artifacts("run1") {
		if a.RowID == "row-a" && a.Replicate == 0 {
			first = a
		}
	}
	if len(first.DrawAuthorizations) != 1 || first.DrawAuthorizations[0].AuthorizedDraw != 1 {
		t.Fatalf("authorizations %+v", first.DrawAuthorizations)
	}
	if len(first.Attempts) != 2 || first.Attempts[1].Draw != 1 || first.Outcome != "ok" {
		t.Fatalf("attempts %+v outcome %s", first.Attempts, first.Outcome)
	}
	seed, _ := seedOf(first.Attempts[1].ObservedDescriptor)
	var q struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal([]byte(fixtureRequests["row-a"]), &q)
	mustEqual(t, "redraw seed", seed, strconv.FormatInt(interpretSeed(q.Question, 1), 10))
	mustEqual(t, "diagnostic", first.DiagnosticAttemptSeqs, []int{first.Attempts[0].AttemptSeq})
}

// T8c: a rejected draw, then a failing draw: the earlier body stays
// diagnostic; production returned nothing; the pair is a scored-zero failure.
func TestRejectedThenFailedDrawNotPromoted(t *testing.T) {
	f := newFixture(t)
	f.server.push(scripted{status: 200, body: completion(rejectedAnswer)}, scripted{status: 503, body: `{"error":{"message":"down"}}`, headers: retryFast})
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	var first artifact
	for _, a := range f.artifacts("run1") {
		if a.RowID == "row-a" && a.Replicate == 0 {
			first = a
		}
	}
	if first.ProductionReturned || first.RawText != nil || first.ContentAttemptSeq != nil || first.RunnerOwned {
		t.Fatalf("outcome %s returned=%v", first.Outcome, first.ProductionReturned)
	}
	if len(first.DiagnosticAttemptSeqs) != 1 || first.DiagnosticAttemptSeqs[0] != first.Attempts[0].AttemptSeq {
		t.Fatal("the earlier body must stay diagnostic")
	}
	for _, r := range f.responses("run1") {
		if r.RowID == "row-a" && r.Draw == 0 && (r.RawText != nil || r.Error == nil || !r.ContractMatch) {
			t.Fatalf("derived row %+v", r)
		}
	}
}

// T9: the run cap binds mid-run; spending persists; a raise keeps spending.
func TestBudgetRunCapAndApproval(t *testing.T) {
	f := newFixture(t)
	f.profileMap["model_max_transport_retries"] = 1
	f.writeProfile()
	f.server.push(scripted{status: 429, body: `{}`, headers: retryFast})
	cfg := f.config("run1")
	cfg.RunCap = 6
	res, err := f.run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeBudget)
	l := f.ledger()
	mustEqual(t, "run spent", l.runSpent["run1"], 6)
	mustEqual(t, "server count", f.server.count(), 6)
	l.close()
	if err := approve(f.paths.Root, "appr-test", 800, "raise", "human:chris", ""); err == nil {
		t.Fatal("a cap above 720 without a reason was accepted")
	}
	if err := approve(f.paths.Root, "appr-test", 800, "raise", "human:chris", "chris raised it"); err != nil {
		t.Fatal(err)
	}
	l2, err := f.tryLedger()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "spent after raise", l2.spent, 6)
	mustEqual(t, "cap after raise", l2.cap, 800)
	l2.close() // approve must be refused for the cap, not for the lock
	err = approve(f.paths.Root, "appr-test", 5, "lower", "human:chris", "")
	if err == nil || !strings.Contains(err.Error(), "below the spending") {
		t.Fatalf("a cap below spending: %v", err)
	}
}

// T9: the approval cap binds across runs and sets.
func TestApprovalCapAcrossRuns(t *testing.T) {
	f := newFixture(t)
	_ = os.Remove(f.paths.Ledger)
	if err := approve(f.paths.Root, "appr-test", 4, "small", "human:chris", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(f.config("run1")); err == nil {
		t.Fatal("a budget below the pairs needed was accepted at start")
	}
	mustEqual(t, "server requests", f.server.count(), 0)
}

// T10: seal, session and membership.
func TestSealMembershipAndSession(t *testing.T) {
	cases := map[string]func(f *fixture){
		"forged set label": func(f *fixture) { f.rewriteSeal(func(_, h map[string]any) { h["set"] = "H2" }) },
		"altered request": func(f *fixture) {
			f.rewriteInputRows(func(rows []map[string]any) []map[string]any {
				rows[0]["request"] = json.RawMessage(`{"question":"What is blocking the Kestrel project now?","time_context":{"axis":"current"}}`)
				return rows
			})
		},
		"duplicate row": func(f *fixture) {
			f.rewriteInputRows(func(rows []map[string]any) []map[string]any { return append(rows, rows[0]) })
		},
		"missing row": func(f *fixture) { f.rewriteInputRows(func(rows []map[string]any) []map[string]any { return rows[:1] }) },
		"extra row": func(f *fixture) {
			f.rewriteInputRows(func(rows []map[string]any) []map[string]any {
				extra := map[string]any{}
				for k, v := range rows[0] {
					extra[k] = v
				}
				extra["row_id"] = "row-z"
				return append(rows, extra)
			})
		},
		"relabelled row": func(f *fixture) {
			f.rewriteInputRows(func(rows []map[string]any) []map[string]any { rows[0]["example_id"] = "heldout:other"; return rows })
		},
		"target smuggled": func(f *fixture) {
			f.rewriteInputRows(func(rows []map[string]any) []map[string]any { rows[0]["target"] = map[string]any{}; return rows })
		},
		"membership digest": func(f *fixture) {
			f.rewriteSeal(func(s, h map[string]any) {
				s["membership_digest"] = strings.Repeat("1", 64)
				h["membership_digest"] = strings.Repeat("1", 64)
			})
		},
		"closed session": func(f *fixture) {
			f.appendOpenings(map[string]any{"kind": "session_closed", "session_id": f.sessionID, "closed_by": "evaluate", "at": "x"})
		},
		"wrong phase": func(f *fixture) {
			f.rewriteSession(func(s map[string]any) { s["phase"] = "H2-final" })
		},
		"not chris": func(f *fixture) { f.rewriteSession(func(s map[string]any) { s["authorized_by"] = "agent:x" }) },
		"reader not allowed": func(f *fixture) {
			f.rewriteSession(func(s map[string]any) { s["allowed_readers"] = []string{"evaluate"} })
		},
		"reopening without reason": func(f *fixture) {
			f.rewriteSession(func(s map[string]any) { s["session_id"] = "sess-0" })
			f.appendOpenings(map[string]any{"kind": "exposure", "session_id": "sess-0", "reader": "capture-input", "event": "start", "run_id": "", "seal_digest": f.sealDigest, "at": "x"})
			f.appendOpenings(map[string]any{"kind": "evaluation_session", "session_id": f.sessionID, "set": "H1", "seal_digest": f.sealDigest,
				"membership_digest": f.currentMembershipDigest(), "evaluation_manifest_sha256": strings.Repeat("cd", 32), "phase": "H1-decision",
				"authorized_by": "human:chris", "allowed_readers": []string{"capture"}, "opened_at": "x"})
		},
		"header session": func(f *fixture) { f.rewriteSeal(func(_, h map[string]any) { h["session_id"] = "sess-other" }) },
		"header system sha": func(f *fixture) {
			f.rewriteSeal(func(_, h map[string]any) { h["expected_system_message_sha256"] = strings.Repeat("0", 64) })
		},
		"header row count":     func(f *fixture) { f.rewriteSeal(func(_, h map[string]any) { h["row_count"] = 5 }) },
		"input world writable": func(f *fixture) { _ = os.Chmod(f.paths.input("H1"), 0o666) },
		"input symlink": func(f *fixture) {
			real := f.paths.input("H1") + ".real"
			_ = os.Rename(f.paths.input("H1"), real)
			_ = os.Symlink(real, f.paths.input("H1"))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			mutate(f)
			_, err := f.run(f.config("run1"))
			if err == nil {
				t.Fatal("accepted")
			}
			if want := map[string]string{"altered request": "request renders differently from the seal", "closed session": "session is closed", "duplicate row": "duplicate row_id", "extra row": "not in the seal", "forged set label": "header does not match", "header row count": "header does not match", "header session": "header does not match", "header system sha": "header does not match", "input symlink": "symlinks are refused", "input world writable": "not writable by group or others", "membership digest": "does not recompute", "missing row": "missing sealed rows", "not chris": "not opened by human:chris", "reader not allowed": "does not allow reader capture", "relabelled row": "example_id differs from the seal", "reopening without reason": "reopen_reason", "target smuggled": "unknown key", "wrong phase": "needs phase"}[name]; want == "" || !strings.Contains(err.Error(), want) {
				t.Fatalf("refused for the wrong reason: %v", err)
			}
			mustEqual(t, "server requests", f.server.count(), 0)
		})
	}
}

func (f *fixture) currentMembershipDigest() string {
	d, _ := membershipDigest(f.membership)
	return d
}

func (f *fixture) rewriteInputRows(edit func([]map[string]any) []map[string]any) {
	lines, _ := readJSONLines(f.paths.input("H1"))
	var rows []map[string]any
	for _, l := range lines[1:] {
		var r map[string]any
		_ = json.Unmarshal(l, &r)
		rows = append(rows, r)
	}
	rows = edit(rows)
	var header map[string]any
	_ = json.Unmarshal(lines[0], &header)
	header["row_count"] = len(rows)
	all := []any{header}
	for _, r := range rows {
		all = append(all, r)
	}
	writePrivate(f.t, f.paths.input("H1"), jsonLines(f.t, all...))
}

func (f *fixture) rewriteSession(edit func(map[string]any)) {
	lines, _ := readJSONLines(f.paths.Openings)
	var s map[string]any
	_ = json.Unmarshal(lines[0], &s)
	edit(s)
	writePrivate(f.t, f.paths.Openings, jsonLines(f.t, s))
}

func (f *fixture) appendOpenings(rec map[string]any) {
	data, _ := os.ReadFile(f.paths.Openings)
	writePrivate(f.t, f.paths.Openings, append(data, jsonLines(f.t, rec)...))
}

// T17: the exposure line is durable before the first request.
func TestExposureBeforeFirstRequest(t *testing.T) {
	f := newFixture(t)
	seen := false
	f.server.onFirst = func() {
		data, _ := os.ReadFile(f.paths.Openings)
		seen = bytes.Contains(data, []byte(`"kind":"exposure"`)) && bytes.Contains(data, []byte(`"reader":"capture"`))
	}
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("the first request reached the provider before the exposure was durable")
	}
}

// T17: two consecutive model_unavailable terminals stop the run.
func TestConsecutiveUnavailableStops(t *testing.T) {
	f := newFixture(t)
	f.server.fallback = scripted{status: 503, body: `{"error":{"message":"down"}}`, headers: retryFast}
	res, err := f.run(f.config("run1"))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, "consecutive_model_unavailable")
	mustEqual(t, "server requests", f.server.count(), 2)
}

// T17: min-interval is enforced between sends, measured at the server.
func TestMinIntervalAtServer(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("run1")
	cfg.MinInterval = 120 * time.Millisecond
	if _, err := f.run(cfg); err != nil {
		t.Fatal(err)
	}
	f.server.mu.Lock()
	defer f.server.mu.Unlock()
	for i := 1; i < len(f.server.times); i++ {
		if gap := f.server.times[i].Sub(f.server.times[i-1]); gap < 110*time.Millisecond {
			t.Fatalf("gap %v below the interval", gap)
		}
	}
}

// T17: a ledger write failure stops calls at once.
func TestWriteFailureStopsCalls(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("run1")
	appends := 0
	cfg.ledgerHook = func(l *approvalLedger) {
		l.failAppend = func() error {
			appends++
			if appends > 4 {
				return os.ErrPermission
			}
			return nil
		}
	}
	if _, err := f.run(cfg); err == nil {
		t.Fatal("a write failure did not stop the run")
	}
	if f.server.count() > 2 {
		t.Fatalf("calls continued after a write failure: %d", f.server.count())
	}
}

// T17: a concurrent runner is refused by the ledger lock.
func TestConcurrentLedgerRefused(t *testing.T) {
	f := newFixture(t)
	l := f.ledger()
	_ = l
	if _, err := f.tryLedger(); err == nil {
		t.Fatal("a second holder got the ledger lock")
	}
}

// T17 + T11: a changed run config on resume is refused; a new run id cannot
// recapture reserved pairs.
func TestResumeConfigAndNewRunID(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("run1")
	cfg.fault = func(p string) error {
		if p == "after_pair_reserved" {
			return os.ErrDeadlineExceeded
		}
		return nil
	}
	if _, err := f.run(cfg); err == nil {
		t.Fatal("fault not surfaced")
	}
	changed := f.config("run1")
	changed.Resume, changed.MinInterval = true, time.Second
	if _, err := f.run(changed); err == nil {
		t.Fatal("changed config on resume accepted")
	}
	if _, err := f.run(f.config("run2")); err == nil {
		t.Fatal("a new run id recaptured reserved pairs")
	}
	mustEqual(t, "server requests", f.server.count(), 0)
}

// T9: the approval cap binds mid-run, independently of the run cap.
func TestApprovalCapBindsMidRun(t *testing.T) {
	f := newFixture(t)
	_ = os.Remove(f.paths.Ledger)
	if err := approve(f.paths.Root, "appr-test", 6, "exact", "human:chris", ""); err != nil {
		t.Fatal(err)
	}
	f.profileMap["model_max_transport_retries"] = 1
	f.writeProfile()
	f.server.push(scripted{status: 429, body: `{}`, headers: retryFast})
	res, err := f.run(f.config("run1"))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeBudget)
	mustEqual(t, "server", f.server.count(), 6)
	mustEqual(t, "spent", f.ledger().spent, 6)
}

// T7: an echo on a redraw suppresses the earlier stored 200 body too.
func TestEchoAfterStoredBodySuppressesAll(t *testing.T) {
	f := newFixture(t)
	f.server.push(scripted{status: 200, body: completion(rejectedAnswer)}, scripted{status: 200, body: completion(testKey)})
	res, err := f.run(f.config("run1"))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeCredentialEcho)
	for _, a := range f.artifacts("run1") {
		for _, at := range a.Attempts {
			if at.ResponseBodyBase64 != nil || at.SystemFingerprint != "" {
				t.Fatal("an earlier body survived a credential echo")
			}
		}
	}
	assertNoKeyAnywhere(t, f.root)
}

// §7: a final draw that production rejected is a scored-zero failure; its
// 2xx body is diagnostic, never the content attempt.
func TestFinalDrawRejectionIsModelFailure(t *testing.T) {
	f := newFixture(t)
	f.profileMap["synthesis_max_resynthesis_attempts"] = 1
	f.writeProfile()
	f.server.push(scripted{status: 200, body: completion(rejectedAnswer)})
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	var first artifact
	for _, a := range f.artifacts("run1") {
		if a.RowID == "row-a" && a.Replicate == 0 {
			first = a
		}
	}
	mustEqual(t, "outcome", first.Outcome, "invalid_output")
	if first.ProductionReturned || first.RawText != nil || first.ContentAttemptSeq != nil || first.RunnerOwned {
		t.Fatal("a rejected final draw was promoted")
	}
	mustEqual(t, "diagnostic", first.DiagnosticAttemptSeqs, []int{first.Attempts[0].AttemptSeq})
}
