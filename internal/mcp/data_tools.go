package mcp

import (
	"context"
	"encoding/json"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Direct data tools (CHAOS-7072, design C and H): data_catalog,
// find_subjects and run_operation. They are for a client whose own model
// plans the reads; every one of them is model-free on our side. Each is
// registered ONLY when the hosted capabilities response advertises its name
// for this caller: the hosted API advertises data_catalog and find_subjects
// when a graph is composed and run_operation only for a credential with the
// data:read scope on a deployment that can serve it, so registering
// unconditionally would offer a tool every call fails.
const (
	toolDataCatalog  = "data_catalog"
	toolFindSubjects = "find_subjects"
	toolRunOperation = "run_operation"
)

// registerDataTools adds the direct data tools this caller was granted.
func registerDataTools(server *mcpsdk.Server, cfg *ProcessConfig, caller *CallerContext) {
	if hostedToolEnabled(caller, toolDataCatalog) {
		server.AddTool(
			buildTool(toolDataCatalog, "Data catalog", dataCatalogRequestSchemaFile, dataCatalogResponseSchemaFile),
			func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				return handleDataCatalog(ctx, cfg, req)
			},
		)
	}
	if hostedToolEnabled(caller, toolFindSubjects) {
		server.AddTool(
			buildTool(toolFindSubjects, "Find subjects", findSubjectsRequestSchemaFile, findSubjectsResponseSchemaFile),
			func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				return handleFindSubjects(ctx, cfg, req)
			},
		)
	}
	if hostedToolEnabled(caller, toolRunOperation) {
		server.AddTool(
			buildTool(toolRunOperation, "Run operation", runOperationRequestSchemaFile, runOperationResponseSchemaFile),
			func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				return handleRunOperation(ctx, cfg, req)
			},
		)
	}
}

// rawToolResult returns the hosted API JSON as structured content, byte for
// byte, with a bounded text summary as the human-readable block. Nothing is
// added to the JSON and nothing is dropped from it.
func rawToolResult(raw json.RawMessage, text string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
		StructuredContent: raw,
	}
}

// dataToolsInstructions is the additive part of the server instructions
// (design H, "Two ways to use this server"). It names only tools this server
// registered, and says nothing about a tool that does not exist in this
// release: more data tools are planned, none is named.
func dataToolsInstructions(caller *CallerContext) string {
	catalog := hostedToolEnabled(caller, toolDataCatalog)
	find := hostedToolEnabled(caller, toolFindSubjects)
	run := hostedToolEnabled(caller, toolRunOperation)
	if !catalog && !find && !run {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nTwo ways to use this server.\n")
	b.WriteString("A. You plan the reads yourself with the data tools, and do the comparison, ranking, charting and explanation:\n")
	if catalog {
		b.WriteString("- data_catalog: the operations, subject kinds and limits you may use.\n")
	}
	if find {
		b.WriteString("- find_subjects: names to ids, and lists of subjects.\n")
	}
	if run {
		b.WriteString("- run_operation: product analytics and lists, by allowlisted operation name.\n")
	}
	b.WriteString("More data tools are planned.\n")
	if hostedToolEnabled(caller, toolInvestigateQuestion) {
		b.WriteString("B. You want our engine's narrative answer: investigate_question. It is built for callers with no model of their own. If you are a model, prefer A.\n")
	}
	b.WriteString("Rules for A:\n")
	b.WriteString("- Read call, completeness, result and coverage first. Missing is not healthy and not zero.\n")
	b.WriteString("- completeness \"unknown\" means unknown. Do not say \"complete\". Do not fill gaps in a series.\n")
	b.WriteString("- No person-level data is served. Do not rank persons. A relation is not a cause.\n")
	b.WriteString("- When you derive a number, say it is yours, show its inputs and state the measure you rank by. Use \"appears\", \"leans\", \"suggests\" for derived statements.\n")
	b.WriteString("- \"last month\" = previous calendar month. \"in the last month\" = trailing 30 days. A team = the repositories and projects it owns.\n")
	b.WriteString("- Never build an id. Take ids from find_subjects or from a response.\n")
	b.WriteString("- Investment per team or repository is not served in this release: only org-wide by theme, subcategory and work type.\n")
	return b.String()
}
