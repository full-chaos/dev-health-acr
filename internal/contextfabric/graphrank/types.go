package graphrank

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CandidateNode is a backend-neutral view of one graph node search/lookup
// result: the canonical attributes an adapter projected onto it, plus
// whatever relevance/score the backend's search returned. A backend adapter
// converts its own wire node type (zep.EntityNode, a FalkorDB record, ...)
// into this shape before handing it to this package.
type CandidateNode struct {
	// UUID is the backend's own identifier for this node. It is opaque to
	// this package -- used only to key maps and to pass back to the
	// adapter's own second-hop fetch callbacks, never interpreted.
	UUID string
	// Name is the backend's own display/search name for the node (e.g. a
	// Zep EntityNode.Name). Used only for exact-term matching alongside the
	// canonical "label" attribute.
	Name       string
	Attributes map[string]interface{}
	Relevance  *float64
	Score      *float64
}

// CandidateEdge is the same idea for a relationship/edge result.
type CandidateEdge struct {
	UUID           string
	Name           string
	Fact           string
	SourceNodeUUID string
	TargetNodeUUID string
	Attributes     map[string]interface{}
	Relevance      *float64
	Score          *float64
	// CreatedAt/ValidAt/InvalidAt/ExpiredAt are RFC3339Nano-formatted
	// timestamps (or empty/nil), matching what every current and planned
	// backend adapter writes and reads back as of CHAOS-3752.
	CreatedAt string
	ValidAt   *string
	InvalidAt *string
	ExpiredAt *string
}

// ResultConfidence normalizes a backend's relevance/score pair into a [0,1]
// confidence, preferring relevance when it is a usable finite number and
// falling back to score (itself normalized if it looks like a distance
// rather than a similarity). Ported unchanged from zepgraph.resultConfidence.
func ResultConfidence(relevance, score *float64) float64 {
	if relevance != nil && !math.IsNaN(*relevance) && !math.IsInf(*relevance, 0) {
		return Clamp(*relevance)
	}
	if score != nil && !math.IsNaN(*score) && !math.IsInf(*score, 0) {
		if *score >= 0 && *score <= 1 {
			return *score
		}
		if *score > 1 {
			return Clamp(1 / *score)
		}
	}
	return 0
}

func Clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// SubjectKey is the stable dedup/map key for a SubjectRef, shared so every
// backend's candidate-merging map keys identically.
func SubjectKey(subject contextfabric.SubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}

// UniqueSorted trims, drops empty and "*" sentinel values, dedups, and
// sorts. Shared by scope handling and evidence-ref-id normalization.
func UniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "*" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// StringAttribute reads a string-typed attribute, returning "" if absent or
// a different type. Shared helper for reading CandidateNode/CandidateEdge
// Attributes maps.
func StringAttribute(attributes map[string]interface{}, key string) string {
	value, _ := attributes[key].(string)
	return value
}

// ParseOptionalTime parses an RFC3339Nano timestamp, returning nil for an
// empty or unparseable value rather than erroring -- graph-sourced temporal
// data is advisory context, never a validation boundary.
func ParseOptionalTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func ParseOptionalTimePtr(value *string) *time.Time {
	if value == nil {
		return nil
	}
	return ParseOptionalTime(*value)
}
