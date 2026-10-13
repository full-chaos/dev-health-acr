package directread

import (
	"fmt"
	"slices"
)

const (
	RelationshipsAxisCurrent = "current"
	RelationshipsAxisAsOf    = "as_of"
)

// RelationshipsAxisVocabulary is the set of effective.axis values read_relationships serves.
func RelationshipsAxisVocabulary() []string {
	return []string{RelationshipsAxisCurrent, RelationshipsAxisAsOf}
}

// validateServedRelationshipsVocabulary refuses a response whose axis or link
// tier is outside the served sets, whatever built the value.
func validateServedRelationshipsVocabulary(response RelationshipsResponse) error {
	if !slices.Contains(RelationshipsAxisVocabulary(), response.Effective.Axis) {
		return fmt.Errorf("effective.axis %q is outside the served set", response.Effective.Axis)
	}
	for _, edge := range response.Edges {
		if tier := edge.Provenance.LinkTier; tier != "" && !slices.Contains(LinkTierVocabulary(), tier) {
			return fmt.Errorf("provenance.link_tier %q is outside the served set", tier)
		}
	}
	return nil
}
