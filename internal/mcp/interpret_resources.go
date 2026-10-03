package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
)

const (
	uriInterpretationOutput = "acr://contract/interpretation-output"
	uriFactKinds            = "acr://guide/fact-kinds"
	uriDataCatalog          = "acr://guide/catalog"
)

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func staticResourceMeta(text, serviceVersion string) mcpsdk.Meta {
	return mcpsdk.Meta{
		"model_output_version": interpretprompt.OutputVersion,
		"prompt_version":       interpretprompt.PromptVersion,
		"sha256":               sha256Hex(text),
		"service_version":      serviceVersion,
	}
}

// registerInterpretResources serves the interpretation contract, the fact-kind
// glossary and the catalogue. The first two register only with
// investigate_question (same gate as the interpret_question prompt); the
// catalogue registers only with data_catalog and is read live for the caller's
// own credential.
func registerInterpretResources(server *mcpsdk.Server, caller *CallerContext, serviceVersion string) {
	if hostedToolEnabled(caller, toolInvestigateQuestion) {
		addStaticResource(server, uriInterpretationOutput, "interpretation-output", "Interpretation output schema",
			"JSON schema of the object the interpretation prompt returns. Validate your own interpretation against it. Version "+interpretprompt.OutputVersion+".",
			"application/schema+json", interpretprompt.OutputSchema(), serviceVersion)
		addStaticResource(server, uriFactKinds, "guide-fact-kinds", "Fact-kind glossary",
			"What each fact kind holds, which subject kinds it serves, and what it is not; the same text the interpretation prompt states.",
			guideMIME, interpretprompt.FactKindsGuide(), serviceVersion)
	}
	if hostedToolEnabled(caller, toolDataCatalog) {
		server.AddResource(&mcpsdk.Resource{
			URI: uriDataCatalog, Name: "guide-catalog", Title: "Data catalogue",
			Description: "The data_catalog answer for your credential, read live. _meta.sha256 on the read is the sha256 of the bytes served.",
			MIMEType:    "application/json",
			Annotations: &mcpsdk.Annotations{Audience: []mcpsdk.Role{"assistant"}},
		}, func(ctx context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			callerCtx, err := CallerFromContext(ctx)
			if err != nil {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "this request carries no authenticated caller identity"}
			}
			raw, err := callerCtx.client.DataCatalog(ctx, nil)
			if err != nil {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the data catalogue is unavailable"}
			}
			text := string(raw)
			meta := mcpsdk.Meta{"sha256": sha256Hex(text), "service_version": serviceVersion}
			return &mcpsdk.ReadResourceResult{Meta: meta, Contents: []*mcpsdk.ResourceContents{{
				URI: uriDataCatalog, MIMEType: "application/json", Text: text, Meta: meta,
			}}}, nil
		})
	}
}

const guideMIME = "text/markdown"

func addStaticResource(server *mcpsdk.Server, uri, name, title, description, mime, text, serviceVersion string) {
	meta := staticResourceMeta(text, serviceVersion)
	server.AddResource(&mcpsdk.Resource{
		URI: uri, Name: name, Title: title, Description: description, MIMEType: mime,
		Annotations: &mcpsdk.Annotations{Audience: []mcpsdk.Role{"assistant"}},
		Meta:        meta,
	}, func(_ context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
		return &mcpsdk.ReadResourceResult{Meta: meta, Contents: []*mcpsdk.ResourceContents{{
			URI: uri, MIMEType: mime, Text: text, Meta: meta,
		}}}, nil
	})
}
