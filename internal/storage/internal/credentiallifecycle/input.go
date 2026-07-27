package credentiallifecycle

import "time"

const MaximumOverlap = 15 * time.Minute

type IssuanceProvenance string

const IssuanceProvenanceDeviceAuthorization IssuanceProvenance = "device_authorization"

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
