// Package factoracle is the O4 differential oracle of the direct data design
// (r5, section J.4): the ops GraphQL answer for a root field against the acr
// fact for the same subject and window.
//
// Three tools, one comparator:
//
//   - graphql_query: a selection shape generated from the production root
//     policy (directread.GraphQLPolicy);
//   - run_operation: the registered document of the same operation, for the
//     same variables; every leaf must equal graphql_query's;
//   - read_facts: the acr fact for the same subject and window, served by
//     the real fact providers.
//
// Every compared leaf is typed ({"t","v"}): the ops type comes from the SDL
// type of the output path in the policy, the acr type from the pair's field
// map. A difference is accepted only when it is one of the named classes
// (Class) and its witness holds; everything else is a Finding.
//
// Run modes:
//
//   - recorded (default `go test`): the listener answers are the replies the
//     real ops MCP listener gave on the venue, replayed over HTTP through the
//     real graphql_query runner; the acr facts are produced by the real
//     providers on a ClickHouse seeded from the scrubbed extract of the same
//     store (testdata/venue).
//   - live (`make o4-oracle-live`, build tag o4venue): both planes are called
//     on the venue through the hosted MCP server. Never a pull request gate.
//
// `make o4-oracle-capture` records the extract and the replies again.
//
// Roots with no acr fact are checked for shape, echo and limits only; the
// reason is recorded per root in rootPairs.
//
// What is compared is held against the production declarations, not against
// a list in this package alone: every output path of a value root is compared
// or excluded with a reason (valuePaths, checked against the root policy), and
// every field and table column a fact provider declares for a kind the oracle
// reads is compared or excluded with a reason (factPlan, checked against
// FactCapability.Fields and against every fact a run reads).
//
// A run is a measurement or it fails (Report.Err): a path that does not serve
// a root must be declared dark by the venue (Oracle.ListenerDark,
// Oracle.OperationDark), a served answer must hold every selected field and
// at least one leaf, two served paths must have leaves to compare, and a
// value root must compare something on every path its plan says it compares.
// A subject with rows in the store and no answer is a finding, never a match
// and never skipped.
//
// The design's six classes are permanent. ClassLatestDayVsWindow is a
// temporary allowance. It is counted only where a run measured it (the same
// value for two different histories; temporaryAllowance), and it expires when
// a covered path leaves the policy, when the contract of its operation
// changes, or when a covered value starts to follow the requested window.
// Both run modes fail on an expired allowance.
package factoracle
