package sidecar

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrUntrustedExecutable is returned when a named external tool ("git",
// "security", "secret-tool") cannot be resolved to a trusted absolute
// filesystem path. It carries only the fixed tool name, never a directory
// entry or resolved path, since either could otherwise surface workspace-
// or environment-controlled text on an operator-facing error.
var ErrUntrustedExecutable = errors.New("sidecar: executable could not be resolved to a trusted absolute path")

// ErrExecutableUnavailable is returned only when a trusted executable is not
// installed in any trusted search directory. Callers may use this exact
// condition to select a documented fallback; trust, permission, and runtime
// failures must remain fail-closed.
var ErrExecutableUnavailable = errors.New("sidecar: trusted executable is unavailable")

// executableResolver resolves the name of an external tool to a trusted
// absolute path, or returns ErrUntrustedExecutable (a tampered or
// misconfigured candidate) or ErrExecutableUnavailable (genuinely absent)
// when it cannot.
type executableResolver func(name string) (string, error)

// currentExecutableResolver is the active resolver seam every external-tool
// invocation in this package goes through (runGit, gitChangedFiles,
// runKeyringCommand): production always uses resolveTrustedExecutable.
// Tests inject an explicit fake resolver (see injectExecutableResolver in
// exec_resolver_test.go) rather than manipulating the process environment,
// so a test proves this seam is what selects the binary, not incidental
// directory-search behavior.
var currentExecutableResolver executableResolver = resolveTrustedExecutable

// trustedExecutableSearchDirs are the only directories resolveTrustedExecutable
// ever consults when resolving a named external tool, in search order.
// This list is fixed at compile time per GOOS; it is never derived from
// the process's PATH environment variable, an MCP client's supplied
// value, or any other workspace- or caller-influenced source. A PATH
// entry -- relative or absolute, and regardless of where it sits in
// PATH's own ordering -- therefore has no effect on resolution at all: it
// is simply never searched, closing the class of attack where a
// workspace- or client-controlled absolute PATH entry shadows the real
// system tool ahead of it in search order. Platforms with no list
// defined here (anything other than darwin/linux) always fail closed in
// resolveTrustedExecutable; see this repository's remediation notes for
// that residual platform caveat.
func trustedExecutableSearchDirs() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/opt/homebrew/bin", "/usr/local/bin"}
	case "linux":
		return []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin"}
	default:
		return nil
	}
}

// trustedExecutablePrefixes bounds where a search-directory candidate's
// fully resolved (symlinks followed) target may live. A trusted search
// directory entry is commonly itself a symlink into a package manager's
// real install location -- Homebrew's /opt/homebrew/bin/git ->
// ../Cellar/git/<version>/bin/git is the common case on macOS -- so that
// indirection is allowed, but only as long as the final target still
// lives under one of these prefixes. This rejects a symlink that escapes
// every trusted system location entirely (for example, one crafted to
// point at a workspace- or otherwise caller-writable path) even though
// the symlink itself sits inside a trusted search directory.
func trustedExecutablePrefixes() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/usr", "/bin", "/sbin", "/opt/homebrew"}
	case "linux":
		return []string{"/usr", "/bin", "/sbin"}
	default:
		return nil
	}
}

// resolveTrustedExecutable resolves name to a trusted absolute path by
// searching only trustedExecutableSearchDirs, never the process's PATH
// environment variable: PATH is a value any workspace, MCP client, or
// other untrusted launcher of this process can set, so it is never
// consulted at all. For each candidate (dir/name), the full symlink chain
// is resolved and the final target must: exist and be a regular file;
// live under one of trustedExecutablePrefixes; be owned by root or by
// this process's own effective user and grant no group- or world-write
// access (verifyTrustedExecutableOwnership -- unix-only, see its own doc
// comment and the non-unix stub for why a platform without that check can
// never reach it, since trustedExecutableSearchDirs is empty there); and
// have at least one executable bit set. A missing candidate (no file at
// dir/name) is skipped so a later trusted directory still gets a chance;
// a candidate that exists but fails any other check (untrusted target,
// wrong type, ownership/permission) aborts resolution immediately with a
// hard error instead of silently trying the next directory, since that
// shape indicates tampering or misconfiguration, not mere absence.
func resolveTrustedExecutable(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, filepath.Separator) {
		return "", fmt.Errorf("%w: %s", ErrUntrustedExecutable, name)
	}
	for _, dir := range trustedExecutableSearchDirs() {
		resolved, err := resolveTrustedCandidate(dir, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("resolve trusted executable %s: %w", name, err)
		}
		if resolved != "" {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrExecutableUnavailable, name)
}

// resolveTrustedCandidate applies every check documented on
// resolveTrustedExecutable to the single candidate dir/name.
func resolveTrustedCandidate(dir, name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	if !isUnderTrustedPrefix(resolved) {
		return "", ErrUntrustedExecutable
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", ErrUntrustedExecutable
	}
	if err := verifyTrustedExecutableOwnership(info); err != nil {
		return "", err
	}
	return resolved, nil
}

func isUnderTrustedPrefix(resolved string) bool {
	for _, prefix := range trustedExecutablePrefixes() {
		if resolved == prefix || strings.HasPrefix(resolved, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// credentialSafeEnviron returns the current process environment with
// every ACR_-prefixed variable removed. It is the only environment every
// git and OS-keyring subprocess this package launches (runGit,
// gitChangedFiles, runKeyringCommand) receives, assigned directly to
// exec.Cmd.Env: none of git's or a keyring backend's own functionality
// depends on this sidecar's own configuration or credential variables,
// and every one of them -- ACR_API_TOKEN directly, the keyring/token-file
// identifiers alongside it, and the rest of the ACR_ namespace with them
// -- would otherwise be inherited by exec.Cmd's default behavior (a nil
// Env means "inherit the full parent environment") into a process
// resolved from a name a workspace can still influence the arguments to,
// even once resolution itself is trusted. Stripping the whole ACR_
// namespace, rather than an allowlist of "known credential" names, is a
// single rule that stays correct as new ACR_ variables are added, instead
// of one that silently under-strips the day a new credential-adjacent
// variable is introduced without a matching allowlist update. Everything
// else in the process environment (HOME, LANG, USER, and so on) passes
// through unchanged, since git and OS keyring backends legitimately
// depend on it for locale- and identity-correct behavior.
func credentialSafeEnviron() []string {
	full := os.Environ()
	safe := make([]string, 0, len(full))
	for _, kv := range full {
		if strings.HasPrefix(kv, "ACR_") {
			continue
		}
		safe = append(safe, kv)
	}
	return safe
}
