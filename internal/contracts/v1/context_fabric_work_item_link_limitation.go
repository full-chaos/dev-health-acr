package v1

// CHAOS-12456: the disclosure of a work item a restricted caller could not
// reach. Such an item is admitted only by a native link of record to a pull
// request of a granted repository.
//
// EACH SENTENCE IS ONE FIXED TEXT, WHATEVER THE CAUSE. The first is the same for
// no such item, an item outside the grant, an item with no link, and an item
// with only a lower-tier link: it is true of all four, so it tells the caller
// nothing about what lies outside its grant. Neither sentence may read as "does
// not exist".
const (
	// ContextFabricWorkItemNoGrantedLinkLimitation is served when the census
	// inside the grant completed and found no linked item.
	ContextFabricWorkItemNoGrantedLinkLimitation = "No pull request link of record connects this work item to a repository this credential can read, so it was not resolved. This does not show that the work item does not exist."
	// ContextFabricWorkItemLinksUnreadLimitation is served when a read that
	// decides such an item failed, was cut, or could not be confirmed.
	ContextFabricWorkItemLinksUnreadLimitation = "The pull request links of work items could not be read or confirmed, so a work item may be left out of this answer. This does not show that it does not exist."
)

// IsContextFabricWorkItemLinkLimitation reports whether a limitation is one of
// the two sentences above.
func IsContextFabricWorkItemLinkLimitation(limitation string) bool {
	return limitation == ContextFabricWorkItemNoGrantedLinkLimitation || limitation == ContextFabricWorkItemLinksUnreadLimitation
}
