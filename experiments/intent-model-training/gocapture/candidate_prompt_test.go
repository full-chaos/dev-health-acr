package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The replace mode of the prompt variant: a candidate system message sent in
// place of production's. Loopback only: no live call.

const testCandidate = "You interpret one engineering question.\nRule A: a \"quoted\" rule with a back\\slash, <angle> & ampersand.\nNon-ASCII: café → ✓\n"

const systemContentPath = "messages[0].content"

func (f *fixture) writeCandidate(text string) string {
	path := filepath.Join(f.root, "candidate.md")
	writePrivate(f.t, path, []byte(text))
	return path
}

func (f *fixture) approveCandidate(name, path string) {
	f.t.Helper()
	if err := approveVariantFile(f.paths.Root, "appr-test", name, "", path, "fixture candidate approval", "human:chris"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) candidateConfig(runID, name, path string) runConfig {
	cfg := f.config(runID)
	cfg.PromptVariant, cfg.SystemMessageFile = name, path
	return cfg
}

// jsonDiffPaths lists every path at which two JSON documents differ, sorted.
// A member or element that only one side has is a difference at its own path.
func jsonDiffPaths(t *testing.T, a, b []byte) []string {
	t.Helper()
	decode := func(data []byte) any {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		return v
	}
	var out []string
	var walk func(path string, x, y any)
	walk = func(path string, x, y any) {
		switch xv := x.(type) {
		case map[string]any:
			yv, ok := y.(map[string]any)
			if !ok {
				out = append(out, path)
				return
			}
			keys := map[string]bool{}
			for k := range xv {
				keys[k] = true
			}
			for k := range yv {
				keys[k] = true
			}
			for k := range keys {
				child := k
				if path != "" {
					child = path + "." + k
				}
				xc, xok := xv[k]
				yc, yok := yv[k]
				if !xok || !yok {
					out = append(out, child)
					continue
				}
				walk(child, xc, yc)
			}
		case []any:
			yv, ok := y.([]any)
			if !ok || len(xv) != len(yv) {
				out = append(out, path)
				return
			}
			for i := range xv {
				walk(fmt.Sprintf("%s[%d]", path, i), xv[i], yv[i])
			}
		default:
			if !reflect.DeepEqual(x, y) {
				out = append(out, path)
			}
		}
	}
	walk("", decode(a), decode(b))
	sort.Strings(out)
	return out
}

// The comparison the wire proof rests on must see a second change. Without
// this test, a diff that only ever returned the system path would pass.
func TestJSONDiffPathsSeesEveryChange(t *testing.T) {
	base := `{"messages":[{"content":"base","role":"system"},{"content":[{"text":"u","type":"text"}],"role":"user"}],"model":"m","seed":7}`
	cases := map[string]struct {
		other string
		want  []string
	}{
		"identical":           {base, nil},
		"system content only": {strings.Replace(base, `"base"`, `"candidate"`, 1), []string{systemContentPath}},
		"system and seed":     {strings.Replace(strings.Replace(base, `"base"`, `"candidate"`, 1), `"seed":7`, `"seed":8`, 1), []string{systemContentPath, "seed"}},
		"user text":           {strings.Replace(base, `"text":"u"`, `"text":"v"`, 1), []string{"messages[1].content[0].text"}},
		"member added":        {strings.Replace(base, `"seed":7`, `"seed":7,"temperature":0`, 1), []string{"temperature"}},
		"member removed":      {strings.Replace(base, `,"seed":7`, ``, 1), []string{"seed"}},
		"message added":       {strings.Replace(base, `],"model"`, `,{"content":"x","role":"user"}],"model"`, 1), []string{"messages"}},
		"number spelled 7.0":  {strings.Replace(base, `"seed":7`, `"seed":7.0`, 1), []string{"seed"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := jsonDiffPaths(t, []byte(base), []byte(c.other)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("paths %v, want %v", got, c.want)
			}
		})
	}
}

func TestReplaceSystemMessage(t *testing.T) {
	bodies := map[string]string{
		"compact":       `{"messages":[{"content":"base \"q\" \\ é <x>\nline","role":"system"},{"content":[{"text":"u","type":"text"}],"role":"user"}],"model":"m","seed":7}`,
		"role first":    `{"model":"m","messages":[{"role":"system","content":"base"},{"role":"user","content":"u"}],"seed":7}`,
		"spaced":        "{ \"seed\" : 7 ,\n \"messages\" : [ { \"role\" : \"system\" ,\n \"content\" :   \"base\"  } , { \"role\":\"user\",\"content\":\"u\" } ] }",
		"extra members": `{"messages":[{"name":"x","content":"base","role":"system","z":{"content":"inner"}}],"content":"top-level"}`,
	}
	encoded, _ := json.Marshal(testCandidate)
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			out, base, err := replaceSystemMessage([]byte(body), testCandidate)
			if err != nil {
				t.Fatal(err)
			}
			if base != systemContent(t, []byte(body)) {
				t.Fatalf("base is not the original content: %q", base)
			}
			if got := systemContent(t, out); got != testCandidate {
				t.Fatalf("content is not the candidate: %q", got)
			}
			// Every byte outside the one string is unchanged.
			start, end, _, err := systemContentSpan([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			want := append(append(append([]byte{}, body[:start]...), encoded...), body[end:]...)
			if !bytes.Equal(out, want) {
				t.Fatal("the replacement changed bytes outside the system content string")
			}
			if got := jsonDiffPaths(t, []byte(body), out); !reflect.DeepEqual(got, []string{systemContentPath}) {
				t.Fatalf("the replacement changed %v", got)
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
			if out, _, err := replaceSystemMessage([]byte(body), testCandidate); err == nil {
				t.Fatalf("accepted: %s", out)
			}
		})
	}
}

func TestCandidateFlagsAndFile(t *testing.T) {
	f := newFixture(t)
	good := f.writeCandidate(testCandidate)
	appendix := f.writeAppendix(testAppendix)
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
	cases := map[string][4]string{
		"both files":        {"cand-v1", appendix, good, "not both"},
		"file without name": {"", "", good, "go together"},
		"upper-case name":   {"Cand-V1", "", good, "invalid prompt-variant name"},
		"missing file":      {"cand-v1", "", filepath.Join(f.root, "nope.md"), "system message file"},
		"empty file":        {"cand-v1", "", write("empty.md", []byte(" \n\n"), 0o600), "empty"},
		"group readable":    {"cand-v1", "", write("open.md", []byte(testCandidate), 0o644), "0600"},
		"symlink":           {"cand-v1", "", link, "symlinks are refused"},
		"invalid utf-8":     {"cand-v1", "", write("bad.md", []byte{'a', 0xff, 'b'}, 0o600), "UTF-8"},
		"control character": {"cand-v1", "", write("nul.md", []byte("a\x00b"), 0o600), "control character"},
		"too large":         {"cand-v1", "", write("big.md", bytes.Repeat([]byte("a"), maxAppendixBytes+1), 0o600), "larger than"},
		"holds the key":     {"cand-v1", "", write("key.md", []byte("rule "+testKey+"\n"), 0o600), "credential"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := f.config("run1")
			cfg.PromptVariant, cfg.SystemAppendFile, cfg.SystemMessageFile = c[0], c[1], c[2]
			_, err := f.run(cfg)
			if err == nil || !strings.Contains(err.Error(), c[3]) {
				t.Fatalf("want a refusal containing %q, got %v", c[3], err)
			}
			mustEqual(t, "server requests", f.server.count(), 0)
			if _, statErr := os.Stat(f.runDir("run1")); statErr == nil {
				t.Fatal("a refused candidate run wrote a run directory")
			}
		})
	}
}

