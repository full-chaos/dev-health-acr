// Package identity is the CHAOS-3898 canonical-id derivation registry and
// segment codec (design brief v4.1, S1 slice).
//
// S1 SCOPE, NO BEHAVIOR CHANGE: this package is a pure derivation library.
// Nothing in this package is wired into a live producer, and nothing here
// changes a live graph key or serving behavior -- the `.v2:` prefix flip,
// the work_item repo-scoping, and the build-aside-and-swap migration
// machinery are S2a/S2 (design brief §6). Today's producers
// (internal/contextfabric/devhealthsource) keep minting their existing,
// unchanged canonical ids; this package exists so S2 has one place to
// derive the NEW ids from, instead of five hand-authored call sites that
// can independently drift.
//
// The registry states "one derivation rule per kind": kind + the source
// natural key minus org_id, segments in ORDER BY order, every variable
// segment passed through the uniform segment codec (Codec), joined by ':'.
// registry_parity_test.go checks the registry's declared segments against
// devhealthschema.EngineFull -- the single declared snapshot of the live
// ClickHouse sorting keys -- so a schema change the registry hasn't been
// updated for fails loudly here instead of silently producing a
// non-canonical id whenever S2 wires this package in.
package identity
