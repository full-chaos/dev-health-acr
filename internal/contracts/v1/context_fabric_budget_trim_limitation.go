package v1

// The budget-trim disclosure. An answer that overran the item budget, with no
// time left to re-synthesize it, is shortened by cutting its claimed-facts
// list; its members are kept. This is the one sentence that says so. It is a
// fixed sentence, recognised by whole-string equality, so a composer and the
// recogniser cannot drift apart and the sentence is never displaced at the
// limitation cap.
const ContextFabricBudgetTrimClaimedFactsLimitation = "This answer was shortened to fit the item budget: the list of facts it claimed was cut, and the members it covers were kept. Ask a narrower question or allow a larger response budget to see the rest."

// IsContextFabricBudgetTrimLimitation reports whether a limitation is the
// budget-trim disclosure.
func IsContextFabricBudgetTrimLimitation(limitation string) bool {
	return limitation == ContextFabricBudgetTrimClaimedFactsLimitation
}
