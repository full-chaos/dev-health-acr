package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const localEvidencePrefix = "local:codegraph:v1:"

var errLocalFederationFinalize = errors.New("mcp: local federation finalization failed")

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
	bundle, err := provider.ContextForTask(child, sidecar.LocalContextRequest{TaskID: localTaskID(scope, input, options), Goal: input.Goal, TaskRef: scope.Scope.TaskRef, RequestedCategories: options.RequestedCategories, Workspace: scope.Workspace, MaxItems: reserve.MaxItems, MaxOutputTokens: reserve.MaxOutputTokens})
	if err != nil {
		return sidecar.LocalEvidenceBundle{}, err
	}
	return sidecar.NormalizeLocalEvidenceBundle(bundle)
}

func localTaskID(scope resolvedTaskScope, input contractsv1.MCPContextForTaskRequest, options contractsv1.PacketOptions) string {
	files := append([]string(nil), scope.Scope.Files...)
	sort.Strings(files)
	categories := make([]string, len(options.RequestedCategories))
	for i, category := range options.RequestedCategories {
		categories[i] = string(category)
	}
	sort.Strings(categories)
	state := sidecar.LocalChangedFilesNotRequested
	if scope.Workspace != nil {
		state = scope.Workspace.ChangedFilesState
	}
	asOf := ""
	if scope.Scope.AsOf != nil {
		asOf = scope.Scope.AsOf.UTC().Format(time.RFC3339Nano)
	}
	payload, _ := json.Marshal(struct {
		Schema, Repository, Goal, Branch, Commit, TaskRef, ChangedFilesState, AsOf string
		Files, Categories                                                          []string
		TimeWindowDays                                                             int
	}{"local-task.v1", strings.ToLower(scope.Repository.Slug), input.Goal, scope.Scope.Branch, strings.ToLower(scope.Scope.CommitSHA), scope.Scope.TaskRef, string(state), asOf, files, categories, scope.Scope.TimeWindowDays})
	digest := sha256.Sum256(payload)
	return "local-task:v1:" + hex.EncodeToString(digest[:])
}

type mappedLocalBundle struct {
	bundle   sidecar.LocalEvidenceBundle
	items    []contractsv1.ContextPacketItem
	refs     []contractsv1.EvidenceRef
	evidence []sidecar.LocalExpandedEvidence
}

func (r *localFederationRuntime) mapLocalBundle(repository string, bundle sidecar.LocalEvidenceBundle, occupied map[string]struct{}) (mappedLocalBundle, error) {
	if err := validateDistinctLocalEvidence(bundle); err != nil {
		return mappedLocalBundle{}, err
	}
	items, refs, err := r.mapBundle(repository, bundle, occupied)
	if err != nil {
		return mappedLocalBundle{}, err
	}
	return mappedLocalBundle{bundle: bundle, items: items, refs: refs, evidence: append([]sidecar.LocalExpandedEvidence(nil), bundle.Evidence...)}, nil
}

