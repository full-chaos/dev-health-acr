package mcp

import (
	"context"
	"path"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// skillsCapability is the extension object the server declares: empty, so
// directoryRead is its default false and resources/directory/read is never
// served.
func skillsCapability() map[string]any { return map[string]any{} }

type skillsListParams struct {
	mcpsdk.ParamsBase
	Cursor string `json:"cursor,omitempty"`
}

type skillsGetParams struct {
	mcpsdk.ParamsBase
	URI string `json:"uri"`
}

type skillsListResult struct {
	mcpsdk.ResultBase
	ResultType string       `json:"resultType"`
	Skills     []skillEntry `json:"skills"`
	mcpsdk.Cacheable
}

type skillsGetResult struct {
	mcpsdk.ResultBase
	ResultType string     `json:"resultType"`
	Skill      skillEntry `json:"skill"`
	mcpsdk.Cacheable
}

// skillsCacheable gives skills/list and skills/get the cache fields every
// other result of this transport carries: the hosted server marks each
// answer private and immediately stale (privateCacheable); STDIO keeps the
// protocol defaults.
func skillsCacheable(cfg *ProcessConfig) mcpsdk.Cacheable {
	if cfg.Transport() == TransportHTTP {
		return mcpsdk.Cacheable{TTLMs: 0, CacheScope: "private"}
	}
	return mcpsdk.Cacheable{TTLMs: 0, CacheScope: "public"}
}

const (
	skillOutcomeServed  = "served"
	skillOutcomeUnknown = "unknown"
)

// logSkillRequest writes the one mcp.skill_request line of a skills call or a
// skill file read: the method, whether it was served, and how many files it
// described or returned. No URI, name or content is on it.
func logSkillRequest(ctx context.Context, cfg *ProcessConfig, method, outcome string, files int) {
	if cfg == nil || cfg.diagnostics == nil {
		return
	}
	transport := TransportSTDIO
	if cfg.Transport() == TransportHTTP {
		transport = TransportHTTP
	}
	fields := eventspec.NewMCPSkillRequestFields(transport, method, outcome, files)
	cfg.diagnostics.InfoContext(ctx, eventspec.MCPSkillRequest.Msg, fields.SlogArgs()...)
}

func unknownSkillError(uri string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "No skill is served at " + boundedURI(uri)}
}

// boundedURI keeps an untrusted URI short enough to echo in an error.
func boundedURI(uri string) string {
	const limit = 256
	if len(uri) <= limit {
		return uri
	}
	return strings.ToValidUTF8(uri[:limit], "") + "..."
}

// registerSkills serves the set: skills/list, skills/get and one resource per
// file. With no skill it registers nothing, so nothing a client sees changes.
func registerSkills(server *mcpsdk.Server, cfg *ProcessConfig) error {
	set := cfg.skills
	if set.empty() {
		return nil
	}
	for _, file := range set.files {
		res := &mcpsdk.Resource{URI: file.uri, Name: path.Base(file.uri), MIMEType: file.mime}
		if i, ok := set.byURI[file.uri]; ok {
			res.Name, _ = set.entries[i].Frontmatter["name"].(string)
			res.Description, _ = set.entries[i].Frontmatter["description"].(string)
		}
		served := file
		server.AddResource(res, func(ctx context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			logSkillRequest(ctx, cfg, "resources/read", skillOutcomeServed, 1)
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
				URI: served.uri, MIMEType: served.mime, Text: served.text,
			}}}, nil
		})
	}
	if err := mcpsdk.AddReceivingCustomMethod(server, methodSkillsList, func(ctx context.Context, _ *mcpsdk.ServerSession, params *skillsListParams) (*skillsListResult, error) {
		if params != nil && params.Cursor != "" {
			logSkillRequest(ctx, cfg, methodSkillsList, skillOutcomeUnknown, 0)
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid cursor"}
		}
		files := 0
		for _, entry := range set.entries {
			files += len(entry.Resources)
		}
		logSkillRequest(ctx, cfg, methodSkillsList, skillOutcomeServed, files)
		return &skillsListResult{ResultType: "complete", Skills: set.entries, Cacheable: skillsCacheable(cfg)}, nil
	}); err != nil {
		return err
	}
	return mcpsdk.AddReceivingCustomMethod(server, methodSkillsGet, func(ctx context.Context, _ *mcpsdk.ServerSession, params *skillsGetParams) (*skillsGetResult, error) {
		uri := ""
		if params != nil {
			uri = params.URI
		}
		i, ok := set.byURI[uri]
		if !ok {
			logSkillRequest(ctx, cfg, methodSkillsGet, skillOutcomeUnknown, 0)
			return nil, unknownSkillError(uri)
		}
		entry := set.entries[i]
		logSkillRequest(ctx, cfg, methodSkillsGet, skillOutcomeServed, len(entry.Resources))
		return &skillsGetResult{ResultType: "complete", Skill: entry, Cacheable: skillsCacheable(cfg)}, nil
	})
}
