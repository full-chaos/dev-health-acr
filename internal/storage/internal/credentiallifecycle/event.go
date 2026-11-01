package credentiallifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	eventSchemaVersion = "acr-credential-lifecycle-event.v1"
	eventAggregateType = "acr_credential"
	eventTypePrefix    = "acr_credential."
	maxWireRefLength   = 128
	encodedRefPrefix   = "sha256_"
)

var (
	ErrInvalidEvent     = errors.New("credential lifecycle event is invalid")
	ErrRevisionConflict = errors.New("credential lifecycle revision already written")

	wireRefPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	scopePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*(:[a-z][a-z0-9_]*)+$`)
)

type EventKind string

const (
	KindIssued         EventKind = "issued"
	KindDeviceRedeemed EventKind = "device_redeemed"
	KindRotated        EventKind = "rotated"
	KindRevoked        EventKind = "revoked"
)

type ActorType string

const (
	ActorUser       ActorType = "user"
	ActorOperator   ActorType = "operator"
	ActorCredential ActorType = "credential"
	ActorSystem     ActorType = "system"
)

// Actor names who acted. Ref is the raw acr-side string: the mapper encodes
// it, and ignores it for a credential actor (the contract fixes that ref) and
// refuses it for the system.
type Actor struct {
	Type ActorType
	Ref  string
}

// Facts are the explicit inputs of one event. The mapper never receives a
// credential record, so a token prefix or hash has no path into a row.
type Facts struct {
	Kind EventKind
	// Credential is the credential the event is about: the new credential for
	// issued, device_redeemed and rotated, and the credential as it stood
	// before the mutation for revoked.
	Credential contractsv1.ClientCredential
	// Revision is the aggregate revision of a revoked event (2 or more); the
	// kinds that open an aggregate are always revision 1.
	Revision int
	ParentID string
	At       time.Time
	Actor    Actor
}

// Row is one outbox row: the wire event and the keys the publisher reads.
type Row struct {
	OrgID       string
	AggregateID string
	Revision    int
	EventType   string
	Payload     []byte
	CreatedAt   time.Time
}

// Sink is the transaction the rows join. Insert must refuse a second row for
// the same (aggregate, revision) with ErrRevisionConflict.
type Sink interface {
	MaxRevision(ctx context.Context, aggregateID string) (int, error)
	Insert(ctx context.Context, row Row) error
}

// OrgWireID is the Auth Control Plane's form of an acr organization id: org_
// and the canonical lower-case UUID text, the form every provisioning event
// carries.
func OrgWireID(orgID string) (string, error) {
	if len(orgID) != 36 {
		return "", fmt.Errorf("%w: organization id", ErrInvalidEvent)
	}
	parsed, err := uuid.Parse(orgID)
	if err != nil {
		return "", fmt.Errorf("%w: organization id", ErrInvalidEvent)
	}
	return "org_" + parsed.String(), nil
}

// WireRef is the wire form of an acr-side owner or actor string: a value that
// fits the opaque id pattern goes as it is, any other value as sha256_ and the
// lower-case hex of its exact bytes. An empty value is never encoded.
func WireRef(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%w: empty reference", ErrInvalidEvent)
	}
	if wireRefPattern.MatchString(value) {
		return value, nil
	}
	sum := sha256.Sum256([]byte(value))
	return encodedRefPrefix + hex.EncodeToString(sum[:]), nil
}

// ClassOf is total: the stored class when it is in the vocabulary (the
// 0058/0063 CHECK admits nothing else), otherwise the class the issuance
// facts derive, ending in org_bearer. It reads the credential and writes
// nothing.
func ClassOf(c contractsv1.ClientCredential) ClientClass {
	if class := ClientClass(c.Provenance.ClientClass); class.Valid() {
		return class
	}
	switch {
	case c.WorkloadBindingID != nil && *c.WorkloadBindingID != "":
		return ClientClassWorkload
	case c.Provenance.ApprovingUserID != "":
		if c.Provenance.ClientID != "" || c.Resource != "" {
			return ClientClassOAuthUser
		}
		return ClientClassNativeDevice
	default:
		return ClientClassOrgBearer
	}
}

type wireEvent struct {
	SchemaVersion  string      `json:"schema_version"`
	AggregateType  string      `json:"aggregate_type"`
	AggregateID    string      `json:"aggregate_id"`
	EventType      string      `json:"event_type"`
	IdempotencyKey string      `json:"idempotency_key"`
	CreatedAt      string      `json:"created_at"`
	Payload        wirePayload `json:"payload"`
}

type wirePayload struct {
	Revision       int        `json:"revision"`
	OrganizationID string     `json:"organization_id"`
	ActorType      ActorType  `json:"actor_type"`
	ActorRef       string     `json:"actor_ref,omitempty"`
	Change         wireChange `json:"change"`
}

type wireChange struct {
	Status           string   `json:"status"`
	AcrClass         string   `json:"acr_class,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	OwnerSubject     string   `json:"owner_subject,omitempty"`
	ExpiresAt        string   `json:"expires_at,omitempty"`
	RotationParentID string   `json:"rotation_parent_id,omitempty"`
}

func wireTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.999999") + "Z"
}

func invalidEvent(what string) error { return fmt.Errorf("%w: %s", ErrInvalidEvent, what) }

// BuildRow maps facts onto the vendored acr-credential-lifecycle-event.v1
// contract. It refuses what the contract refuses, so an event that cannot be
// valid is never written.
func BuildRow(f Facts) (Row, error) {
	c := f.Credential
	if f.At.IsZero() {
		return Row{}, invalidEvent("time")
	}
	org, err := OrgWireID(c.OrgID)
	if err != nil {
		return Row{}, err
	}
	if !wireRefPattern.MatchString(c.CredentialID) {
		return Row{}, invalidEvent("aggregate id")
	}
	revision := 1
	kind := f.Kind
	class := ClassOf(c)
	switch kind {
	case KindIssued, KindDeviceRedeemed, KindRotated:
		if kind == KindDeviceRedeemed && class == ClientClassBrowser {
			kind = KindIssued
		}
		if kind == KindDeviceRedeemed && class != ClientClassNativeDevice && class != ClientClassOAuthUser {
			return Row{}, invalidEvent("device_redeemed class")
		}
		if kind == KindRotated && class == ClientClassBrowser {
			return Row{}, invalidEvent("rotated browser credential")
		}
	case KindRevoked:
		if f.Revision < 2 {
			return Row{}, invalidEvent("revoked revision")
		}
		revision = f.Revision
	default:
		return Row{}, invalidEvent("event kind")
	}
	if kind == KindRotated && !wireRefPattern.MatchString(f.ParentID) {
		return Row{}, invalidEvent("rotation parent")
	}
	actorRef, err := actorRef(f, kind)
	if err != nil {
		return Row{}, err
	}
	change := wireChange{Status: "revoked"}
	if kind != KindRevoked {
		scopes := slices.Clone(c.Scopes)
		slices.Sort(scopes)
		scopes = slices.Compact(scopes)
		if len(scopes) == 0 || len(scopes) > 64 {
			return Row{}, invalidEvent("scopes")
		}
		for _, scope := range scopes {
			if len(scope) > 64 || !scopePattern.MatchString(scope) {
				return Row{}, invalidEvent("scope")
			}
		}
		change = wireChange{Status: "active", AcrClass: string(class), Scopes: scopes}
		if c.Provenance.ApprovingUserID != "" {
			if change.OwnerSubject, err = WireRef(c.Provenance.ApprovingUserID); err != nil {
				return Row{}, err
			}
		}
		if c.ExpiresAt != nil {
			change.ExpiresAt = wireTime(*c.ExpiresAt)
		}
		if kind == KindRotated {
			change.RotationParentID = f.ParentID
		}
	}
	event := wireEvent{
		SchemaVersion: eventSchemaVersion, AggregateType: eventAggregateType, AggregateID: c.CredentialID,
		EventType:      eventTypePrefix + string(kind),
		IdempotencyKey: eventAggregateType + ":" + c.CredentialID + ":" + strconv.Itoa(revision),
		CreatedAt:      wireTime(f.At),
		Payload: wirePayload{
			Revision: revision, OrganizationID: org, ActorType: f.Actor.Type, ActorRef: actorRef, Change: change,
		},
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return Row{}, invalidEvent("encode")
	}
	return Row{
		OrgID: c.OrgID, AggregateID: c.CredentialID, Revision: revision, EventType: event.EventType,
		Payload: payload, CreatedAt: f.At.UTC(),
	}, nil
}