func validateDistinctLocalEvidence(bundle sidecar.LocalEvidenceBundle) error {
	seenIDs, seenLocators, seenKeys := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, evidence := range bundle.Evidence {
		key := evidence.QueryID + "\x00" + evidence.Locator
		for value, seen := range map[string]map[string]struct{}{evidence.ID: seenIDs, evidence.Locator: seenLocators, key: seenKeys} {
			if _, exists := seen[value]; exists {
				return fmt.Errorf("duplicate local evidence")
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func (m *mappedLocalBundle) trimTo(maxItems, maxTokens, maxBytes int) bool {
	trimmed := false
	for len(m.items) > 0 && (len(m.items) > maxItems || localEstimatedTokens(m.evidence) > maxTokens || localJSONBytes(m.items, m.refs) > maxBytes) {
		last := len(m.items) - 1
		m.items = m.items[:last]
		m.refs = m.refs[:last]
		m.evidence = m.evidence[:last]
		trimmed = true
	}
	return trimmed
}

func localEstimatedTokens(evidence []sidecar.LocalExpandedEvidence) int {
	total := 0
	for _, value := range evidence {
		total += value.EstimatedTokens
	}
	return total
}

func localJSONBytes(items []contractsv1.ContextPacketItem, refs []contractsv1.EvidenceRef) int {
	encoded, err := json.Marshal(struct {
		Items []contractsv1.ContextPacketItem `json:"items"`
		Refs  []contractsv1.EvidenceRef       `json:"evidence_refs"`
	}{Items: items, Refs: refs})
	if err != nil {
		return 0
	}
	return len(encoded)
}

func localContext(bundle sidecar.LocalEvidenceBundle, mapped mappedLocalBundle, trimmed bool) contractsv1.MCPLocalContext {
	warnings := appendDistinctWarnings(make([]string, 0, len(bundle.Warnings)+1), bundle.Warnings)
	if trimmed {
		warnings = appendDistinctWarning(warnings, "local_budget_exhausted")
	}
	return contractsv1.MCPLocalContext{
		Provider: bundle.ProviderID, Status: localContextStatus(bundle.Status), ProviderVersion: bundle.ProviderVersion,
		QueryVersion: bundle.QueryVersion, IndexedAt: bundle.IndexedAt, IndexedRef: bundle.IndexedRef, IndexedCommit: bundle.IndexedCommit, Freshness: localFreshness(bundle.Freshness),
		Warnings: warnings, Items: mapped.items, EvidenceRefs: mapped.refs,
	}
}

func (r *localFederationRuntime) unavailableContext(err error) (contractsv1.MCPLocalContext, bool) {
	provider := "local_index"
	if r.config.Provider == sidecar.LocalIndexProviderAuto || r.config.Provider == sidecar.LocalIndexProviderCodeGraph {
		provider = "codegraph"
	}
	warning := ""
	var localErr *sidecar.LocalIndexError
	if errors.As(err, &localErr) {
		if localErr.Code() != sidecar.LocalIndexErrorTimeout {
			return contractsv1.MCPLocalContext{}, false
		}
		warning = string(localErr.Code())
	} else if errors.Is(err, context.DeadlineExceeded) {
		warning = string(sidecar.LocalIndexErrorTimeout)
	} else {
		return contractsv1.MCPLocalContext{}, false
	}
	return contractsv1.MCPLocalContext{
		Provider: provider, Status: contractsv1.MCPLocalContextUnavailable, ProviderVersion: "unavailable", QueryVersion: "unavailable",
		Freshness: contractsv1.MCPLocalFreshnessUnknown, Warnings: []string{warning}, Items: []contractsv1.ContextPacketItem{}, EvidenceRefs: []contractsv1.EvidenceRef{},
	}, true
}

func localContextStatus(status sidecar.LocalIndexStatus) contractsv1.MCPLocalContextStatus {
	switch status {
	case sidecar.LocalIndexStatusAvailable:
		return contractsv1.MCPLocalContextAvailable
	case sidecar.LocalIndexStatusDegraded:
		return contractsv1.MCPLocalContextDegraded
	default:
		return contractsv1.MCPLocalContextUnavailable
	}
}

func localFreshness(freshness sidecar.LocalIndexFreshness) contractsv1.MCPLocalFreshness {
	switch freshness {
	case sidecar.LocalIndexFreshnessFresh:
		return contractsv1.MCPLocalFreshnessFresh
	case sidecar.LocalIndexFreshnessStale:
		return contractsv1.MCPLocalFreshnessStale
	default:
		return contractsv1.MCPLocalFreshnessUnknown
	}
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
		id, err := r.publicID(repository, bundle, evidence, occupied)
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
		ref := contractsv1.EvidenceRef{SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: id, Source: contractsv1.EvidenceSource{System: "local_index", EntityType: "code", EntityID: id, DisplayLabel: boundedText(evidence.Title, 256)}, Provenance: "derived", Confidence: confidence, Citation: "local CodeGraph index", ObservedAt: observedAt, SourceVersion: bundle.ProviderVersion, Availability: availability, Metadata: map[string]any{"query_id": evidence.QueryID, "relation": evidence.Relation, "start_line": evidence.StartLine}}
		item := contractsv1.ContextPacketItem{SchemaVersion: contractsv1.ContextPacketItemSchema, PacketItemID: id + ":item", Category: contractsv1.CategoryEvidence, ClaimKind: contractsv1.ClaimObserved, Title: boundedText(evidence.Title, 256), Summary: boundedText(evidence.Excerpt, 1024), WhyIncluded: "Local code evidence matched the task scope.", RuleID: "local_code_evidence", Confidence: confidence, Severity: severity, Rank: index + 1, Flags: contractsv1.ItemFlags{UntrustedContent: true, Uncertain: availability != contractsv1.EvidenceAvailable}, RelatedEntities: []contractsv1.RelatedEntity{{Type: "repository", ID: repository, Label: repository}}, EvidenceRefIDs: []string{id}}
		items, refs = append(items, item), append(refs, ref)
	}
	return items, refs, nil
}

func (r *localFederationRuntime) publicID(repository string, bundle sidecar.LocalEvidenceBundle, evidence sidecar.LocalExpandedEvidence, occupied map[string]struct{}) (string, error) {
	for counter := range 256 {
		payload := struct {
			Schema, Kind, Provider, Version, Repository, IndexedAt, Ref, Commit, Query, QueryVersion, EvidenceQuery, Locator string
			Counter                                                                                                          int
		}{contractsv1.EvidenceRefSchema, "code", bundle.ProviderID, bundle.ProviderVersion, strings.ToLower(repository), indexedAt(bundle), bundle.IndexedRef, bundle.IndexedCommit, bundle.QueryID, bundle.QueryVersion, evidence.QueryID, evidence.Locator, counter}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		digest := r.hash(encoded)
		id := localEvidencePrefix + hex.EncodeToString(digest[:])
		itemID := id + ":item"
		_, evidenceOccupied := occupied[id]
		_, itemOccupied := occupied[itemID]
		if !evidenceOccupied && !itemOccupied {
			occupied[id] = struct{}{}
			occupied[itemID] = struct{}{}
			return id, nil
		}
	}
	return "", errLocalFederationFinalize
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
