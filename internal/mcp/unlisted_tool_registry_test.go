package mcp

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEveryManifestToolNotDisabledByDefaultIsRegisteredForAFullCaller(t *testing.T) {
	caps := validCapabilitiesFixture()
	for _, name := range manifestToolNames() {
		if !contains(caps.EnabledTools, name) {
			caps.EnabledTools = append(caps.EnabledTools, name)
		}
	}
	cfg, caller := (&Bootstrap{Capabilities: caps}).split(io.Discard)
	session := connectedClientForCaller(t, cfg, caller)
	listed := listedToolNames(t, session)
	for _, name := range manifestToolNames() {
		if entry, _ := lookupManifestEntry(name); entry.DisabledByDefault {
			continue
		}
		require.Contains(t, listed, name, "a tool in the manifest is not registered by the server; the unlisted-call path would misjudge it")
	}
}

func contains(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}
