package devhealthsource_test

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The window tests below walk the trailing window pass after pass: they pin
// that walk, not the pause between passes, so their sources have no pause.

func newUnpacedClickHouseSource(client contextpacket.ClickHouseQueryClient) (*devhealthsource.ClickHouseProjectionSource, error) {
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		return nil, err
	}
	return source.WithWindowPassInterval(0)
}

func newUnpacedTeamsProjectsSource(client contextpacket.ClickHouseQueryClient, enabled bool) (*devhealthsource.TeamsProjectsSource, error) {
	source, err := devhealthsource.NewTeamsProjectsSource(client, enabled)
	if err != nil {
		return nil, err
	}
	return source.WithWindowPassInterval(0)
}
