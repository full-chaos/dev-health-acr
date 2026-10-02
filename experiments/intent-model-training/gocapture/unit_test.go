package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/experiments/intent-model-training/internal/interpreq"
)

// transportFixture drives captureTransport directly with a recorded
// production request body.
type transportFixture struct {
	t         *testing.T
	fx        *fixture
	transport *captureTransport
	body      []byte
	inv       *invocation
	ledger    *approvalLedger
}

func newTransportFixture(t *testing.T) *transportFixture {
	t.Helper()
	fx := newFixture(t)
	golden, err := loadGolden(goldenPath())
	if err != nil {
		t.Fatal(err)
	}
	l := fx.ledger()
	n := 720
	if err := l.append(ledgerRecord{Kind: "run_started", RunID: "run1", Set: "H1", SealDigest: fx.sealDigest, RunConfigSHA256: "x", RunCapHTTPAttempts: &n}); err != nil {
		t.Fatal(err)
	}
	body := recordEnvelope(t) // row-b, draw 0
	rendered, _ := interpreq.Render([]byte(fixtureRequests["row-b"]))
	tr := &captureTransport{next: &http.Transport{}, allow: fx.server.allow(), model: "gpt-5.6-luna", golden: golden, ledger: l,
		scanner: newCredScanner(testKey), now: time.Now}
	inv := &invocation{pair: pairKey{fx.sealDigest, "row-b", 0}, runID: "run1", question: rendered.Decoded.Request.Question,
		systemSHA: helperSystemSHA(t), inputSHA: rendered.InputSHA256}
	return &transportFixture{t: t, fx: fx, transport: tr, body: body, inv: inv, ledger: l}
}

func (tf *transportFixture) send(body []byte, method, rawURL string) error {
	tf.transport.begin(tf.inv)
	defer tf.transport.end()
	req, _ := http.NewRequest(method, rawURL, bytes.NewReader(body))
	resp, err := tf.transport.RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}

func (tf *transportFixture) url() string { return tf.fx.server.server.URL + "/v1/chat/completions" }

// T2/T4: the unmodified production body passes; a wrong expected user sha
// is refused unsent and latches.
func TestTransportChecksUserContent(t *testing.T) {
	tf := newTransportFixture(t)
	if err := tf.send(tf.body, http.MethodPost, tf.url()); err != nil {
		t.Fatalf("production body refused: %v", err)
	}
	mustEqual(t, "server", tf.fx.server.count(), 1)
	tf.inv.inputSHA = strings.Repeat("0", 64)
	if err := tf.send(tf.body, http.MethodPost, tf.url()); err == nil {
		t.Fatal("wrong user content accepted")
	}
	mustEqual(t, "latch", tf.inv.latch, outcomeContractFailure)
	if err := tf.send(tf.body, http.MethodPost, tf.url()); !errors.Is(err, errLatched) {
		t.Fatal("a send after the latch was not refused")
	}
	mustEqual(t, "server", tf.fx.server.count(), 1)
}

// T5: allowlist breaches are refused unsent in the transport itself.
func TestTransportAllowlist(t *testing.T) {
	for name, target := range map[string][2]string{
		"method": {http.MethodGet, ""},
		"path":   {http.MethodPost, "/v1/completions"},
		"host":   {http.MethodPost, "HOST"},
		"scheme": {http.MethodPost, "SCHEME"},
	} {
		t.Run(name, func(t *testing.T) {
			tf := newTransportFixture(t)
			u := tf.url()
			switch target[1] {
			case "/v1/completions":
				u = tf.fx.server.server.URL + "/v1/completions"
			case "HOST":
				u = strings.Replace(u, "127.0.0.1", "localhost", 1)
			case "SCHEME":
				u = strings.Replace(u, "http://", "https://", 1)
			}
			if err := tf.send(tf.body, target[0], u); err == nil {
				t.Fatal("accepted")
			}
			mustEqual(t, "latch", tf.inv.latch, outcomeAllowlist)
			mustEqual(t, "server", tf.fx.server.count(), 0)
		})
	}
}

