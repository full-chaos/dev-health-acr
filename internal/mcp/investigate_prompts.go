package mcp

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

// registerInvestigatePrompts adds the static prompts that build well-formed
// investigate_question, follow-up, and source_evidence calls. Rendering reads
// only the embedded registry snapshot and the prompt arguments: no handler
// reads caller, credential, or organization state, calls a model, or reaches
// the hosted API. The investigate and continue prompts register only when the
// investigate_question tool does, so a prompt never names a tool that is
// absent; expand_evidence follows source_evidence, which is always present.
func registerInvestigatePrompts(server *mcpsdk.Server, caller *CallerContext) {
	vocab, err := guide.LoadPromptVocab()
	if err != nil {
		// The vocabulary is a compile-time embed checked by the parity tests.
		panic("mcp: " + err.Error())
	}
	for _, def := range guide.PromptDefs(vocab) {
		if def.Name != guide.PromptExpand && !hostedToolEnabled(caller, toolInvestigateQuestion) {
			continue
		}
		name := def.Name
		prompt := &mcpsdk.Prompt{Name: name, Title: def.Title, Description: def.Description}
		for _, arg := range def.Args {
			prompt.Arguments = append(prompt.Arguments, &mcpsdk.PromptArgument{
				Name: arg.Name, Title: arg.Title, Description: arg.Description, Required: arg.Required,
			})
		}
		server.AddPrompt(prompt, func(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
			var args map[string]string
			if req != nil && req.Params != nil {
				args = req.Params.Arguments
			}
			rendered, err := guide.RenderPrompt(vocab, name, args)
			if err != nil {
				var argErr *guide.ArgError
				if errors.As(err, &argErr) {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: argErr.Msg}
				}
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "prompt could not be rendered"}
			}
			return &mcpsdk.GetPromptResult{
				Description: rendered.Description,
				Messages: []*mcpsdk.PromptMessage{{
					Role:    "user",
					Content: &mcpsdk.TextContent{Text: rendered.Text},
				}},
			}, nil
		})
	}
}

// promptCompletionHandler completes closed-vocabulary prompt arguments from
// the same registry snapshot the prompts render from. It answers for prompt
// references only.
func promptCompletionHandler() func(context.Context, *mcpsdk.CompleteRequest) (*mcpsdk.CompleteResult, error) {
	vocab, err := guide.LoadPromptVocab()
	if err != nil {
		panic("mcp: " + err.Error())
	}
	return func(_ context.Context, req *mcpsdk.CompleteRequest) (*mcpsdk.CompleteResult, error) {
		values := []string{}
		if req != nil && req.Params != nil && req.Params.Ref != nil && req.Params.Ref.Type == "ref/prompt" {
			values = guide.CompletePrompt(vocab, req.Params.Ref.Name, req.Params.Argument.Name, req.Params.Argument.Value)
		}
		return &mcpsdk.CompleteResult{Completion: mcpsdk.CompletionResultDetails{Values: values, Total: len(values)}}, nil
	}
}
