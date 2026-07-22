package sidecar

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maxCodeGraphStdoutBytes     = 1 << 20
	maxCodeGraphStderrBytes     = 8 << 10
	codeGraphTraversalDepth     = 2
	codeGraphJSONQueryVersion   = "codegraph-json-contract-v1"
	codeGraphCommandResultLimit = 12
)

var (
	ErrCodeGraphUnavailable       = errors.New("codegraph local index is unavailable")
	ErrCodeGraphOutputTooLarge    = errors.New("codegraph local index output exceeded the maximum size")
	ErrCodeGraphArgumentsRejected = errors.New("codegraph arguments are not permitted")
)

// CodeGraphRunner executes only ADR-0005's fixed, read-only CodeGraph JSON commands.
type CodeGraphRunner struct {
	Config            LocalIndexConfig
	resolveExecutable func(string) (string, error)
}

type codeGraphRunCommand string

const (
	codeGraphRunStatus   codeGraphRunCommand = "status"
	codeGraphRunQuery    codeGraphRunCommand = "query"
	codeGraphRunCallers  codeGraphRunCommand = "callers"
	codeGraphRunCallees  codeGraphRunCommand = "callees"
	codeGraphRunImpact   codeGraphRunCommand = "impact"
	codeGraphRunAffected codeGraphRunCommand = "affected"
	codeGraphRunFiles    codeGraphRunCommand = "files"
)

type codeGraphQueryRequest struct {
	GitRoot string
	Search  string
	Limit   int
}

type codeGraphAffectedRequest struct {
	GitRoot string
	Files   []string
}

type codeGraphFilesRequest struct {
	GitRoot string
	Filter  string
	Pattern string
}

func (r CodeGraphRunner) Status(ctx context.Context, gitRoot string) ([]byte, error) {
	return r.run(ctx, gitRoot, codeGraphRunStatus, []string{"status", "--json"})
}

func (r CodeGraphRunner) Query(ctx context.Context, request codeGraphQueryRequest) ([]byte, error) {
	if !validCodeGraphText(request.Search, maxLocalTaskBytes) || !validCodeGraphLimit(request.Limit) {
		return nil, ErrCodeGraphArgumentsRejected
	}
	return r.run(ctx, request.GitRoot, codeGraphRunQuery, []string{"query", "--json", request.Search, "--limit", strconv.Itoa(request.Limit)})
}

func (r CodeGraphRunner) Callers(ctx context.Context, request codeGraphQueryRequest) ([]byte, error) {
	if !validCodeGraphText(request.Search, maxLocalEvidenceTitleBytes) || !validCodeGraphLimit(request.Limit) {
		return nil, ErrCodeGraphArgumentsRejected
	}
	return r.run(ctx, request.GitRoot, codeGraphRunCallers, []string{"callers", "--json", request.Search, "--limit", strconv.Itoa(request.Limit)})
}

func (r CodeGraphRunner) Callees(ctx context.Context, request codeGraphQueryRequest) ([]byte, error) {
	if !validCodeGraphText(request.Search, maxLocalEvidenceTitleBytes) || !validCodeGraphLimit(request.Limit) {
		return nil, ErrCodeGraphArgumentsRejected
	}
	return r.run(ctx, request.GitRoot, codeGraphRunCallees, []string{"callees", "--json", request.Search, "--limit", strconv.Itoa(request.Limit)})
}

func (r CodeGraphRunner) Impact(ctx context.Context, request codeGraphQueryRequest) ([]byte, error) {
	if !validCodeGraphText(request.Search, maxLocalEvidenceTitleBytes) {
		return nil, ErrCodeGraphArgumentsRejected
	}
	return r.run(ctx, request.GitRoot, codeGraphRunImpact, []string{"impact", "--json", request.Search, "--depth", strconv.Itoa(codeGraphTraversalDepth)})
}