// T5: a 3xx is a stop and is never followed.
func TestRedirectRefused(t *testing.T) {
	f := newFixture(t)
	f.server.push(scripted{status: 302, body: ``, headers: map[string]string{"Location": "http://example.invalid/v1/chat/completions"}})
	res, err := f.run(f.config("run1"))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeAllowlist)
	mustEqual(t, "server", f.server.count(), 1)
}

// T8: a seed on the wire never authorizes itself.
func TestSeedCannotSelfAuthorize(t *testing.T) {
	tf := newTransportFixture(t)
	var body map[string]json.RawMessage
	_ = json.Unmarshal(tf.body, &body)
	body["seed"] = json.RawMessage(strconv.FormatInt(interpretSeed(tf.inv.question, 1), 10))
	mutated, _ := json.Marshal(body)
	if err := tf.send(mutated, http.MethodPost, tf.url()); err == nil {
		t.Fatal("seed(d=1) accepted without an authorization")
	}
	mustEqual(t, "server", tf.fx.server.count(), 0)
	// An authorization for a draw other than the current one latches.
	tf2 := newTransportFixture(t)
	tf2.transport.begin(tf2.inv)
	tf2.transport.authorize(5, "x")
	tf2.transport.end()
	mustEqual(t, "latch", tf2.inv.latch, outcomeContractFailure)
}

// T15: every descriptor mutation is caught.
func TestDescriptorMutations(t *testing.T) {
	tf := newTransportFixture(t)
	base := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(tf.body))
	dec.UseNumber()
	_ = dec.Decode(&base)
	golden, _ := loadGolden(goldenPath())
	expected, _ := expectedDescriptor(golden, "gpt-5.6-luna", tf.inv.systemSHA, tf.inv.inputSHA, interpretSeed(tf.inv.question, 0))
	expCJ, _ := descriptorCJ(expected)
	check := func(name string, edit func(m map[string]any)) {
		clone := map[string]any{}
		raw, _ := json.Marshal(base)
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		_ = d.Decode(&clone)
		edit(clone)
		body, _ := json.Marshal(clone)
		obs, err := observedDescriptor(body)
		if err != nil {
			return // an unparseable body is refused too
		}
		obsCJ, _ := descriptorCJ(obs)
		if bytes.Equal(obsCJ, expCJ) {
			t.Fatalf("%s: mutation not detected", name)
		}
	}
	// The unmodified body must match, or every check below is vacuous.
	obs, _ := observedDescriptor(tf.body)
	obsCJ, _ := descriptorCJ(obs)
	if !bytes.Equal(obsCJ, expCJ) {
		t.Fatal("the production body does not match its expected descriptor")
	}
	msgs := func(m map[string]any) []any { return m["messages"].([]any) }
	check("missing seed", func(m map[string]any) { delete(m, "seed") })
	check("wrong seed", func(m map[string]any) { m["seed"] = json.Number("12345") })
	check("seed above 2^53", func(m map[string]any) { m["seed"] = json.Number("9007199254740993") })
	check("seed as string", func(m map[string]any) { m["seed"] = m["seed"].(json.Number).String() })
	check("response_format", func(m map[string]any) { m["response_format"] = map[string]any{"type": "json_schema"} })
	check("response_format removed", func(m map[string]any) { delete(m, "response_format") })
	check("temperature", func(m map[string]any) { m["temperature"] = json.Number("0") })
	check("max_tokens", func(m map[string]any) { m["max_completion_tokens"] = json.Number("512") })
	check("model", func(m map[string]any) { m["model"] = "gpt-other" })
	check("extra message member", func(m map[string]any) { msgs(m)[0].(map[string]any)["name"] = "x" })
	check("parts to string", func(m map[string]any) {
		msgs(m)[1].(map[string]any)["content"] = msgs(m)[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	})
	check("string to parts", func(m map[string]any) {
		msgs(m)[0].(map[string]any)["content"] = []any{map[string]any{"type": "text", "text": msgs(m)[0].(map[string]any)["content"]}}
	})
	check("extra part member", func(m map[string]any) {
		msgs(m)[1].(map[string]any)["content"].([]any)[0].(map[string]any)["cache_control"] = map[string]any{}
	})
	check("third message", func(m map[string]any) {
		m["messages"] = append(msgs(m), map[string]any{"role": "user", "content": "x"})
	})
	check("swapped order", func(m map[string]any) { ms := msgs(m); ms[0], ms[1] = ms[1], ms[0] })
	check("system text", func(m map[string]any) { msgs(m)[0].(map[string]any)["content"] = "other" })
	// Duplicate keys make the body ambiguous: refused outright.
	dup := bytes.Replace(tf.body, []byte(`"model":`), []byte(`"model":"a","model":`), 1)
	if _, err := observedDescriptor(dup); err == nil {
		t.Fatal("duplicate keys accepted")
	}
}

