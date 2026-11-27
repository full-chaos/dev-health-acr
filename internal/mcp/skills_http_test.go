package mcp_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP Skills extension over the hosted transport, with the synthetic
// fixture skill in testdata/skills registered only here.

const (
	httpSkillURI    = "skill://fixture-notes/SKILL.md"
	httpSkillRefURI = "skill://fixture-notes/references/checklist.md"
	skillsExtKey    = "io.modelcontextprotocol/skills"
)

type skillsStack struct {
	hosted *hostedAPI
	e      *endpoint
	caller issued
}

func newSkillsStack(t *testing.T, withSkills bool) *skillsStack {
	t.Helper()
	hosted := newHostedAPI(t)
	logs := &syncBuffer{}
	cfg, err := acrmcp.NewHTTPProcessConfig(hosted.sidecarConfig(), testIdentity, logs)
	if err != nil {
		t.Fatal(err)
	}
	var skills fs.FS
	if withSkills {
		skills = os.DirFS("testdata/skills")
	}
	if err := acrmcp.SetSkillsForTest(cfg, skills); err != nil {
		t.Fatal(err)
	}
	handler, err := acrmcp.NewHTTPHandler(cfg, acrmcp.HTTPHandlerOptions{BasePath: "/mcp", Identity: testIdentity, MaxRequestBodyBytes: 1 << 20, ResolveTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e := &endpoint{handler: handler, logs: logs}
	e.server = httptest.NewServer(handler)
	t.Cleanup(e.server.Close)
	return &skillsStack{hosted: hosted, e: e, caller: hosted.issue(readScopes, []string{repoAnswers}, nil)}
}

type skillsReply struct {
	status int
	raw    []byte
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int64 `json:"code"`
	} `json:"error"`
}

func skillsMeta() map[string]any {
	return map[string]any{mcpsdk.MetaKeyProtocolVersion: "2026-07-28", mcpsdk.MetaKeyClientCapabilities: map[string]any{}}
}

func (s *skillsStack) rpc(t *testing.T, header http.Header, method string, params map[string]any) skillsReply {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = skillsMeta()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	resp := postMCP(t, s.e, http.MethodPost, body, header)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	reply := skillsReply{status: resp.StatusCode, raw: raw}
	payload := raw
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload = nil
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 1<<20), 1<<22)
		for scanner.Scan() {
			if line, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				payload = []byte(line)
			}
		}
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &reply)
	}
	return reply
}

type httpSkillEntry struct {
	URI         string         `json:"uri"`
	Frontmatter map[string]any `json:"frontmatter"`
	Resources   []struct {
		URI    string `json:"uri"`
		Digest string `json:"digest"`
		Size   int    `json:"size"`
	} `json:"resources"`
}

func checkHTTPFixtureEntry(t *testing.T, entry httpSkillEntry) {
	t.Helper()
	files := map[string]string{httpSkillURI: "testdata/skills/fixture-notes/SKILL.md", httpSkillRefURI: "testdata/skills/fixture-notes/references/checklist.md"}
	if entry.URI != httpSkillURI || entry.Frontmatter["name"] != "fixture-notes" || len(entry.Resources) != len(files) {
		t.Fatalf("entry %+v", entry)
	}
	for _, res := range entry.Resources {
		raw, err := os.ReadFile(files[res.URI])
		if err != nil {
			t.Fatalf("manifest lists %q: %v", res.URI, err)
		}
		sum := sha256.Sum256(raw)
		if res.Digest != "sha256:"+hex.EncodeToString(sum[:]) || res.Size != len(raw) {
			t.Fatalf("%s: manifest %s/%d does not match the file", res.URI, res.Digest, res.Size)
		}
	}
}

func TestSkillsListServesTheFixtureManifestHTTP(t *testing.T) {
	s := newSkillsStack(t, true)
	reply := s.rpc(t, bearerHeader(s.caller.token), "skills/list", nil)
	var got struct {
		ResultType string           `json:"resultType"`
		Skills     []httpSkillEntry `json:"skills"`
		TTLMs      *int             `json:"ttlMs"`
		CacheScope string           `json:"cacheScope"`
	}
	if reply.Error != nil || json.Unmarshal(reply.Result, &got) != nil {
		t.Fatalf("skills/list answered %s", reply.raw)
	}
	if got.ResultType != "complete" || got.TTLMs == nil || *got.TTLMs != 0 || got.CacheScope != "private" || len(got.Skills) != 1 {
		t.Fatalf("skills/list answered %s", reply.raw)
	}
	checkHTTPFixtureEntry(t, got.Skills[0])
}

func TestSkillsGetAnswersTheEntryHTTP(t *testing.T) {
	s := newSkillsStack(t, true)
	reply := s.rpc(t, bearerHeader(s.caller.token), "skills/get", map[string]any{"uri": httpSkillURI})
	var got struct {
		ResultType string         `json:"resultType"`
		Skill      httpSkillEntry `json:"skill"`
		TTLMs      *int           `json:"ttlMs"`
		CacheScope string         `json:"cacheScope"`
	}
	if reply.Error != nil || json.Unmarshal(reply.Result, &got) != nil || got.ResultType != "complete" || got.TTLMs == nil || got.CacheScope != "private" {
		t.Fatalf("skills/get answered %s", reply.raw)
	}
	checkHTTPFixtureEntry(t, got.Skill)
}

