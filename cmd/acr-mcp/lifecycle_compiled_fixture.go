//go:build acr_compiled_lifecycle_fixture

package main

import (
	"errors"
	"fmt"
	"os"
)

const compiledLifecycleBrowserMarkerEnvironment = "ACR_TEST_BROWSER_EVENT_MARKER"

var errCompiledLifecycleBrowserFixture = errors.New("compiled lifecycle browser fixture")

func init() {
	lifecycleBrowserOpen = func(string) error {
		recordCompiledLifecycleBrowserEvent("browser.open\n")
		return errCompiledLifecycleBrowserFixture
	}
}

func recordCompiledLifecycleBrowserEvent(event string) {
	marker, ok := os.LookupEnv(compiledLifecycleBrowserMarkerEnvironment)
	if !ok || marker == "" {
		panic("compiled lifecycle browser fixture marker is required")
	}
	file, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(fmt.Sprintf("record compiled lifecycle browser fixture event: %v", err))
	}
	if _, err := file.WriteString(event); err != nil {
		_ = file.Close()
		panic(fmt.Sprintf("write compiled lifecycle browser fixture event: %v", err))
	}
	if err := file.Close(); err != nil {
		panic(fmt.Sprintf("close compiled lifecycle browser fixture marker: %v", err))
	}
}
