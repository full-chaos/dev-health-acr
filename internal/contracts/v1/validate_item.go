package v1

import (
	"fmt"
	"math"
)

func (i ContextPacketItem) Validate() error {
	if i.SchemaVersion != ContextPacketItemSchema {
		return fmt.Errorf("schema_version must be %q", ContextPacketItemSchema)
	}
	if !stringLengthBetween(i.PacketItemID, 8, 256) || !stringLengthBetween(i.Title, 1, 500) || !stringLengthBetween(i.Summary, 1, 4000) || !stringLengthBetween(i.WhyIncluded, 1, 2000) || !stringLengthBetween(i.RuleID, 1, 256) {
		return fmt.Errorf("context packet item string violates v1 bounds")
	}
	if math.IsNaN(i.Confidence) || math.IsInf(i.Confidence, 0) || i.Confidence < 0 || i.Confidence > 1 || i.Rank < 0 {
		return fmt.Errorf("context packet item number violates v1 bounds")
	}
	if !validItemEnums(i) || !validScope(i.ValidityScope) || !validRelatedEntities(i.RelatedEntities) || !validEvidenceIDs(i.EvidenceRefIDs) {
		return fmt.Errorf("context packet item violates v1 bounds")
	}
	if i.ClaimKind == ClaimObserved && len(i.EvidenceRefIDs) == 0 {
		return fmt.Errorf("observed claims require evidence")
	}
	return nil
}

func validItemEnums(item ContextPacketItem) bool {
	switch item.Category {
	case CategoryState, CategoryPressure, CategoryCause, CategoryEvidence, CategoryAction:
	default:
		return false
	}
	switch item.ClaimKind {
	case ClaimObserved, ClaimInferred, ClaimRecommendation:
	default:
		return false
	}
	switch item.Severity {
	case SeverityInfo, SeverityWarning, SeverityHigh, SeverityCritical:
		return true
	default:
		return false
	}
}

func validScope(scope ValidityScope) bool {
	return stringLengthBetween(scope.Branch, 0, 512) && stringLengthBetween(scope.CommitSHA, 0, 64)
}

func validRelatedEntities(entities []RelatedEntity) bool {
	if entities == nil {
		return false
	}
	if len(entities) > 100 {
		return false
	}
	for _, entity := range entities {
		if !stringLengthBetween(entity.Type, 1, 100) || !stringLengthBetween(entity.ID, 1, 1024) || !stringLengthBetween(entity.Label, 1, 1000) || !optionalURI(entity.URL, 2048) {
			return false
		}
	}
	return true
}

func validEvidenceIDs(ids []string) bool {
	if ids == nil {
		return false
	}
	if len(ids) > 100 || !uniqueStrings(ids) {
		return false
	}
	for _, id := range ids {
		if !stringLengthBetween(id, 8, 256) {
			return false
		}
	}
	return true
}
