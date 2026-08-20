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