// T17: the shared descriptor test vectors (for the Python evaluator)
// regenerate identically. GOCAPTURE_WRITE_GOLDEN=1 rewrites them.
func TestDescriptorVectors(t *testing.T) {
	body := recordEnvelope(t)
	type vector struct {
		Name             string `json:"name"`
		RequestBody      string `json:"request_body"`
		DescriptorCJ     string `json:"descriptor_cj"`
		DescriptorSHA256 string `json:"descriptor_sha256"`
		CJUnicodeInput   string `json:"cj_unicode_input,omitempty"`
		CJUnicodeOutput  string `json:"cj_unicode_output,omitempty"`
	}
	var vectors []vector
	add := func(name string, b []byte) {
		d, err := observedDescriptor(b)
		if err != nil {
			t.Fatal(err)
		}
		c, err := descriptorCJ(d)
		if err != nil {
			t.Fatal(err)
		}
		vectors = append(vectors, vector{Name: name, RequestBody: string(b), DescriptorCJ: string(c), DescriptorSHA256: sha256Hex(c)})
	}
	add("production_row_b_draw0", body)
	add("temperature_added", bytes.Replace(body, []byte(`"model":`), []byte(`"temperature":0,"model":`), 1))
	uni, _ := cj(map[string]any{"s": "café \U0001F600 <&> \"q\"\n\t\u0001"})
	vectors = append(vectors, vector{Name: "cj_unicode", CJUnicodeInput: "café \U0001F600 <&> \"q\"\n\t\u0001", CJUnicodeOutput: string(uni)})
	out, _ := json.MarshalIndent(vectors, "", " ")
	out = append(out, '\n')
	path := filepath.Join(experimentDir(), "gocapture", "testdata", "descriptor_vectors.json")
	if os.Getenv("GOCAPTURE_WRITE_GOLDEN") == "1" {
		writePrivate(t, path, out)
	}
	checked, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(checked, out) {
		t.Fatalf("descriptor vectors are stale or missing: %v", err)
	}
	mustEqual(t, "cj unicode", string(uni), `{"s":"caf\u00e9 \ud83d\ude00 <&> \"q\"\n\t\u0001"}`)
}

