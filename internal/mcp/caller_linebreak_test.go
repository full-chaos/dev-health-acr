package mcp

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveCallerRefusesALineBreakInTheBearerBeforeAnyHostedCall(t *testing.T) {
	fx := newMultiCallerFixture(t)
	known := fixtureToken(0x35)
	fx.register(known, "caller-known", validCapabilitiesFixture())
	cfg := NewProcessConfig(fixtureConfig(t, fx.Server), testReleaseIdentity(), io.Discard)
	rows := map[string]string{
		"trailing_lf":   known + "\n",
		"trailing_crlf": known + "\r\n",
		"embedded_lf":   known[:20] + "\n" + known[20:],
		"embedded_cr":   known[:20] + "\r" + known[20:],
	}

	for name, bearer := range rows {
		caller, err := ResolveCaller(context.Background(), cfg, CallerCredential{Bearer: bearer})

		require.ErrorIs(t, err, ErrCallerCredentialInvalid, "%s", name)
		require.Nil(t, caller, "%s", name)
	}
	require.Zero(t, fx.unauthorizedCount(), "a malformed credential must never reach the hosted API")
}
