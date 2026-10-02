//go:build unix

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// H3 phase and the prompt-variant control (SPEC-incumbent-capture.md R7).
// Loopback only: no live call.

const testAppendix = "Additional rules.\n1. A \"quoted\" rule with a back\\slash, <angle> & ampersand.\n2. Non-ASCII: café → ✓\n"

var systemTextOnce sync.Once
var systemText string

// helperSystemText is the production system message, from the helper.
func helperSystemText(t *testing.T) string {
	t.Helper()
	systemTextOnce.Do(func() {
		out, err := exec.Command(builtHelper, "system-message").Output()
		if err != nil {
			return
		}
		var v struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(out, &v) == nil {
			systemText = v.Text
		}
	})
	if systemText == "" {
		t.Fatal("the helper built for this test run gave no system-message: a missing measurement fails, it never skips")
	}
	if sha256Hex([]byte(systemText)) != helperSystemSHA(t) {
		t.Fatal("helper system message text and sha disagree")
	}
	return systemText
}

func (f *fixture) writeAppendix(text string) string {
	path := filepath.Join(f.root, "appendix.md")
	writePrivate(f.t, path, []byte(text))
	return path
}

func (f *fixture) approveVariant(name, path string) {
	f.t.Helper()
	if err := approveVariant(f.paths.Root, "appr-test", name, path, "fixture variant approval", "human:chris"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) variantConfig(runID, name, path string) runConfig {
	cfg := f.config(runID)
	cfg.PromptVariant, cfg.SystemAppendFile = name, path
	return cfg
}

func systemContent(t *testing.T, body []byte) string {
	t.Helper()
	var parsed struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Messages) == 0 || parsed.Messages[0].Role != "system" {
		t.Fatalf("body has no system message first: %v", err)
	}
	var text string
	if err := json.Unmarshal(parsed.Messages[0].Content, &text); err != nil {
		t.Fatal("system content is not a string")
	}
	return text
}

func jsonKeys(t *testing.T, data []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return sortedKeys(m)
}

// ------------------------------------------------------------------ H3 phase

func TestPhaseForTable(t *testing.T) {
	for set, want := range map[string]string{"H1": "H1-decision", "H2": "H2-final", "H3": "H3-round3"} {
		got, err := phaseFor(set)
		if err != nil || got != want {
			t.Fatalf("phaseFor(%s) = %q, %v; want %q", set, got, err, want)
		}
	}
	for _, set := range []string{"H4", "h3", "", "H3 "} {
		if _, err := phaseFor(set); err == nil {
			t.Fatalf("phaseFor(%q) accepted", set)
		}
	}
}

// retarget turns the H1 fixture into a fixture of another set.
func (f *fixture) retarget(set, phase string) {
	f.t.Helper()
	sealLines, _ := readJSONLines(f.paths.Seals)
	var seal map[string]any
	_ = json.Unmarshal(sealLines[0], &seal)
	seal["set"] = set
	writePrivate(f.t, f.paths.Seals, jsonLines(f.t, seal))
	f.rewriteSession(func(s map[string]any) { s["set"], s["phase"] = set, phase })
	lines, _ := readJSONLines(f.paths.input("H1"))
	var header map[string]any
	_ = json.Unmarshal(lines[0], &header)
	header["set"] = set
	all := []any{header}
	for _, l := range lines[1:] {
		all = append(all, json.RawMessage(l))
	}
	if err := os.MkdirAll(filepath.Dir(f.paths.input(set)), 0o700); err != nil {
		f.t.Fatal(err)
	}
	writePrivate(f.t, f.paths.input(set), jsonLines(f.t, all...))
	if set != "H1" {
		_ = os.Remove(f.paths.input("H1"))
	}
}

// An H3 session opens only in its own phase: H3-round3 is captured in
// full, and an H3 session carrying another set's phase is refused.
func TestH3SessionPhase(t *testing.T) {
	f := newFixture(t)
	f.retarget("H3", "H3-round3")
	res, err := f.run(f.config("run1"))
	if err != nil {
		t.Fatalf("an H3 session in phase H3-round3 was refused: %v", err)
	}
	mustEqual(t, "stop", res.StopReason, "")
	mustEqual(t, "terminal", res.Terminal, 6)
	mustEqual(t, "server requests", f.server.count(), 6)
	if _, err := os.Stat(filepath.Join(f.paths.runDir("H3", "run1"), "responses.jsonl")); err != nil {
		t.Fatalf("the H3 capture wrote no responses under heldout/H3: %v", err)
	}

	for _, phase := range []string{"H2-final", "H1-decision", "h3-round3", ""} {
		wrong := newFixture(t)
		wrong.retarget("H3", phase)
		_, err := wrong.run(wrong.config("run1"))
		if err == nil || !strings.Contains(err.Error(), "set H3 needs phase H3-round3") {
			t.Fatalf("an H3 session with phase %q: %v", phase, err)
		}
		mustEqual(t, "server requests", wrong.server.count(), 0)
	}
}

// ------------------------------------------------------------------ splice

