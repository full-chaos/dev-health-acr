package mcp

import (
	"context"
	"encoding/json"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type elicitOutcome int

const (
	elicitUnavailable elicitOutcome = iota
	elicitAccepted
	elicitDeclined
)

type elicitOption struct {
	Value string
	Title string
}

// elicitChoice asks the user to pick one option for one named field. It is
// transport-neutral: no SDK type appears in it.
type elicitChoice struct {
	Message string
	Field   string
	Title   string
	Options []elicitOption
}

type elicitResult struct {
	Outcome elicitOutcome
	Value   string
	// Sent is true only when an elicitation/create request left the session.
	// A declined or accepted result was answered by the client, so it is sent.
	Sent bool
}

// elicitTransport carries one elicitChoice to the user. It returns
// elicitUnavailable when it cannot ask, and never a value the options do not hold.
type elicitTransport interface {
	ask(ctx context.Context, c elicitChoice) elicitResult
}

var elicitTimeout = 2 * time.Minute

// elicit never fails a tool: every outcome except an accepted, offered value
// leaves the caller on its unchanged answer.
func elicit(ctx context.Context, t elicitTransport, c elicitChoice) elicitResult {
	if t == nil || len(c.Options) < 2 {
		return elicitResult{Outcome: elicitUnavailable}
	}
	ctx, cancel := context.WithTimeout(ctx, elicitTimeout)
	defer cancel()
	res := t.ask(ctx, c)
	if res.Outcome != elicitAccepted {
		return elicitResult{Outcome: res.Outcome, Sent: res.Sent || res.Outcome == elicitDeclined}
	}
	for _, o := range c.Options {
		if o.Value == res.Value {
			res.Sent = true
			return res
		}
	}
	return elicitResult{Outcome: elicitUnavailable, Sent: true}
}

type clientElicitationCaps struct {
	Elicitation *struct {
		Form *struct{} `json:"form"`
		URL  *struct{} `json:"url"`
	} `json:"elicitation"`
}

// formElicitationAvailable reads the client capabilities of THIS request from
// raw _meta (revisions that carry them per request); a request without them
// falls back to the initialize capabilities of its session. A bare
// `elicitation: {}` means form mode.
func formElicitationAvailable(req *mcpsdk.CallToolRequest) bool {
	if req == nil {
		return false
	}
	var caps *clientElicitationCaps
	if req.Params != nil {
		if raw, ok := requestClientCapabilities(req.Params); ok {
			caps = &clientElicitationCaps{}
			if json.Unmarshal(raw, caps) != nil {
				return false
			}
		}
	}
	if caps == nil {
		return sessionFormCapable(req.Session)
	}
	if caps.Elicitation == nil {
		return false
	}
	return caps.Elicitation.Form != nil || caps.Elicitation.URL == nil
}

// sessionFormCapable reports whether the initialize capabilities of ss allow a
// form elicitation: the same gate ServerSession.Elicit applies before it sends
// anything. A bare `elicitation: {}` means form mode.
func sessionFormCapable(ss *mcpsdk.ServerSession) bool {
	if ss == nil {
		return false
	}
	ip := ss.InitializeParams()
	if ip == nil || ip.Capabilities == nil || ip.Capabilities.Elicitation == nil {
		return false
	}
	e := ip.Capabilities.Elicitation
	return e.Form != nil || e.URL == nil
}

// sessionElicitTransport is the STDIO / pre-2026-07-28 transport: an inline
// elicitation/create on the session. Hosted 2026-07-28 will add a second
// transport behind elicitTransport.
type sessionElicitTransport struct {
	session *mcpsdk.ServerSession
}

func newElicitTransport(cfg *ProcessConfig, req *mcpsdk.CallToolRequest) elicitTransport {
	if cfg.Transport() != TransportSTDIO || req == nil || req.Session == nil || !formElicitationAvailable(req) {
		return nil
	}
	return sessionElicitTransport{session: req.Session}
}

func (s sessionElicitTransport) ask(ctx context.Context, c elicitChoice) elicitResult {
	if !sessionFormCapable(s.session) {
		return elicitResult{Outcome: elicitUnavailable}
	}
	oneOf := make([]map[string]any, 0, len(c.Options))
	for _, o := range c.Options {
		oneOf = append(oneOf, map[string]any{"const": o.Value, "title": o.Title})
	}
	res, err := s.session.Elicit(ctx, &mcpsdk.ElicitParams{
		Mode:    "form",
		Message: c.Message,
		RequestedSchema: map[string]any{
			"type":     "object",
			"required": []string{c.Field},
			"properties": map[string]any{
				c.Field: map[string]any{"type": "string", "title": c.Title, "oneOf": oneOf},
			},
		},
	})
	if err != nil || res == nil {
		return elicitResult{Outcome: elicitUnavailable, Sent: true}
	}
	if res.Action != "accept" {
		return elicitResult{Outcome: elicitDeclined, Sent: true}
	}
	v, ok := res.Content[c.Field].(string)
	if !ok {
		return elicitResult{Outcome: elicitUnavailable, Sent: true}
	}
	return elicitResult{Outcome: elicitAccepted, Value: v, Sent: true}
}
