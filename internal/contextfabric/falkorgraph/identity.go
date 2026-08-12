package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func isAlreadyExists(err error) bool {
	return errors.Is(err, errAlreadyExists)
}

// Node/edge property keys, shared across identity.go, projection.go, and
// reader.go so the write and read sides never drift on a literal string.
const (
	propOrgID          = "org_id"
	propKind           = "subject_kind"
	propCanonicalID    = "canonical_id"
	propRelationshipID = "relationship_id"
	propLabel          = "label"
	propAliases        = "aliases"
	propPreviousNames  = "previous_names"
	propProviderPrefix = "provider_"
	propPropertyPrefix = "property_"
	propAuthzRepos     = "authorization_repositories"
	propAuthzProjects  = "authorization_projects"
	propAuthzTeams     = "authorization_teams"
	propEvidenceRefs   = "evidence_refs"
	propSourceVersion  = "source_version"
	propObservedAt     = "observed_at"    // RFC3339Nano, display/read-back only
	propObservedAtNs   = "observed_at_ns" // int64 epoch-nanos, every comparison
	propValidFrom      = "valid_from"
	propValidFromNs    = "valid_from_ns"
	propValidTo        = "valid_to"
	propValidToNs      = "valid_to_ns"
	propSearchText     = "search_text"

	labelSubject   = "Subject"
	labelRelation  = "Relates"       // generic edge label; specific type lives in the relation_type property
	labelWatermark = "_AcrWatermark" // reserved label, never matched by any Subject-scoped read
)

// graphKey derives the server-owned, caller-opaque graph key for one
// organization: the same server-owned-prefix-plus-digest scheme zepgraph
// already uses for its Zep graph ID, so both backends agree on tenancy
// identity for free and neither can be steered by caller input.
func graphKey(prefix, orgID string) string {
	digest := sha256.Sum256([]byte("context-fabric-graph\x00" + orgID))
	return strings.TrimSpace(prefix) + "-" + hex.EncodeToString(digest[:16])
}

// kindLabel converts a SubjectKind into a Cypher-safe PascalCase label
// (e.g. "work_item" -> "WorkItem"), mirroring zepgraph.zepLabel's
// convention so the same subject kind reads the same way in both backends'
// tooling. Nodes carry BOTH the generic ":Subject" label (bootstrap only
// ever needs one constraint, not one per kind -- see ensureOrgGraph) and
// this kind-specific label (so a kind-scoped query can filter by label
// instead of a property comparison).
func kindLabel(kind contextfabric.SubjectKind) string {
	parts := strings.Split(string(kind), "_")
	for index, part := range parts {
		if part == "" {
			continue
		}
		parts[index] = strings.ToUpper(part[:1]) + part[1:]
	}
	label := strings.Join(parts, "")
	if label == "" {
		return "Unknown"
	}
	return label
}

// nsTimestamp converts a time.Time to epoch nanoseconds for indexed
// comparison. RFC3339Nano-formatted strings are NEVER compared directly --
// verified live (docs/design/context-fabric-falkordb-adapter.md §5):
// time.Format(time.RFC3339Nano) trims trailing zeros, so a whole-second
// timestamp and a sub-second timestamp render at different string lengths
// and lexicographic comparison silently gives the wrong answer for a mixed
// set. Every temporal property is written as a (string, _ns int64) pair;
// every comparison and ORDER BY uses only the _ns half.
func nsTimestamp(value time.Time) int64 {
	return value.UTC().UnixNano()
}

// ensureOrgGraph bootstraps the schema (index + unique constraint on
// Subject(org_id, canonical_id); index + unique constraint on
// Relates-typed edges' relationship_id) for one organization's graph key,
// idempotently, caching success in-process so steady-state batches never
// repeat the cost -- only a brand-new organization's first write pays it.
//
// This is NOT a graph-existence check and deliberately does not try to be
// one: FalkorDB auto-creates a graph key on ANY read or write against it
// (verified -- there is no "graph not found" error), so "does this org's
// graph exist" cannot be answered by reading it, only by GRAPH.LIST
// enumeration or a dedicated marker. Bootstrap does not need that answer:
// index/constraint creation is idempotent (already-exists is treated as
// success), so running it against a graph key that already has the schema
// is a fast no-op, and running it against a brand-new key creates the
// schema. See identity_test.go for the auto-create-on-read regression this
// guards against being silently reintroduced.
func (a *Adapter) ensureOrgGraph(ctx context.Context, key string) error {
	a.bootstrapMu.RLock()
	done := a.bootstrapDone[key]
	a.bootstrapMu.RUnlock()
	if done {
		return nil
	}
	a.bootstrapMu.Lock()
	defer a.bootstrapMu.Unlock()
	if a.bootstrapDone[key] {
		return nil
	}
	if err := a.bootstrapSchema(ctx, key); err != nil {
		return err
	}
	a.bootstrapDone[key] = true
	return nil
}

