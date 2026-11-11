package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/full-chaos/dev-health-go/pep"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ApprovalDecisionMode is ACR_DEVICE_APPROVAL_PDP: whether an approval asks the
// policy decision point (auth-service) about its approver (CHAOS-13586).
type ApprovalDecisionMode string

const (
	// ApprovalDecisionOff asks nothing: approvals are as before.
	ApprovalDecisionOff ApprovalDecisionMode = "off"
	// ApprovalDecisionShadow asks and records the answer, and never refuses.
	ApprovalDecisionShadow ApprovalDecisionMode = "shadow"
	// ApprovalDecisionEnforce refuses on a deny (403) and on a failure to get
	// a decision (503): fail closed.
	ApprovalDecisionEnforce ApprovalDecisionMode = "enforce"
)

// ParseApprovalDecisionMode reads the mode; "" is off. An unknown value is off
// with an error the caller logs: a typo never passes as enforce, and it never
// stops the start.
func ParseApprovalDecisionMode(value string) (ApprovalDecisionMode, error) {
	switch ApprovalDecisionMode(value) {
	case "", ApprovalDecisionOff:
		return ApprovalDecisionOff, nil
	case ApprovalDecisionShadow, ApprovalDecisionEnforce:
		return ApprovalDecisionMode(value), nil
	}
	return ApprovalDecisionOff, errors.New("ACR_DEVICE_APPROVAL_PDP is not off, shadow or enforce; the approver check is off")
}

var (
	// ErrApprovalNotPermitted refuses an approval the policy decision point
	// denied, or one with no usable approver reference. It is NOT an
	// ErrInvalidDeviceFlow: the routes answer 403 access_denied.
	ErrApprovalNotPermitted = errors.New("the approver may not issue this credential")
	// ErrApprovalDecisionUnavailable refuses an approval when no decision could
	// be had (outage, timeout, rate limit, a malformed answer, missing
	// configuration): the routes answer 503.
	ErrApprovalDecisionUnavailable = errors.New("the approval decision is unavailable")
)

// The closed outcomes of one approval decision (log and metric).
const (
	approvalOutcomeAllowed          = "allowed"
	approvalOutcomeDenied           = "denied"
	approvalOutcomeObligation       = "obligation_mismatch"
	approvalOutcomeReferenceMissing = "reference_missing"
	approvalOutcomeUnavailable      = "unavailable"
	approvalOutcomeNotConfigured    = "not_configured"
)

// ApprovalDecisions asks the policy decision point whether the approver may
// issue the credential, once per approval, after the local checks and before
// the approval is stored. Architecture: web docs/auth-acr-device-approval.md.
type ApprovalDecisions struct {
	mode     ApprovalDecisionMode
	decider  pep.Decider
	logger   *slog.Logger
	outcomes metric.Int64Counter
}

// NewApprovalDecisions builds the check. In shadow or enforce a nil decider
// (no workload client or PDP URL) is logged once as an ERROR and every
// approval reports not_configured: enforce refuses it (503), shadow records it.
// The process starts either way.
func NewApprovalDecisions(mode ApprovalDecisionMode, decider pep.Decider, logger *slog.Logger) (*ApprovalDecisions, error) {
	if logger == nil {
		logger = slog.Default()
	}
	outcomes, err := otel.Meter("github.com/full-chaos/dev-health-acr/internal/auth").Int64Counter("acr_device_approval_decisions_total",
		metric.WithDescription("Device approval decisions by the policy decision point, by mode and outcome."))
	if err != nil {
		return nil, fmt.Errorf("device approval decision counter: %w", err)
	}
	if mode != ApprovalDecisionOff && decider == nil {
		logger.Error("device approval decisions are on but no policy decision point client is configured; enforce refuses every approval",
			"mode", string(mode), "missing", "ACR_DEVICE_APPROVAL_PDP_URL or ACR_AUTH_WORKLOAD_*")
	}
	return &ApprovalDecisions{mode: mode, decider: decider, logger: logger, outcomes: outcomes}, nil
}

// Check is nil when the approval may go on. organizationWide approvals check
// "*" against the repository obligation; others check each repository.
func (d *ApprovalDecisions) Check(ctx context.Context, principal storage.Principal, repositories []string) error {
	if d == nil || d.mode == ApprovalDecisionOff {
		return nil
	}
	outcome, reason := d.decide(ctx, principal, repositories)
	d.outcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("mode", string(d.mode)), attribute.String("outcome", outcome)))
	d.logger.InfoContext(ctx, "device approval decision", "mode", string(d.mode), "outcome", outcome, "reason", reason)
	if d.mode != ApprovalDecisionEnforce {
		return nil
	}
	switch outcome {
	case approvalOutcomeAllowed:
		return nil
	case approvalOutcomeUnavailable, approvalOutcomeNotConfigured:
		return ErrApprovalDecisionUnavailable
	default:
		return ErrApprovalNotPermitted
	}
}

func (d *ApprovalDecisions) decide(ctx context.Context, principal storage.Principal, repositories []string) (string, string) {
	if principal.ApproverReference == "" {
		reason := principal.ApproverReferenceFault
		if reason == "" {
			reason = ApproverReferenceAbsent
		}
		return approvalOutcomeReferenceMissing, reason
	}
	if d.decider == nil {
		return approvalOutcomeNotConfigured, "no_client"
	}
	decision := d.decider.Decide(ctx, pep.Request{
		PrincipalReference: principal.ApproverReference,
		Action:             approverReferenceAction,
		ResourceType:       "organization",
		ResourceKey:        principal.OrgID,
		OrganizationID:     principal.OrgID,
	})
	reason := string(decision.Reason())
	if !decision.Allowed() {
		if decision.Local() && decision.Reason() != pep.ReasonRefused && decision.Reason() != pep.ReasonInvalidRequest {
			return approvalOutcomeUnavailable, reason
		}
		return approvalOutcomeDenied, reason
	}
	obligations := decision.Obligations()
	if !obligations.TenantAllows(principal.OrgID) {
		return approvalOutcomeObligation, "tenant_scope"
	}
	for _, repository := range repositories {
		if !obligations.RepositoryAllowed(repository) {
			return approvalOutcomeObligation, "repository_scope"
		}
	}
	return approvalOutcomeAllowed, reason
}