func actorRef(f Facts, kind EventKind) (string, error) {
	switch f.Actor.Type {
	case ActorSystem:
		if f.Actor.Ref != "" {
			return "", invalidEvent("system actor reference")
		}
		return "", nil
	case ActorCredential:
		switch kind {
		case KindRevoked:
			return f.Credential.CredentialID, nil
		case KindRotated:
			return f.ParentID, nil
		default:
			return "", invalidEvent("credential actor on " + string(kind))
		}
	case ActorUser, ActorOperator:
		return WireRef(f.Actor.Ref)
	default:
		return "", invalidEvent("actor type")
	}
}

// EnsureIssued writes revision 1 for a credential the stream has not seen: an
// issued event by the system, at the credential's own creation time, from the
// credential as it stands. It writes nothing when the aggregate has a row.
func EnsureIssued(ctx context.Context, sink Sink, c contractsv1.ClientCredential) error {
	max, err := sink.MaxRevision(ctx, c.CredentialID)
	if err != nil {
		return err
	}
	if max > 0 {
		return nil
	}
	return insertFacts(ctx, sink, Facts{Kind: KindIssued, Credential: c, At: c.CreatedAt, Actor: Actor{Type: ActorSystem}})
}

// Append writes one event in the caller's transaction. A revoked event first
// writes the credential's revision 1 when the stream has none, then takes the
// next revision; the kinds that open an aggregate are revision 1. Every error
// is returned: the caller's transaction must not commit without the row.
func Append(ctx context.Context, sink Sink, f Facts) error {
	if f.Kind == KindRevoked {
		if err := EnsureIssued(ctx, sink, f.Credential); err != nil {
			return err
		}
		max, err := sink.MaxRevision(ctx, f.Credential.CredentialID)
		if err != nil {
			return err
		}
		f.Revision = max + 1
	}
	return insertFacts(ctx, sink, f)
}

func insertFacts(ctx context.Context, sink Sink, f Facts) error {
	row, err := BuildRow(f)
	if err != nil {
		return err
	}
	return sink.Insert(ctx, row)
}

// CreateKind is the event kind of a credential issue: a redeemed device
// authorization is device_redeemed (the mapper writes issued for the browser
// class), every other path is issued.
func CreateKind(in CreateInput) EventKind {
	if in.IssuanceProvenance == IssuanceProvenanceDeviceAuthorization {
		return KindDeviceRedeemed
	}
	return KindIssued
}

// CreateActor is the actor of a credential issue: the system when acr mints
// it itself (a workload exchange, or a refresh grant, whose actor is not the
// approving user), otherwise the user named by the request.
func CreateActor(in CreateInput) Actor {
	switch {
	case in.IssuanceProvenance == IssuanceProvenanceWorkloadExchange:
		return Actor{Type: ActorSystem}
	case in.IssuanceProvenance == IssuanceProvenanceDeviceAuthorization && in.ApprovingUserID != "" && in.ActorID != in.ApprovingUserID:
		return Actor{Type: ActorSystem}
	}
	return Actor{Type: ActorUser, Ref: in.ActorID}
}

// RotationActor is the actor of a rotation: the credential itself for a self
// rotation, otherwise the user named by the request.
func RotationActor(in RotationInput) Actor {
	if in.SelfCredential {
		return Actor{Type: ActorCredential}
	}
	return Actor{Type: ActorUser, Ref: in.ActorID}
}

// RevocationActor is the actor of a revocation, from the same input the audit
// row reads: self, the operator, the system, or a user.
func RevocationActor(in RevocationInput) Actor {
	switch {
	case in.SelfCredential:
		return Actor{Type: ActorCredential}
	case in.ActorType == string(ActorSystem):
		return Actor{Type: ActorSystem}
	case in.ActorType == string(ActorOperator):
		return Actor{Type: ActorOperator, Ref: in.ActorID}
	default:
		return Actor{Type: ActorUser, Ref: in.ActorID}
	}
}

// RollbackActor is the actor of a rotation rollback: the successor itself when
// it asked (the self-service path), otherwise the user named by the request.
func RollbackActor(in RotationRollbackInput) Actor {
	if in.ActorID == in.SuccessorCredentialID {
		return Actor{Type: ActorCredential}
	}
	return Actor{Type: ActorUser, Ref: in.ActorID}
}
