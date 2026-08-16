package sidecar

import (
	"context"
	"errors"
)

type LocalIndexErrorCode string

const (
	LocalIndexErrorDisabled         LocalIndexErrorCode = "local_index_disabled"
	LocalIndexErrorExecutableAbsent LocalIndexErrorCode = "local_index_executable_absent"
	// LocalIndexErrorSpawnUnavailable is CHAOS-3861's split off
	// LocalIndexErrorExecutableAbsent: the executable IS present and
	// usable, but the OS could not fork a new process for it right now
	// (EAGAIN/EMFILE/ENOMEM -- host resource pressure). Distinct from
	// "absent" because it is transient and, in principle, retryable,
	// where "absent" is a persistent configuration problem. See
	// classifyCodeGraphSpawnError.
	LocalIndexErrorSpawnUnavailable LocalIndexErrorCode = "local_index_spawn_unavailable"
	// LocalIndexErrorSpawnFailed is sol review F1 (CHAOS-3861): the
	// classifyCodeGraphSpawnError bucket for a cmd.Start()/StdoutPipe()
	// failure that is neither "genuinely absent" (fs.ErrNotExist/
	// fs.ErrPermission/ENOEXEC) nor a recognized transient host-resource
	// errno. Before this code existed, that bucket had no sentinel of its
	// own, so localIndexErrorCodeFor's default case mapped it to
	// LocalIndexErrorMalformed -- an operator-facing lie: "malformed"
	// means the CodeGraph process ran and produced invalid output, not
	// that it never started at all.
	LocalIndexErrorSpawnFailed           LocalIndexErrorCode = "local_index_spawn_failed"
	LocalIndexErrorMissing               LocalIndexErrorCode = "local_index_missing"
	LocalIndexErrorIncompatibleVersion   LocalIndexErrorCode = "local_index_incompatible_version"
	LocalIndexErrorStale                 LocalIndexErrorCode = "local_index_stale"
	LocalIndexErrorWorktreeMismatch      LocalIndexErrorCode = "local_worktree_mismatch"
	LocalIndexErrorWorkspaceDirty        LocalIndexErrorCode = "local_workspace_dirty"
	LocalIndexErrorChangedFilesTruncated LocalIndexErrorCode = "changed_files_truncated"
	LocalIndexErrorTimeout               LocalIndexErrorCode = "local_index_timeout"
	LocalIndexErrorCancelled             LocalIndexErrorCode = "local_index_cancelled"
	LocalIndexErrorMalformed             LocalIndexErrorCode = "local_index_malformed"
	LocalIndexErrorOversized             LocalIndexErrorCode = "local_index_oversized"
	LocalIndexErrorUnsupportedCapability LocalIndexErrorCode = "local_index_unsupported_capability"
	LocalIndexErrorQueryBudgetExhausted  LocalIndexErrorCode = "local_query_budget_exhausted"
	LocalIndexErrorIndexedCommitUnknown  LocalIndexErrorCode = "indexed_commit_unknown"
)

var (
	errCodeGraphExecutableAbsent = errors.New("codegraph executable absent")
	// errCodeGraphSpawnUnavailable is CHAOS-3861: the codegraph
	// executable exists and is usable, but the host could not fork a
	// new process for it right now. See classifyCodeGraphSpawnError.
	errCodeGraphSpawnUnavailable = errors.New("codegraph could not be started: host process resources are exhausted")
	// errCodeGraphSpawnFailed is sol review F1 (CHAOS-3861): the
	// classifyCodeGraphSpawnError catch-all for a spawn failure that is
	// neither absent nor a recognized transient errno. Named for what is
	// actually known -- spawn failed, cause unrecognized -- rather than
	// implying anything about CodeGraph's OUTPUT (which never ran).
	errCodeGraphSpawnFailed    = errors.New("codegraph could not be started for an unrecognized reason")
	errLocalIndexConfigInvalid = errors.New("local index configuration invalid")
	errLocalIndexDisabled      = errors.New("local index disabled")
	errCodeGraphMissing        = errors.New("codegraph index missing")
	errCodeGraphMismatch       = errors.New("codegraph worktree mismatch")
	errCodeGraphStale          = errors.New("codegraph index stale")
	errCodeGraphUnsupported    = errors.New("codegraph unsupported capability")
	errCodeGraphIncompatible   = errors.New("codegraph incompatible version")
)

// LocalIndexError is a safe, structured local-index failure classification.
type LocalIndexError struct {
	code      LocalIndexErrorCode
	status    LocalIndexStatus
	freshness LocalIndexFreshness
	warnings  []string
	cause     error
}

func newLocalIndexError(code LocalIndexErrorCode, status LocalIndexStatus, freshness LocalIndexFreshness, warnings []string, cause error) *LocalIndexError {
	return &LocalIndexError{code: code, status: status, freshness: freshness, warnings: append([]string(nil), warnings...), cause: cause}
}

func (e *LocalIndexError) Code() LocalIndexErrorCode      { return e.code }
func (e *LocalIndexError) Status() LocalIndexStatus       { return e.status }
func (e *LocalIndexError) Freshness() LocalIndexFreshness { return e.freshness }
func (e *LocalIndexError) Warnings() []string             { return append([]string(nil), e.warnings...) }
func (e *LocalIndexError) Error() string                  { return "acr: local index " + string(e.code) }
func (e *LocalIndexError) Unwrap() error                  { return e.cause }

func localIndexErrorCodeFor(err error) LocalIndexErrorCode {
	switch {
	case errors.Is(err, context.Canceled):
		return LocalIndexErrorCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return LocalIndexErrorTimeout
	case errors.Is(err, ErrCodeGraphOutputTooLarge):
		return LocalIndexErrorOversized
	case errors.Is(err, errLocalIndexDisabled):
		return LocalIndexErrorDisabled
	case errors.Is(err, errCodeGraphExecutableAbsent):
		return LocalIndexErrorExecutableAbsent
	case errors.Is(err, errCodeGraphSpawnUnavailable):
		return LocalIndexErrorSpawnUnavailable
	case errors.Is(err, errCodeGraphSpawnFailed):
		return LocalIndexErrorSpawnFailed
	case errors.Is(err, errCodeGraphMissing):
		return LocalIndexErrorMissing
	case errors.Is(err, errCodeGraphMismatch):
		return LocalIndexErrorWorktreeMismatch
	case errors.Is(err, errCodeGraphUnsupported):
		return LocalIndexErrorUnsupportedCapability
	case errors.Is(err, errCodeGraphIncompatible):
		return LocalIndexErrorIncompatibleVersion
	case errors.Is(err, errCodeGraphDecode):
		return LocalIndexErrorMalformed
	default:
		return LocalIndexErrorMalformed
	}
}

func localIndexFailure(err error) error {
	code := localIndexErrorCodeFor(err)
	return newLocalIndexError(code, LocalIndexStatusUnavailable, LocalIndexFreshnessUnknown, []string{string(code)}, err)
}
