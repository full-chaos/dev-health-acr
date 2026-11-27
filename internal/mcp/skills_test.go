package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.yaml.in/yaml/v3"
)

const (
	fixtureSkillURI     = "skill://fixture-notes/SKILL.md"
	fixtureSkillRefURI  = "skill://fixture-notes/references/checklist.md"
	fixtureSkillDir     = "testdata/skills"
	fixtureSkillMDPath  = "testdata/skills/fixture-notes/SKILL.md"
	fixtureSkillRefPath = "testdata/skills/fixture-notes/references/checklist.md"
)

type rawSkillsResult struct {
	mcpsdk.ResultBase
	raw []byte
}

func (r *rawSkillsResult) UnmarshalJSON(b []byte) error {
	r.raw = append([]byte(nil), b...)
	return nil
}

type rawSkillsParams struct {
	mcpsdk.ParamsBase
	URI    string `json:"uri,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type skillWire struct {
	URI         string         `json:"uri"`
	Frontmatter map[string]any `json:"frontmatter"`
	Resources   []struct {
		URI    string `json:"uri"`
		Digest string `json:"digest"`
		Size   int    `json:"size"`
	} `json:"resources"`
}

type skillsListWire struct {
	ResultType string      `json:"resultType"`
	Skills     []skillWire `json:"skills"`
	NextCursor *string     `json:"nextCursor"`
	TTLMs      *int        `json:"ttlMs"`
	CacheScope string      `json:"cacheScope"`
}

type skillsGetWire struct {
	ResultType string    `json:"resultType"`
	Skill      skillWire `json:"skill"`
	TTLMs      *int      `json:"ttlMs"`
	CacheScope string    `json:"cacheScope"`
}

// stdioSkillsClient serves the STDIO server for the fixture bootstrap with
// skills (nil serves none) over an in-memory transport.
func stdioSkillsClient(t *testing.T, skills fsFixture, logs io.Writer) *mcpsdk.ClientSession {
	t.Helper()
	fx := newFixtureServer(t)
	boot := newFixtureBootstrap(t, fx)
	if logs == nil {
		logs = io.Discard
	}
	cfg, caller := boot.split(logs)
	cfg.diagnostics = slog.New(slog.NewJSONHandler(logs, nil))
	if skills != nil {
		set, err := newSkillSet(skills)
		if err != nil {
			t.Fatal(err)
		}
		cfg.skills = set
	}
	server := newStdioServer(cfg, caller, "test-version")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	for _, method := range []string{methodSkillsList, methodSkillsGet} {
		if err := mcpsdk.AddSendingCustomMethod[*rawSkillsParams, *rawSkillsResult](client, method); err != nil {
			t.Fatal(err)
		}
	}
	t1, t2 := mcpsdk.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Close()
	})
	return session
}

type fsFixture = fstest.MapFS

func fixtureSkillFS(t *testing.T) fsFixture {
	t.Helper()
	out := fsFixture{}
	for name, path := range map[string]string{
		"fixture-notes/SKILL.md":                fixtureSkillMDPath,
		"fixture-notes/references/checklist.md": fixtureSkillRefPath,
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = &fstest.MapFile{Data: raw}
	}
	return out
}

func callSkills(t *testing.T, session *mcpsdk.ClientSession, method string, params *rawSkillsParams) ([]byte, error) {
	t.Helper()
	if params == nil {
		params = &rawSkillsParams{}
	}
	res, err := mcpsdk.CallCustomMethod[*rawSkillsParams, *rawSkillsResult](context.Background(), session, method, params)
	if err != nil {
		return nil, err
	}
	return res.raw, nil
}

func wantInvalidParams(t *testing.T, what string, err error) {
	t.Helper()
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("%s: got %v, want JSON-RPC -32602", what, err)
	}
}

func digestOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func checkFixtureEntry(t *testing.T, entry skillWire) {
	t.Helper()
	if entry.URI != fixtureSkillURI {
		t.Fatalf("entry uri %q, want %q", entry.URI, fixtureSkillURI)
	}
	want := map[string][]byte{fixtureSkillURI: readFile(t, fixtureSkillMDPath), fixtureSkillRefURI: readFile(t, fixtureSkillRefPath)}
	if len(entry.Resources) != len(want) {
		t.Fatalf("manifest has %d files, want %d: %+v", len(entry.Resources), len(want), entry.Resources)
	}
	for _, res := range entry.Resources {
		raw, ok := want[res.URI]
		if !ok {
			t.Fatalf("manifest lists %q, not a fixture file", res.URI)
		}
		if res.Digest != digestOf(raw) || res.Size != len(raw) {
			t.Fatalf("%s: manifest %s/%d, file %s/%d", res.URI, res.Digest, res.Size, digestOf(raw), len(raw))
		}
	}
	block := strings.SplitN(string(want[fixtureSkillURI]), "---\n", 3)[1]
	var frontmatter map[string]any
	if err := yaml.Unmarshal([]byte(block), &frontmatter); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(frontmatter)
	gotJSON, _ := json.Marshal(entry.Frontmatter)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatalf("frontmatter %s, want %s", gotJSON, wantJSON)
	}
}

func TestSkillsListServesTheFixtureManifest(t *testing.T) {
	session := stdioSkillsClient(t, fixtureSkillFS(t), nil)
	raw, err := callSkills(t, session, methodSkillsList, nil)
	if err != nil {
		t.Fatalf("skills/list: %v", err)
	}
	var got skillsListWire
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ResultType != "complete" || got.TTLMs == nil || got.CacheScope != "public" || got.NextCursor != nil {
		t.Fatalf("skills/list envelope %s", raw)
	}
	if len(got.Skills) != 1 {
		t.Fatalf("skills/list served %d skills, want 1: %s", len(got.Skills), raw)
	}
	checkFixtureEntry(t, got.Skills[0])
	if _, err := callSkills(t, session, methodSkillsList, &rawSkillsParams{Cursor: "c1"}); err == nil {
		t.Fatal("a cursor this server never issued must be refused")
	} else {
		wantInvalidParams(t, "skills/list cursor", err)
	}
}

func TestSkillsGetAnswersTheEntry(t *testing.T) {
	session := stdioSkillsClient(t, fixtureSkillFS(t), nil)
	raw, err := callSkills(t, session, methodSkillsGet, &rawSkillsParams{URI: fixtureSkillURI})
	if err != nil {
		t.Fatalf("skills/get: %v", err)
	}
	var got skillsGetWire
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ResultType != "complete" || got.TTLMs == nil || got.CacheScope != "public" {
		t.Fatalf("skills/get envelope %s", raw)
	}
	checkFixtureEntry(t, got.Skill)
}

func TestSkillsReadServesSkillFileAndSupportingFile(t *testing.T) {
	session := stdioSkillsClient(t, fixtureSkillFS(t), nil)
	for uri, path := range map[string]string{fixtureSkillURI: fixtureSkillMDPath, fixtureSkillRefURI: fixtureSkillRefPath} {
		read, err := session.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("read %s: %v", uri, err)
		}
		if len(read.Contents) != 1 || read.Contents[0].URI != uri || read.Contents[0].Text != string(readFile(t, path)) || read.Contents[0].MIMEType != "text/markdown" {
			t.Fatalf("read %s returned %+v", uri, read.Contents)
		}
	}
}

func TestSkillsUnknownURIIsInvalidParams(t *testing.T) {
	session := stdioSkillsClient(t, fixtureSkillFS(t), nil)
	for _, uri := range []string{"skill://missing-skill/SKILL.md", fixtureSkillRefURI, "skill://fixture-notes", ""} {
		_, err := callSkills(t, session, methodSkillsGet, &rawSkillsParams{URI: uri})
		wantInvalidParams(t, "skills/get "+uri, err)
	}
	for _, uri := range []string{"skill://missing-skill/SKILL.md", "skill://fixture-notes/references/missing.md"} {
		_, err := session.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: uri})
		wantInvalidParams(t, "resources/read "+uri, err)
	}
}

func TestSkillsManifestDigestAndSizeEqualServedBytes(t *testing.T) {
	session := stdioSkillsClient(t, fixtureSkillFS(t), nil)
	raw, err := callSkills(t, session, methodSkillsGet, &rawSkillsParams{URI: fixtureSkillURI})
	if err != nil {
		t.Fatal(err)
	}
	var got skillsGetWire
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, res := range got.Skill.Resources {
		read, err := session.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: res.URI})
		if err != nil {
			t.Fatalf("read %s: %v", res.URI, err)
		}
		served := []byte(read.Contents[0].Text)
		if digestOf(served) != res.Digest || len(served) != res.Size {
			t.Fatalf("%s: manifest %s/%d, served %s/%d", res.URI, res.Digest, res.Size, digestOf(served), len(served))
		}
	}
}

func TestSkillsFrontmatterNameEqualsDirectory(t *testing.T) {
	set, err := newSkillSet(fixtureSkillFS(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range set.entries {
		segments := strings.Split(strings.TrimPrefix(entry.URI, skillScheme), "/")
		if name, _ := entry.Frontmatter["name"].(string); len(segments) != 2 || segments[0] != name || segments[1] != skillFileName {
			t.Fatalf("entry %s carries name %q", entry.URI, name)
		}
	}
	mismatch := fixtureSkillFS(t)
	mismatch["other-notes/SKILL.md"] = mismatch["fixture-notes/SKILL.md"]
	if _, err := newSkillSet(mismatch); err == nil || !strings.Contains(err.Error(), "does not equal its directory") {
		t.Fatalf("a name that differs from its directory registered: %v", err)
	}
}

func skillMD(name, description string) []byte {
	return []byte("---\nname: " + name + "\ndescription: " + description + "\n---\n\n# Body\n")
}

func TestSkillsRegistrationRefusesOverClaudeCodeCaps(t *testing.T) {
	ok := skillMD("fixture-notes", "A synthetic test skill.")
	cases := map[string]fsFixture{
		"text file over 100000 bytes": {
			"fixture-notes/SKILL.md": {Data: ok},
			"fixture-notes/big.md":   {Data: bytes.Repeat([]byte("a"), skillMaxFileBytes+1)},
		},
		"skill over 400000 bytes": {
			"fixture-notes/SKILL.md": {Data: ok},
			"fixture-notes/a.md":     {Data: bytes.Repeat([]byte("a"), skillMaxFileBytes)},
			"fixture-notes/b.md":     {Data: bytes.Repeat([]byte("b"), skillMaxFileBytes)},
			"fixture-notes/c.md":     {Data: bytes.Repeat([]byte("c"), skillMaxFileBytes)},
			"fixture-notes/d.md":     {Data: bytes.Repeat([]byte("d"), skillMaxFileBytes)},
		},
		"binary file":            {"fixture-notes/SKILL.md": {Data: ok}, "fixture-notes/blob.bin": {Data: []byte{0x89, 0x50, 0x00, 0xff}}},
		"no SKILL.md":            {"fixture-notes/README.md": {Data: ok}},
		"upper-case name":        {"Fixture-Notes/SKILL.md": {Data: skillMD("Fixture-Notes", "x")}},
		"double hyphen name":     {"fixture--notes/SKILL.md": {Data: skillMD("fixture--notes", "x")}},
		"empty description":      {"fixture-notes/SKILL.md": {Data: []byte("---\nname: fixture-notes\ndescription: \"\"\n---\n")}},
		"no frontmatter":         {"fixture-notes/SKILL.md": {Data: []byte("# Fixture notes\n")}},
		"file at the skill root": {"SKILL.md": {Data: ok}},
	}
	for name, fsys := range cases {
		if _, err := newSkillSet(fsys); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
	atCap := fsFixture{
		"fixture-notes/SKILL.md": {Data: ok},
		"fixture-notes/a.md":     {Data: bytes.Repeat([]byte("a"), skillMaxFileBytes)},
		"fixture-notes/b.md":     {Data: bytes.Repeat([]byte("b"), skillMaxFileBytes)},
		"fixture-notes/c.md":     {Data: bytes.Repeat([]byte("c"), skillMaxFileBytes-len(ok)-1)},
	}
	if _, err := newSkillSet(atCap); err != nil {
		t.Fatalf("a skill under both caps was refused: %v", err)
	}
}

// Exact capability bytes of the STDIO initialize answer with no skill: the
// answer this server gave before the skills extension existed.
const stdioCapabilitiesNoSkills = `{"completions":{},"logging":{},"prompts":{"listChanged":true},"resources":{"listChanged":true},"tools":{"listChanged":true}}`

func TestSkillsDiscoverUnchangedWithNoSkillSTDIO(t *testing.T) {
	session := stdioSkillsClient(t, nil, nil)
	got, err := json.Marshal(session.InitializeResult().Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != stdioCapabilitiesNoSkills {
		t.Fatalf("initialize capabilities %s, want %s", got, stdioCapabilitiesNoSkills)
	}
	if _, err := callSkills(t, session, methodSkillsList, nil); err == nil {
		t.Fatal("skills/list answered with no skill registered")
	}
}

func TestSkillsDeclaredOnlyWithASkillSTDIO(t *testing.T) {
	session := stdioSkillsClient(t, fixtureSkillFS(t), nil)
	got, err := json.Marshal(session.InitializeResult().Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"extensions":{"io.modelcontextprotocol/skills":{}},` + strings.TrimPrefix(stdioCapabilitiesNoSkills, "{")
	if string(got) != want {
		t.Fatalf("initialize capabilities %s, want %s", got, want)
	}
}

