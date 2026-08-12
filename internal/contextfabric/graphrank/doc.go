// Package graphrank holds the backend-neutral subject-resolution and
// graph-discovery decision logic originally extracted from zepgraph's
// reader.go (CHAOS-3752, before zepgraph's CHAOS-3771 deletion). It is
// shared by every Context Fabric graph backend adapter (falkorgraph today,
// and any future one) so that confidence thresholds, ambiguity/
// clarification behavior, evidence-budget admission order, and second-hop
// verification semantics -- each individually hardened by a specific
// historical Codex review finding -- are proven once and never hand-ported
// twice.
//
// Nothing in this package imports a backend SDK. It operates over the
// neutral CandidateNode/CandidateEdge shapes (a node/edge's canonical
// attributes plus a relevance/score pair) and the existing backend-neutral
// contextfabric contract types (SubjectRef, SubjectCandidate,
// SubjectResolution, RelationshipPath, GraphContext, ...). A backend adapter
// converts its own wire types into these shapes, calls the exported
// functions here for the decision, and converts the result back -- it never
// duplicates the decision logic itself.
//
// CHAOS-3752: extracted ahead of the falkorgraph adapter so zepgraph and
// falkorgraph shared one implementation instead of two hand-ports drifting
// apart, while both existed. See ADR 0007 (superseded) and ADR 0009
// (current, with its CHAOS-3771 addendum) for why this logic looks the way
// it does.
package graphrank
