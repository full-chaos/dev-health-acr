package gatevocab

// RelationshipsReadLogMessage is the one Info line every read_relationships
// call writes (CHAOS-7074).
const RelationshipsReadLogMessage = "context fabric direct relationships read"

// RelationshipsStatus is the status of one read_relationships response.
type RelationshipsStatus string

const (
	// RelationshipsComplete: every edge of the requested walk was examined;
	// this page and the pages before it hold every visible edge.
	RelationshipsComplete RelationshipsStatus = "complete"
	// RelationshipsPartial: this page is served, and more pages follow
	// (page.next_cursor) or a bound cut the walk (truncated_by).
	RelationshipsPartial RelationshipsStatus = "partial"
	// RelationshipsDenied: the root subject was not admitted
	// (denied_or_not_found). No edge is served.
	RelationshipsDenied RelationshipsStatus = "denied"
	// RelationshipsUnavailable: a gate decision or a graph read failed. It
	// is never served as a partial answer.
	RelationshipsUnavailable RelationshipsStatus = "unavailable"
	// RelationshipsInvalid: the request or its cursor was refused before
	// any read.
	RelationshipsInvalid RelationshipsStatus = "invalid"
)

// RelationshipsStatusVocabulary is the closed set of statuses.
func RelationshipsStatusVocabulary() [5]RelationshipsStatus {
	return [5]RelationshipsStatus{RelationshipsComplete, RelationshipsPartial, RelationshipsDenied, RelationshipsUnavailable, RelationshipsInvalid}
}

// EdgeWithheldReason names why the edge gate withheld one edge. It reaches
// telemetry as a count; the wire sees one total (edges_not_visible).
type EdgeWithheldReason string

const (
	EdgeVisible EdgeWithheldReason = ""
	// EdgeWithheldAttributes: the edge's own authorization attributes
	// refuse the caller.
	EdgeWithheldAttributes EdgeWithheldReason = "edge_attributes"
	// EdgeWithheldSource: the subject gate did not admit the source node.
	EdgeWithheldSource EdgeWithheldReason = "source_not_visible"
	// EdgeWithheldTarget: the subject gate did not admit the target node.
	EdgeWithheldTarget EdgeWithheldReason = "target_not_visible"
)

// EdgeWithheldAttributionRelation: a project-only caller and a DOCUMENTED_BY
// or HAS_EPISODE edge (E2, CHAOS-12300).
const EdgeWithheldAttributionRelation EdgeWithheldReason = "attribution_relation"

// EdgeWithheldProjectList: a project-only caller and an edge whose real
// project list holds no granted identity (E1, CHAOS-12300).
const EdgeWithheldProjectList EdgeWithheldReason = "edge_project_list"

// EdgeWithheldReasonVocabulary is the closed set of withheld reasons.
func EdgeWithheldReasonVocabulary() [5]EdgeWithheldReason {
	return [5]EdgeWithheldReason{EdgeWithheldAttributes, EdgeWithheldSource, EdgeWithheldTarget, EdgeWithheldAttributionRelation, EdgeWithheldProjectList}
}

// CursorOutcome is what a page did with a cursor.
type CursorOutcome string

const (
	CursorIssued     CursorOutcome = "issued"
	CursorAccepted   CursorOutcome = "accepted"
	CursorExpired    CursorOutcome = "expired"
	CursorStale      CursorOutcome = "stale"
	CursorInvalid    CursorOutcome = "invalid"
	CursorForeignOrg CursorOutcome = "foreign_org"
)

// CursorOutcomeVocabulary is the closed set of cursor outcomes (design J.3).
func CursorOutcomeVocabulary() [6]CursorOutcome {
	return [6]CursorOutcome{CursorIssued, CursorAccepted, CursorExpired, CursorStale, CursorInvalid, CursorForeignOrg}
}

// RelationshipsFailureClass names a read_relationships failure for the trace.
type RelationshipsFailureClass string

const (
	RelationshipsFailureGate  RelationshipsFailureClass = "gate_unavailable"
	RelationshipsFailureGraph RelationshipsFailureClass = "graph_read"
	RelationshipsFailureProof RelationshipsFailureClass = "gate_decision_refused"
	// RelationshipsFailureVocabulary is a response refused because a served
	// value fell outside its pinned value set.
	RelationshipsFailureVocabulary RelationshipsFailureClass = "served_vocabulary_refused"
)

// RelationshipsFailureClassVocabulary is the closed set of failure classes.
func RelationshipsFailureClassVocabulary() [4]RelationshipsFailureClass {
	return [4]RelationshipsFailureClass{RelationshipsFailureGate, RelationshipsFailureGraph, RelationshipsFailureProof, RelationshipsFailureVocabulary}
}
