package v1

// ContextFabricMinimumAnswerBytes is the smallest serialized-byte budget under
// which an answer can exist at all. It is MEASURED, not chosen, and
// minimum_answer_bytes_test.go is where it is measured.
//
// WHAT IT MEASURES. The smallest document that is still an ANSWER rather than an
// envelope: every envelope field at its own validated maximum -- an 8000-byte
// question, maximal version metadata, a plan carrying every fact kind and the
// full ContextFabricPlanNarrowingMaxCount narrowing steps -- plus the
// irreducible answer content of one committed subject, one driver, and the two
// facts that driver cites. The document is run through Validate() BEFORE being
// measured: a measurement of a document the contract would reject bounds
// nothing.
//
// THE NUMBER IT REPLACES. The minimal-answer-floor paper carried a provisional
// 32768 and said plainly of it: "the paper does not get to invent this number."
// This is the measurement that supersedes it. It is deliberately LOWER, because
// raising a request minimum is an INPUT tightening -- every byte above the true
// worst case is caller breakage bought for nothing. It is consumer-identical to
// the provisional value: ask-dev requests 262,144 and MCP defaults to 65,536, so
// both clear either number, and ask-dev's one sub-minimum test fixture breaks at
// both. Only the caller-facing tightening differs, which is why the smaller
// number wins.
//
// WHY IT IS NOT ROUNDED. A rounded pin absorbs growth: add a bounded envelope
// field, and a constant with slack keeps passing while the real worst case
// creeps toward it -- a 16 KiB pin over this floor would have hidden 2,778
// bytes of creep. Pinned to the measured value EXACTLY, any change to the worst
// case fails at the moment it is made, and the mutation kills in BOTH
// directions without needing a second assertion to say so.
//
// The cost is accepted and is disclosure rather than churn: because this
// constant is published in the request schema, any envelope growth becomes a
// wire-visible minimum change and a consumer pin bump. That is the honest
// consequence of a bound that callers are held to.
//
// WHY IT IS A NEW CONSTANT rather than a raise of ContextFabricSerializedBytesMin.
// That constant is load-bearing elsewhere:
// ContextFabricClaimedFactCombinedContentBytesMax derives from it, so raising it
// in place would silently multiply CHAOS-4785's dual-table fact bound -- a
// model-authorable output bound -- inside a change whose whole purpose is to
// tighten a budget. The two quantities are unrelated and now say so.
//
// THE PROOF THIS EXISTS FOR. Request validation bounds the echoed question at
// 8000 characters while ContextFabricSerializedBytesMin is 8192. The maximal
// envelope measures 12,791 bytes carrying ZERO budgeted items -- no driver, no
// fact, no candidate, no member. So a caller may today legally configure a
// service, or request a budget, at a size in which no answer can exist. The
// floor paper argued this on paper; the accompanying test executes it.
const ContextFabricMinimumAnswerBytes = 13606
