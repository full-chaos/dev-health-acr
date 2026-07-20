package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const localEvidencePrefix = "local:codegraph:v1:"

type localFederationRuntime struct {
	config          sidecar.LocalIndexConfig
	clock           func() time.Time
	hash            func([]byte) [32]byte
	providerFactory func(sidecar.LocalIndexConfig, sidecar.LocalWorkspaceSnapshot) sidecar.LocalIndexProvider
	cache           *localEvidenceCache
}

func newLocalFederationRuntime(config sidecar.LocalIndexConfig, clock func() time.Time, hash func([]byte) [32]byte) *localFederationRuntime {
	return &localFederationRuntime{config: config, clock: clock, hash: hash, providerFactory: sidecar.NewWorkspaceLocalIndexProvider, cache: newLocalEvidenceCache(1024, 30*time.Minute, clock)}
}

func (r *localFederationRuntime) eligible(snapshot *sidecar.LocalWorkspaceSnapshot) bool {
	return r != nil && snapshot != nil && r.config.Err == nil && r.config.Provider != sidecar.LocalIndexProviderDisabled
}

func (r *localFederationRuntime) bundle(ctx context.Context, scope resolvedTaskScope, input contractsv1.MCPContextForTaskRequest, options contractsv1.PacketOptions) (sidecar.LocalEvidenceBundle, error) {
	if !r.eligible(scope.Workspace) {
		return sidecar.LocalEvidenceBundle{}, sidecar.ErrLocalIndexUnavailable
	}
	reserve := localReservation(r.config, options)
	if reserve.MaxItems == 0 || reserve.MaxOutputTokens == 0 || reserve.MaxSerializedBytes == 0 {
		return sidecar.LocalEvidenceBundle{}, sidecar.ErrLocalIndexUnavailable
	}
	provider := r.providerFactory(r.config, *scope.Workspace)
	child, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()
	return provider.ContextForTask(child, sidecar.LocalContextRequest{Goal: input.Goal, TaskRef: scope.Scope.TaskRef, RequestedCategories: options.RequestedCategories, Workspace: scope.Workspace, MaxItems: reserve.MaxItems, MaxOutputTokens: reserve.MaxOutputTokens})
}

func localReservation(config sidecar.LocalIndexConfig, options contractsv1.PacketOptions) contractsv1.PacketOptions {
	items := min(config.MaxItems, options.MaxItems/4)
	tokens := min(config.MaxOutputTokens, options.MaxOutputTokens/4)
	bytes := min(int(config.MaxSerializedBytes), options.MaxSerializedBytes/4)
	if options.MaxItems-items < 1 || options.MaxOutputTokens-tokens < 500 || options.MaxSerializedBytes-bytes < 8192 {
		return contractsv1.PacketOptions{}
	}
	return contractsv1.PacketOptions{MaxItems: items, MaxOutputTokens: tokens, MaxSerializedBytes: bytes}
}

func (r *localFederationRuntime) mapBundle(repository string, bundle sidecar.LocalEvidenceBundle, occupied map[string]struct{}) ([]contractsv1.ContextPacketItem, []contractsv1.EvidenceRef, error) {
	items := make([]contractsv1.ContextPacketItem, 0, len(bundle.Evidence))
	refs := make([]contractsv1.EvidenceRef, 0, len(bundle.Evidence))
	for index, evidence := range bundle.Evidence {
		id, err := r.publicID(repository, bundle, evidence, index, occupied)
		if err != nil {
			return nil, nil, err
		}
		availability := contractsv1.EvidenceAvailable
		confidence, severity := 0.80, contractsv1.SeverityInfo
		if bundle.Status != sidecar.LocalIndexStatusAvailable {
			availability, confidence, severity = contractsv1.EvidenceStale, 0.60, contractsv1.SeverityWarning
		}
		observedAt := r.clock().UTC()
		if bundle.IndexedAt != nil {
			observedAt = bundle.IndexedAt.UTC()
		}
		ref := contractsv1.EvidenceRef{SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: id, Source: contractsv1.EvidenceSource{System: "local_index", EntityType: "code", EntityID: id, DisplayLabel: boundedText(evidence.Title, 256)}, Provenance: "derived", Confidence: confidence, Citation: "local CodeGraph index", ObservedAt: observedAt, SourceVersion: bundle.ProviderVersion, Availability: availability, Metadata: map[string]any{"query_id": evidence.QueryID, "relation": evidence.Relation, "repository_path": evidence.RepositoryPath, "start_line": evidence.StartLine}}
		item := contractsv1.ContextPacketItem{SchemaVersion: contractsv1.ContextPacketItemSchema, PacketItemID: id + ":item", Category: contractsv1.CategoryEvidence, ClaimKind: contractsv1.ClaimObserved, Title: boundedText(evidence.Title, 256), Summary: boundedText(evidence.Excerpt, 1024), WhyIncluded: "Local code evidence matched the task scope.", RuleID: "local_code_evidence", Confidence: confidence, Severity: severity, Rank: index + 1, Flags: contractsv1.ItemFlags{UntrustedContent: true, Uncertain: availability != contractsv1.EvidenceAvailable}, RelatedEntities: []contractsv1.RelatedEntity{{Type: "repository", ID: repository, Label: repository}}, EvidenceRefIDs: []string{id}}
		items, refs = append(items, item), append(refs, ref)
	}
	return items, refs, nil
}

func (r *localFederationRuntime) publicID(repository string, bundle sidecar.LocalEvidenceBundle, evidence sidecar.LocalExpandedEvidence, counter int, occupied map[string]struct{}) (string, error) {
	for {
		payload := struct {
			Schema, Kind, Provider, Version, Repository, IndexedAt, Ref, Commit, Query, QueryVersion, EvidenceQuery, Locator string
			Counter                                                                                                          int
		}{contractsv1.EvidenceRefSchema, "code", bundle.ProviderID, bundle.ProviderVersion, strings.ToLower(repository), indexedAt(bundle), "", "indexed_commit_unknown", bundle.QueryID, bundle.QueryVersion, evidence.QueryID, evidence.Locator, counter}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		digest := r.hash(encoded)
		id := localEvidencePrefix + hex.EncodeToString(digest[:])
		if _, exists := occupied[id]; !exists {
			occupied[id] = struct{}{}
			return id, nil
		}
		counter++
	}
}

func indexedAt(bundle sidecar.LocalEvidenceBundle) string {
	if bundle.IndexedAt == nil {
		return ""
	}
	return bundle.IndexedAt.UTC().Format(time.RFC3339Nano)
}

func boundedText(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

var _ = sha256.Sum256
