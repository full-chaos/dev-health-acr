package entitlements

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/google/uuid"
)

// principalAccessSchemaVersion is the Dev Health principal access read's
// document (ops CHAOS-12084, .github/docs-legacy/architecture/
// acr-entitlement-route.md "Principal access read").
const principalAccessSchemaVersion = "acr_principal_access.v1"

// invalidSessionCode is the stable code of the read's 400 for a sid that is
// not a session id in its wire form.
const invalidSessionCode = "acr_access_invalid_sid"

// The stable codes of the read's 503 for an inventory above its limit: the
// project inventory (ops CHAOS-12224) and the repository inventory (ops
// CHAOS-12267). Ops refuses such an inventory rather than truncate it, so the
// read never carries a partial scope.
const (
	projectScopeOverLimitCode    = "acr_access_project_scope_over_limit"
	repositoryScopeOverLimitCode = "acr_access_repository_scope_over_limit"
)

// maxProjectScopeProjects is the most projects one project_scope may carry. It
// matches ops projectinventory.Limit: more is refused, never truncated.
const maxProjectScopeProjects = 100

// projectScopeSeparator is refused in a project identity. The contract refuses
// it in every authorization value (it is the separator a delimited backend
// uses) and EncodeSegment does not escape it, so the membership projection can
// never state such a project.
const projectScopeSeparator = '|'

var (
	// ErrPrincipalUnknown means Dev Health answered its coded 404
	// (acr_organization_unknown): the organization does not exist, so the
	// principal has no access. It is never a grant. A user Dev Health does
	// not know is a 200 with member false.
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
	// Projects is the organization's live project inventory (ops CHAOS-12224,
	// project_scope), as the canonical project.v2 identities derived once here:
	// exact, sorted and unique. Nil when the answer names none: no project_scope
	// (the ops flag is off, or the organization has repositories) or kind
	// "none". It is an inventory, not a grant: only the access gate turns it
	// into one, for a "*" consent grant with no repository inventory.
	Projects []string
}

// SessionAccess is the bound session's half of the answer.
type SessionAccess struct {
	Live          bool
	Impersonating bool
}

// PrincipalAccess reads, without any cache, the access of userID in orgID,
// and the liveness of sessionID when it is not empty. The 404 coded
// acr_organization_unknown is ErrPrincipalUnknown; a 400 for the session id
// is ErrSessionInvalid; any other 404 (a route Dev Health does not serve), a
// transport failure, a timeout, every other status and a body that is not
// exactly the acr_principal_access.v1 document for this org, user and session
// are a rejection (unavailable): an outage never reads as an answer. A 503
// with an inventory over-limit code is a rejection too, with its own reason
// (AccessScopeOverLimit): it grants nothing, and it is not an outage.
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
	if !c.authorize(ctx, request) {
		return PrincipalAccess{}, rejected(reasonTransport)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return PrincipalAccess{}, rejected(reasonTransport)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// Only the coded 404 is an answer: the read sends canonical UUIDs,
		// so an uncoded 404 is a route this Dev Health does not serve (an
		// older go-api, a wrong origin), which must stay 503, never a deny.
		if bodyCode(response.Body, c.maxBody) == unknownOrganizationCode {
			return PrincipalAccess{}, ErrPrincipalUnknown
		}
		return PrincipalAccess{}, rejected(reasonStatus)
	case http.StatusBadRequest:
		if sessionID != "" && bodyCode(response.Body, c.maxBody) == invalidSessionCode {
			return PrincipalAccess{}, ErrSessionInvalid
		}
		return PrincipalAccess{}, rejected(reasonStatus)
	case http.StatusServiceUnavailable:
		switch bodyCode(response.Body, c.maxBody) {
		case projectScopeOverLimitCode:
			return PrincipalAccess{}, rejected(reasonProjectScopeOverLimit)
		case repositoryScopeOverLimitCode:
			return PrincipalAccess{}, rejected(reasonRepositoryScopeOverLimit)
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
	// ProjectScope is optional: ops sends it only with its flag on and only for
	// an organization with no repository inventory.
	ProjectScope *struct {
		Kind     *string                `json:"kind"`
		Projects *[]projectScopeProject `json:"projects"`
	} `json:"project_scope"`
}

// projectScopeProject is one pair of project_scope: the projects row's own
// provider and id, as stored (ops projectinventory).
type projectScopeProject struct {
	Provider *string `json:"provider"`
	ID       *string `json:"id"`
}

// decodePrincipalAccess accepts exactly one acr_principal_access.v1 object
// for orgID and userID: every field present with its type, no unknown field,
// a session object exactly when one was asked for, a repository scope of kind
// "repos" with a non-empty repos array or kind "none" with none, and, when
// present, a project scope of kind "projects" with 1 to
// maxProjectScopeProjects valid, distinct pairs or kind "none" with none.
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
	if document.ProjectScope != nil {
		if document.ProjectScope.Kind == nil {
			return PrincipalAccess{}, invalid
		}
		switch *document.ProjectScope.Kind {
		case "none":
			if projectsKeyPresent(payload) {
				return PrincipalAccess{}, invalid
			}
		case "projects":
			if document.ProjectScope.Projects == nil {
				return PrincipalAccess{}, invalid
			}
			projects, err := canonicalProjectScope(*document.ProjectScope.Projects)
			if err != nil {
				return PrincipalAccess{}, invalid
			}
			access.Projects = projects
		default:
			return PrincipalAccess{}, invalid
		}
	}
	return access, nil
}

// projectsKeyPresent reports whether project_scope carries a projects key at
// all. The strict decode reads an absent key and a null one alike, and kind
// "none" allows neither: the wire contract says it has no projects key. It
// reads the document the strict decode has already accepted, so a failure here
// cannot happen and counts as present (refused).
func projectsKeyPresent(payload []byte) bool {
	var document struct {
		ProjectScope struct {
			Projects json.RawMessage `json:"projects"`
		} `json:"project_scope"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		return true
	}
	return document.ProjectScope.Projects != nil
}

// canonicalProjectScope derives the canonical identity of every pair, once,
// and returns them sorted. It is all or nothing (CHAOS-12107 v3 V3): an empty
// list, too many pairs, an empty or invalid pair, an oversized identity and a
// duplicate each refuse the whole list. A pair is never dropped, so a non-empty
// answer can never shrink to the empty grant, which reads as unrestricted.
func canonicalProjectScope(pairs []projectScopeProject) ([]string, error) {
	if len(pairs) == 0 || len(pairs) > maxProjectScopeProjects {
		return nil, errors.New("project scope size out of bounds")
	}
	projects := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		if pair.Provider == nil || pair.ID == nil || strings.TrimSpace(*pair.Provider) == "" || strings.TrimSpace(*pair.ID) == "" {
			return nil, errors.New("project scope pair is empty")
		}
		project, omitted, err := identity.Derive(identity.KindProject, []string{*pair.Provider, *pair.ID}, nil)
		if err != nil {
			return nil, err
		}
		if omitted || strings.ContainsRune(project, projectScopeSeparator) {
			return nil, errors.New("project scope identity is not an authorization value")
		}
		projects = append(projects, project)
	}
	slices.Sort(projects)
	if len(slices.Compact(slices.Clone(projects))) != len(projects) {
		return nil, errors.New("project scope has a duplicate")
	}
	return projects, nil
}
