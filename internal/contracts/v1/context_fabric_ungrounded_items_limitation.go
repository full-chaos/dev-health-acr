package v1

// ContextFabricUngroundedItemsDroppedLimitation is the one sentence an answer
// carries when the written synthesis stated something the stored facts do not
// support and the server left that out rather than refuse the whole answer.
// The reason is always the same closed one: the fact a claim, driver or
// finding rested on is not among the facts read for the question. It is a
// fixed sentence recognised by whole-string equality and is never displaced at
// the limitation cap.
const ContextFabricUngroundedItemsDroppedLimitation = "Part of the written analysis was left out because the facts read for this question do not support it: a claimed fact that no stored fact confirms, or a driver or finding that rested on one. What is served is supported by those facts."

// IsContextFabricUngroundedItemsDroppedLimitation reports whether a limitation
// is the ungrounded-items disclosure.
func IsContextFabricUngroundedItemsDroppedLimitation(limitation string) bool {
	return limitation == ContextFabricUngroundedItemsDroppedLimitation
}
