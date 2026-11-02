package v1

import "fmt"

const (
	CredentialLifecycleListSchema = "credential_lifecycle_list.v1"

	CredentialLifecycleStatusActive  = "active"
	CredentialLifecycleStatusRevoked = "revoked"
	CredentialLifecycleStatusExpired = "expired"

	CredentialLifecycleListDefaultLimit = 100
	CredentialLifecycleListMaxLimit     = 500
)

// CredentialLifecycleItem is one credential as the reconciliation read names
// it: the public id, the highest outbox revision and the state of the latest
// event. Nothing else about the credential is on the wire.
type CredentialLifecycleItem struct {
	CredentialID string `json:"credential_id"`
	Revision     int    `json:"revision"`
	Status       string `json:"status"`
}

// CredentialLifecycleList is one page of the read-only listing, ordered by
// credential id. NextAfter is the id to pass as `after` for the next page, or
// null on the last page.
type CredentialLifecycleList struct {
	SchemaVersion string                    `json:"schema_version"`
	Items         []CredentialLifecycleItem `json:"items"`
	NextAfter     *string                   `json:"next_after"`
}

func ValidCredentialLifecycleStatus(status string) bool {
	switch status {
	case CredentialLifecycleStatusActive, CredentialLifecycleStatusRevoked, CredentialLifecycleStatusExpired:
		return true
	default:
		return false
	}
}

// ValidCredentialLifecycleCursor reports whether value can be a credential id
// cursor: the wire id alphabet of the lifecycle outbox, 1 to 128 characters.
func ValidCredentialLifecycleCursor(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9', character == '_', character == '-':
		default:
			return false
		}
	}
	return true
}

func (l CredentialLifecycleList) Validate() error {
	if l.SchemaVersion != CredentialLifecycleListSchema || l.Items == nil || len(l.Items) > CredentialLifecycleListMaxLimit {
		return fmt.Errorf("credential lifecycle list violates v1 bounds")
	}
	previous := ""
	for index, item := range l.Items {
		if !ValidCredentialLifecycleCursor(item.CredentialID) || item.Revision < 1 || !ValidCredentialLifecycleStatus(item.Status) || (index > 0 && item.CredentialID <= previous) {
			return fmt.Errorf("credential lifecycle list item violates v1 bounds")
		}
		previous = item.CredentialID
	}
	if l.NextAfter != nil && (len(l.Items) == 0 || !ValidCredentialLifecycleCursor(*l.NextAfter) || *l.NextAfter != previous) {
		return fmt.Errorf("credential lifecycle list cursor violates v1 bounds")
	}
	return nil
}
