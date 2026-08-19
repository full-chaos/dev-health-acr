package devhealthsource

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// This file is CHAOS-3898 Slice S3's hand-off layer to CHAOS-3896 (design
// brief v4.1 §6's S3 row: "uniform bridge fns + injectivity pins +
// anchor_collision. S2a+S2+S3 unblocks 3896 Slices B and C."). It closes
// the precondition 3896's own design brief v6 §1.4 names explicitly: "for
// every census kind, (source natural key) <-> (graph canonical id) is
// injective... delivered and pinned by 3898." Nothing here changes any
// existing shadow-round decision (CHAOS-3899's RunShadowEvidenceRound is
// untouched) -- these are new, additively-exposed primitives a future 3896
// Slice B/C consumes when it performs the keyed graph existence read
// (§1.4's nodeByKindID call) and the project-anchor soundness check.

// BridgeSatisfierToCanonicalID computes the GRAPH canonical id that a
// Count==1 RunCensus result's own SatisfierNaturalKey (this package's
// censusKindRegistryEntries[kind].identityColumn value, captured in
// CensusResult.SatisfierNaturalKey / graphrank.CensusOutcome.SatisfierNaturalKey)
// bridges to. This is the uniform bridge fn design brief v4.1 §5 item 1
// lists as a registry facet alongside "derive" and "anchor extractors" --
// one dispatcher over the SAME per-kind registry BuildCensusDiscriminator
// and RunCensus already use, rather than a second, parallel kind switch.
//
// The caller is 3896 Slice C's commit path (brief §1.4): once a census
// names exactly one satisfier, the keyed graph read (nodeByKindID) needs
// this satisfier's canonical id to look up -- BridgeSatisfierToCanonicalID
// is that translation, kept in this package because it is the census
// registry's own natural-key SHAPE (identityColumn's concatenation order)
// that a caller must invert correctly; graphrank deliberately has no
// devhealthsource dependency of its own (see KindHasAnchorFK's doc comment
// on why that boundary is mirrored rather than shared), so this function
// stays on devhealthsource's side of it, exported for graphrank/3896 code
// to call.
//
// omitted mirrors identity.Derive's own "whole-row omit, never truncate"
// contract (H6): true only for the four changed kinds (pull_request is
// grandfathered and never omits, see bridgePullRequestSatisfier), and only
// when the derived id would exceed identity.MaxNaturalKeyBytes -- a case
// the D10 bound-omit ledger already tracks as "none live" today. err is
// returned only for a malformed satisfierNaturalKey (wrong segment count)
// or an unregistered kind -- both programmer errors, never a live-data
// condition, since satisfierNaturalKey only ever comes from this package's
// own RunCensus.
func BridgeSatisfierToCanonicalID(kind graphrank.CensusKind, satisfierNaturalKey string) (canonicalID string, omitted bool, err error) {
	entry, ok := censusKindRegistryEntries[kind]
	if !ok {
		return "", false, fmt.Errorf("devhealthsource: %s is not a registered census kind", kind)
	}
	if entry.bridgeCanonicalID == nil {
		return "", false, fmt.Errorf("devhealthsource: %s has no registered census bridge", kind)
	}
	return entry.bridgeCanonicalID(satisfierNaturalKey)
}

// splitCensusNaturalKey splits a census identityColumn value
// ("<org_id>:<segment>:<segment>...") into exactly wantSegments trailing
// segments after the leading org_id, using SplitN so that only the FIRST
// (wantSegments) colons are treated as separators -- any colon inside the
// LAST segment's own raw value (e.g. a work_item_id shaped like
// "linear:CHAOS-3896", or a provider-issued review_id containing one)
// stays part of that segment rather than fragmenting it. This mirrors the
// SAME first-N-colon-cut convention workItemTicketKeyPredicate's own doc
// comment already establishes for ticketKeyAlias's inverse (registry.go),
// applied here to the census witness format instead of a single
// work_item_id value. ok is false whenever the input does not split into
// exactly wantSegments+1 pieces (org_id plus every wanted segment) --
// callers must treat that as "cannot parse", never attempt a partial
// bridge (the same fail-closed discipline identity.Segments already uses).
func splitCensusNaturalKey(satisfierNaturalKey string, wantSegments int) (segments []string, ok bool) {
	parts := strings.SplitN(satisfierNaturalKey, ":", wantSegments+1)
	if len(parts) != wantSegments+1 {
		return nil, false
	}
	return parts[1:], true
}

// bridgeSegmentCount looks up kind's own registered natural-key segment
// count (identity.Registry, registry.go) rather than hardcoding it here --
// a future column added to a Registration entry is picked up automatically
// instead of silently mis-splitting the census witness string.
func bridgeSegmentCount(kind string) int {
	reg, ok := identity.Lookup(kind)
	if !ok {
		return 0
	}
	return len(reg.Columns)
}

func bridgeWorkItemSatisfier(satisfierNaturalKey string) (string, bool, error) {
	segments, ok := splitCensusNaturalKey(satisfierNaturalKey, bridgeSegmentCount(identity.KindWorkItem)) // [repo_id, work_item_id]
	if !ok {
		return "", false, fmt.Errorf("devhealthsource: malformed work_item census natural key %q", satisfierNaturalKey)
	}
	return identity.Derive(identity.KindWorkItem, segments, nil)
}

