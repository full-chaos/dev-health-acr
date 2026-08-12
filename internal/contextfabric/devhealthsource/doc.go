// Package devhealthsource is the production contextfabric.ProjectionSource.
// It reads the same canonical Dev Health data ACR already reads for context
// packets (internal/contextpacket's ClickHouse boundary) rather than
// inventing a second ingest path. See docs/design/context-fabric-projection-worker.md
// for the full design and the entity/relationship coverage this package
// provides today.
//
// Two entity families have no canonical source in this repository yet
// (organization-level Team and Project entities, and Decision/Document
// content): rather than fabricate data for them, this package exposes a
// documented, gated-off stub (see teams_projects.go) so the contract is
// visible without pretending it is implemented.
package devhealthsource
