package falkorgraph

import (
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A walk reads, of each node, only what it decides with: the identity (kind,
// canonical id, label) and the three authorization lists. A Subject node can
// carry wide text and an embedding of thousands of numbers, and one link read
// returns thousands of rows, so the walk's reads return a map of these
// properties in place of the node. The link edge is read the same way: its tier,
// its rank and its relationship id.
var walkNodeProperties = []string{propKind, propCanonicalID, propLabel, propAuthzRepos, propAuthzProjects, propAuthzTeams}

var walkLinkProperties = []string{propPropertyPrefix + linkTierProperty, propPropertyPrefix + linkRankProperty, propRelationshipID}

// walkProjection is the map of a variable's walk properties, returned under
// the variable's own name so the ORDER BY of a read keeps its text.
func walkProjection(variable string, properties []string) string {
	entries := make([]string, 0, len(properties))
	for _, property := range properties {
		entries = append(entries, fmt.Sprintf("%[1]s: %[2]s.%[1]s", property, variable))
	}
	return fmt.Sprintf("{%s} AS %s", strings.Join(entries, ", "), variable)
}

// walkNodeProjected reports whether a position's nodes may be read as a
// projection. A project is always read whole: the project reach rewrite of a
// restricted caller's read (project_reach.go) works on whole project nodes.
func walkNodeProjected(position treePosition) bool {
	return treeNodes[position].kind != contractsv1.ContextFabricSubjectProject
}

// walkNode reads a node column of a walk read: a projection map, or a whole
// node.
func walkNode(value interface{}) *node {
	switch v := value.(type) {
	case *node:
		return v
	case map[string]interface{}:
		return &node{Properties: normalizeProperties(v)}
	}
	return nil
}

// walkEdge reads an edge column of a walk read: a projection map, or a whole
// edge.
func walkEdge(value interface{}) *edge {
	switch v := value.(type) {
	case *edge:
		return v
	case map[string]interface{}:
		return &edge{Properties: normalizeProperties(v)}
	}
	return nil
}