func (a *Adapter) bootstrapSchema(ctx context.Context, key string) error {
	// Composite on (org_id, subject_kind, canonical_id): canonical_id alone
	// is not globally unique across kinds (a work_item and a repository can
	// legitimately share the same upstream ID string), matching zepgraph's
	// nodeUUID, which hashes kind together with canonical_id for exactly
	// this reason.
	if err := a.api.createIndex(ctx, key, labelSubject, []string{propOrgID, propKind, propCanonicalID}, false); err != nil {
		return safeDependencyError("bootstrap subject index", err)
	}
	if err := a.api.createConstraint(ctx, key, true, "NODE", labelSubject, []string{propOrgID, propKind, propCanonicalID}); err != nil {
		return safeDependencyError("bootstrap subject constraint", err)
	}
	if err := a.api.createIndex(ctx, key, labelRelation, []string{propRelationshipID}, true); err != nil {
		return safeDependencyError("bootstrap relationship index", err)
	}
	if err := a.api.createConstraint(ctx, key, true, "RELATIONSHIP", labelRelation, []string{propRelationshipID}); err != nil {
		return safeDependencyError("bootstrap relationship constraint", err)
	}
	if err := a.createFulltextIndex(ctx, key); err != nil {
		return err
	}
	return a.pollConstraintsOperational(ctx, key)
}

// createFulltextIndex creates the lexical search index on Subject.search_text
// (verified: CALL db.idx.fulltext.createNodeIndex works and produces real,
// varying relevance scores -- docs/design/context-fabric-falkordb-adapter.md
// §6.1/§6.2). Like range indexes, this is not idempotent server-side; a
// repeat call's "already indexed"-shaped error is treated as success the
// same way createIndex does.
func (a *Adapter) createFulltextIndex(ctx context.Context, key string) error {
	cypher := fmt.Sprintf("CALL db.idx.fulltext.createNodeIndex('%s', '%s')", labelSubject, propSearchText)
	_, err := a.api.query(ctx, key, cypher, nil, false)
	if err == nil {
		return nil
	}
	if isAlreadyExists(err) {
		return nil
	}
	return safeDependencyError("bootstrap fulltext index", err)
}

// pollConstraintsOperational waits for every constraint on key to leave
// PENDING. Constraint creation is asynchronous (verified: GRAPH.CONSTRAINT
// CREATE returns PENDING immediately; CALL db.constraints() later reports
// OPERATIONAL) -- bootstrap must poll, not assume, or an ApplyProjectionBatch
// racing right behind its own bootstrap call could write against a
// constraint that isn't enforcing yet.
func (a *Adapter) pollConstraintsOperational(ctx context.Context, key string) error {
	deadline := time.Now().Add(a.config.RequestTimeout)
	for {
		statuses, err := a.api.constraints(ctx, key)
		if err != nil {
			return safeDependencyError("poll constraint status", err)
		}
		// Codex P2e: strict allowlist, not a PENDING/FAILED denylist. The
		// original form defaulted allOperational=true and only flipped it
		// for the two known-bad statuses, so any OTHER value -- an unknown
		// status this server version might report, or an empty string from
		// a malformed/partial response -- silently fell through as
		// "operational" and let a write proceed against a constraint that
		// was never actually confirmed enforcing.
		allOperational := true
		for _, status := range statuses {
			switch status.Status {
			case "OPERATIONAL":
				// fine, keep checking the rest
			case "PENDING":
				allOperational = false
			case "FAILED":
				return errConstraintBootstrapFailed
			default:
				return fmt.Errorf("%w: unexpected constraint status %q for %s.%s", errConstraintBootstrapFailed, status.Status, status.EntityType, status.Label)
			}
		}
		if allOperational {
			return nil
		}
		if time.Now().After(deadline) {
			return errConstraintBootstrapTimedOut
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
