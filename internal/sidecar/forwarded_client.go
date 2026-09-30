package sidecar

import (
	"errors"
	"net/netip"
)

// WithForwardedClient derives a client that states address (the resolved end
// caller, an IP literal) as the X-Forwarded-For of EVERY hosted API call it
// makes, so the hosted API's per-address gate keys on the caller and not on
// this process. It is a property of the per-request client, not of a context:
// tool handlers run on a context the transport does not control, and a
// context-carried address was lost after authentication. A value that is not
// an IP literal is refused: nothing free-form reaches the wire.
func (c *Client) WithForwardedClient(address string) (*Client, error) {
	if c == nil {
		return nil, errors.New("acr: a base client is required")
	}
	parsed, err := netip.ParseAddr(address)
	if err != nil {
		return nil, errors.New("acr: the forwarded client address is not an IP literal")
	}
	derived := *c
	derived.forwardedClient = parsed.Unmap().String()
	return &derived, nil
}