func TestSpliceSystemMessage(t *testing.T) {
	suffix := variantSeparator + testAppendix
	bodies := map[string]string{
		"compact":       `{"messages":[{"content":"base \"q\" \\ é <x>\nline","role":"system"},{"content":[{"text":"u","type":"text"}],"role":"user"}],"model":"m","seed":7}`,
		"role first":    `{"model":"m","messages":[{"role":"system","content":"base"},{"role":"user","content":"u"}],"seed":7}`,
		"spaced":        "{ \"seed\" : 7 ,\n \"messages\" : [ { \"role\" : \"system\" ,\n \"content\" :   \"base\"  } , { \"role\":\"user\",\"content\":\"u\" } ] }",
		"extra members": `{"messages":[{"name":"x","content":"base","role":"system","z":{"content":"inner"}}],"content":"top-level"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			out, base, err := spliceSystemMessage([]byte(body), suffix)
			if err != nil {
				t.Fatal(err)
			}
			if got := systemContent(t, out); got != base+suffix || got != systemContent(t, []byte(body))+suffix {
				t.Fatalf("content is not base + suffix: %q", got)
			}
			// Every byte outside the one string is unchanged: the result is
			// the input with one insertion before the content's closing quote.
			encoded, _ := json.Marshal(suffix)
			insert := encoded[1 : len(encoded)-1]
			at := bytes.Index(out, insert)
			if at < 0 || !bytes.Equal(append(append([]byte{}, out[:at]...), out[at+len(insert):]...), []byte(body)) {
				t.Fatal("the splice changed bytes outside the inserted suffix")
			}
			// Everything except the system content is the same JSON.
			var before, after map[string]any
			_ = json.Unmarshal([]byte(body), &before)
			_ = json.Unmarshal(out, &after)
			after["messages"].([]any)[0].(map[string]any)["content"] = before["messages"].([]any)[0].(map[string]any)["content"]
			a, _ := json.Marshal(before)
			b, _ := json.Marshal(after)
			if !bytes.Equal(a, b) {
				t.Fatal("the splice changed something other than the system content")
			}
		})
	}
	refused := map[string]string{
		"parts content":      `{"messages":[{"content":[{"text":"base","type":"text"}],"role":"system"}]}`,
		"first is user":      `{"messages":[{"content":"u","role":"user"},{"content":"base","role":"system"}]}`,
		"no messages":        `{"model":"m"}`,
		"empty messages":     `{"messages":[]}`,
		"no content":         `{"messages":[{"role":"system"}]}`,
		"null content":       `{"messages":[{"role":"system","content":null}]}`,
		"duplicate content":  `{"messages":[{"role":"system","content":"a","content":"b"}]}`,
		"messages not array": `{"messages":{"role":"system","content":"a"}}`,
		"not json":           `not json`,
	}
	for name, body := range refused {
		t.Run("refused "+name, func(t *testing.T) {
			if out, _, err := spliceSystemMessage([]byte(body), suffix); err == nil {
				t.Fatalf("accepted: %s", out)
			}
		})
	}
}

// ------------------------------------------------------------------ flags and approval

func TestVariantFlagsAndAppendixFile(t *testing.T) {
	f := newFixture(t)
	good := f.writeAppendix(testAppendix)
	write := func(name string, data []byte, mode os.FileMode) string {
		path := filepath.Join(f.root, name)
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(path, mode)
		return path
	}
	link := filepath.Join(f.root, "link.md")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	cases := map[string][3]string{
		"name without file": {"rules-v1", "", "go together"},
		"file without name": {"", good, "go together"},
		"upper-case name":   {"Rules-V1", good, "invalid prompt-variant name"},
		"bracket in name":   {"rules[1]", good, "invalid prompt-variant name"},
		"dotdot name":       {"a..b", good, "invalid prompt-variant name"},
		"missing file":      {"rules-v1", filepath.Join(f.root, "nope.md"), "system append file"},
		"empty file":        {"rules-v1", write("empty.md", []byte(" \n\n"), 0o600), "empty"},
		"group readable":    {"rules-v1", write("open.md", []byte(testAppendix), 0o644), "0600"},
		"symlink":           {"rules-v1", link, "symlinks are refused"},
		"invalid utf-8":     {"rules-v1", write("bad.md", []byte{'a', 0xff, 'b'}, 0o600), "UTF-8"},
		"control character": {"rules-v1", write("nul.md", []byte("a\x00b"), 0o600), "control character"},
		"too large":         {"rules-v1", write("big.md", bytes.Repeat([]byte("a"), maxAppendixBytes+1), 0o600), "larger than"},
		"holds the key":     {"rules-v1", write("key.md", []byte("rule "+testKey+"\n"), 0o600), "credential"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.run(f.variantConfig("run1", c[0], c[1]))
			if err == nil || !strings.Contains(err.Error(), c[2]) {
				t.Fatalf("want a refusal containing %q, got %v", c[2], err)
			}
			mustEqual(t, "server requests", f.server.count(), 0)
			if _, statErr := os.Stat(f.runDir("run1")); statErr == nil {
				t.Fatal("a refused variant run wrote a run directory")
			}
		})
	}
}

// A variant run makes live calls: it needs chris's own approval record that
// names this variant and this appendix. Refused before any write or call.
func TestVariantNeedsItsApprovalRecord(t *testing.T) {
	f := newFixture(t)
	path := f.writeAppendix(testAppendix)
	refused := func(name, appendix, want string) {
		t.Helper()
		_, err := f.run(f.variantConfig("run1", name, appendix))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want a refusal containing %q, got %v", want, err)
		}
		mustEqual(t, "server requests", f.server.count(), 0)
		if _, statErr := os.Stat(f.runDir("run1")); statErr == nil {
			t.Fatal("a refused variant run wrote a run directory")
		}
	}
	// The plain approval (720 attempts) does not approve a variant.
	refused("rules-v1", path, "no variant_approval record names prompt variant")
	// Only chris approves, and only under an existing approval.
	if err := approveVariant(f.paths.Root, "appr-test", "rules-v1", path, "x", "agent:lane"); err == nil {
		t.Fatal("a variant approval by an agent was accepted")
	}
	if err := approveVariant(f.paths.Root, "appr-other", "rules-v1", path, "x", "human:chris"); err == nil {
		t.Fatal("a variant approval under an approval id with no approval record was accepted")
	}
	if err := approveVariant(f.paths.Root, "appr-test", "rules-v1", path, "", "human:chris"); err == nil {
		t.Fatal("a variant approval without a citation was accepted")
	}
	refused("rules-v1", path, "no variant_approval record names prompt variant")
	// An approval that names another variant does not cover this one.
	f.approveVariant("other-v1", path)
	refused("rules-v1", path, "no variant_approval record names prompt variant")
	// An approval for this name with another appendix does not cover a changed file.
	f.approveVariant("rules-v1", path)
	changed := filepath.Join(f.root, "appendix-changed.md")
	writePrivate(t, changed, []byte(testAppendix+"3. One more rule.\n"))
	refused("rules-v1", changed, "approved for another appendix")
	// One name, one appendix: the changed file cannot be approved under the same name.
	if err := approveVariant(f.paths.Root, "appr-test", "rules-v1", changed, "x", "human:chris"); err == nil || !strings.Contains(err.Error(), "new variant name") {
		t.Fatalf("a second appendix under one variant name: %v", err)
	}
	// Approving the same name and file again is a no-op, not a second record.
	before, _ := os.ReadFile(f.paths.Ledger)
	f.approveVariant("rules-v1", path)
	after, _ := os.ReadFile(f.paths.Ledger)
	if !bytes.Equal(before, after) {
		t.Fatal("a repeated variant approval wrote a second record")
	}
	// With the record, the run goes ahead.
	if _, err := f.run(f.variantConfig("run1", "rules-v1", path)); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "server requests", f.server.count(), 6)
}

// ------------------------------------------------------------------ the variant run

// The variant sends the production request with ONE change: the system
// message is the production message, the separator and the appendix. Its
// artifacts, rows, run.json and ledger records all name the variant.
func TestVariantHappyPathWireProofAndLabels(t *testing.T) {
	// The incumbent's requests for the same rows, for the comparison.
	inc := newFixture(t)
	if _, err := inc.run(inc.config("run1")); err != nil {
		t.Fatal(err)
	}
	incumbentBodies := map[string][]byte{} // user sha -> body
	for _, b := range inc.server.bodies {
		d, err := observedDescriptor(b)
		if err != nil {
			t.Fatal(err)
		}
		incumbentBodies[d.Messages[1].ContentSHA256] = b
	}

	f := newFixture(t)
	path := f.writeAppendix(testAppendix)
	f.approveVariant("rules-v1", path)
	res, err := f.run(f.variantConfig("var1", "rules-v1", path))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, "")
	mustEqual(t, "server requests", f.server.count(), 6)

	base := helperSystemText(t)
	wantSystem := base + variantSeparator + testAppendix
	wantSystemSHA := sha256Hex([]byte(wantSystem))
	suffixJSON, _ := json.Marshal(variantSeparator + testAppendix)
	insert := suffixJSON[1 : len(suffixJSON)-1]
	for i, body := range f.server.bodies {
		if got := systemContent(t, body); got != wantSystem {
			t.Fatalf("request %d: the system message is not production + separator + appendix", i)
		}
		d, err := observedDescriptor(body)
		if err != nil {
			t.Fatal(err)
		}
		want, ok := incumbentBodies[d.Messages[1].ContentSHA256]
		if !ok {
			t.Fatalf("request %d: no incumbent request carries the same user content", i)
		}
		// Byte for byte the incumbent's request, plus the one insertion.
		at := bytes.Index(body, insert)
		if at < 0 || !bytes.Equal(append(append([]byte{}, body[:at]...), body[at+len(insert):]...), want) {
			t.Fatalf("request %d differs from the incumbent's request in more than the appended system text", i)
		}
		// Descriptor: only the system content sha differs.
		wd, _ := observedDescriptor(want)
		if wd.Messages[0].ContentSHA256 != helperSystemSHA(t) || d.Messages[0].ContentSHA256 != wantSystemSHA {
			t.Fatal("system content shas")
		}
		d.Messages[0].ContentSHA256 = wd.Messages[0].ContentSHA256
		a, _ := descriptorCJ(d)
		b, _ := descriptorCJ(wd)
		if !bytes.Equal(a, b) {
			t.Fatalf("request %d: model, seed, response format, user message or envelope differ from the incumbent's", i)
		}
	}

	arts := f.artifacts("var1")
	mustEqual(t, "artifacts", len(arts), 6)
	for name, a := range arts {
		if a.Schema != variantArtifactSchema || a.Variant == nil {
			t.Fatalf("artifact %s is not marked as a variant artifact", name)
		}
		v := a.Variant
		if v.Name != "rules-v1" || v.Candidate != "incumbent-variant[rules-v1]" || v.AppendixSHA256 != sha256Hex([]byte(testAppendix)) ||
			v.AppendixBytes != len(testAppendix) || v.SeparatorSHA256 != sha256Hex([]byte(variantSeparator)) ||
			v.BaseSystemMessageSHA256 != helperSystemSHA(t) || v.SystemMessageSHA256 != wantSystemSHA {
			t.Fatalf("artifact %s variant block %+v", name, v)
		}
		for _, at := range a.Attempts {
			if !at.Sent || at.ObservedDescriptorSHA256 != at.ExpectedDescriptorSHA256 {
				t.Fatalf("attempt %+v", at)
			}
			body, _ := base64.StdEncoding.DecodeString(*at.RequestBodyBase64)
			if sha256Hex(body) != at.RequestSHA256 || systemContent(t, body) != wantSystem {
				t.Fatal("the saved request is not the request that was sent")
			}
			// The base proof: the production request was the incumbent's.
			if at.BaseExpectedDescriptorSHA256 == "" || at.BaseExpectedDescriptorSHA256 != at.BaseObservedDescriptorSHA256 ||
				at.BaseExpectedDescriptorSHA256 == at.ExpectedDescriptorSHA256 || at.BaseRequestSHA256 == "" || at.BaseRequestSHA256 == at.RequestSHA256 {
				t.Fatalf("base proof %+v", at)
			}
			var d descriptor
			_ = json.Unmarshal(at.ObservedDescriptor, &d)
			production, ok := incumbentBodies[d.Messages[1].ContentSHA256]
			if !ok || sha256Hex(production) != at.BaseRequestSHA256 {
				t.Fatal("base_request_sha256 is not the sha of the request production built")
			}
		}
	}
	rows := f.responses("var1")
	mustEqual(t, "rows", len(rows), 6)
	for _, r := range rows {
		if r.Candidate != "incumbent-variant[rules-v1]" || r.PromptSHA256 != wantSystemSHA || r.Variant == nil || r.Variant.Name != "rules-v1" ||
			!r.ContractMatch || r.RawText == nil || *r.RawText != okAnswer {
			t.Fatalf("row %+v", r)
		}
	}
	// run.json, the kept appendix and the report.
	config, _ := os.ReadFile(filepath.Join(f.runDir("var1"), "run.json"))
	var recorded runConfigRecord
	if err := json.Unmarshal(config, &recorded); err != nil || recorded.Variant == nil || recorded.Variant.Name != "rules-v1" ||
		recorded.Variant.AppendixSHA256 != sha256Hex([]byte(testAppendix)) || recorded.Variant.BaseSystemMessageSHA256 != helperSystemSHA(t) {
		t.Fatalf("run.json variant block: %s", config)
	}
	kept, err := os.ReadFile(filepath.Join(f.runDir("var1"), systemAppendName))
	if err != nil || string(kept) != testAppendix {
		t.Fatalf("the run directory does not keep the appendix: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(f.runDir("var1"), systemAppendName)); info.Mode().Perm() != 0o600 {
		t.Fatal("the kept appendix is not private")
	}
	var report captureReport
	data, _ := os.ReadFile(filepath.Join(f.runDir("var1"), "report.json"))
	_ = json.Unmarshal(data, &report)
	if !report.Complete || report.Canonical != 6 || report.Variant != "rules-v1" || report.Candidate != "incumbent-variant[rules-v1]" {
		t.Fatalf("report %+v", report)
	}
	// Ledger: every record of the run names the variant; the incumbent series is empty.
	lines, _ := readJSONLines(f.paths.Ledger)
	counted := 0
	for _, line := range lines {
		var rec ledgerRecord
		_ = json.Unmarshal(line, &rec)
		if rec.RunID != "var1" {
			continue
		}
		counted++
		if rec.Variant != "rules-v1" {
			t.Fatalf("ledger record without the variant name: %s", line)
		}
		if rec.Kind == "run_started" && rec.AppendixSHA256 != sha256Hex([]byte(testAppendix)) {
			t.Fatal("run_started does not bind the appendix")
		}
	}
	mustEqual(t, "ledger records of the run", counted, 1+6+6+6)
	l := f.ledger()
	mustEqual(t, "incumbent pairs", len(l.pairs), 0)
	mustEqual(t, "variant pairs", len(l.pairsOf("rules-v1")), 6)
	mustEqual(t, "server count = reservations", f.server.count(), l.spent)
}

// Retries and redraws: every attempt carries the appendix exactly once,
// and the redraw is still authorized only by production's rejection event.
func TestVariantRetriesAndRedraws(t *testing.T) {
	f := newFixture(t)
	f.profileMap["model_max_transport_retries"] = 1
	f.writeProfile()
	path := f.writeAppendix(testAppendix)
	f.approveVariant("rules-v1", path)
	// row-a#0: 429 then a rejected draw, then the redraw succeeds.
	f.server.push(scripted{status: 429, body: `{"error":{"message":"slow down"}}`, headers: retryFast},
		scripted{status: 200, body: completion(rejectedAnswer)})
	if _, err := f.run(f.variantConfig("var1", "rules-v1", path)); err != nil {
		t.Fatal(err)
	}
	wantSystem := helperSystemText(t) + variantSeparator + testAppendix
	mustEqual(t, "server requests", f.server.count(), 8)
	for i, body := range f.server.bodies {
		if systemContent(t, body) != wantSystem {
			t.Fatalf("request %d does not carry the appendix exactly once", i)
		}
	}
	var first artifact
	for _, a := range f.artifacts("var1") {
		if a.RowID == "row-a" && a.Replicate == 0 {
			first = a
		}
	}
	if len(first.Attempts) != 3 || first.Attempts[0].Status != 429 || first.Attempts[2].Draw != 1 || first.Outcome != "ok" ||
		len(first.DrawAuthorizations) != 1 {
		t.Fatalf("attempts %+v outcome %s", first.Attempts, first.Outcome)
	}
	seed, _ := seedOf(first.Attempts[2].ObservedDescriptor)
	var q struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal([]byte(fixtureRequests["row-a"]), &q)
	mustEqual(t, "redraw seed", seed, strconv.FormatInt(interpretSeed(q.Question, 1), 10))
	mustEqual(t, "server = reservations", f.server.count(), f.ledger().spent)
}

// The variant refuses every difference other than the appendix: the request
// production builds must be exactly the incumbent's.
func TestVariantRefusesAnyOtherDifference(t *testing.T) {
	variant := &promptVariant{Name: "rules-v1", Appendix: []byte(testAppendix), AppendixSHA256: sha256Hex([]byte(testAppendix))}
	control := newTransportFixture(t)
	control.transport.variant = variant
	if err := control.send(control.body, http.MethodPost, control.url()); err != nil {
		t.Fatalf("the production request was refused in variant mode: %v", err)
	}
	mustEqual(t, "control sent", control.fx.server.count(), 1)
	if systemContent(t, control.fx.server.bodies[0]) != helperSystemText(t)+variantSeparator+testAppendix {
		t.Fatal("the control request did not carry the appendix")
	}
	already, _, err := spliceSystemMessage(control.body, variant.suffix())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(tf *transportFixture) []byte{
		"wrong model": func(tf *transportFixture) []byte {
			return mutateBody(t, tf.body, func(m map[string]any) { m["model"] = "gpt-5.6-other" })
		},
		"wrong seed": func(tf *transportFixture) []byte {
			return mutateBody(t, tf.body, func(m map[string]any) {
				m["seed"] = json.Number(strconv.FormatInt(interpretSeed(tf.inv.question, 0)+1, 10))
			})
		},
		"missing seed": func(tf *transportFixture) []byte {
			return mutateBody(t, tf.body, func(m map[string]any) { delete(m, "seed") })
		},
		"temperature added": func(tf *transportFixture) []byte {
			return mutateBody(t, tf.body, func(m map[string]any) { m["temperature"] = json.Number("0") })
		},
		"response format": func(tf *transportFixture) []byte {
			return mutateBody(t, tf.body, func(m map[string]any) { m["response_format"] = map[string]any{"type": "text"} })
		},
		"another production system message": func(tf *transportFixture) []byte {
			return mutateBody(t, tf.body, func(m map[string]any) {
				m["messages"].([]any)[0].(map[string]any)["content"] = "another system prompt"
			})
		},
		"user content": func(tf *transportFixture) []byte {
			return mutateBody(t, tf.body, func(m map[string]any) {
				m["messages"].([]any)[1].(map[string]any)["content"] = []any{map[string]any{"type": "text", "text": "another question"}}
			})
		},
		"third message": func(tf *transportFixture) []byte {
			return mutateBody(t, tf.body, func(m map[string]any) {
				m["messages"] = append(m["messages"].([]any), map[string]any{"role": "user", "content": "more"})
			})
		},
		"appendix already present": func(*transportFixture) []byte { return already },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			tf := newTransportFixture(t)
			tf.transport.variant = variant
			err := tf.send(build(tf), http.MethodPost, tf.url())
			if err == nil {
				t.Fatal("the attempt was sent")
			}
			// Refused by the base proof itself, before anything is changed:
			// the request production built is not the incumbent's.
			if !errors.Is(err, errVariantBase) {
				t.Fatalf("refused, but not by the base proof: %v", err)
			}
			mustEqual(t, "latch", tf.inv.latch, outcomeContractFailure)
			mustEqual(t, "server requests", tf.fx.server.count(), 0)
			if len(tf.inv.attempts) != 1 || tf.inv.attempts[0].Sent {
				t.Fatalf("the refused attempt must be recorded unsent: %+v", tf.inv.attempts)
			}
			if at := tf.inv.attempts[0]; at.BaseExpectedDescriptorSHA256 == "" || at.BaseObservedDescriptorSHA256 == at.BaseExpectedDescriptorSHA256 ||
				at.ExpectedDescriptorSHA256 != "" || at.BaseRequestSHA256 != "" {
				t.Fatalf("the refused attempt must record the failed base proof and no variant request: %+v", at)
			}
			if err := tf.send(tf.body, http.MethodPost, tf.url()); !errors.Is(err, errLatched) {
				t.Fatal("a send after the latch was not refused")
			}
			mustEqual(t, "server requests", tf.fx.server.count(), 0)
		})
	}
	// A wrong sealed system sha: the production request is not the incumbent's.
	tf := newTransportFixture(t)
	tf.transport.variant = variant
	tf.inv.systemSHA = strings.Repeat("0", 64)
	if err := tf.send(tf.body, http.MethodPost, tf.url()); !errors.Is(err, errVariantBase) {
		t.Fatalf("a wrong sealed system sha: %v", err)
	}
	mustEqual(t, "server requests", tf.fx.server.count(), 0)
}

// The incumbent and a variant on one seal under one approval: separate
// pairs, one budget, and no way to resume or derive one as the other.
func TestVariantAndIncumbentNeverMix(t *testing.T) {
	f := newFixture(t)
	path := f.writeAppendix(testAppendix)
	f.approveVariant("rules-v1", path)
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(f.variantConfig("var1", "rules-v1", path)); err != nil {
		t.Fatalf("a variant run after the incumbent run of the same seal: %v", err)
	}
	mustEqual(t, "server requests", f.server.count(), 12)
	l := f.ledger()
	mustEqual(t, "one budget", l.spent, 12)
	mustEqual(t, "incumbent pairs", len(l.pairs), 6)
	mustEqual(t, "variant pairs", len(l.pairsOf("rules-v1")), 6)
	for k, st := range l.pairs {
		if st.ReservedRun != "run1" || st.Terminal == nil || st.Terminal.Variant != "" {
			t.Fatalf("incumbent pair %+v: %+v", k, st)
		}
	}
	for k, st := range l.pairsOf("rules-v1") {
		if st.ReservedRun != "var1" || st.Terminal == nil || st.Terminal.Variant != "rules-v1" {
			t.Fatalf("variant pair %+v: %+v", k, st)
		}
	}
	l.close()
	for _, r := range f.responses("run1") {
		if r.Candidate != "incumbent" || r.Variant != nil || r.PromptSHA256 != helperSystemSHA(t) {
			t.Fatalf("incumbent row %+v", r)
		}
	}
	for _, r := range f.responses("var1") {
		if r.Candidate != "incumbent-variant[rules-v1]" {
			t.Fatalf("variant row %+v", r)
		}
	}
	// Resume the incumbent run as a variant, and the variant run as the incumbent.
	asVariant := f.variantConfig("run1", "rules-v1", path)
	asVariant.Resume = true
	if _, err := f.run(asVariant); err == nil || !strings.Contains(err.Error(), "run configuration changed") {
		t.Fatalf("an incumbent run resumed as a variant: %v", err)
	}
	asIncumbent := f.config("var1")
	asIncumbent.Resume = true
	if _, err := f.run(asIncumbent); err == nil || !strings.Contains(err.Error(), "run configuration changed") {
		t.Fatalf("a variant run resumed as the incumbent: %v", err)
	}
	// A second run of either series on the same seal is refused.
	if _, err := f.run(f.config("run2")); err == nil || !strings.Contains(err.Error(), "already reserved under another run id") {
		t.Fatalf("a second incumbent run: %v", err)
	}
	if _, err := f.run(f.variantConfig("var2", "rules-v1", path)); err == nil || !strings.Contains(err.Error(), "already reserved under another run id") {
		t.Fatalf("a second run of the same variant: %v", err)
	}
	mustEqual(t, "server requests", f.server.count(), 12)
	// derive regenerates each run's own series, byte for byte.
	for _, runID := range []string{"run1", "var1"} {
		before, _ := os.ReadFile(filepath.Join(f.runDir(runID), "responses.jsonl"))
		if err := deriveCommand(f.paths.Root, f.sessionID, "appr-test", runID); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(filepath.Join(f.runDir(runID), "responses.jsonl"))
		if !bytes.Equal(before, after) {
			t.Fatalf("derive changed %s/responses.jsonl", runID)
		}
	}
	// A variant artifact placed in the incumbent run is refused by derive.
	names := []string{}
	for name := range f.artifacts("var1") {
		names = append(names, name)
	}
	sort.Strings(names)
	variantArtifact, _ := os.ReadFile(filepath.Join(f.runDir("var1"), "raw", names[0]))
	target := filepath.Join(f.runDir("run1"), "raw", names[0])
	original, _ := os.ReadFile(target)
	_ = os.Chmod(target, 0o600)
	if err := os.WriteFile(target, variantArtifact, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deriveCommand(f.paths.Root, f.sessionID, "appr-test", "run1"); err == nil {
		t.Fatal("derive accepted a variant artifact in the incumbent run")
	}
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The variant's calls spend from the same approval cap as the incumbent's.
func TestVariantSpendsFromTheSameApproval(t *testing.T) {
	f := newFixture(t)
	// One profile for both runs of the seal (a seal is captured under one
	// deployment profile): the retry policy the storm below needs.
	f.profileMap["model_max_transport_retries"] = 2
	f.writeProfile()
	path := f.writeAppendix(testAppendix)
	f.approveVariant("rules-v1", path)
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	// 6 spent; lower the cap to 9: 3 attempts are left, the variant needs 6.
	if err := approve(f.paths.Root, "appr-test", 9, "lower", "human:chris", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(f.variantConfig("var1", "rules-v1", path)); err == nil || !strings.Contains(err.Error(), "budget too small") {
		t.Fatalf("a variant run beyond the approval cap: %v", err)
	}
	mustEqual(t, "server requests", f.server.count(), 6)
	// With 2 more than the grid needs, a retry storm stops at the cap.
	if err := approve(f.paths.Root, "appr-test", 14, "raise", "human:chris", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		f.server.push(scripted{status: 429, body: `{}`, headers: retryFast})
	}
	// The variant run is new: its first start was refused before any write.
	res, err := f.run(f.variantConfig("var1", "rules-v1", path))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, outcomeBudget)
	l := f.ledger()
	mustEqual(t, "approval spent", l.spent, 14)
	mustEqual(t, "server requests", f.server.count(), 14)
	// The stop record of the variant run names the variant, like every other record of the run.
	lines, _ := readJSONLines(f.paths.Ledger)
	stops := 0
	for _, line := range lines {
		var rec ledgerRecord
		_ = json.Unmarshal(line, &rec)
		if rec.Kind == "run_stopped" {
			stops++
			if rec.RunID != "var1" || rec.Variant != "rules-v1" {
				t.Fatalf("stop record %s", line)
			}
		}
	}
	mustEqual(t, "stop records", stops, 1)
}

// A crash between the published artifact and its terminal record: resume
// completes the variant pair as a variant pair and never resends. A changed
// kept appendix refuses the resume.
func TestVariantCrashResume(t *testing.T) {
	f := newFixture(t)
	path := f.writeAppendix(testAppendix)
	f.approveVariant("rules-v1", path)
	cfg := f.variantConfig("var1", "rules-v1", path)
	fired := false
	cfg.fault = func(p string) error {
		if p == "before_pair_terminal" && !fired {
			fired = true
			return errors.New("simulated crash")
		}
		return nil
	}
	if _, err := f.run(cfg); err == nil {
		t.Fatal("crash not surfaced")
	}
	mustEqual(t, "requests before the crash", f.server.count(), 1)
	kept := filepath.Join(f.runDir("var1"), systemAppendName)
	original, _ := os.ReadFile(kept)
	if err := os.WriteFile(kept, append(append([]byte{}, original...), "tampered\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	resume := f.variantConfig("var1", "rules-v1", path)
	resume.Resume = true
	if _, err := f.run(resume); err == nil || !strings.Contains(err.Error(), systemAppendName) {
		t.Fatalf("a resume with a changed kept appendix: %v", err)
	}
	if err := os.WriteFile(kept, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(resume); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "requests after resume", f.server.count(), 6)
	l := f.ledger()
	first := l.pairsOf("rules-v1")[pairKey{f.sealDigest, "row-a", 0}]
	if first == nil || first.Terminal == nil || first.Uncertain || first.Terminal.Variant != "rules-v1" {
		t.Fatalf("the crashed variant pair was not completed as a variant pair: %+v", first)
	}
	mustEqual(t, "incumbent pairs", len(l.pairs), 0)
	mustEqual(t, "rows", len(f.responses("var1")), 6)
}

// Ledger rules for variant records.
func TestVariantLedgerRules(t *testing.T) {
	f := newFixture(t)
	l := f.ledger()
	sha := sha256Hex([]byte(testAppendix))
	n, zero := 10, 0
	refuse := func(name string, rec ledgerRecord) {
		t.Helper()
		if err := l.append(rec); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	accept := func(name string, rec ledgerRecord) {
		t.Helper()
		if err := l.append(rec); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	refuse("variant run before its approval", ledgerRecord{Kind: "run_started", RunID: "var1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, Variant: "rules-v1", AppendixSHA256: sha})
	refuse("variant approval by an agent", ledgerRecord{Kind: "variant_approval", Variant: "rules-v1", AppendixSHA256: sha, ApprovedBy: "agent:x", Source: "s"})
	refuse("variant approval without a name", ledgerRecord{Kind: "variant_approval", AppendixSHA256: sha, ApprovedBy: "human:chris", Source: "s"})
	refuse("variant approval without an appendix sha", ledgerRecord{Kind: "variant_approval", Variant: "rules-v1", ApprovedBy: "human:chris", Source: "s"})
	refuse("variant approval without a citation", ledgerRecord{Kind: "variant_approval", Variant: "rules-v1", AppendixSHA256: sha, ApprovedBy: "human:chris"})
	accept("variant approval", ledgerRecord{Kind: "variant_approval", Variant: "rules-v1", AppendixSHA256: sha, ApprovedBy: "human:chris", Source: "s"})
	refuse("another appendix under the same name", ledgerRecord{Kind: "variant_approval", Variant: "rules-v1", AppendixSHA256: strings.Repeat("1", 64), ApprovedBy: "human:chris", Source: "s"})
	refuse("variant run with another appendix", ledgerRecord{Kind: "run_started", RunID: "var1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, Variant: "rules-v1", AppendixSHA256: strings.Repeat("1", 64)})
	refuse("variant run without its appendix sha", ledgerRecord{Kind: "run_started", RunID: "var1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, Variant: "rules-v1"})
	refuse("appendix sha on an incumbent run", ledgerRecord{Kind: "run_started", RunID: "run1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, AppendixSHA256: sha})
	accept("variant run", ledgerRecord{Kind: "run_started", RunID: "var1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, Variant: "rules-v1", AppendixSHA256: sha})
	accept("incumbent run", ledgerRecord{Kind: "run_started", RunID: "run1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n})
	refuse("incumbent pair in a variant run", ledgerRecord{Kind: "pair_reserved", RunID: "var1", SealDigest: "s", RowID: "r", Replicate: &zero})
	refuse("variant pair in an incumbent run", ledgerRecord{Kind: "pair_reserved", RunID: "run1", SealDigest: "s", RowID: "r", Replicate: &zero, Variant: "rules-v1"})
	refuse("pair of an unapproved variant", ledgerRecord{Kind: "pair_reserved", RunID: "var9", SealDigest: "s", RowID: "r", Replicate: &zero, Variant: "other-v1"})
	accept("variant pair", ledgerRecord{Kind: "pair_reserved", RunID: "var1", SealDigest: "s", RowID: "r", Replicate: &zero, Variant: "rules-v1"})
	accept("incumbent pair with the same key", ledgerRecord{Kind: "pair_reserved", RunID: "run1", SealDigest: "s", RowID: "r", Replicate: &zero})
	seq := l.nextSeq
	refuse("incumbent http reservation in a variant run", ledgerRecord{Kind: "http_reserved", RunID: "var1", SealDigest: "s", RowID: "r", Replicate: &zero, AttemptSeq: &seq, Draw: &zero})
	if _, err := l.reserveHTTP(pairKey{"s", "r", 0}, "var1", 0, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.reserveHTTP(pairKey{"s", "r", 0}, "run1", 0, "x"); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "one budget", l.spent, 2)
	yes := true
	refuse("incumbent stop record on a variant run", ledgerRecord{Kind: "run_stopped", RunID: "var1", Reason: "x"})
	refuse("variant stop record on an incumbent run", ledgerRecord{Kind: "run_stopped", RunID: "run1", Reason: "x", Variant: "rules-v1"})
	refuse("incumbent terminal on a variant run", ledgerRecord{Kind: "pair_terminal", RunID: "var1", SealDigest: "s", RowID: "r", Replicate: &zero, Outcome: "ok", ProductionReturned: &yes})
	accept("variant terminal", ledgerRecord{Kind: "pair_terminal", RunID: "var1", SealDigest: "s", RowID: "r", Replicate: &zero, Outcome: "ok", ProductionReturned: &yes, Variant: "rules-v1"})
	if st := l.pairs[pairKey{"s", "r", 0}]; st == nil || st.Terminal != nil {
		t.Fatal("the variant terminal closed the incumbent pair")
	}
	// The whole ledger replays to the same state.
	l.close()
	replayed, err := f.tryLedger()
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.close()
	if replayed.spent != 2 || replayed.variants["rules-v1"] != sha || replayed.pairsOf("rules-v1")[pairKey{"s", "r", 0}].Terminal == nil ||
		replayed.pairs[pairKey{"s", "r", 0}].Terminal != nil {
		t.Fatal("the replayed ledger differs")
	}
	lines, _ := readJSONLines(f.paths.Ledger)
	var last ledgerRecord
	_ = json.Unmarshal(lines[len(lines)-2], &last)
	if last.Kind != "http_reserved" || last.Variant != "" {
		t.Fatalf("an incumbent reservation carries a variant: %+v", last)
	}
	_ = json.Unmarshal(lines[len(lines)-3], &last)
	if last.Kind != "http_reserved" || last.Variant != "rules-v1" {
		t.Fatalf("a variant reservation does not carry its variant: %+v", last)
	}
}

// ------------------------------------------------------------------ the incumbent path is unchanged

// Without the variant flags the request is byte for byte what production
// sends, and no incumbent file gains a member: the key sets below are the
// ones written before the variant mode existed.
func TestNonVariantRequestAndFilesUnchanged(t *testing.T) {
	production := recordEnvelope(t) // the production runtime through a plain recording transport: row-b, draw 0
	f := newFixture(t)
	if _, err := f.run(f.config("run1")); err != nil {
		t.Fatal(err)
	}
	productionUser, err := observedDescriptor(production)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for i, body := range f.server.bodies {
		d, err := observedDescriptor(body)
		if err != nil {
			t.Fatal(err)
		}
		if systemContent(t, body) != helperSystemText(t) {
			t.Fatalf("request %d: the incumbent system message is not the production system message", i)
		}
		if d.Messages[1].ContentSHA256 != productionUser.Messages[1].ContentSHA256 {
			continue
		}
		matched++
		if !bytes.Equal(body, production) {
			t.Fatalf("request %d: the incumbent request differs from the bytes production sends", i)
		}
	}
	mustEqual(t, "row-b requests compared byte for byte", matched, 3)

	keys := func(list string) []string { out := strings.Fields(list); sort.Strings(out); return out }
	equalKeys := func(what string, got []string, want string) {
		t.Helper()
		if strings.Join(got, " ") != strings.Join(keys(want), " ") {
			t.Fatalf("%s members changed:\n got %v\nwant %v", what, got, keys(want))
		}
	}
	config, _ := os.ReadFile(filepath.Join(f.runDir("run1"), "run.json"))
	equalKeys("run.json", jsonKeys(t, config), `schema run_id approval_id set seal_digest membership_digest session_id input_sha256 profile_sha256
		golden_sha256 build_manifest_sha256 binary_sha256 source_pin allowlist min_interval_ms run_cap_http_attempts reopening interference_threshold_ms`)
	if _, err := os.Stat(filepath.Join(f.runDir("run1"), systemAppendName)); err == nil {
		t.Fatal("an incumbent run kept an appendix file")
	}
	entries, _ := os.ReadDir(filepath.Join(f.runDir("run1"), "raw"))
	mustEqual(t, "artifacts", len(entries), 6)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(f.runDir("run1"), "raw", e.Name()))
		equalKeys("artifact", jsonKeys(t, data), `schema approval_id seal_digest row_id replicate run_id example_id set source_pin binary_sha256 profile_sha256
			run_config_sha256 attempts draw_authorizations latch_tripped latch_reason outcome runner_owned production_returned content_attempt_seq
			diagnostic_attempt_seqs raw_text finish_reason receipt normalized_sha256 duration_ms harness_overhead_ms recovered recovery_index recovery_of
			captured_at self_sha256`)
		var a struct {
			Schema   string            `json:"schema"`
			Attempts []json.RawMessage `json:"attempts"`
		}
		_ = json.Unmarshal(data, &a)
		mustEqual(t, "artifact schema", a.Schema, "gocapture.artifact.v1")
		for _, at := range a.Attempts {
			equalKeys("attempt", jsonKeys(t, at), `attempt_seq attempt_index draw sent request_body_base64 request_sha256 expected_descriptor
				expected_descriptor_sha256 observed_descriptor observed_descriptor_sha256 status transport_error response_sha256 response_bytes
				response_body_base64 system_fingerprint sent_at duration_ms harness_overhead_ms`)
		}
	}
	rows, _ := readJSONLines(filepath.Join(f.runDir("run1"), "responses.jsonl"))
	mustEqual(t, "rows", len(rows), 6)
	for _, row := range rows {
		equalKeys("response row", jsonKeys(t, row), `candidate row_id example_id draw remote model_id model_revision adapter_id adapter_sha256 request_sha256
			prompt_sha256 template_sha256 contract_match contract_reason contract_detail decoding raw_text finish_reason token_counts timings_s error
			production_returned capture`)
		var r responseRow
		_ = json.Unmarshal(row, &r)
		if r.Candidate != "incumbent" || r.PromptSHA256 != helperSystemSHA(t) {
			t.Fatalf("incumbent row %+v", r)
		}
	}
	report, _ := os.ReadFile(filepath.Join(f.runDir("run1"), "report.json"))
	equalKeys("report.json", jsonKeys(t, report), `schema run_id seal_digest sealed_rows canonical_pairs expected_pairs complete uncertain_pairs recovered_pairs
		outcomes_by_replicate runner_owned http_reserved_run approval_spent approval_cap max_harness_overhead_ms interference_threshold_ms`)
	want := map[string]string{
		"approval":      "kind approval_id cap_http_attempts approved_by source at",
		"run_started":   "kind approval_id run_id set seal_digest run_config_sha256 run_cap_http_attempts at",
		"pair_reserved": "kind approval_id seal_digest row_id replicate run_id at",
		"http_reserved": "kind approval_id seal_digest row_id replicate run_id attempt_seq draw expected_descriptor_sha256 at",
		"pair_terminal": "kind approval_id seal_digest row_id replicate run_id outcome production_returned artifact_sha256 artifact_name at",
	}
	lines, _ := readJSONLines(f.paths.Ledger)
	seen := map[string]int{}
	for _, line := range lines {
		var rec struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(line, &rec)
		seen[rec.Kind]++
		equalKeys("ledger "+rec.Kind, jsonKeys(t, line), want[rec.Kind])
	}
	if seen["approval"] != 1 || seen["run_started"] != 1 || seen["pair_reserved"] != 6 || seen["http_reserved"] != 6 || seen["pair_terminal"] != 6 || len(seen) != 5 {
		t.Fatalf("ledger kinds %v", seen)
	}
}

// An artifact belongs to exactly one series.
func TestArtifactSeries(t *testing.T) {
	incumbent := artifact{Schema: artifactSchema}
	variant := artifact{Schema: variantArtifactSchema, Variant: &variantRecord{Name: "rules-v1"}}
	halfMarked := []artifact{
		{Schema: artifactSchema, Variant: &variantRecord{Name: "rules-v1"}},
		{Schema: variantArtifactSchema},
	}
	if incumbent.seriesError("") != nil || variant.seriesError("rules-v1") != nil {
		t.Fatal("an artifact was refused in its own series")
	}
	if incumbent.seriesError("rules-v1") == nil || variant.seriesError("") == nil || variant.seriesError("other-v1") == nil {
		t.Fatal("an artifact was accepted in another series")
	}
	for _, a := range halfMarked {
		if a.seriesError("") == nil || a.seriesError("rules-v1") == nil {
			t.Fatalf("a half-marked artifact was accepted: %+v", a)
		}
	}
}