// A candidate run needs chris's own record for this name, this mode and
// this file. An append-mode approval of the same name or the same file does
// not cover it. Refused before any write or call.
func TestCandidateNeedsItsOwnApprovalRecord(t *testing.T) {
	f := newFixture(t)
	path := f.writeCandidate(testCandidate)
	refused := func(name, file, want string) {
		t.Helper()
		_, err := f.run(f.candidateConfig("run1", name, file))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want a refusal containing %q, got %v", want, err)
		}
		mustEqual(t, "server requests", f.server.count(), 0)
		if _, statErr := os.Stat(f.runDir("run1")); statErr == nil {
			t.Fatal("a refused candidate run wrote a run directory")
		}
	}
	refused("cand-v1", path, "no variant_approval record names prompt variant")
	// The same name and the same file, approved as an APPENDIX, do not approve a replacement.
	f.approveVariant("cand-v1", path)
	refused("cand-v1", path, "approved in the other mode")
	if err := approveVariantFile(f.paths.Root, "appr-test", "cand-v1", "", path, "x", "human:chris"); err == nil || !strings.Contains(err.Error(), "new variant name") {
		t.Fatalf("one name approved in both modes: %v", err)
	}
	// Only chris approves, with a citation, with one file.
	if err := approveVariantFile(f.paths.Root, "appr-test", "cand-v2", "", path, "x", "agent:lane"); err == nil {
		t.Fatal("a candidate approval by an agent was accepted")
	}
	if err := approveVariantFile(f.paths.Root, "appr-test", "cand-v2", "", path, "", "human:chris"); err == nil {
		t.Fatal("a candidate approval without a citation was accepted")
	}
	if err := approveVariantFile(f.paths.Root, "appr-test", "cand-v2", path, path, "x", "human:chris"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("a candidate approval with both files: %v", err)
	}
	refused("cand-v2", path, "no variant_approval record names prompt variant")
	f.approveCandidate("cand-v2", path)
	// The replace approval does not approve the same file as an appendix.
	if _, err := f.run(f.variantConfig("run1", "cand-v2", path)); err == nil || !strings.Contains(err.Error(), "approved in the other mode") {
		t.Fatalf("an append run under a replace approval: %v", err)
	}
	mustEqual(t, "server requests", f.server.count(), 0)
	// A changed file is another candidate.
	changed := filepath.Join(f.root, "candidate-changed.md")
	writePrivate(t, changed, []byte(testCandidate+"Rule B.\n"))
	refused("cand-v2", changed, "approved for another system message")
	if err := approveVariantFile(f.paths.Root, "appr-test", "cand-v2", "", changed, "x", "human:chris"); err == nil || !strings.Contains(err.Error(), "new variant name") {
		t.Fatalf("a second file under one candidate name: %v", err)
	}
	// Approving the same name, mode and file again writes nothing.
	before, _ := os.ReadFile(f.paths.Ledger)
	f.approveCandidate("cand-v2", path)
	after, _ := os.ReadFile(f.paths.Ledger)
	if !bytes.Equal(before, after) {
		t.Fatal("a repeated candidate approval wrote a second record")
	}
	// A candidate that IS the production system message is the incumbent.
	same := filepath.Join(f.root, "same.md")
	writePrivate(t, same, []byte(helperSystemText(t)))
	f.approveCandidate("same-v1", same)
	if _, err := f.run(f.candidateConfig("run1", "same-v1", same)); !errors.Is(err, errVariantIsIncumbent) {
		t.Fatalf("the production system message as a candidate: %v", err)
	}
	mustEqual(t, "server requests", f.server.count(), 0)
	if _, statErr := os.Stat(f.runDir("run1")); statErr == nil {
		t.Fatal("the refused run wrote a run directory")
	}
	// With its record, the run goes ahead.
	if _, err := f.run(f.candidateConfig("run1", "cand-v2", path)); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "server requests", f.server.count(), 6)
}

