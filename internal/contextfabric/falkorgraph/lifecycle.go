package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// This file is CHAOS-3898 S2a's falkorgraph-side KeyResolver plumbing
// (design brief §3.1): the six graph-key call sites (reader.go's
// ResolveSubjects/DiscoverContext; projection.go's ApplyProjectionBatch/
// ProjectionWatermark/PurgeOrganization; this file's own DeleteEpochGraph)
// resolve their key through resolveReadKey/resolveWriteKey below instead of
// deriving graphKey(a.config.GraphPrefix, orgID) inline. A nil
// Config.EpochResolver -- every production composition root today --
// degrades every one of them to epoch 0's key, byte-identical to
// pre-CHAOS-3898 behavior; see Config.EpochResolver's own doc comment.

// resolveReadKey resolves the key a READ call site should use: the org's
// ACTIVE epoch (design brief §3.1: "readers resolve the ACTIVE key").
// role is stamped on the cf_resolved_graph_key signal (§2.0/§5b) -- the
// role vocabulary itself is defined once, in contextfabric.GraphKeyRole,
// so a caller cannot invent an ad hoc string.
func (a *Adapter) resolveReadKey(ctx context.Context, orgID string, role contextfabric.GraphKeyRole) (string, error) {
	epoch, err := a.resolveActiveEpoch(ctx, orgID)
	if err != nil {
		return "", err
	}
	key := graphKeyForEpoch(a.config.GraphPrefix, orgID, epoch)
	a.stampResolvedKey(ctx, orgID, epoch, role, key)
	return key, nil
}

// resolveWriteKey resolves the key a WRITE call site should use: the org's
// open BUILD target epoch while a build is in progress, else the ACTIVE
// epoch (design brief §3.1: "the projector resolves the BUILD key while a
// build is open").
func (a *Adapter) resolveWriteKey(ctx context.Context, orgID string) (string, error) {
	if a.config.EpochResolver == nil {
		key := graphKeyForEpoch(a.config.GraphPrefix, orgID, 0)
		a.stampResolvedKey(ctx, orgID, 0, contextfabric.GraphKeyRoleProjectionWrite, key)
		return key, nil
	}
	buildEpoch, ok, err := a.config.EpochResolver.ResolveBuildEpoch(ctx, orgID)
	if err != nil {
		return "", safeDependencyError("resolve build epoch", err)
	}
	epoch := buildEpoch
	if !ok {
		epoch, err = a.resolveActiveEpoch(ctx, orgID)
		if err != nil {
			return "", err
		}
	}
	key := graphKeyForEpoch(a.config.GraphPrefix, orgID, epoch)
	a.stampResolvedKey(ctx, orgID, epoch, contextfabric.GraphKeyRoleProjectionWrite, key)
	return key, nil
}

func (a *Adapter) resolveActiveEpoch(ctx context.Context, orgID string) (int64, error) {
	if a.config.EpochResolver == nil {
		return 0, nil
	}
	epoch, err := a.config.EpochResolver.ResolveActiveEpoch(ctx, orgID)
	if err != nil {
		return 0, safeDependencyError("resolve active epoch", err)
	}
	return epoch, nil
}

func (a *Adapter) stampResolvedKey(ctx context.Context, orgID string, epoch int64, role contextfabric.GraphKeyRole, key string) {
	if a.config.LifecycleTelemetry != nil {
		a.config.LifecycleTelemetry.RecordResolvedGraphKey(ctx, orgID, epoch, role, key)
	}
}

// DeleteEpochGraph is the retire executor's terminal action
// (contextfabric.EpochGraphDeleter, design brief §3.5): it deletes the
// graph key for ONE specific epoch, refusing outright (never deleting
// anything) when epoch == activeEpoch (the isSweepTargetSafe shape carried
// over from hnsw_sweep.go's own safety check -- see graphKeyForEpoch's doc
// comment for why an epoch-number inequality is equivalent to a
// string-key inequality here). Epoch 0 is NOT otherwise special: an
// organization's first-ever flip legitimately retires it once it is no
// longer active. orgID and activeEpoch are both explicit parameters, never
// re-derived internally, matching contextfabric.EpochGraphDeleter's own
// "caller cannot forget the safety comparison" contract.
func (a *Adapter) DeleteEpochGraph(ctx context.Context, orgID string, epoch, activeEpoch int64) error {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return errors.New("falkorgraph: organization is required")
	}
	if epoch < 0 {
		return errors.New("falkorgraph: epoch must be non-negative")
	}
	// The ONLY structural safety invariant: never delete the epoch that is
	// CURRENTLY active/serving (the isSweepTargetSafe shape). Epoch 0 is
	// NOT otherwise special here -- an organization's first-ever flip
	// (0 -> 1) legitimately retires epoch 0's legacy graph exactly like
	// any other abandoned epoch, once it is no longer ActiveEpoch.
	if epoch == activeEpoch {
		return fmt.Errorf("falkorgraph: refusing to delete epoch %d -- it is organization %s's active epoch", epoch, orgID)
	}
	key := graphKeyForEpoch(a.config.GraphPrefix, orgID, epoch)
	if a.config.LifecycleTelemetry != nil {
		a.config.LifecycleTelemetry.RecordResolvedGraphKey(ctx, orgID, epoch, contextfabric.GraphKeyRoleRetireTarget, key)
	}
	err := a.api.deleteGraph(ctx, key)
	a.bootstrapMu.Lock()
	delete(a.bootstrapDone, key)
	a.bootstrapMu.Unlock()
	if err != nil && !errors.Is(err, ErrNotFound) {
		return safeDependencyError("delete epoch organization graph", err)
	}
	return nil
}

var _ contextfabric.EpochGraphDeleter = (*Adapter)(nil)

// AssertResolvedPrefix is the CHAOS-3898 §2.0 startup/config assertion:
// each process asserts a non-empty resolved graph-key prefix at boot and
// logs the resolved value once. Config.validate() (already invoked by
// New/NewWithEmbedder) independently refuses an empty/oversized
// GraphPrefix at Adapter construction -- this function exists so
// composition can additionally emit the §5b RecordStartupPrefixAssertion
// signal and the one-time boot log line design brief §2.0 asks for, before
// the Adapter is even constructed. logger/telemetry are nil-safe (fall
// back to slog.Default() / NoopGraphLifecycleTelemetry respectively).
func AssertResolvedPrefix(logger *slog.Logger, telemetry contextfabric.GraphLifecycleTelemetry, prefix string) error {
	if logger == nil {
		logger = slog.Default()
	}
	if telemetry == nil {
		telemetry = contextfabric.NoopGraphLifecycleTelemetry{}
	}
	resolved := strings.TrimSpace(prefix)
	telemetry.RecordStartupPrefixAssertion(context.Background(), resolved != "")
	if resolved == "" {
		return errors.New("falkorgraph: resolved graph key prefix is empty at startup")
	}
	logger.Info("context_fabric: resolved falkordb graph key prefix", "graph_prefix", resolved)
	return nil
}