// T12: canonical files are write-once; derived files regenerate atomically;
// every path component is confined (no symlink is followed).
func TestPublicationIsWriteOnce(t *testing.T) {
	base := t.TempDir()
	root, err := openRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.close()
	dir, err := root.walk(true, "d")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.close()
	noFault := func(string) error { return nil }
	if err := dir.publishWriteOnce("a.json", []byte("one"), noFault); err != nil {
		t.Fatal(err)
	}
	if err := dir.publishWriteOnce("a.json", []byte("two"), noFault); err == nil {
		t.Fatal("a canonical file was replaced")
	}
	data, _ := os.ReadFile(filepath.Join(base, "d", "a.json"))
	mustEqual(t, "content", string(data), "one")
	info, _ := os.Stat(filepath.Join(base, "d", "a.json"))
	mustEqual(t, "mode", info.Mode().Perm(), os.FileMode(0o600))
	for _, bad := range []string{"../x", ".hidden", "a/b", "", ".."} {
		if err := dir.publishWriteOnce(bad, []byte("x"), noFault); err == nil {
			t.Fatalf("unsafe name %q accepted", bad)
		}
	}
	if err := dir.replaceDerived("r.json", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := dir.replaceDerived("r.json", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(base, "d", "r.json"))
	mustEqual(t, "derived", string(data), "v2")
	_ = os.MkdirAll(filepath.Join(base, "loose"), 0o775)
	_ = os.Chmod(filepath.Join(base, "loose"), 0o775)
	if _, err := root.walk(false, "loose"); err == nil {
		t.Fatal("a group-writable directory was accepted")
	}
	_ = os.MkdirAll(filepath.Join(base, "readable"), 0o755)
	_ = os.Chmod(filepath.Join(base, "readable"), 0o755)
	if d, err := root.walk(false, "readable"); err != nil {
		t.Fatalf("a 0755 directory (as the Python side creates) was refused: %v", err)
	} else {
		d.close()
	}
	_ = os.Symlink(filepath.Join(base, "d"), filepath.Join(base, "link"))
	if _, err := root.walk(false, "link"); err == nil {
		t.Fatal("a symlinked directory was accepted")
	}
	_ = os.WriteFile(filepath.Join(base, "d", "real.json"), []byte("x"), 0o600)
	_ = os.Symlink(filepath.Join(base, "d", "real.json"), filepath.Join(base, "d", "sym.json"))
	if _, err := dir.readFile("sym.json"); err == nil {
		t.Fatal("a symlinked file was read")
	}
}

// T12: every file the run writes is private.
func TestRunFilesArePrivate(t *testing.T) {
	f := newFixture(t)
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Walk(f.paths.runDir("H1", "run1"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %v", path, info.Mode().Perm())
		}
		return nil
	})
	info, _ := os.Stat(f.paths.Ledger)
	mustEqual(t, "ledger mode", info.Mode().Perm(), os.FileMode(0o600))
}

// T11: crash windows. Resume completes verified pairs, marks the rest
// uncertain and never resends; recovery writes a separate artifact.
func TestCrashWindowsAndRecovery(t *testing.T) {
	for _, point := range []string{"after_pair_reserved", "after_send", "artifact_after_temp_write", "artifact_after_link", "before_pair_terminal"} {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t)
			cfg := f.config("run1")
			fired := false
			cfg.fault = func(p string) error {
				if p == point && !fired {
					fired = true
					return errors.New("simulated crash")
				}
				return nil
			}
			if _, err := f.run(cfg); err == nil {
				t.Fatal("crash not surfaced")
			}
			sentBefore := f.server.count()
			resume := f.config("run1")
			resume.Resume = true
			if _, err := f.run(resume); err != nil {
				t.Fatal(err)
			}
			l := f.ledger()
			first := l.pairs[pairKey{f.sealDigest, "row-a", 0}]
			switch point {
			case "artifact_after_link", "before_pair_terminal":
				if first.Terminal == nil || first.Uncertain {
					t.Fatal("a verified published artifact was not completed")
				}
			default:
				if !first.Uncertain {
					t.Fatal("an in-flight pair was not marked uncertain")
				}
			}
			// 5 remaining pairs, one request each; the crashed pair is never resent.
			mustEqual(t, "requests after resume", f.server.count()-sentBefore, 5)
			entries, _ := os.ReadDir(filepath.Join(f.runDir("run1"), "raw"))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".tmp-") {
					t.Fatal("a temp file survived resume")
				}
			}
			if point != "after_pair_reserved" && point != "after_send" && point != "artifact_after_temp_write" {
				return
			}
			spent := l.spent
			l.close()
			rec := f.config("run1")
			rec.Resume, rec.RecoverRow, rec.RecoverReplicate, rec.RecoverIndex = true, "row-a", 0, 1
			_, err := f.run(rec)
			if err == nil || !strings.Contains(err.Error(), "recovery needs --authorized-by human:chris") {
				t.Fatalf("recovery without authorization: %v", err)
			}
			rec.RecoverBy, rec.RecoverReason = "human:chris", "crash test"
			if _, err := f.run(rec); err != nil {
				t.Fatal(err)
			}
			l2 := f.ledger()
			st := l2.pairs[pairKey{f.sealDigest, "row-a", 0}]
			if !st.Uncertain || st.recoveryTerminal() == nil || l2.spent != spent+1 {
				t.Fatal("recovery must keep the uncertainty, add one outcome and charge the budget")
			}
			if _, err := os.Stat(filepath.Join(f.runDir("run1"), "raw", artifactName("row-a", 0, 1))); err != nil {
				t.Fatal("no separate recovery artifact")
			}
			for _, r := range f.responses("run1") {
				if r.RowID == "row-a" && r.Draw == 0 && !r.Capture.Recovered {
					t.Fatal("the recovered row is not labelled")
				}
			}
		})
	}
}