// The wire proof. A candidate run sends the request production built with
// ONE change: the content of the system message is the candidate file.
func TestCandidateWireDiffIsTheSystemMessageOnly(t *testing.T) {
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
	path := f.writeCandidate(testCandidate)
	f.approveCandidate("cand-v1", path)
	res, err := f.run(f.candidateConfig("cand1", "cand-v1", path))
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stop", res.StopReason, "")
	mustEqual(t, "server requests", f.server.count(), 6)

	candidateSHA := sha256Hex([]byte(testCandidate))
	encoded, _ := json.Marshal(testCandidate)
	if testCandidate == helperSystemText(t) {
		t.Fatal("the fixture candidate equals the production system message")
	}
	for i, body := range f.server.bodies {
		if got := systemContent(t, body); got != testCandidate {
			t.Fatalf("request %d: the system message is not the candidate file", i)
		}
		d, err := observedDescriptor(body)
		if err != nil {
			t.Fatal(err)
		}
		want, ok := incumbentBodies[d.Messages[1].ContentSHA256]
		if !ok {
			t.Fatalf("request %d: no incumbent request carries the same user content", i)
		}
		// The wire diff: exactly one path differs.
		if got := jsonDiffPaths(t, want, body); !reflect.DeepEqual(got, []string{systemContentPath}) {
			t.Fatalf("request %d differs from the incumbent's request at %v, want only %s", i, got, systemContentPath)
		}
		// Byte for byte the incumbent's request, with the one string replaced.
		start, end, base, err := systemContentSpan(want)
		if err != nil {
			t.Fatal(err)
		}
		if base != helperSystemText(t) {
			t.Fatalf("request %d: the incumbent request does not carry the production system message", i)
		}
		if !bytes.Equal(body, append(append(append([]byte{}, want[:start]...), encoded...), want[end:]...)) {
			t.Fatalf("request %d differs from the incumbent's request outside the system content string", i)
		}
		// Descriptor: only the system content sha differs.
		wd, _ := observedDescriptor(want)
		if wd.Messages[0].ContentSHA256 != helperSystemSHA(t) || d.Messages[0].ContentSHA256 != candidateSHA {
			t.Fatal("system content shas")
		}
		d.Messages[0].ContentSHA256 = wd.Messages[0].ContentSHA256
		a, _ := descriptorCJ(d)
		b, _ := descriptorCJ(wd)
		if !bytes.Equal(a, b) {
			t.Fatalf("request %d: model, seed, response format, user message or envelope differ from the incumbent's", i)
		}
	}

	arts := f.artifacts("cand1")
	mustEqual(t, "artifacts", len(arts), 6)
	for name, a := range arts {
		if a.Schema != variantArtifactSchema || a.Variant == nil {
			t.Fatalf("artifact %s is not marked as a variant artifact", name)
		}
		v := a.Variant
		if v.Name != "cand-v1" || v.Candidate != "incumbent-variant[cand-v1]" || v.Mode != variantModeReplace || v.SeparatorSHA256 != "" ||
			v.AppendixSHA256 != candidateSHA || v.AppendixBytes != len(testCandidate) ||
			v.BaseSystemMessageSHA256 != helperSystemSHA(t) || v.SystemMessageSHA256 != candidateSHA {
			t.Fatalf("artifact %s variant block %+v", name, v)
		}
		for _, at := range a.Attempts {
			if !at.Sent || at.ObservedDescriptorSHA256 != at.ExpectedDescriptorSHA256 {
				t.Fatalf("attempt %+v", at)
			}
			body, _ := base64.StdEncoding.DecodeString(*at.RequestBodyBase64)
			if sha256Hex(body) != at.RequestSHA256 || systemContent(t, body) != testCandidate {
				t.Fatal("the saved request is not the request that was sent")
			}
			// The base proof: the request production built was the incumbent's.
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
	rows := f.responses("cand1")
	mustEqual(t, "rows", len(rows), 6)
	for _, r := range rows {
		if r.Candidate != "incumbent-variant[cand-v1]" || r.PromptSHA256 != candidateSHA || r.Variant == nil || r.Variant.Mode != variantModeReplace ||
			!r.ContractMatch || r.RawText == nil || *r.RawText != okAnswer {
			t.Fatalf("row %+v", r)
		}
	}
	// run.json, the kept file and the report.
	config, _ := os.ReadFile(filepath.Join(f.runDir("cand1"), "run.json"))
	var recorded runConfigRecord
	if err := json.Unmarshal(config, &recorded); err != nil || recorded.Variant == nil || recorded.Variant.Name != "cand-v1" ||
		recorded.Variant.Mode != variantModeReplace || recorded.Variant.AppendixSHA256 != candidateSHA ||
		recorded.Variant.BaseSystemMessageSHA256 != helperSystemSHA(t) || recorded.Variant.SeparatorSHA256 != "" {
		t.Fatalf("run.json variant block: %s", config)
	}
	kept, err := os.ReadFile(filepath.Join(f.runDir("cand1"), systemMessageName))
	if err != nil || string(kept) != testCandidate {
		t.Fatalf("the run directory does not keep the candidate system message: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(f.runDir("cand1"), systemMessageName)); info.Mode().Perm() != 0o600 {
		t.Fatal("the kept system message is not private")
	}
	if _, err := os.Stat(filepath.Join(f.runDir("cand1"), systemAppendName)); err == nil {
		t.Fatal("a replace-mode run directory holds an appendix file")
	}
	var report captureReport
	data, _ := os.ReadFile(filepath.Join(f.runDir("cand1"), "report.json"))
	_ = json.Unmarshal(data, &report)
	if !report.Complete || report.Canonical != 6 || report.Variant != "cand-v1" || report.Candidate != "incumbent-variant[cand-v1]" {
		t.Fatalf("report %+v", report)
	}
	// Ledger: the approval and the run start bind the mode; every record of the run names the variant.
	lines, _ := readJSONLines(f.paths.Ledger)
	counted, approvals := 0, 0
	for _, line := range lines {
		var rec ledgerRecord
		_ = json.Unmarshal(line, &rec)
		if rec.Kind == "variant_approval" {
			approvals++
			if rec.Variant != "cand-v1" || rec.VariantMode != variantModeReplace || rec.AppendixSHA256 != candidateSHA {
				t.Fatalf("variant_approval does not bind the name, the mode and the file: %s", line)
			}
		}
		if rec.RunID != "cand1" {
			continue
		}
		counted++
		if rec.Variant != "cand-v1" {
			t.Fatalf("ledger record without the variant name: %s", line)
		}
		if (rec.Kind == "run_started") != (rec.VariantMode == variantModeReplace) {
			t.Fatalf("the mode belongs on run_started and on no other record of the run: %s", line)
		}
		if rec.Kind == "run_started" && rec.AppendixSHA256 != candidateSHA {
			t.Fatal("run_started does not bind the candidate file")
		}
	}
	mustEqual(t, "variant approvals", approvals, 1)
	mustEqual(t, "ledger records of the run", counted, 1+6+6+6)
	l := f.ledger()
	mustEqual(t, "incumbent pairs", len(l.pairs), 0)
	mustEqual(t, "candidate pairs", len(l.pairsOf("cand-v1")), 6)
	mustEqual(t, "server count = reservations", f.server.count(), l.spent)
}

// The candidate mode refuses every difference other than the system message:
// the request production builds must be exactly the incumbent's.
func TestCandidateRefusesAnyOtherDifference(t *testing.T) {
	variant := &promptVariant{Name: "cand-v1", Appendix: []byte(testCandidate), AppendixSHA256: sha256Hex([]byte(testCandidate)), Mode: variantModeReplace}
	control := newTransportFixture(t)
	control.transport.variant = variant
	if err := control.send(control.body, http.MethodPost, control.url()); err != nil {
		t.Fatalf("the production request was refused in candidate mode: %v", err)
	}
	mustEqual(t, "control sent", control.fx.server.count(), 1)
	if got := jsonDiffPaths(t, control.body, control.fx.server.bodies[0]); !reflect.DeepEqual(got, []string{systemContentPath}) {
		t.Fatalf("the control request differs from production's at %v", got)
	}
	if systemContent(t, control.fx.server.bodies[0]) != testCandidate {
		t.Fatal("the control request did not carry the candidate")
	}
	already, _, err := replaceSystemMessage(control.body, testCandidate)
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
		"candidate already in place": func(*transportFixture) []byte { return already },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			tf := newTransportFixture(t)
			tf.transport.variant = variant
			err := tf.send(build(tf), http.MethodPost, tf.url())
			if err == nil {
				t.Fatal("the attempt was sent")
			}
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
				t.Fatalf("the refused attempt must record the failed base proof and no candidate request: %+v", at)
			}
			if err := tf.send(tf.body, http.MethodPost, tf.url()); !errors.Is(err, errLatched) {
				t.Fatal("a send after the latch was not refused")
			}
			mustEqual(t, "server requests", tf.fx.server.count(), 0)
		})
	}
	// The production message as the candidate: the transport refuses it too.
	tf := newTransportFixture(t)
	same := helperSystemText(t)
	tf.transport.variant = &promptVariant{Name: "same-v1", Appendix: []byte(same), AppendixSHA256: sha256Hex([]byte(same)), Mode: variantModeReplace}
	if err := tf.send(tf.body, http.MethodPost, tf.url()); !errors.Is(err, errVariantIsIncumbent) {
		t.Fatalf("the production system message as the candidate: %v", err)
	}
	mustEqual(t, "server requests", tf.fx.server.count(), 0)
}

