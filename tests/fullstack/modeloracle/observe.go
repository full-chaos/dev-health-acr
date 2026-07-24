package main

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
)

func newTrimReader(raw []byte) io.Reader { return bytes.NewReader(bytes.TrimSpace(raw)) }

// EvidenceSighting is one evidence reference the run actually returned, paired with the
// entity it points at.
//
// The wire evidence_ref_id is an opaque signed token, so it cannot be predicted or pattern
// matched. The entity tuple is the stable identity. The pairing is available because
// ranking.go emits exactly one packet item per evidence reference, carrying that single
// reference and a single related entity.
type EvidenceSighting struct {
	EvidenceRefID string `json:"evidence_ref_id"`
	EntityType    string `json:"entity_type"`
	EntityID      string `json:"entity_id"`
	// Label is the display label the run actually returned for this reference. The final
	// answer's wording is built from it, so a claim's prose is evidence the live path
	// produced rather than text copied out of the plan.
	Label    string `json:"label,omitempty"`
	Expanded bool   `json:"expanded"`
}

// Observation is what the scripted model learned from live tool responses. Every field the
// final answer reports comes from here, never from the plan, so the harness cannot pass with
// a broken ACR read path.
type Observation struct {
	PacketStatus    string             `json:"packet_status"`
	ScopeResolution string             `json:"scope_resolution"`
	Sightings       []EvidenceSighting `json:"sightings"`
	Warnings        []string           `json:"warnings"`
}

// EvidenceRefIDs returns every reference seen, in stable order.
func (o Observation) EvidenceRefIDs() []string {
	out := make([]string, 0, len(o.Sightings))
	for _, sighting := range o.Sightings {
		out = append(out, sighting.EvidenceRefID)
	}
	return out
}

func (o *Observation) merge(other Observation) {
	if other.PacketStatus != "" {
		o.PacketStatus = other.PacketStatus
	}
	if other.ScopeResolution != "" {
		o.ScopeResolution = other.ScopeResolution
	}
	for _, sighting := range other.Sightings {
		o.addSighting(sighting)
	}
	o.Warnings = mergeUnique(o.Warnings, other.Warnings)
}

// addSighting keeps one entry per reference and lets a later expansion enrich an earlier
// packet-only sighting rather than duplicating it.
func (o *Observation) addSighting(sighting EvidenceSighting) {
	if sighting.EvidenceRefID == "" {
		return
	}
	for i := range o.Sightings {
		if o.Sightings[i].EvidenceRefID != sighting.EvidenceRefID {
			continue
		}
		if sighting.EntityType != "" {
			o.Sightings[i].EntityType = sighting.EntityType
		}
		if sighting.EntityID != "" {
			o.Sightings[i].EntityID = sighting.EntityID
		}
		if sighting.Label != "" {
			o.Sightings[i].Label = sighting.Label
		}
		o.Sightings[i].Expanded = o.Sightings[i].Expanded || sighting.Expanded
		return
	}
	o.Sightings = append(o.Sightings, sighting)
}

func (o *Observation) sortSightings() {
	sort.SliceStable(o.Sightings, func(i, j int) bool {
		if o.Sightings[i].EntityType != o.Sightings[j].EntityType {
			return o.Sightings[i].EntityType < o.Sightings[j].EntityType
		}
		if o.Sightings[i].EntityID != o.Sightings[j].EntityID {
			return o.Sightings[i].EntityID < o.Sightings[j].EntityID
		}
		return o.Sightings[i].EvidenceRefID < o.Sightings[j].EvidenceRefID
	})
}