// T13: the build manifest binds the binary and the production tree.
func TestBuildManifest(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "gocapture")
	_ = os.WriteFile(exe, []byte("binary"), 0o700)
	repo, err := gitOutput(experimentDir(), "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := gitOutput(experimentDir(), "rev-parse", "HEAD")
	pin = strings.TrimSpace(pin)
	write := func(m buildManifest) {
		data, _ := json.Marshal(m)
		_ = os.WriteFile(exe+".build.json", data, 0o600)
	}
	good := buildManifest{Schema: "gocapture.build.v1", BinarySHA256: sha256Hex([]byte("binary")), SourcePin: pin, ProductionTreeClean: true, VCSModifiedPaths: []string{"experiments/"}}
	facts := buildFacts{ExecutablePath: exe, VCSRevision: pin, RepoRoot: strings.TrimSpace(repo)}
	write(good)
	if _, err := verifyBuild(facts, pin); err != nil {
		t.Fatalf("good manifest refused: %v", err)
	}
	bad := map[string]func(m *buildManifest, f *buildFacts){
		"binary hash":    func(m *buildManifest, _ *buildFacts) { m.BinarySHA256 = strings.Repeat("0", 64) },
		"modified prod":  func(m *buildManifest, _ *buildFacts) { m.VCSModifiedPaths = []string{"internal/contextfabric/x.go"} },
		"tree not clean": func(m *buildManifest, _ *buildFacts) { m.ProductionTreeClean = false },
		"manifest pin":   func(m *buildManifest, _ *buildFacts) { m.SourcePin = strings.Repeat("1", 40) },
		"vcs revision":   func(_ *buildManifest, f *buildFacts) { f.VCSRevision = strings.Repeat("1", 40) },
	}
	for name, mutate := range bad {
		m, fc := good, facts
		mutate(&m, &fc)
		write(m)
		if _, err := verifyBuild(fc, pin); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

// T15/T7: an unparseable body and a body carrying the key are refused unsent.
func TestTransportRefusesAmbiguousOrKeyedBody(t *testing.T) {
	tf := newTransportFixture(t)
	dup := bytes.Replace(tf.body, []byte(`"model":`), []byte(`"model":"a","model":`), 1)
	if err := tf.send(dup, http.MethodPost, tf.url()); err == nil {
		t.Fatal("a duplicate-key body was sent")
	}
	mustEqual(t, "latch", tf.inv.latch, outcomeContractFailure)
	tf2 := newTransportFixture(t)
	keyed := bytes.Replace(tf2.body, []byte(`"model":`), []byte(`"user":"`+testKey+`","model":`), 1)
	if err := tf2.send(keyed, http.MethodPost, tf2.url()); err == nil {
		t.Fatal("a body carrying the key was sent")
	}
	mustEqual(t, "latch", tf2.inv.latch, outcomeCredentialEcho)
	mustEqual(t, "server", tf.fx.server.count()+tf2.fx.server.count(), 0)
}