func TestCandidateCrashResume(t *testing.T) {
	f := newFixture(t)
	path := f.writeCandidate(testCandidate)
	f.approveCandidate("cand-v1", path)
	cfg := f.candidateConfig("cand1", "cand-v1", path)
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
	kept := filepath.Join(f.runDir("cand1"), systemMessageName)
	original, _ := os.ReadFile(kept)
	if err := os.WriteFile(kept, append(append([]byte{}, original...), "tampered\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	resume := f.candidateConfig("cand1", "cand-v1", path)
	resume.Resume = true
	if _, err := f.run(resume); err == nil || !strings.Contains(err.Error(), systemMessageName) {
		t.Fatalf("a resume with a changed kept system message: %v", err)
	}
	if err := os.WriteFile(kept, original, 0o600); err != nil {
		t.Fatal(err)
	}
	// A resume in the other mode is another run configuration.
	other := f.variantConfig("cand1", "cand-v1", path)
	other.Resume = true
	if _, err := f.run(other); err == nil {
		t.Fatal("a candidate run was resumed as an append-mode run")
	}
	mustEqual(t, "requests after the refused resumes", f.server.count(), 1)
	if _, err := f.run(resume); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "requests after resume", f.server.count(), 6)
	for i, body := range f.server.bodies {
		if systemContent(t, body) != testCandidate {
			t.Fatalf("request %d does not carry the candidate", i)
		}
	}
	mustEqual(t, "rows", len(f.responses("cand1")), 6)
}

func TestCandidateLedgerRules(t *testing.T) {
	f := newFixture(t)
	l := f.ledger()
	sha := sha256Hex([]byte(testCandidate))
	n := 10
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
	refuse("an unknown mode", ledgerRecord{Kind: "variant_approval", Variant: "cand-v1", AppendixSHA256: sha, VariantMode: "prepend", ApprovedBy: "human:chris", Source: "s"})
	refuse("a replace run before its approval", ledgerRecord{Kind: "run_started", RunID: "cand1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, Variant: "cand-v1", AppendixSHA256: sha, VariantMode: variantModeReplace})
	accept("replace approval", ledgerRecord{Kind: "variant_approval", Variant: "cand-v1", AppendixSHA256: sha, VariantMode: variantModeReplace, ApprovedBy: "human:chris", Source: "s"})
	accept("append approval of another name", ledgerRecord{Kind: "variant_approval", Variant: "rules-v1", AppendixSHA256: sha, ApprovedBy: "human:chris", Source: "s"})
	refuse("the replace name approved again as an appendix", ledgerRecord{Kind: "variant_approval", Variant: "cand-v1", AppendixSHA256: sha, ApprovedBy: "human:chris", Source: "s"})
	refuse("the append name approved again as a replacement", ledgerRecord{Kind: "variant_approval", Variant: "rules-v1", AppendixSHA256: sha, VariantMode: variantModeReplace, ApprovedBy: "human:chris", Source: "s"})
	refuse("an append run under the replace approval", ledgerRecord{Kind: "run_started", RunID: "cand1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, Variant: "cand-v1", AppendixSHA256: sha})
	refuse("a replace run under the append approval", ledgerRecord{Kind: "run_started", RunID: "var1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, Variant: "rules-v1", AppendixSHA256: sha, VariantMode: variantModeReplace})
	refuse("a mode on an incumbent run", ledgerRecord{Kind: "run_started", RunID: "run1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, VariantMode: variantModeReplace})
	accept("replace run", ledgerRecord{Kind: "run_started", RunID: "cand1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, Variant: "cand-v1", AppendixSHA256: sha, VariantMode: variantModeReplace})
	accept("append run", ledgerRecord{Kind: "run_started", RunID: "var1", SealDigest: "s", RunConfigSHA256: "c", RunCapHTTPAttempts: &n, Variant: "rules-v1", AppendixSHA256: sha})
	// The whole ledger replays to the same state.
	l.close()
	replayed, err := f.tryLedger()
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.close()
	if replayed.variants["cand-v1"] != sha || replayed.variantModes["cand-v1"] != variantModeReplace || replayed.variantModes["rules-v1"] != variantModeAppend {
		t.Fatalf("the replayed ledger differs: %+v", replayed.variantModes)
	}
}

// An append-mode run writes the records it wrote before the replace mode
// existed: no file of it gains a member.
func TestAppendModeRecordsHaveNoModeMember(t *testing.T) {
	f := newFixture(t)
	path := f.writeAppendix(testAppendix)
	f.approveVariant("rules-v1", path)
	if _, err := f.run(f.variantConfig("var1", "rules-v1", path)); err != nil {
		t.Fatal(err)
	}
	ledger, _ := os.ReadFile(f.paths.Ledger)
	if bytes.Contains(ledger, []byte("variant_mode")) {
		t.Fatal("an append-mode ledger holds a variant_mode member")
	}
	variantKeys := func(data []byte) []string {
		var holder struct {
			Variant json.RawMessage `json:"variant"`
		}
		if err := json.Unmarshal(data, &holder); err != nil || holder.Variant == nil {
			t.Fatalf("no variant block: %v", err)
		}
		return jsonKeys(t, holder.Variant)
	}
	config, _ := os.ReadFile(filepath.Join(f.runDir("var1"), "run.json"))
	mustEqual(t, "run.json variant members", strings.Join(variantKeys(config), ","),
		"appendix_bytes,appendix_sha256,base_system_message_sha256,candidate,name,separator_sha256")
	raw, err := filepath.Glob(filepath.Join(f.runDir("var1"), "raw", "*.json"))
	if err != nil || len(raw) != 6 {
		t.Fatalf("raw artifacts: %v %v", raw, err)
	}
	for _, name := range raw {
		data, _ := os.ReadFile(name)
		mustEqual(t, "artifact variant members", strings.Join(variantKeys(data), ","),
			"appendix_bytes,appendix_sha256,base_system_message_sha256,candidate,name,separator_sha256,system_message_sha256")
	}
	if _, err := os.Stat(filepath.Join(f.runDir("var1"), systemMessageName)); err == nil {
		t.Fatal("an append-mode run directory holds a system message file")
	}
}
