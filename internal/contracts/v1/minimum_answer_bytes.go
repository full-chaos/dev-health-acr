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
// worst case is caller breakage bought for nothing.
//
// WHY IT IS NOT ROUNDED. A rounded pin absorbs growth: add a bounded envelope
// field, and a constant with slack keeps passing while the real worst case
// creeps toward it. Pinned to the measured value EXACTLY, any growth fails the
// measurement test at the moment of the change rather than in production. That
// is the same reason the paper rejected "raise one cap by one" as a mutation
// oracle and required the pin itself be mutated by LOWERING it.
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