func TestSkillsRequestLineSTDIO(t *testing.T) {
	logs := &bytes.Buffer{}
	session := stdioSkillsClient(t, fixtureSkillFS(t), logs)
	if _, err := callSkills(t, session, methodSkillsList, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := callSkills(t, session, methodSkillsGet, &rawSkillsParams{URI: "skill://missing-skill/SKILL.md"}); err == nil {
		t.Fatal("unknown skill answered")
	}
	if _, err := session.ReadResource(context.Background(), &mcpsdk.ReadResourceParams{URI: fixtureSkillRefURI}); err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	for _, line := range strings.Split(logs.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "mcp skill request" {
			lines = append(lines, rec)
		}
	}
	want := []map[string]any{
		{"transport": "stdio", "method": "skills/list", "outcome": "served", "files": float64(2)},
		{"transport": "stdio", "method": "skills/get", "outcome": "unknown", "files": float64(0)},
		{"transport": "stdio", "method": "resources/read", "outcome": "served", "files": float64(1)},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d skill request lines, want %d: %s", len(lines), len(want), logs.String())
	}
	for i, w := range want {
		for k, v := range w {
			if lines[i][k] != v {
				t.Fatalf("line %d %s = %v, want %v", i, k, lines[i][k], v)
			}
		}
		if strings.Contains(logs.String(), "fixture-notes") || strings.Contains(logs.String(), "missing-skill") {
			t.Fatalf("a skill request line carries a URI or name: %s", logs.String())
		}
	}
}