func (r CodeGraphRunner) Affected(ctx context.Context, request codeGraphAffectedRequest) ([]byte, error) {
	if len(request.Files) == 0 || len(request.Files) > DefaultMaxChangedFiles {
		return nil, ErrCodeGraphArgumentsRejected
	}
	arguments := []string{"affected", "--json", "--stdin", "--depth", strconv.Itoa(codeGraphTraversalDepth)}
	input := make([]string, 0, len(request.Files))
	for _, file := range request.Files {
		if !validRepositoryRelativePath(file) {
			return nil, ErrCodeGraphArgumentsRejected
		}
		input = append(input, file)
	}
	return r.runInput(ctx, request.GitRoot, codeGraphRunAffected, arguments, []byte(strings.Join(input, "\n")+"\n"))
}

func (r CodeGraphRunner) Files(ctx context.Context, request codeGraphFilesRequest) ([]byte, error) {
	if request.Filter != "" && !validRepositoryRelativePath(request.Filter) {
		return nil, ErrCodeGraphArgumentsRejected
	}
	if request.Pattern != "" && !validCodeGraphText(request.Pattern, maxLocalEvidenceLocatorBytes) {
		return nil, ErrCodeGraphArgumentsRejected
	}
	arguments := []string{"files", "--json"}
	if request.Filter != "" {
		arguments = append(arguments, "--filter", request.Filter)
	}
	if request.Pattern != "" {
		arguments = append(arguments, "--pattern", request.Pattern)
	}
	arguments = append(arguments, "--max-depth", strconv.Itoa(codeGraphTraversalDepth), "--no-metadata")
	return r.run(ctx, request.GitRoot, codeGraphRunFiles, arguments)
}

func (r CodeGraphRunner) run(ctx context.Context, gitRoot string, command codeGraphRunCommand, arguments []string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !trustedCodeGraphRoot(gitRoot) || r.Config.Provider == LocalIndexProviderDisabled || r.Config.Err != nil {
		return nil, errors.Join(errCodeGraphMissing, ErrCodeGraphUnavailable)
	}
	path, err := r.executable()
	if err != nil {
		return nil, errors.Join(errCodeGraphExecutableAbsent, ErrCodeGraphUnavailable)
	}
	deadline, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	guard, err := openCodeGraphDB(gitRoot)
	if err != nil {
		return nil, err
	}
	defer guard.close()
	payload, err := runCodeGraphJSON(deadline, path, gitRoot, command, arguments, nil)
	if !guard.unchanged(gitRoot) {
		return nil, errCodeGraphMissing
	}
	return payload, err
}

func (r CodeGraphRunner) runInput(ctx context.Context, gitRoot string, command codeGraphRunCommand, arguments []string, input []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !trustedCodeGraphRoot(gitRoot) || r.Config.Provider == LocalIndexProviderDisabled || r.Config.Err != nil {
		return nil, errors.Join(errCodeGraphMissing, ErrCodeGraphUnavailable)
	}
	path, err := r.executable()
	if err != nil {
		return nil, errors.Join(errCodeGraphExecutableAbsent, ErrCodeGraphUnavailable)
	}
	deadline, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	guard, err := openCodeGraphDB(gitRoot)
	if err != nil {
		return nil, err
	}
	defer guard.close()
	payload, err := runCodeGraphJSON(deadline, path, gitRoot, command, arguments, bytes.Clone(input))
	if !guard.unchanged(gitRoot) {
		return nil, errCodeGraphMissing
	}
	return payload, err
}

func (r CodeGraphRunner) executable() (string, error) {
	if r.resolveExecutable != nil {
		return r.resolveExecutable("codegraph")
	}
	if r.Config.Executable == "" {
		return currentExecutableResolver("codegraph")
	}
	if !filepath.IsAbs(r.Config.Executable) {
		return "", ErrUntrustedExecutable
	}
	path, err := filepath.EvalSymlinks(r.Config.Executable)
	if err != nil {
		return "", ErrUntrustedExecutable
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", ErrUntrustedExecutable
	}
	if err := verifyTrustedExecutableOwnership(info); err != nil {
		return "", err
	}
	return path, nil
}

func (r CodeGraphRunner) timeout() time.Duration {
	if r.Config.Timeout == 0 {
		return 3 * time.Second
	}
	return r.Config.Timeout
}

func validCodeGraphLimit(limit int) bool {
	return limit > 0 && limit <= codeGraphCommandResultLimit
}