func TestSkillsReadServesSkillFileAndSupportingFileHTTP(t *testing.T) {
	s := newSkillsStack(t, true)
	for uri, path := range map[string]string{httpSkillURI: "testdata/skills/fixture-notes/SKILL.md", httpSkillRefURI: "testdata/skills/fixture-notes/references/checklist.md"} {
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		reply := s.rpc(t, bearerHeader(s.caller.token), "resources/read", map[string]any{"uri": uri})
		var got struct {
			Contents []struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"contents"`
		}
		if reply.Error != nil || json.Unmarshal(reply.Result, &got) != nil || len(got.Contents) != 1 || got.Contents[0].URI != uri || got.Contents[0].Text != string(want) {
			t.Fatalf("read %s answered %s", uri, reply.raw)
		}
	}
}

func TestSkillsUnknownURIIsInvalidParamsHTTP(t *testing.T) {
	s := newSkillsStack(t, true)
	for method, uri := range map[string]string{"skills/get": "skill://missing-skill/SKILL.md", "resources/read": "skill://fixture-notes/references/missing.md"} {
		reply := s.rpc(t, bearerHeader(s.caller.token), method, map[string]any{"uri": uri})
		if reply.Error == nil || reply.Error.Code != -32602 {
			t.Fatalf("%s %s answered %s, want -32602", method, uri, reply.raw)
		}
	}
}

// Exact capability bytes of the hosted discover answer with no skill (tasks
// off): the answer this server gave before the skills extension existed.
const httpCapabilitiesNoSkills = `{"completions":{},"logging":{},"prompts":{},"resources":{},"tools":{}}`

func discoverCapabilities(t *testing.T, s *skillsStack) string {
	t.Helper()
	reply := s.rpc(t, bearerHeader(s.caller.token), "server/discover", nil)
	var got struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if reply.Error != nil || json.Unmarshal(reply.Result, &got) != nil {
		t.Fatalf("server/discover answered %s", reply.raw)
	}
	return string(got.Capabilities)
}

func TestSkillsDiscoverUnchangedWithNoSkillHTTP(t *testing.T) {
	s := newSkillsStack(t, false)
	if got := discoverCapabilities(t, s); got != httpCapabilitiesNoSkills {
		t.Fatalf("discover capabilities %s, want %s", got, httpCapabilitiesNoSkills)
	}
	if reply := s.rpc(t, bearerHeader(s.caller.token), "skills/list", nil); reply.Error == nil {
		t.Fatalf("skills/list answered with no skill registered: %s", reply.raw)
	}
}

func TestSkillsDeclaredOnlyWithASkillHTTP(t *testing.T) {
	s := newSkillsStack(t, true)
	want := `{"extensions":{"` + skillsExtKey + `":{}},` + strings.TrimPrefix(httpCapabilitiesNoSkills, "{")
	if got := discoverCapabilities(t, s); got != want {
		t.Fatalf("discover capabilities %s, want %s", got, want)
	}
}

// Serving a skill grants no access: the skill methods sit behind the same
// bearer gate as every other method, every caller gets the same bytes, and a
// tool the caller was not given stays refused.
func TestSkillsGrantNoAccess(t *testing.T) {
	s := newSkillsStack(t, true)
	if reply := s.rpc(t, http.Header{}, "skills/list", nil); reply.status != http.StatusUnauthorized {
		t.Fatalf("skills/list with no bearer answered HTTP %d", reply.status)
	}
	if reply := s.rpc(t, http.Header{}, "resources/read", map[string]any{"uri": httpSkillURI}); reply.status != http.StatusUnauthorized {
		t.Fatalf("a skill read with no bearer answered HTTP %d", reply.status)
	}
	other := s.hosted.issue(readScopes, []string{repoPlain}, nil)
	first := s.rpc(t, bearerHeader(s.caller.token), "skills/list", nil)
	second := s.rpc(t, bearerHeader(other.token), "skills/list", nil)
	if first.Error != nil || !bytes.Equal(first.Result, second.Result) {
		t.Fatalf("skills/list differs by caller: %s vs %s", first.raw, second.raw)
	}
	reply := s.rpc(t, bearerHeader(s.caller.token), "tools/call", map[string]any{"name": "record_episode", "arguments": map[string]any{}})
	if reply.Error == nil && !strings.Contains(string(reply.Result), `"isError":true`) {
		t.Fatalf("a tool the caller was not given ran with skills on: %s", reply.raw)
	}
}

func TestSkillsRequestLineHTTP(t *testing.T) {
	s := newSkillsStack(t, true)
	s.rpc(t, bearerHeader(s.caller.token), "skills/get", map[string]any{"uri": httpSkillURI})
	var line map[string]any
	for _, raw := range strings.Split(string(s.e.logs.Bytes()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(raw), &rec) == nil && rec["msg"] == "mcp skill request" {
			line = rec
		}
	}
	if line["transport"] != "http" || line["method"] != "skills/get" || line["outcome"] != "served" || line["files"] != float64(2) {
		t.Fatalf("skill request line %v in %s", line, string(s.e.logs.Bytes()))
	}
	var request map[string]any
	for _, raw := range strings.Split(string(s.e.logs.Bytes()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(raw), &rec) == nil && rec["method"] == "skills/get" && rec["msg"] != "mcp skill request" {
			request = rec
		}
	}
	if request == nil {
		t.Fatalf("no request line records method skills/get: %s", string(s.e.logs.Bytes()))
	}
}
