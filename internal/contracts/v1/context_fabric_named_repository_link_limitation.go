package v1

// CHAOS-12505 (ruling 3, from the CHAOS-12523 rows): a work item committed beside
// one repository the question names. A work item has no repository column, so the
// two are related only through a pull request link (the entity tree); the answer
// says whether a link joins them, read under the caller's grants by the keyed link
// read, or that the link could not be read.
//
// EACH SENTENCE IS ONE FIXED TEXT: no subject, label, tier or count is
// interpolated. A link of a lower tier is never shown as native: it names its tier.
const (
	// ContextFabricNamedRepositoryNativeLinkLimitation: a native link joins them.
	ContextFabricNamedRepositoryNativeLinkLimitation = "The work item is joined to a pull request of the named repository by a native link."
	// ContextFabricNamedRepositoryTextLinkLimitation: the strongest link is an
	// explicit text reference.
	ContextFabricNamedRepositoryTextLinkLimitation = "The work item is joined to a pull request of the named repository only by an explicit text reference, not by a native link."
	// ContextFabricNamedRepositoryHeuristicLinkLimitation: the strongest link is a
	// heuristic match.
	ContextFabricNamedRepositoryHeuristicLinkLimitation = "The work item is joined to a pull request of the named repository only by a heuristic match, not by a native link."
	// ContextFabricNamedRepositoryNoLinkLimitation: the read completed and found no
	// link it admits.
	ContextFabricNamedRepositoryNoLinkLimitation = "No pull request link this answer admits joins the work item to the named repository, so the work item's facts are its own and say nothing about that repository."
	// ContextFabricNamedRepositoryLinkUnreadLimitation: the read failed, was cut, or
	// could not start.
	ContextFabricNamedRepositoryLinkUnreadLimitation = "Whether a pull request link joins the work item to the named repository could not be read, so this answer does not show that it does or does not."
)

// ContextFabricNamedRepositoryLinkedLimitation is the sentence for a link of
// tier, the strongest the read admitted: native, explicit_text or heuristic. A
// tier it does not know reads as unread, never as native.
func ContextFabricNamedRepositoryLinkedLimitation(tier string) string {
	switch tier {
	case EvidenceProvenanceNative:
		return ContextFabricNamedRepositoryNativeLinkLimitation
	case EvidenceProvenanceExplicitText:
		return ContextFabricNamedRepositoryTextLinkLimitation
	case EvidenceProvenanceHeuristic:
		return ContextFabricNamedRepositoryHeuristicLinkLimitation
	}
	return ContextFabricNamedRepositoryLinkUnreadLimitation
}

// IsContextFabricNamedRepositoryLinkLimitation reports whether a limitation is
// one of the sentences above.
func IsContextFabricNamedRepositoryLinkLimitation(limitation string) bool {
	switch limitation {
	case ContextFabricNamedRepositoryNativeLinkLimitation, ContextFabricNamedRepositoryTextLinkLimitation, ContextFabricNamedRepositoryHeuristicLinkLimitation,
		ContextFabricNamedRepositoryNoLinkLimitation, ContextFabricNamedRepositoryLinkUnreadLimitation:
		return true
	}
	return false
}
