package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

// registerGuideResources adds the static guide resources. They are read-only,
// embedded, generated from the ACR registries, and identical for every
// caller with the same tool catalogue: the text only varies with whether
// investigate_question is listed, and no handler reads credential or
// organization state.
func registerGuideResources(server *mcpsdk.Server, caller *CallerContext) {
	serviceModel := hostedToolEnabled(caller, toolInvestigateQuestion)
	for _, res := range guide.Resources() {
		uri := res.URI
		server.AddResource(&mcpsdk.Resource{
			URI:         uri,
			Name:        res.Name,
			Title:       res.Title,
			Description: res.Description,
			MIMEType:    guide.MIMEType,
			Annotations: &mcpsdk.Annotations{Audience: []mcpsdk.Role{"assistant"}},
		}, func(_ context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			text, err := guide.TextFor(uri, serviceModel)
			if err != nil {
				return nil, mcpsdk.ResourceNotFoundError(uri)
			}
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
				URI: uri, MIMEType: guide.MIMEType, Text: text,
			}}}, nil
		})
	}
}
