package v1

import "errors"

// MaxRepositoryScopes bounds the repository scopes of one device or consent
// approval, of one device authorization's repository hints and of one issued
// credential. It equals the ops decision point's repository allowlist bound
// (MaxRepositoryAllowlist, 256): the web sends the allowlist the decision
// returned, so a lower bound here refuses a list the decision point allows.
// The device schemas' maxItems carry the same number (a test pins them).
const MaxRepositoryScopes = 256

// TooManyRepositoriesReason is the invalid_request reason of an approval that
// names more than MaxRepositoryScopes repositories; details carry
// max_repository_scopes.
const TooManyRepositoriesReason = "too_many_repositories"

// ErrTooManyRepositoryScopes is the validation error of a repository scope
// list longer than MaxRepositoryScopes.
var ErrTooManyRepositoryScopes = errors.New("repository scopes exceed the v1 bound")
