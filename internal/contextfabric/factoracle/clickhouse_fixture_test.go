package factoracle

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// One ClickHouse server for the package: every test that needs a store seeds
// its own database on it.
var (
	sharedServerOnce sync.Once
	sharedServer     *clickHouseServer
	sharedServerErr  error
)

type clickHouseServer struct {
	nativeAddr string
	httpURL    string
	terminate  func()
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedServer != nil {
		sharedServer.terminate()
	}
	os.Exit(code)
}

func startClickHouseServer() (*clickHouseServer, error) {
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{"9000/tcp", "8123/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForAll(wait.ForListeningPort("9000/tcp"), wait.ForHTTP("/ping").WithPort("8123/tcp")).WithDeadline(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		return nil, fmt.Errorf("start ClickHouse container: %w", err)
	}
	terminate := func() {
		if err := container.Terminate(context.Background()); err != nil {
			log.Printf("factoracle: terminate ClickHouse container: %v", err)
		}
	}
	host, err := container.Host(ctx)
	if err != nil {
		terminate()
		return nil, err
	}
	native, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		terminate()
		return nil, err
	}
	web, err := container.MappedPort(ctx, "8123/tcp")
	if err != nil {
		terminate()
		return nil, err
	}
	return &clickHouseServer{
		nativeAddr: net.JoinHostPort(host, native.Port()),
		httpURL:    "http://" + net.JoinHostPort(host, web.Port()),
		terminate:  terminate,
	}, nil
}

func clickHouse(t *testing.T) *clickHouseServer {
	t.Helper()
	sharedServerOnce.Do(func() { sharedServer, sharedServerErr = startClickHouseServer() })
	if sharedServerErr != nil {
		t.Fatalf("ClickHouse fixture: %v", sharedServerErr)
	}
	return sharedServer
}

var databaseNameUnsafe = regexp.MustCompile(`[^a-z0-9_]`)

// seedStore loads an extract into a database of its own and returns the
// production query client over it.
func seedStore(t *testing.T, extract *Extract) *runtimeclickhouse.Client {
	t.Helper()
	server := clickHouse(t)
	database := "o4_" + databaseNameUnsafe.ReplaceAllString(strings.ToLower(t.Name()), "_") + fmt.Sprintf("_%d", time.Now().UnixNano()%1_000_000)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := extract.Seed(ctx, SeedTarget{BaseURL: server.httpURL, User: "acr", Password: "acr", Database: database}); err != nil {
		t.Fatalf("seed %s: %v", database, err)
	}
	client, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + server.nativeAddr + "/" + database, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open query client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// admitAll is the graph side of the subject gate for the fixture caller: an
// unrestricted principal of the fixture organization, for whom every subject
// of the extract exists and is readable.
type admitAll struct{}

func (admitAll) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "o4-oracle", Epoch: 1}, nil
}

func (admitAll) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range out {
		out[i] = contextfabric.StoredSubjectAdmitted
	}
	return out, nil
}

func (admitAll) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

func fixturePrincipal() storage.Principal {
	return storage.Principal{OrgID: FixtureOrgID, Subject: "o4-oracle", CredentialID: "o4-oracle"}
}

// localPlanes wires both real tools: the production fact providers over the
// seeded store behind read_facts, and the production graphql_query runner
// with the production HTTP listener client pointed at the replay listener.
func localPlanes(t *testing.T, store *runtimeclickhouse.Client, recording *Recording) *LocalPlanes {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(store), contextfabric.FactRegistryOptions{Logger: quiet})
	if err != nil {
		t.Fatalf("fact registry: %v", err)
	}
	gate := directread.NewSubjectGate(admitAll{}, nil)
	facts := directread.NewFactsReader(gate, directread.NewFactReader(registry.WithoutScopeExpansion()), nil)

	listener := NewReplayListener(FixtureOrgID)
	server := httptest.NewServer(listener)
	t.Cleanup(server.Close)
	client, err := directread.NewHTTPGraphQLClient(server.URL, 30*time.Second)
	if err != nil {
		t.Fatalf("listener client: %v", err)
	}
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatalf("root policy: %v", err)
	}
	runner, err := directread.NewGraphQLRunner(directread.GraphQLRunnerConfig{Policy: policy, Gate: gate, Client: client, Logger: quiet})
	if err != nil {
		t.Fatalf("graphql runner: %v", err)
	}
	return &LocalPlanes{Runner: runner, Facts_: facts, Principal: fixturePrincipal(), Listener: listener, Recording: recording}
}
