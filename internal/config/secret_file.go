package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maximumSecretFileBytes int64 = 64 << 10

var (
	ErrSecretSourceConflict  = errors.New("configuration secret has conflicting sources")
	ErrSecretFileInvalid     = errors.New("configuration secret file is invalid")
	ErrSecretFilePermissions = errors.New("configuration secret file has unsafe permissions")
	ErrSecretFileUnreadable  = errors.New("configuration secret file cannot be read")
	ErrSecretFileTooLarge    = errors.New("configuration secret file is too large")
)

// SecretValue reads a trimmed value from exactly one of KEY or KEY_FILE.
// Secret files must be regular and may not be writable by group or others.
func SecretValue(lookup func(string) (string, bool), key string) (string, error) {
	direct, hasDirect := lookup(key)
	fileKey := key + "_FILE"
	path, hasPath := lookup(fileKey)
	if hasDirect && hasPath {
		return "", fmt.Errorf("%s: %w", key, ErrSecretSourceConflict)
	}
	if !hasPath {
		return strings.TrimSpace(direct), nil
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s: %w", fileKey, ErrSecretFileInvalid)
	}
	return readSecretFile(strings.TrimSpace(path), fileKey)
}

func readSecretFile(path, key string) (string, error) {
	beforeOpen, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, ErrSecretFileUnreadable)
	}
	if !beforeOpen.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %w", key, ErrSecretFileInvalid)
	}
	if beforeOpen.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%s: %w", key, ErrSecretFilePermissions)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, ErrSecretFileUnreadable)
	}
	defer file.Close()
	afterOpen, err := file.Stat()
	if err != nil || !afterOpen.Mode().IsRegular() || !os.SameFile(beforeOpen, afterOpen) {
		return "", fmt.Errorf("%s: %w", key, ErrSecretFileInvalid)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumSecretFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, ErrSecretFileUnreadable)
	}
	if int64(len(contents)) > maximumSecretFileBytes {
		return "", fmt.Errorf("%s: %w", key, ErrSecretFileTooLarge)
	}
	return strings.TrimSpace(string(contents)), nil
}
