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
	// Relevance is an already-bounded [0,1] confidence value the ADAPTER
	// itself declares, not a raw backend score -- see ResultConfidence.
	// Setting this is how a backend states "I have already normalized my
	// own retrieval score into a documented, bounded band" (AC-3778-0).
	Relevance *float64
	// Score is the backend's own raw, unnormalized retrieval value, with a
	// meaning that is backend-specific and NOT interpreted by this package
	// beyond ResultConfidence's fallback heuristic (see its doc comment for
	// what that heuristic assumes and why an adapter with a differently-
	// shaped score, e.g. an unbounded lexical relevance score, must
	// normalize into Relevance instead of relying on it).
	Score *float64
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
//
// D11 / AC-3778-0: the `score > 1 -> Clamp(1/score)` fallback arm assumes
// score behaves like a DISTANCE -- 0 means identical, and confidence should
// fall as the number grows without bound. That assumption is correct for
// some similarity metrics (e.g. an L2/Euclidean distance) but is actively
// WRONG for an open-ended lexical relevance score such as RediSearch's
// full-text score: a HIGHER such score means MORE relevant, not further
// away, so this arm inverts it. falkorgraph's fulltextSearchNodes therefore
// never leaves this arm to interpret its raw score -- it normalizes into
// Relevance itself (an absolute, per-candidate function of that candidate's
// own exact matched-query-term coverage, into the documented [0.50, 0.75]
// band; see queries.go's fulltextRelevanceFromMatchedTerms) before a
// CandidateNode ever reaches this function, so the preferred Relevance
// branch above is always the one taken for a lexical hit. Round 2 of this
// fix (Codex P1/P3) replaced an earlier per-call-relative (max-min)
// version of that normalization specifically because it produced
// confidence values that were only meaningful relative to whatever else
// came back in the SAME query's result set -- comparing two such values
// from two different queries (exactly what ResolveSubjects' merge/sort
// does) compared numbers from two different, incompatible scales.
//
// This arm is left in place, unchanged, for a hypothetical backend whose
// score genuinely IS a bounded-above-1 distance. It is graphrank's
// documented, backend-neutral default for "I got a raw score and nothing
// else" -- graphrank cannot itself know whether a given backend's score
// means "closer" or "more relevant" (that decision belongs at the adapter
// boundary, not inside this shared helper). Any future backend, including a
// vector-similarity one (CHAOS-3778), MUST make the same explicit choice
// falkorgraph made here about what its own score means, and normalize into
// Relevance if that meaning does not match this arm's distance assumption
// -- merging a vector score that doesn't fit this arm straight into Score
// and letting it fall through here is exactly the bug this fixes.
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
