package limits

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidSubject         = errors.New("limits: invalid subject")
	ErrInvalidRequestClass    = errors.New("limits: invalid request class")
	ErrInvalidPolicy          = errors.New("limits: invalid policy")
	ErrInvalidUsage           = errors.New("limits: invalid resource usage")
	ErrResourceBudgetExceeded = errors.New("limits: resource budget exceeded")
)

type RequestClass uint8

const (
	RequestClassAuth RequestClass = iota + 1
	RequestClassContext
	RequestClassEvidence
	RequestClassSnapshot
	RequestClassEpisode
)

type Subject struct {
	OrgID        string
	CredentialID string
}

type ResourceBudget struct{ MaxItems, MaxTokens, MaxBytes int64 }

type AuthPolicy struct {
	Window                          time.Duration
	PerOrgLimit, PerCredentialLimit int
	Resources                       ResourceBudget
}
type ContextPolicy struct {
	Window                          time.Duration
	PerOrgLimit, PerCredentialLimit int
	Resources                       ResourceBudget
}
type EvidencePolicy struct {
	Window                          time.Duration
	PerOrgLimit, PerCredentialLimit int
	Resources                       ResourceBudget
}
type SnapshotPolicy struct {
	Window                          time.Duration
	PerOrgLimit, PerCredentialLimit int
	Resources                       ResourceBudget
}
type EpisodePolicy struct {
	Window                          time.Duration
	PerOrgLimit, PerCredentialLimit int
	Resources                       ResourceBudget
}

type PolicySet struct {
	Auth     AuthPolicy
	Context  ContextPolicy
	Evidence EvidencePolicy
	Snapshot SnapshotPolicy
	Episode  EpisodePolicy
}

type quotaPolicy struct {
	Window                          time.Duration
	PerOrgLimit, PerCredentialLimit int
	Resources                       ResourceBudget
}

func (p PolicySet) policy(class RequestClass) (quotaPolicy, error) {
	var policy quotaPolicy
	switch class {
	case RequestClassAuth:
		policy = quotaPolicy(p.Auth)
	case RequestClassContext:
		policy = quotaPolicy(p.Context)
	case RequestClassEvidence:
		policy = quotaPolicy(p.Evidence)
	case RequestClassSnapshot:
		policy = quotaPolicy(p.Snapshot)
	case RequestClassEpisode:
		policy = quotaPolicy(p.Episode)
	default:
		return quotaPolicy{}, ErrInvalidRequestClass
	}
	if policy.PerOrgLimit < 0 || policy.PerCredentialLimit < 0 || !policy.Resources.valid() || (policy.Window <= 0 && (policy.PerOrgLimit > 0 || policy.PerCredentialLimit > 0)) {
		return quotaPolicy{}, fmt.Errorf("%w for request class %d", ErrInvalidPolicy, class)
	}
	return policy, nil
}

func (budget ResourceBudget) valid() bool {
	return budget.MaxItems >= 0 && budget.MaxTokens >= 0 && budget.MaxBytes >= 0
}

func (budget ResourceBudget) allows(usage ResourceUsage) bool {
	return (budget.MaxItems == 0 || usage.Items <= budget.MaxItems) &&
		(budget.MaxTokens == 0 || usage.Tokens <= budget.MaxTokens) &&
		(budget.MaxBytes == 0 || usage.Bytes <= budget.MaxBytes)
}

func (p PolicySet) validate() error {
	for _, class := range []RequestClass{RequestClassAuth, RequestClassContext, RequestClassEvidence, RequestClassSnapshot, RequestClassEpisode} {
		if _, err := p.policy(class); err != nil {
			return err
		}
	}
	return nil
}

func (s Subject) validate() error {
	if !validIdentifier(s.OrgID) || !validIdentifier(s.CredentialID) {
		return ErrInvalidSubject
	}
	return nil
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}
