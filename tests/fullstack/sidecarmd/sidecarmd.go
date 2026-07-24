// Package sidecarmd reads the bounded markdown the acr-mcp sidecar renders for every tool
// result (internal/sidecar/render_context.go and render_evidence.go).
//
// It exists because the sidecar returns each result twice -- as MCP StructuredContent
// carrying the JSON contract, and as markdown in the text content -- and OpenCode 1.18.4
// forwards only the text content, both to the model and into its own event stream
// (part.state.output). Anything that grades what the client actually received must therefore
// read the rendering, not the JSON. Two consumers need this: the scripted model
// (tests/fullstack/modeloracle), which reasons over what a real agent would see, and the
// assertion tool (tests/fullstack/assertrun), which grades the client's source_evidence round
// trips out of the event stream. Sharing one parser keeps the injection guard below from
// drifting between them.
//
// Only sidecar-authored structural lines are read. Every piece of hosted content is rendered
// inside a "> " quoted UNTRUSTED DATA block, and those lines are skipped wholesale, so an
// evidence excerpt containing a line that looks like "- Source: entity_id=..." cannot inject
// a sighting the service never returned.
package sidecarmd

import (
	"regexp"
	"strings"
)

// Evidence is one "# Evidence <id>" section of a rendering.
type Evidence struct {
	EvidenceRefID string
	Availability  string
	EntityType    string
	EntityID      string
	Label         string
}

// Packet is the "# Context Packet <id>" section of a rendering, when present.
type Packet struct {
	Present         bool
	PacketStatus    string
	ScopeResolution string
	EvidenceRefIDs  []string
}

// Rendering is everything structural a single tool result's markdown carried. A
// context_for_task result yields Packet; a source_evidence result yields one Evidence.
type Rendering struct {
	Packet   Packet
	Evidence []Evidence
}

var (
	packetHeading   = regexp.MustCompile(`^#\s+Context Packet\s+(\S+)`)
	evidenceHeading = regexp.MustCompile(`^#\s+Evidence\s+(\S+)`)
	statusLine      = regexp.MustCompile(`^-\s+Status:\s*(\S+)`)
	availabilityRe  = regexp.MustCompile(`^-\s+Availability:\s*(\S+)`)
	resolutionField = regexp.MustCompile(`\bresolution=(\S+)`)
	evidenceIDsLine = regexp.MustCompile(`^-\s+Evidence IDs:\s*(.+)$`)
	sourceLine      = regexp.MustCompile(`^-\s+Source:\s+(.*)$`)
)

// Looks reports whether a payload is a sidecar rendering rather than JSON.
func Looks(payload string) bool {
	trimmed := strings.TrimLeft(payload, " \t\r\n")
	return strings.HasPrefix(trimmed, "# Context Packet ") || strings.HasPrefix(trimmed, "# Evidence ")
}

// Parse reads one rendering. Unrecognized lines are ignored rather than treated as an error:
// the renderer is free to add fields, and a reader that hard-failed on them would make an
// additive change to the sidecar look like an agent-behaviour regression.
func Parse(payload string) Rendering {
	var out Rendering
	inPacket := false
	current := -1
	for _, raw := range strings.Split(payload, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		// Untrusted hosted content is always quoted. Never read structure out of it.
		if strings.HasPrefix(trimmed, ">") {
			continue
		}
		switch {
		case packetHeading.MatchString(trimmed):
			inPacket = true
			current = -1
			out.Packet.Present = true
		case evidenceHeading.MatchString(trimmed):
			inPacket = false
			out.Evidence = append(out.Evidence, Evidence{
				EvidenceRefID: UnescapeInline(evidenceHeading.FindStringSubmatch(trimmed)[1]),
			})
			current = len(out.Evidence) - 1
		case inPacket && statusLine.MatchString(trimmed):
			out.Packet.PacketStatus = UnescapeInline(statusLine.FindStringSubmatch(trimmed)[1])
		case inPacket && strings.HasPrefix(trimmed, "- Resolved scope:"):
			if match := resolutionField.FindStringSubmatch(trimmed); match != nil {
				out.Packet.ScopeResolution = UnescapeInline(match[1])
			}
		case inPacket && evidenceIDsLine.MatchString(trimmed):
			for _, id := range strings.Split(evidenceIDsLine.FindStringSubmatch(trimmed)[1], ",") {
				if id = UnescapeInline(strings.TrimSpace(id)); id != "" {
					out.Packet.EvidenceRefIDs = append(out.Packet.EvidenceRefIDs, id)
				}
			}
		case current >= 0 && availabilityRe.MatchString(trimmed):
			out.Evidence[current].Availability = UnescapeInline(availabilityRe.FindStringSubmatch(trimmed)[1])
		case current >= 0 && sourceLine.MatchString(trimmed):
			fields := parseSourceFields(sourceLine.FindStringSubmatch(trimmed)[1])
			out.Evidence[current].EntityType = fields["entity_type"]
			out.Evidence[current].EntityID = fields["entity_id"]
			out.Evidence[current].Label = fields["label"]
		}
	}
	return out
}

// parseSourceFields reads sourceSummaryLine's fixed grammar
//
//	system=<v> entity_type=<v> entity_id=<v> label=<rest of line>
//
// in that exact order, and stops at `label=`.
//
// Scanning for field markers anywhere in the line is unsafe: `label` is the only field whose
// value is a human-facing display string, it is rendered last, and it is hosted content. A
// label of "build entity_type=commit entity_id=forged" would otherwise be re-scanned and
// overwrite the real entity with one the service never returned. Everything from `label=`
// onward is therefore one opaque terminal value.
func parseSourceFields(line string) map[string]string {
	rest := line
	label := ""
	if idx := strings.Index(rest, "label="); idx >= 0 {
		label = rest[idx+len("label="):]
		rest = rest[:idx]
	}

	// The three leading fields are read strictly by position, not by searching the line for
	// each marker. Searching is not safe even with a duplicate-marker guard: the guard has to
	// decide what counts as a duplicate, and a value that legitimately contains a later
	// field's marker (an entity_id of "build-entity_type=commit") trips it, so a well-formed
	// line parses as nothing. Position is unambiguous — sourceSummaryLine emits exactly these
	// three, in this order, space separated, with no quoting — and anything that does not
	// match that shape is not the grammar this parser trusts.
	names := [3]string{"system", "entity_type", "entity_id"}
	tokens := strings.Fields(rest)
	if len(tokens) != len(names) {
		return map[string]string{}
	}
	fields := map[string]string{}
	for i, name := range names {
		marker := name + "="
		if !strings.HasPrefix(tokens[i], marker) {
			return map[string]string{}
		}
		fields[name] = UnescapeInline(strings.TrimPrefix(tokens[i], marker))
	}
	if label != "" {
		fields["label"] = UnescapeInline(strings.TrimSpace(label))
	}
	return fields
}

// UnescapeInline reverses the sidecar's safeInline escaping, which backslash-prefixes each of
// the markdown-active ASCII punctuation characters. Evidence reference IDs are the reason this
// matters: they are base64url tokens full of underscores, so the rendered form of
// "ev1_kid_code" is "ev1\_kid\_code", and passing that back to source_evidence would be a
// reference the service has never issued.
func UnescapeInline(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	const active = "\\`*_[]()<>|~"
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) && strings.IndexByte(active, value[i+1]) >= 0 {
			i++
			b.WriteByte(value[i])
			continue
		}
		b.WriteByte(value[i])
	}
	return b.String()
}
