package contextpacket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	staleAfterSeconds        = 86_400
	defaultSnapshotRetention = 30 * 24 * time.Hour
	snapshotSaveTimeout      = time.Second
)

var ErrEvidenceScopeMismatch = errors.New("contextpacket: evidence bundle scope does not match resolved scope")

type Options struct {
	Now                                   func() time.Time
	ServiceVersion, MinimumSidecarVersion string
	SnapshotStore                         storage.PacketStore
	SnapshotRetention                     time.Duration
	Observer                              AssemblyObserver
	StoreBackend                          StoreBackend
	Tracer                                AssemblyTraceBoundary
}
type Assembler struct {
	store   storage.EvidenceStore
	options Options
}

func NewAssembler(store storage.EvidenceStore, options Options) *Assembler {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ServiceVersion == "" {
		options.ServiceVersion = "development"
	}
	if options.MinimumSidecarVersion == "" {
		options.MinimumSidecarVersion = "0.1.0"
	}
	if options.SnapshotRetention <= 0 {
		options.SnapshotRetention = defaultSnapshotRetention
	}
	return &Assembler{store: store, options: options}
}

func (a *Assembler) Assemble(ctx context.Context, principal storage.Principal, request contractsv1.ContextPacketRequest) (packet contractsv1.ContextPacket, resultErr error) {
	started := a.options.Now()
	ctx, completeTrace := a.startTrace(ctx, TraceObservation{Stage: TraceStageRequest})
	defer func() { completeTrace(operationOutcome(resultErr)) }()
	defer func() { a.observePacket(ctx, request, packet, resultErr, a.options.Now().Sub(started)) }()
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return contractsv1.ContextPacket{}, err
		}
		return a.degraded(ctx, a.basePacket(principal, request), principal, request, err)
	}
	if err := request.Validate(); err != nil {
		return contractsv1.ContextPacket{}, fmt.Errorf("validate context packet request: %w", err)
	}
	if a.store == nil {
		return contractsv1.ContextPacket{}, errors.New("contextpacket: evidence store is required")
	}
	packet = a.basePacket(principal, request)
	scope, err := a.resolveScope(ctx, principal, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return contractsv1.ContextPacket{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return a.degraded(ctx, packet, principal, request, context.DeadlineExceeded)
		}
		return a.degraded(ctx, packet, principal, request, err)
	}
	scope, err = normalizeUnresolvedScope(principal, request, packet.ContextPacketID, scope)
	if err != nil {
		return contractsv1.ContextPacket{}, err
	}
	packet.ResolvedScope, packet.Repository.RepoID = scope, scope.RepoID
	bundle, err := a.contextForTask(ctx, principal, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return contractsv1.ContextPacket{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return a.degraded(ctx, packet, principal, request, context.DeadlineExceeded)
		}
		return a.degraded(ctx, packet, principal, request, err)
	}
	bundle.ResolvedScope, err = normalizeUnresolvedScope(principal, request, packet.ContextPacketID, bundle.ResolvedScope)
	if err != nil {
		return contractsv1.ContextPacket{}, err
	}
	if !sameScope(scope, bundle.ResolvedScope) {
		return a.degraded(ctx, packet, principal, request, ErrEvidenceScopeMismatch)
	}
	if bundle.QueryVersion != "" {
		packet.QueryVersion = bundle.QueryVersion
	}
	if err := validateEvidenceBundle(bundle.Evidence); err != nil {
		return a.degraded(ctx, packet, principal, request, err)
	}
	visible, hidden := displayableEvidence(bundle.Evidence)
	bundle.Evidence = visible
	bundle.Unavailable = append(bundle.Unavailable, hidden...)
	packet.Freshness.Watermarks, packet.Coverage, packet.Warnings = sortedWatermarks(bundle.Watermarks), coverage(bundle), warnings(bundle)
	rankingStarted := a.options.Now()
	rankingContext, completeRankingTrace := a.startTrace(ctx, TraceObservation{Stage: TraceStageRanking})
	ranked, quotaTruncated := rankEvidence(bundle.Evidence, scope, request.Goal, request.Options.IncludeLowConfidence, request.Options.RequestedCategories, request.Options.MaxItems)
	items := requestedCategories(ranked, request.Options.RequestedCategories)
	completeRankingTrace(OperationSuccess)
	a.observeRanking(rankingContext, packet.QueryVersion, packet.RankingVersion, a.options.Now().Sub(rankingStarted))
	packet.Budget.Truncated = quotaTruncated
	if err := applyBudget(&packet, items); err != nil {
		return contractsv1.ContextPacket{}, err
	}
	if err := finalizePacket(&packet); err != nil {
		return contractsv1.ContextPacket{}, err
	}
	setStatus(&packet, bundle, len(ranked))
	if err := finalizePacket(&packet); err != nil {
		return contractsv1.ContextPacket{}, err
	}
	if err := validatePacket(packet); err != nil {
		return contractsv1.ContextPacket{}, err
	}
	return a.saveSnapshot(ctx, principal, packet)
}