func mergeUnique(into, from []string) []string {
	seen := make(map[string]bool, len(into))
	out := make([]string, 0, len(into)+len(from))
	for _, values := range [][]string{into, from} {
		for _, value := range values {
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// observeToolResult reads an MCP response envelope structurally rather than pinning one
// shape, because context_for_task, source_evidence and the sidecar's rendered wrapper all
// nest the payload differently.
func observeToolResult(payload string) Observation {
	var decoded any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		if looksLikeSidecarMarkdown(payload) {
			return observeSidecarMarkdown(payload)
		}
		return Observation{}
	}
	var observation Observation
	walk(decoded, &observation)
	observation.sortSightings()
	return observation
}

func walk(node any, out *Observation) {
	switch value := node.(type) {
	case map[string]any:
		readPacket(value, out)
		readPacketItem(value, out)
		readExpandedEvidence(value, out)
		// The sidecar may hand a structured payload back as an embedded JSON string.
		// A client that wraps the result in its own JSON envelope leaves the payload as an
		// embedded string, which is either the JSON contract or the sidecar's markdown.
		for _, key := range []string{"text", "output", "structured"} {
			nested, ok := value[key].(string)
			if !ok {
				continue
			}
			if looksLikeJSON(nested) || looksLikeSidecarMarkdown(nested) {
				out.merge(observeToolResult(nested))
			}
		}
		for _, child := range value {
			walk(child, out)
		}
	case []any:
		for _, child := range value {
			walk(child, out)
		}
	}
}

// readPacket only trusts a node that carries the packet's own identity field, so an
// unrelated nested "status" — a CI evidence citation, for instance — cannot be mistaken for
// the packet status.
func readPacket(value map[string]any, out *Observation) {
	if _, isPacket := value["context_packet_id"]; !isPacket {
		return
	}
	if status, ok := value["status"].(string); ok {
		out.PacketStatus = status
	}
	if scope, ok := value["resolved_scope"].(map[string]any); ok {
		if resolution, ok := scope["resolution"].(string); ok {
			out.ScopeResolution = resolution
		}
	}
	if warnings, ok := value["warnings"].([]any); ok {
		for _, warning := range warnings {
			if text, ok := warning.(string); ok {
				out.Warnings = append(out.Warnings, text)
			}
		}
	}
}

// readPacketItem pairs an item's single evidence reference with its single related entity.
// A malformed item that carries several of either is recorded without an entity rather than
// guessing a pairing.
func readPacketItem(value map[string]any, out *Observation) {
	rawIDs, hasIDs := value["evidence_ref_ids"].([]any)
	if !hasIDs {
		return
	}
	entities, _ := value["related_entities"].([]any)
	unambiguous := len(rawIDs) == 1 && len(entities) == 1
	for index, rawID := range rawIDs {
		id, ok := rawID.(string)
		if !ok || id == "" {
			continue
		}
		sighting := EvidenceSighting{EvidenceRefID: id}
		if unambiguous {
			if entity, ok := entities[index].(map[string]any); ok {
				sighting.EntityType, _ = entity["type"].(string)
				sighting.EntityID, _ = entity["id"].(string)
			}
		}
		out.addSighting(sighting)
	}
}

// readExpandedEvidence reads expanded_evidence.v1, which is the authoritative mapping from a
// reference to its source entity.
func readExpandedEvidence(value map[string]any, out *Observation) {
	if _, expanded := value["availability"]; !expanded {
		return
	}
	id, ok := value["evidence_ref_id"].(string)
	if !ok || id == "" {
		return
	}
	sighting := EvidenceSighting{EvidenceRefID: id, Expanded: true}
	if source, ok := value["source"].(map[string]any); ok {
		sighting.EntityType, _ = source["entity_type"].(string)
		sighting.EntityID, _ = source["entity_id"].(string)
	}
	out.addSighting(sighting)
}

func looksLikeJSON(value string) bool {
	trimmed := bytes.TrimSpace([]byte(value))
	return len(trimmed) > 1 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// isEmpty reports that a tool result told the model nothing at all — no packet status, no
// scope, no evidence, no warnings.
func (o Observation) isEmpty() bool {
	return o.PacketStatus == "" && o.ScopeResolution == "" && len(o.Sightings) == 0 && len(o.Warnings) == 0
}
