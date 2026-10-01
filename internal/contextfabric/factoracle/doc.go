// Package factoracle is the O4 differential oracle of the direct data design
// (r5, section J.4): the ops GraphQL answer for a root field against the acr
// fact for the same subject and window.
//
// Two planes, one comparator:
//
//   - ops plane: a selection shape generated from the production root policy
//     (directread.GraphQLPolicy), run through graphql_query;
//   - acr plane: read_facts, served by the real fact providers.
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
package factoracle
