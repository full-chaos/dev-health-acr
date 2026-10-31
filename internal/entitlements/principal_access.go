package entitlements

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// principalAccessSchemaVersion is the Dev Health principal access read's
// document (ops CHAOS-12084, .github/docs-legacy/architecture/
// acr-entitlement-route.md "Principal access read").
const principalAccessSchemaVersion = "acr_principal_access.v1"

// invalidSessionCode is the stable code of the read's 400 for a sid that is
// not a session id in its wire form.
const invalidSessionCode = "acr_access_invalid_sid"

var (
	// ErrPrincipalUnknown means Dev Health answered 404 for the organization
	// or the user: the principal has no access. It is never a grant.
	ErrPrincipalUnknown = errors.New("Dev Health does not know the principal")
	// ErrSessionInvalid means Dev Health refused the bound session id as not
	// a session id: the session is not live. It is never a grant.
	ErrSessionInvalid = errors.New("Dev Health refused the session id")
)

// PrincipalAccess is one live answer of the principal access read (CHAOS-12092,
// design §3.1): what the user may do in the organization right now. It is
// never cached: every protected call by a user-class credential reads it.
type PrincipalAccess struct {
	Member        bool
	Impersonating bool
	// Session is nil when the read named no session.
	Session             *SessionAccess
	AskDev              bool
	AgentContextRuntime bool
	// Repositories is the organization's live repository inventory, sorted.
	// Empty means the inventory is empty (kind "none").
	Repositories []string
}

// SessionAccess is the bound session's half of the answer.
type SessionAccess struct {
	Live          bool
	Impersonating bool
}

// PrincipalAccess reads, without any cache, the access of userID in orgID,
// and the liveness of sessionID when it is not empty. A 404 is
// ErrPrincipalUnknown; a 400 for the session id is ErrSessionInvalid; a
// transport failure, a timeout, every other status and a body that is not
// exactly the acr_principal_access.v1 document for this org, user and session
// are a rejection (unavailable): an outage never reads as an answer.
func (c *Client) PrincipalAccess(ctx context.Context, orgID, userID, sessionID string) (PrincipalAccess, error) {
	if !canonicalUUID(orgID) || !canonicalUUID(userID) {
		return PrincipalAccess{}, ErrPrincipalUnknown
	}
	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, principalAccessURL(c.baseURL, orgID, userID, sessionID), nil)
	if err != nil {
		return PrincipalAccess{}, rejected(reasonTransport)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return PrincipalAccess{}, rejected(reasonTransport)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return PrincipalAccess{}, ErrPrincipalUnknown
	case http.StatusBadRequest:
		if sessionID != "" && bodyCode(response.Body, c.maxBody) == invalidSessionCode {
			return PrincipalAccess{}, ErrSessionInvalid
		}
		return PrincipalAccess{}, rejected(reasonStatus)
	default:
		return PrincipalAccess{}, rejected(reasonStatus)
	}
	payload, err := readBounded(response.Body, c.maxBody)
	if err != nil {
		return PrincipalAccess{}, rejected(reasonDecode)
	}
	access, err := decodePrincipalAccess(payload, orgID, userID, sessionID != "")
	if err != nil {
		if errors.Is(err, errUnknownField) {
			return PrincipalAccess{}, rejected(reasonUnknownField)
		}
		return PrincipalAccess{}, rejected(reasonSchema)
	}
	return access, nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func principalAccessURL(baseURL *url.URL, orgID, userID, sessionID string) string {
	copy := *baseURL
	copy.Path = "/api/v1/internal/acr/orgs/" + url.PathEscape(orgID) + "/principals/" + url.PathEscape(userID) + "/access"
	copy.RawPath = ""
	if sessionID != "" {
		copy.RawQuery = url.Values{"sid": {sessionID}}.Encode()
	}
	return copy.String()
}

func bodyCode(body io.Reader, maxBytes int64) string {
	payload, err := readBounded(body, maxBytes)
	if err != nil {
		return ""
	}
	var parsed struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(payload, &parsed) != nil {
		return ""
	}
	return parsed.Code
}

type principalAccessDocument struct {
	SchemaVersion *string `json:"schema_version"`
	OrgID         *string `json:"org_id"`
	UserID        *string `json:"user_id"`
	Member        *bool   `json:"member"`
	Impersonating *bool   `json:"impersonating"`
	Session       *struct {
		Live          *bool `json:"live"`
		Impersonating *bool `json:"impersonating"`
	} `json:"session"`
	Entitlements *struct {
		AskDev              *bool `json:"ask_dev"`
		AgentContextRuntime *bool `json:"agent_context_runtime"`
	} `json:"entitlements"`
	RepositoryScope *struct {
		Kind  *string   `json:"kind"`
		Repos *[]string `json:"repos"`
	} `json:"repository_scope"`
}

// decodePrincipalAccess accepts exactly one acr_principal_access.v1 object
// for orgID and userID: every field present with its type, no unknown field,
// a session object exactly when one was asked for, and a repository scope of
// kind "repos" with a non-empty repos array or kind "none" with none.
func decodePrincipalAccess(payload []byte, orgID, userID string, sessionAsked bool) (PrincipalAccess, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document principalAccessDocument
	if err := decoder.Decode(&document); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return PrincipalAccess{}, errUnknownField
		}
		return PrincipalAccess{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return PrincipalAccess{}, errors.New("trailing JSON")
	}
	invalid := errors.New("invalid principal access document")
	switch {
	case document.SchemaVersion == nil || *document.SchemaVersion != principalAccessSchemaVersion,
		document.OrgID == nil || *document.OrgID != orgID,
		document.UserID == nil || *document.UserID != userID,
		document.Member == nil, document.Impersonating == nil,
		document.Entitlements == nil || document.Entitlements.AskDev == nil || document.Entitlements.AgentContextRuntime == nil,
		document.RepositoryScope == nil || document.RepositoryScope.Kind == nil,
		(document.Session != nil) != sessionAsked:
		return PrincipalAccess{}, invalid
	}
	access := PrincipalAccess{
		Member: *document.Member, Impersonating: *document.Impersonating,
		AskDev: *document.Entitlements.AskDev, AgentContextRuntime: *document.Entitlements.AgentContextRuntime,
	}
	if document.Session != nil {
		if document.Session.Live == nil || document.Session.Impersonating == nil {
			return PrincipalAccess{}, invalid
		}
		access.Session = &SessionAccess{Live: *document.Session.Live, Impersonating: *document.Session.Impersonating}
	}
	switch *document.RepositoryScope.Kind {
	case "none":
		if document.RepositoryScope.Repos != nil {
			return PrincipalAccess{}, invalid
		}
	case "repos":
		if document.RepositoryScope.Repos == nil || len(*document.RepositoryScope.Repos) == 0 {
			return PrincipalAccess{}, invalid
		}
		for _, repository := range *document.RepositoryScope.Repos {
			if strings.TrimSpace(repository) == "" {
				return PrincipalAccess{}, invalid
			}
		}
		access.Repositories = append([]string(nil), *document.RepositoryScope.Repos...)
	default:
		return PrincipalAccess{}, invalid
	}
	return access, nil
}
