package mcp

import (
	"context"
	_ "embed"
	"encoding/json"
	"slices"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP Apps extension (ext-apps specification 2026-01-26): a tool names a
// ui:// resource in _meta.ui.resourceUri and a host that renders apps shows
// that HTML with the tool result's structuredContent.
const (
	appViewExtension  = "io.modelcontextprotocol/ui"
	appViewMIMEType   = "text/html;profile=mcp-app"
	appViewAnswerURI  = "ui://dev-health/investigate-answer"
	appViewLegacyMeta = "ui/resourceUri"
)

//go:embed appview/answer.html
var appViewAnswerHTML string

// registerAppView serves the answer view next to investigate_question. The
// view reads only the structuredContent the host hands it; it calls no tool
// and makes no network request, so it opens no path around authorization.
func registerAppView(server *mcpsdk.Server, cfg *ProcessConfig, caller *CallerContext) {
	if !hostedToolEnabled(caller, toolInvestigateQuestion) {
		return
	}
	server.AddResource(&mcpsdk.Resource{
		URI: appViewAnswerURI, Name: "investigate-answer-view", Title: "Investigation answer view",
		Description: "Interactive view of an investigate_question answer, for hosts that render MCP Apps.",
		MIMEType:    appViewMIMEType,
	}, func(ctx context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		if ok, reason := liveToolEnabled(ctx, caller, toolInvestigateQuestion); !ok {
			logSurfaceRefusal(ctx, cfg, "resource", appViewAnswerURI, reason)
			return nil, mcpsdk.ResourceNotFoundError(appViewAnswerURI)
		}
		return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
			URI: appViewAnswerURI, MIMEType: appViewMIMEType, Text: appViewAnswerHTML,
		}}}, nil
	})
}

// appViewMiddleware shows the view only to a client that declares it can
// render it: investigate_question gains its _meta.ui entry in tools/list and
// the view appears in resources/list. Every other client gets both lists
// unchanged. Tool results are never touched.
func appViewMiddleware() mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || (method != "tools/list" && method != "resources/list") {
				return result, err
			}
			declared := clientDeclaresAppView(req)
			switch listed := result.(type) {
			case *mcpsdk.ListToolsResult:
				if declared {
					withAppViewMeta(listed)
				}
			case *mcpsdk.ListResourcesResult:
				if !declared {
					listed.Resources = slices.DeleteFunc(slices.Clone(listed.Resources), func(r *mcpsdk.Resource) bool {
						return r != nil && strings.HasPrefix(r.URI, "ui://")
					})
				}
			}
			return result, nil
		}
	}
}

// withAppViewMeta copies the listed investigate_question tool before it sets
// _meta: the listed pointers are the server's registered tools.
func withAppViewMeta(listed *mcpsdk.ListToolsResult) {
	tools := slices.Clone(listed.Tools)
	for i, tool := range tools {
		if tool == nil || tool.Name != toolInvestigateQuestion {
			continue
		}
		copied := *tool
		meta := mcpsdk.Meta{}
		for k, v := range tool.Meta {
			meta[k] = v
		}
		meta["ui"] = map[string]any{"resourceUri": appViewAnswerURI}
		meta[appViewLegacyMeta] = appViewAnswerURI
		copied.Meta = meta
		tools[i] = &copied
	}
	listed.Tools = tools
}

// clientDeclaresAppView reports whether the client lists the MCP Apps
// extension with the HTML app MIME type: in THIS request's _meta
// capabilities when it sends them, else in its initialize capabilities.
func clientDeclaresAppView(req mcpsdk.Request) bool {
	var raw json.RawMessage
	if req != nil && !isNilValue(req.GetParams()) {
		raw, _ = requestClientCapabilities(req.GetParams())
	}
	if raw == nil && req != nil {
		ss, _ := req.GetSession().(*mcpsdk.ServerSession)
		if ss == nil {
			return false
		}
		ip := ss.InitializeParams()
		if ip == nil || ip.Capabilities == nil {
			return false
		}
		encoded, err := json.Marshal(ip.Capabilities)
		if err != nil {
			return false
		}
		raw = encoded
	}
	if raw == nil {
		return false
	}
	var caps struct {
		Extensions map[string]struct {
			MIMETypes []string `json:"mimeTypes"`
		} `json:"extensions"`
	}
	if json.Unmarshal(raw, &caps) != nil {
		return false
	}
	ext, ok := caps.Extensions[appViewExtension]
	return ok && slices.Contains(ext.MIMETypes, appViewMIMEType)
}