func (a *Assembler) basePacket(principal storage.Principal, request contractsv1.ContextPacketRequest) contractsv1.ContextPacket {
	now := a.options.Now().UTC()
	if request.Scope.AsOf != nil {
		now = request.Scope.AsOf.UTC()
	}
	return contractsv1.ContextPacket{SchemaVersion: contractsv1.ContextPacketSchema, ContextPacketID: packetID(principal, request), RequestID: request.RequestID, GeneratedAt: now, Goal: request.Goal, Repository: request.Repository, RequestedScope: request.Scope, QueryVersion: QueryVersionV1, RankingVersion: RankingVersionV1, Summary: "Evidence-backed context for the requested goal.", Items: []contractsv1.ContextPacketItem{}, RequiredChecks: []contractsv1.RequiredCheck{}, RecommendedNextSteps: []contractsv1.RecommendedStep{}, Freshness: contractsv1.Freshness{AsOf: now, StaleAfterSeconds: staleAfterSeconds, Watermarks: []contractsv1.SourceWatermark{}}, Coverage: contractsv1.Coverage{SourcesConsidered: []string{}, SourcesAvailable: []string{}, SourcesUnavailable: []contractsv1.UnavailableSource{}, DegradedReasons: []string{}}, Budget: contractsv1.PacketBudget{MaxItems: request.Options.MaxItems, MaxOutputTokens: request.Options.MaxOutputTokens, MaxSerializedBytes: request.Options.MaxSerializedBytes}, Warnings: []string{}, Compatibility: contractsv1.Compatibility{ServiceVersion: a.options.ServiceVersion, MinimumSidecarVersion: a.options.MinimumSidecarVersion, SupportedSchemaVersions: []string{contractsv1.ContextPacketSchema, contractsv1.ContextPacketItemSchema, contractsv1.EvidenceRefSchema}}}
}

func (a *Assembler) degraded(ctx context.Context, packet contractsv1.ContextPacket, principal storage.Principal, request contractsv1.ContextPacketRequest, cause error) (contractsv1.ContextPacket, error) {
	if packet.ResolvedScope.RepoID == "" {
		scope, err := unresolvedScope(principal, request, packet.ContextPacketID)
		if err != nil {
			return contractsv1.ContextPacket{}, err
		}
		packet.ResolvedScope, packet.Repository.RepoID = scope, scope.RepoID
	}
	packet.Status = contractsv1.PacketDegraded
	packet.Coverage.Partial = true
	reason := "evidence_retrieval_unavailable"
	if errors.Is(cause, ErrEvidenceScopeMismatch) {
		reason = "evidence_scope_mismatch"
	} else if errors.Is(cause, context.DeadlineExceeded) {
		reason = "evidence_retrieval_timed_out"
	}
	packet.Coverage.DegradedReasons, packet.Warnings, packet.Summary = []string{reason}, []string{reason}, "Evidence retrieval did not complete for the requested goal."
	if err := finalizePacket(&packet); err != nil {
		return contractsv1.ContextPacket{}, err
	}
	if err := validatePacket(packet); err != nil {
		return contractsv1.ContextPacket{}, err
	}
	return a.saveSnapshot(ctx, principal, packet)
}

func (a *Assembler) saveSnapshot(ctx context.Context, principal storage.Principal, packet contractsv1.ContextPacket) (contractsv1.ContextPacket, error) {
	if a.options.SnapshotStore == nil {
		return packet, nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return packet, nil
	}
	if packet.Status == contractsv1.PacketDegraded {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			ctx = context.Background()
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, snapshotSaveTimeout)
		defer cancel()
	}
	expiresAt := a.options.Now().UTC().Add(a.options.SnapshotRetention)
	if err := a.options.SnapshotStore.SaveSnapshot(ctx, principal, packet, expiresAt); err != nil {
		return contractsv1.ContextPacket{}, fmt.Errorf("save packet snapshot: %w", err)
	}
	return packet, nil
}

func unresolvedScope(principal storage.Principal, request contractsv1.ContextPacketRequest, id string) (contractsv1.ResolvedScope, error) {
	plan, err := BuildReadPlanV1(principal, request)
	if err != nil {
		return contractsv1.ResolvedScope{}, err
	}
	return contractsv1.ResolvedScope{RepoID: "unresolved:" + id, RepoSlug: plan.RepoSlug, Branch: plan.Branch, CommitSHA: plan.CommitSHA, Resolution: contractsv1.ScopeUnresolved, FallbackReasons: []string{"scope_resolution_unavailable"}}, nil
}

func normalizeUnresolvedScope(principal storage.Principal, request contractsv1.ContextPacketRequest, packetID string, scope contractsv1.ResolvedScope) (contractsv1.ResolvedScope, error) {
	if scope.Resolution != contractsv1.ScopeUnresolved || scope.RepoID != "" {
		return scope, nil
	}
	normalized, err := unresolvedScope(principal, request, packetID)
	if err != nil {
		return contractsv1.ResolvedScope{}, err
	}
	if scope.RepoSlug != "" {
		normalized.RepoSlug = scope.RepoSlug
	}
	if len(scope.FallbackReasons) > 0 {
		normalized.FallbackReasons = scope.FallbackReasons
	}
	return normalized, nil
}
func sameScope(a, b contractsv1.ResolvedScope) bool {
	return a.RepoID == b.RepoID && a.RepoSlug == b.RepoSlug && a.Branch == b.Branch && a.CommitSHA == b.CommitSHA && a.Resolution == b.Resolution && strings.Join(a.FallbackReasons, "\000") == strings.Join(b.FallbackReasons, "\000")
}
func packetID(p storage.Principal, r contractsv1.ContextPacketRequest) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{p.OrgID, r.RequestID, r.Repository.Slug, r.Scope.Branch, r.Scope.CommitSHA}, "\000")))
	return "pkt_" + hex.EncodeToString(sum[:12])
}
