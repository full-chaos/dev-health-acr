// Package guide serves the static MCP guide resources. The text in
// zz_generated.go is generated from the ACR registries by ./gen and is
// identical for every caller. This package links no engine code.
package guide

import "fmt"

//go:generate go run ./gen

// MIMEType is the media type of every guide resource.
const MIMEType = "text/markdown"

// Resource describes one guide resource.
type Resource struct {
	URI         string
	File        string
	Name        string
	Title       string
	Description string
}

// URIs of the guide resources.
const (
	URIQuestions    = "acr://guide/questions"
	URIVocabulary   = "acr://guide/vocabulary"
	URIConversation = "acr://guide/conversation"
)

var resources = []Resource{
	{
		URI: URIQuestions, File: "questions.md", Name: "guide-questions", Title: "What ACR can answer",
		Description: "Question families ACR answers, one example question each, and which tool to call. Read before asking an organization, team, or project question.",
	},
	{
		URI: URIVocabulary, File: "vocabulary.md", Name: "guide-vocabulary", Title: "ACR vocabulary",
		Description: "Subject kinds, cohort-discoverable kinds, handle grammar, evidence windows, result statuses, and render kinds.",
	},
	{
		URI: URIConversation, File: "conversation.md", Name: "guide-conversation", Title: "Follow-ups, clarification, and evidence",
		Description: "How to answer a clarification with receipts, confirm a window, carry a conversation, fetch a stored result, and expand evidence.",
	},
}

// Resources returns the resource list in registration order.
func Resources() []Resource {
	out := make([]Resource, len(resources))
	copy(out, resources)
	return out
}

// Text returns the embedded text of a resource.
func Text(uri string) (string, error) {
	for _, r := range resources {
		if r.URI != uri {
			continue
		}
		text, ok := generated[r.File]
		if !ok || text == "" {
			return "", fmt.Errorf("guide: no generated text for %s", r.File)
		}
		return text, nil
	}
	return "", fmt.Errorf("guide: unknown resource %q", uri)
}
