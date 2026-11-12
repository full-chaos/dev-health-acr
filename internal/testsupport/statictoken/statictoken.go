// Package statictoken is a fixed workload token source for tests of the Dev Health entitlement client.
package statictoken

import "context"

// Source returns its own value as the workload token for every audience and scope.
type Source string

// Token returns s.
func (s Source) Token(context.Context, string, ...string) (string, error) { return string(s), nil }
