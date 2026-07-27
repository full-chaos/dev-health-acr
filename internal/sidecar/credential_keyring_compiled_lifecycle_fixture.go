//go:build acr_compiled_lifecycle_fixture

package sidecar

import (
	"context"
	"fmt"
	"os"
)

const compiledLifecycleKeyringMarkerEnvironment = "ACR_TEST_KEYRING_EVENT_MARKER"

func init() {
	currentKeyringLookup = compiledLifecycleKeyringLookup
	currentKeyringWriter = compiledLifecycleKeyringWriter
	currentKeyringDeleter = compiledLifecycleKeyringDeleter
}

func compiledLifecycleKeyringLookup(context.Context, string, string) (string, bool, error) {
	recordCompiledLifecycleKeyringEvent("keyring.lookup\n")
	return "", false, ErrExecutableUnavailable
}

func compiledLifecycleKeyringWriter(context.Context, string, string, string) error {
	recordCompiledLifecycleKeyringEvent("keyring.write\n")
	return errKeyringWriteUnavailable
}

func compiledLifecycleKeyringDeleter(context.Context, string, string) error {
	recordCompiledLifecycleKeyringEvent("keyring.delete\n")
	return errKeyringWriteUnavailable
}

func recordCompiledLifecycleKeyringEvent(event string) {
	marker, ok := os.LookupEnv(compiledLifecycleKeyringMarkerEnvironment)
	if !ok || marker == "" {
		panic("compiled lifecycle keyring fixture marker is required")
	}
	file, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(fmt.Sprintf("record compiled lifecycle keyring fixture event: %v", err))
	}
	if _, err := file.WriteString(event); err != nil {
		_ = file.Close()
		panic(fmt.Sprintf("write compiled lifecycle keyring fixture event: %v", err))
	}
	if err := file.Close(); err != nil {
		panic(fmt.Sprintf("close compiled lifecycle keyring fixture marker: %v", err))
	}
}