func bridgeCIPipelineRunSatisfier(satisfierNaturalKey string) (string, bool, error) {
	segments, ok := splitCensusNaturalKey(satisfierNaturalKey, bridgeSegmentCount(identity.KindCIPipelineRun)) // [repo_id, run_id]
	if !ok {
		return "", false, fmt.Errorf("devhealthsource: malformed ci_pipeline_run census natural key %q", satisfierNaturalKey)
	}
	return identity.Derive(identity.KindCIPipelineRun, segments, nil)
}

func bridgePullRequestReviewSatisfier(satisfierNaturalKey string) (string, bool, error) {
	segments, ok := splitCensusNaturalKey(satisfierNaturalKey, bridgeSegmentCount(identity.KindPullRequestReview)) // [repo_id, number, review_id]
	if !ok {
		return "", false, fmt.Errorf("devhealthsource: malformed pull_request_review census natural key %q", satisfierNaturalKey)
	}
	return identity.Derive(identity.KindPullRequestReview, segments, nil)
}

// bridgePullRequestSatisfier never omits and never calls identity.Derive:
// pull_request is grandfathered onto the pre-CHAOS-3898 scheme (design
// brief §1.2: "pull_request grandfathered... injective, colon-free by
// type"), not a registry kind (identity.Lookup("pull_request") reports
// false), so there is no `.v2:` codec to invert here -- only the plain
// "pull_request:<repo_id>:<number>" concatenation every existing producer
// and reader already uses.
func bridgePullRequestSatisfier(satisfierNaturalKey string) (string, bool, error) {
	segments, ok := splitCensusNaturalKey(satisfierNaturalKey, 2) // pull_request: [repo_id, number]
	if !ok {
		return "", false, fmt.Errorf("devhealthsource: malformed pull_request census natural key %q", satisfierNaturalKey)
	}
	return "pull_request:" + segments[0] + ":" + segments[1], false, nil
}

// AnchorCollision reports whether anchorCanonicalID -- a would-be ANCHOR
// discriminator (design brief v5 §1.2's "anchor" row; 3896 brief v6 §1.4)
// -- names a raw source id that resolves to MORE THAN ONE provider within
// orgID: the SAME "ambiguous -> omit + ledger" defect
// queryWorkItemProjects/queryProjectTeams already guard at PROJECTION time
// via key_resolution_count (count() OVER (PARTITION BY id)), checked here
// live, at BIND time, before a census predicate is ever built from it.
//
// Only SubjectProject anchors carry this defect (design brief v4.1 §1.4:
// "projects.id alone does not name a provider" -- projects are unique by
// (org, provider, id), but the anchor's raw FK value on the work_item base
// table, w.project_id, carries no provider column of its own). Every other
// anchor kind this registry supports (SubjectRepository) has no such
// column collapse, so AnchorCollision always reports false for it without
// issuing a query.
//
// WHY THIS MATTERS (the soundness gap this closes): BuildCensusDiscriminator's
// project-anchor predicate compares w.project_id against
// canonicalIDValue(SubjectProject, anchorCanonicalID) -- which DROPS the
// provider (it returns project.v2:<provider>:<id>'s LAST segment only,
// registry.go's own Segments doc comment). If the SAME raw id is shared by
// two different providers' projects in this org, that predicate silently
// matches work_items belonging to EITHER provider's project, not just the
// one the anchor was actually bound to -- a false-positive satisfier the
// census's own aggregate-first protocol cannot detect (it has no way to
// know its own FK predicate was under-qualified). A caller MUST call
// AnchorCollision before trusting that predicate and refuse the round
// (the `anchor_collision` DegradationReason, graphrank package) rather
// than let a collided id reach BuildCensusDiscriminator at all --
// exactly the "anchor_collision typed non-decisive census outcome at BIND
// TIME" design brief v4.1 §1.4 specifies, exposed here for 3896's future
// Slice B/C BindAnchor-adjacent call site to consume. This function does
// not itself change RunShadowEvidenceRound's shipped, stable decision
// logic (CHAOS-3899) -- wiring the check into that round's control flow is
// Slice B/C's own scope, not this hand-off layer's.
func AnchorCollision(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, anchorKind contextfabric.SubjectKind, anchorCanonicalID string) (collision bool, err error) {
	if anchorKind != contextfabric.SubjectProject {
		return false, nil
	}
	if client == nil {
		return false, fmt.Errorf("devhealthsource: anchor collision check requires a ClickHouseQueryClient")
	}
	rawID := canonicalIDValue(anchorKind, anchorCanonicalID)
	statement := "SELECT count() FROM projects FINAL WHERE org_id = {anchor_collision_org_id:String} AND id = {anchor_collision_project_id:String}"
	bindings := []contextpacket.ClickHouseBinding{
		{Name: "anchor_collision_org_id", Value: orgID},
		{Name: "anchor_collision_project_id", Value: rawID},
	}
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, fmt.Errorf("devhealthsource: anchor collision statement returned no row")
	}
	var keyResolutionCount uint64
	if err := rows.Scan(&keyResolutionCount); err != nil {
		return false, err
	}
	if rows.Next() {
		return false, fmt.Errorf("devhealthsource: anchor collision statement returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return keyResolutionCount > 1, nil
}
