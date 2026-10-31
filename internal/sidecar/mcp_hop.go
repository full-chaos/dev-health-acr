package sidecar

import (
	"errors"
	"os"
	"strings"
)

// ErrHopTokenUnavailable reports that the hosted MCP endpoint is configured
// to prove its hop (CHAOS-12094) and could not read its token file. The call
// is not sent: without the hop acr-api refuses an MCP-bound bearer anyway,
// and this names the cause on our side. The message carries no path and no
// OS error text.
var ErrHopTokenUnavailable = errors.New("acr: the hosted MCP hop token could not be read")

// maxHopTokenBytes bounds the token file read: a projected ServiceAccount
// token is a JWT of a few KiB.
const maxHopTokenBytes = 16 << 10

// hopToken reads the projected ServiceAccount token fresh on every call:
// kubelet rotates the file in place before the token expires.
func readHopToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > maxHopTokenBytes {
		return "", ErrHopTokenUnavailable
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", ErrHopTokenUnavailable
	}
	return token, nil
}
