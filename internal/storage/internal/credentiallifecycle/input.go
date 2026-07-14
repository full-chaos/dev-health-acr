package credentiallifecycle

import "time"

const MaximumOverlap = 15 * time.Minute

type CreateInput struct {
	CredentialID     string
	OrgID            string
	Name             string
	TokenPrefix      string
	TokenHash        string
	RepositoryScopes []string
	Scopes           []string
	ActorID          string
	ExpiresAt        *time.Time
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
