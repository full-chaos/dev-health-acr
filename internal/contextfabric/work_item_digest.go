package contextfabric

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// WorkItemAuthorizationDigestVersion names the fixed encoding domain for
// work-item census authorization. It is not caller-provided authorization
// input; changing raw grant or request values remains the only dynamic change
// that affects this projection.
const WorkItemAuthorizationDigestVersion = "work-item-authorization.v1"

// workItemAuthorizationDigestDocument is deliberately limited to the two
// census inputs. Organization identity is enforced by the request and result
// store boundaries; project and team scope are live anchor checks, not census
// digest inputs. Selector values remain raw here. The reader adapter owns
// selector interpretation; this digest only makes the input order-independent.
type workItemAuthorizationDigestDocument struct {
	Version                   string   `json:"version"`
	PrincipalRepositoryGrants []string `json:"principal_repository_grants"`
	RequestedRepositoryScope  []string `json:"requested_repository_scope"`
}

// WorkItemAuthorizationDigest returns the authorization digest used by the
// work-item census reuse and by-id paths. The canonical encoding is the fixed
// version tag plus JSON arrays named principal_repository_grants and
// requested_repository_scope. Each array is a copy of its raw input sorted by
// string value. No selector is interpreted, dropped, folded, or substituted;
// the 5751 reader adapter remains the only selector translation seam. A nil or
// empty requested list means no requested repository restriction in the
// existing request contract, so both encode as an empty array. An empty
// principal grant list retains its raw empty-array input even though the
// authorization adapter treats that existing convention as organization-wide.
// Consequently, changing any raw selector value changes the digest even when
// the reader would grant the same repositories.
func WorkItemAuthorizationDigest(principal storage.Principal, requested []string) (string, error) {
	document := workItemAuthorizationDigestDocument{
		Version:                   WorkItemAuthorizationDigestVersion,
		PrincipalRepositoryGrants: canonicalWorkItemDigestValues(principal.RepositoryScopes),
		RequestedRepositoryScope:  canonicalWorkItemDigestValues(requested),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode work-item authorization digest input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalWorkItemDigestValues copies and sorts raw values. Returning a
// non-nil empty slice is intentional: nil and empty requested scope have the
// same meaning at the request boundary and therefore one JSON representation.
func canonicalWorkItemDigestValues(values []string) []string {
	canonical := append([]string{}, values...)
	sort.Strings(canonical)
	return canonical
}
