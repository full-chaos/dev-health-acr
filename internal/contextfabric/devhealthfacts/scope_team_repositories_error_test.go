package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// repositoryErrorClient fails the team repository read at once and holds the
// unlinked issue count (the read that runs beside it) until released. Every
// other statement fails at once too, so the turn ends as soon as the scope
// phase does.
type repositoryErrorClient struct {
	release       chan struct{}
	honorsContext bool
	// holdOthers, when set, keeps every other statement waiting, so the scope
	// phase is still running when the cancel of the count is looked for.
	holdOthers    chan struct{}
	mu            sync.Mutex
	countStarted  bool
	countCanceled bool
}

func (c *repositoryErrorClient) Query(ctx context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	switch {
	case strings.Contains(statement, "best_origin_team_id"):
		return nil, errors.New("repository read failed")
	case strings.Contains(statement, "SELECT uniqExact"):
		c.mu.Lock()
		c.countStarted = true
		c.mu.Unlock()
		if c.honorsContext {
			select {
			case <-ctx.Done():
				c.mu.Lock()
				c.countCanceled = true
				c.mu.Unlock()
				return nil, ctx.Err()
			case <-c.release:
			}
		} else {
			<-c.release
		}
		return nil, errors.New("count released")
	}
	if c.holdOthers != nil {
		select {
		case <-c.holdOthers:
		case <-ctx.Done():
		}
	}
	return nil, errors.New("no database in this probe")
}

func (c *repositoryErrorClient) state() (started, canceled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.countStarted, c.countCanceled
}

func readTeamTurnWith(t *testing.T, client contextpacket.ClickHouseQueryClient) chan struct{} {
	t.Helper()
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), contextfabric.FactRegistryOptions{ScopeExpander: devhealthfacts.NewScopeExpander(client)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = registry.ReadFacts(context.Background(), storage.Principal{OrgID: sharedTestOrgID(t), RepositoryScopes: []string{"acme/allowed"}}, severalKindsTeamRequest())
	}()
	return done
}

// A failed repository read ends the turn's scope phase as fast as the serial
// walk did, even when the count beside it never answers.
func TestTeamRepositoryReadErrorDoesNotWaitForTheUnlinkedCount(t *testing.T) {
	client := &repositoryErrorClient{release: make(chan struct{})}
	defer close(client.release)
	done := readTeamTurnWith(t, client)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFacts stayed blocked after the repository read failed, waiting for the unlinked count")
	}
	if started, _ := client.state(); !started {
		t.Fatal("the unlinked count never started beside the repository read: the probe does not reach the concurrent path")
	}
}

// The count that is left behind is told to stop at once, while the rest of the
// turn's scope phase is still running.
func TestTeamRepositoryReadErrorCancelsTheUnlinkedCount(t *testing.T) {
	client := &repositoryErrorClient{release: make(chan struct{}), honorsContext: true, holdOthers: make(chan struct{})}
	defer close(client.release)
	done := readTeamTurnWith(t, client)
	cancelled := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if _, canceled := client.state(); canceled {
			cancelled = true
			break
		}
	}
	close(client.holdOthers)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFacts did not return")
	}
	if !cancelled {
		t.Fatal("the unlinked count was not cancelled after the repository read failed")
	}
}
