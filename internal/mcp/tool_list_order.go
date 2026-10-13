package mcp

import (
	"context"
	"slices"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolListRank is the order tools/list serves. The SDK sorts a list by tool
// name, which puts investigate_question ahead of every fact-reading tool; a
// model reads the list top down, so the tools that call no model come first
// and investigate_question comes last.
var toolListRank = map[string]int{
	toolContextForTask:                0,
	toolDataCatalog:                   1,
	toolFindSubjects:                  2,
	toolReadFacts:                     3,
	toolReadRelationships:             4,
	toolRunOperation:                  5,
	toolGraphQLQuery:                  6,
	toolSourceEvidence:                7,
	toolInvestigationResult:           8,
	toolInvestigateWithInterpretation: 9,
	toolInvestigateQuestion:           10,
	toolRecordEpisode:                 11,
}

func toolListRankOf(name string) int {
	if rank, ok := toolListRank[name]; ok {
		return rank
	}
	return len(toolListRank)
}

func toolListOrderMiddleware() mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || method != "tools/list" {
				return result, err
			}
			if listed, ok := result.(*mcpsdk.ListToolsResult); ok && listed != nil {
				slices.SortStableFunc(listed.Tools, func(a, b *mcpsdk.Tool) int {
					return toolListRankOf(a.Name) - toolListRankOf(b.Name)
				})
			}
			return result, err
		}
	}
}
