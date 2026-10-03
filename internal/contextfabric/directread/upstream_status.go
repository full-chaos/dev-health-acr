package directread

import (
	"encoding/json"
	"regexp"
)

// UpstreamGraphQLCode is the closed token for the upstream GraphQL error
// code (errors[0].extensions.code). Any code outside the list is "other".
type UpstreamGraphQLCode string

const (
	GraphQLCodeValidationFailed UpstreamGraphQLCode = "graphql_validation_failed"
	GraphQLCodeParseFailed      UpstreamGraphQLCode = "graphql_parse_failed"
	GraphQLCodeMCPRefused       UpstreamGraphQLCode = "mcp_refused"
	GraphQLCodeReadBudget       UpstreamGraphQLCode = "mcp_read_budget_exceeded"
	GraphQLCodeOther            UpstreamGraphQLCode = "other"
)

// UpstreamGraphQLCodeVocabulary is the closed set of GraphQL code tokens.
func UpstreamGraphQLCodeVocabulary() [5]UpstreamGraphQLCode {
	return [5]UpstreamGraphQLCode{GraphQLCodeValidationFailed, GraphQLCodeParseFailed, GraphQLCodeMCPRefused, GraphQLCodeReadBudget, GraphQLCodeOther}
}

var upstreamGraphQLCodes = map[string]UpstreamGraphQLCode{
	"GRAPHQL_VALIDATION_FAILED": GraphQLCodeValidationFailed,
	"GRAPHQL_PARSE_FAILED":      GraphQLCodeParseFailed,
	MCPRefusedCode:              GraphQLCodeMCPRefused,
	MCPReadBudgetExceededCode:   GraphQLCodeReadBudget,
}

// upstreamVariableMaxLen bounds a variable name carried from an upstream
// GraphQL error path.
const upstreamVariableMaxLen = 64

var graphQLNamePattern = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)

// callerFault says whether the code is a GraphQL protocol rejection of the
// request document or its variables (upstream HTTP 422).
func (c UpstreamGraphQLCode) callerFault() bool {
	return c == GraphQLCodeValidationFailed || c == GraphQLCodeParseFailed
}

// parseUpstreamGraphQLError reads closed tokens from a GraphQL error
// envelope: errors[0].extensions.code and, when path is ["variable", name],
// the name. The message text is never read out. A body that is not an
// envelope yields empty values.
func parseUpstreamGraphQLError(body []byte) (UpstreamGraphQLCode, string) {
	var answer struct {
		Errors []struct {
			Path       []json.RawMessage `json:"path"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &answer) != nil || len(answer.Errors) == 0 {
		return "", ""
	}
	first := answer.Errors[0]
	code := UpstreamGraphQLCode("")
	if first.Extensions.Code != "" {
		code = GraphQLCodeOther
		if known, ok := upstreamGraphQLCodes[first.Extensions.Code]; ok {
			code = known
		}
	}
	variable := ""
	if len(first.Path) >= 2 {
		var head, name string
		if json.Unmarshal(first.Path[0], &head) == nil && head == "variable" && json.Unmarshal(first.Path[1], &name) == nil &&
			len(name) <= upstreamVariableMaxLen && graphQLNamePattern.MatchString(name) {
			variable = name
		}
	}
	return code, variable
}
