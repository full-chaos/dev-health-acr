package credentiallifecycle

import "time"

const MaximumOverlap = 15 * time.Minute

type IssuanceProvenance string

const (
	IssuanceProvenanceDeviceAuthorization IssuanceProvenance = "device_authorization"
	// IssuanceProvenanceWorkloadExchange marks a credential row minted by
	// RFC 8693 workload token exchange (CHAOS-4013): a k8s projected
	// ServiceAccount JWT, validated via TokenReview, resolved to a
	// declarative WorkloadBindingID.
	IssuanceProvenanceWorkloadExchange IssuanceProvenance = "workload_exchange"
)

// ClientClass is a credential's issuance class (CHAOS-12083). normalizeCreate
// derives it from the issuance path; the vocabulary is closed and mirrored by
// the acr.client_credentials client_class CHECK (migration 0058).
type ClientClass string

const (
	// ClientClassBrowser is reserved for the first-party web browser client
	// (CHAOS-12070); no issuance path derives it yet.
	ClientClassBrowser ClientClass = "browser"
	// ClientClassOAuthUser is a user-approved credential issued by an OAuth
	// exchange (authorization code or RFC 8628 device grant) to a client.
	ClientClassOAuthUser ClientClass = "oauth_user"
	// ClientClassNativeDevice is a user-approved credential issued by the
	// legacy native device flow (no OAuth client, no resource).
	ClientClassNativeDevice ClientClass = "native_device"
	// ClientClassOperator is a credential an operator issued directly (the
	// acr-api credentials command). It has no approving user.
	ClientClassOperator ClientClass = "operator"
	// ClientClassWorkload is a credential minted by RFC 8693 workload token
	// exchange. It has no approving user.
	ClientClassWorkload ClientClass = "workload"
)

// Valid reports whether c is a member of the closed vocabulary.
func (c ClientClass) Valid() bool {
	switch c {
	case ClientClassBrowser, ClientClassOAuthUser, ClientClassNativeDevice, ClientClassOperator, ClientClassWorkload:
		return true
	default:
		return false
	}
}

// UserApproved reports whether credentials of class c carry an approving
// user.
func (c ClientClass) UserApproved() bool {
	return c == ClientClassBrowser || c == ClientClassOAuthUser || c == ClientClassNativeDevice
}

type CreateInput struct {
	CredentialID       string
	OrgID              string
	Name               string
	TokenPrefix        string
	TokenHash          string
	RepositoryScopes   []string
	Scopes             []string
	ActorID            string
	ExpiresAt          *time.Time
	IssuanceProvenance IssuanceProvenance
	// WorkloadBindingID is set only alongside
	// IssuanceProvenanceWorkloadExchange -- see that constant's doc
	// comment. normalizeCreate enforces the pairing.
	WorkloadBindingID string
	// Resource is the RFC 8707 protected resource this credential is bound
	// to. Empty for every issuance path except an OAuth authorization-code
	// exchange that named one; never set alongside
	// IssuanceProvenanceWorkloadExchange. normalizeCreate enforces both.
	Resource string
	// ClientID is the OAuth client the credential is issued to
	// (CHAOS-12083). Only an OAuth exchange sets it, and only with a
	// Resource.
	ClientID string
	// ApprovingUserID is the web-session subject that approved the device
	// authorization behind a redeemed credential (CHAOS-12083). The device
	// authorization store sets it from the locked authorization row, never
	// from a caller, and it must be empty on every other issuance path.
	ApprovingUserID string
	// ClientClass is derived by normalizeCreate from the issuance path
	// (deriveClientClass). A caller that sets it must name the class the
	// path derives; it can never choose one.
	ClientClass ClientClass
}

type RotationReplacement struct {
	CredentialID     string
	Name             string
	TokenPrefix      string
	TokenHash        string
	RepositoryScopes []string
	Scopes           []string
	ExpiresAt        *time.Time
	Overlap          time.Duration
	Immediate        bool
}

type RotationInput struct {
	OrgID              string
	SourceCredentialID string
	ActorID            string
	Replacement        RotationReplacement
}

type RevocationInput struct {
	OrgID        string
	CredentialID string
	ActorID      string
	// ActorType is the audit actor type; empty means "user".
	ActorType string
	// Reason is optional free text recorded in the audit metadata.
	Reason string
}

// RotationRollbackInput identifies the exact rotation that may be undone. The
// storage adapter validates this input and atomically revokes only the recorded
// successor; callers must not compose separate reads and revocation calls.
type RotationRollbackInput struct {
	OrgID                 string
	SourceCredentialID    string
	SuccessorCredentialID string
	ActorID               string
	RollbackUntil         time.Time
}
