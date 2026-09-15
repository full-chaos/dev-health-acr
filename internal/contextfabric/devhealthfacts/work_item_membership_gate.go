package devhealthfacts

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// defaultWorkItemMembershipProcessGate is created once for the lifetime of
// this process. A nil reader option must share the same bounded admission
// state across all readers; creating a gate per reader would multiply the
// effective population bound when a caller constructs several readers.
//
// The limits are the existing internal work-item scope limits. They are kept
// in contextfabric so this declaration does not add a public configuration
// surface or alter the established bound.
var defaultWorkItemMembershipProcessGate = func() *contextfabric.WorkItemMembershipGate {
	gate, err := contextfabric.NewWorkItemMembershipGate(
		contextfabric.DefaultWorkItemMembershipMaxInFlight,
		contextfabric.DefaultWorkItemMembershipQueueCapacity,
	)
	if err != nil {
		panic(err)
	}
	return gate
}()
