package hosted

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// These tests cover the seam CHAOS-3770 and CHAOS-3771 share:
// buildContextFabricInvestigator now constructs a falkorgraph reader AND,
// independently, a model runtime. Neither constructor dials anything eagerly
// (falkorgraph builds a lazy client, pginvestigation.NewStore only requires a
// non-nil handle), so composition is provable here without a container.
//
// What is being proved is enablement independence: the graph backend and the
// model provider are separate switches, and the investigator is composed
// whenever the graph half is configured, whether or not a model is.

// idleConnector satisfies database/sql without a driver or a connection.
// buildContextFabricInvestigator only needs a non-nil *sql.DB handle to build
// pginvestigation.Store; nothing in this path issues a query.
type idleConnector struct{}

func (idleConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("hosted composition tests never open a connection")
}
func (idleConnector) Driver() driver.Driver { return nil }

func investigatorBuildRequest(t *testing.T) (buildRequest, postgresComponents, clickHouseComponents) {
	t.Helper()
	events := []string{}
	request := testBuildRequest(t, &events, "")
	request.config.EnableContextFabricInvestigations = true
	database := sql.OpenDB(idleConnector{})
	t.Cleanup(func() { _ = database.Close() })
	return request, postgresComponents{db: database}, clickHouseComponents{}
}

// configureGraph sets the FalkorDB selection CHAOS-3771 cut the runtime over
// to. ALLOW_INSECURE is required because the fixture address is plaintext.
//
// The request timeout is pinned to the one-second floor on purpose:
// falkorgraph.New dials eagerly during composition and blocks for the full
// request timeout when nothing is listening (it then composes anyway, so an
// unreachable graph does not fail startup). At the 30s default that would
// cost this file 30s per case for no added coverage.
func configureGraph(t *testing.T) {
	t.Helper()
	t.Setenv(falkorgraph.EnvAddr, "127.0.0.1:6379")
	t.Setenv(falkorgraph.EnvAllowInsecure, "true")
	t.Setenv(falkorgraph.EnvRequestTimeout, "1s")
}

func TestBuildContextFabricInvestigator_composesFalkorGraphWithAModelRuntime(t *testing.T) {
	// Given both halves configured: the FalkorDB graph backend and a model
	// provider.
	configureGraph(t)
	t.Setenv(modelprovider.EnvAPIKey, "sk-test")
	request, postgres, clickhouse := investigatorBuildRequest(t)

	// When
	investigator, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)

	// Then
	if err != nil {
		t.Fatalf("buildContextFabricInvestigator() = %v, want a composed investigator", err)
	}
	if investigator == nil {
		t.Fatal("investigator is nil with both the graph backend and a model provider configured")
	}
}

func TestBuildContextFabricInvestigator_composesFalkorGraphWithoutAModelRuntime(t *testing.T) {
	// Given the graph backend configured and NO model provider -- the
	// default deployment shape, and the one whose clean 503 CHAOS-3770 had
	// to preserve.
	configureGraph(t)
	request, postgres, clickhouse := investigatorBuildRequest(t)

	// When
	investigator, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)

	// Then the investigator is still composed: the graph and canonical-fact
	// layers are live, and only the answer step degrades per request.
	if err != nil {
		t.Fatalf("buildContextFabricInvestigator() = %v, want composition to succeed without a model provider", err)
	}
	if investigator == nil {
		t.Fatal("investigator is nil without a model provider; the graph layer must stay live and 503 per request instead")
	}
}

func TestBuildContextFabricInvestigator_staysUnbuiltWithoutTheGraphBackend(t *testing.T) {
	// Given a model provider but no graph backend.
	t.Setenv(modelprovider.EnvAPIKey, "sk-test")
	request, postgres, clickhouse := investigatorBuildRequest(t)

	// When
	investigator, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)

	// Then a configured model alone must not compose an investigator: the
	// graph backend is still the gate, and an unconfigured one never fails
	// composition.
	if err != nil {
		t.Fatalf("buildContextFabricInvestigator() = %v, want no failure over an unconfigured graph backend", err)
	}
	if investigator != nil {
		t.Fatal("investigator was composed without a graph backend")
	}
}

// TestBuildContextFabricInvestigator_wiresBothReuseSnapshottersWhenReuseIsEnabled
// is CHAOS-3782 Codex round-3 finding 1: engine.go only captures the
// rebuild-epoch snapshot when EngineDependencies.ReuseEpochSnapshotter is
// set, but this composition wired only the watermark snapshotter, leaving
// ReuseEpochSnapshotter nil in production. Saved rows then always carry a
// nil invalidation_epoch, and store.go's FindReusable requires a non-nil
// epoch to reuse -- so hosted answer reuse silently never fires for any
// new result, even with ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE set.
//
// This reaches through the unexported Engine fields via reflect --
// FieldByName + IsNil never call Interface(), so this does not need
// Engine to expose new production surface purely for this test -- because
// that unexported-field wiring is exactly the seam that silently broke.
func TestBuildContextFabricInvestigator_wiresBothReuseSnapshottersWhenReuseIsEnabled(t *testing.T) {
	// Given the graph backend configured and answer reuse turned on.
	configureGraph(t)
	request, postgres, clickhouse := investigatorBuildRequest(t)
	request.config.AnswerReuseMaxAge = time.Hour

	// When
	investigator, _, _, reuseInvalidator, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)
	if err != nil {
		t.Fatalf("buildContextFabricInvestigator() = %v, want a composed investigator", err)
	}
	engine, ok := investigator.(*contextfabric.Engine)
	if !ok {
		t.Fatalf("investigator is a %T, want *contextfabric.Engine", investigator)
	}

	// Then BOTH snapshotters Engine captures immediately before a fresh
	// graph read must be wired -- one without the other silently disables
	// reuse for every new result.
	engineValue := reflect.ValueOf(engine).Elem()
	if field := engineValue.FieldByName("reuseSnapshotter"); field.IsNil() {
		t.Error("reuseSnapshotter is nil with answer reuse enabled; watermark snapshots will never be captured")
	}
	if field := engineValue.FieldByName("reuseEpochSnapshotter"); field.IsNil() {
		t.Error("reuseEpochSnapshotter is nil with answer reuse enabled; saved results will never carry an invalidation epoch, so FindReusable can never reuse them (CHAOS-3782 round-3 finding 1)")
	}
	// CHAOS-3786, codex round-1 P1(b): the model-config PUT/DELETE routes
	// need a real ReuseInvalidator to invalidate reuse on a chain change;
	// with answer reuse enabled, this must not be nil.
	if reuseInvalidator == nil {
		t.Error("reuseInvalidator is nil with answer reuse enabled; a model config change could never invalidate reuse for the organization (CHAOS-3786)")
	}
}

// TestBuildContextFabricInvestigator_wiresEveryReuseKeyVersionAuthority is
// CHAOS-3862 sol round-2 finding F4: every prior test in this file that
// touches reuse composition builds the engine and asserts a DEPENDENCY
// (a snapshotter, an invalidator) is non-nil. None of them ever asserted
// the OPTIONS-level version-authority strings/slices Engine actually
// received -- so zeroing EngineOptions.ReusePromptVersions (or any of the
// other reuse-key options fields) in open.go would leave every engine/store
// unit test green (they hand-build EngineOptions themselves) while
// production silently crated the corresponding dimension of reuse to a
// permanent "always NULL, never matches" no-op. This test closes that gap
// by building the REAL composition (buildContextFabricInvestigator, the
// same seam every other test in this file exercises) and reflecting on the
// resulting *contextfabric.Engine's own unexported reuse-key fields --
// mirroring TestBuildContextFabricInvestigator_wiresBothReuseSnapshottersWhenReuseIsEnabled's
// FieldByName technique immediately above, extended from IsNil() to
// String()/Len() since these are string and []string fields, not
// interfaces. Value.String() and Value.Len() are safe to call on a field
// reached through an unexported parent field (unlike Interface()), so no
// new production surface is needed purely for this test.
func TestBuildContextFabricInvestigator_wiresEveryReuseKeyVersionAuthority(t *testing.T) {
	configureGraph(t)
	// A configured model provider (mirroring
	// TestBuildContextFabricInvestigator_composesFalkorGraphWithAModelRuntime)
	// is required for reuseModelIdentities to be non-empty: with NO model
	// provider configured, an empty chain is the CORRECT behavior (see
	// TestBuildContextFabricInvestigator_composesFalkorGraphWithoutAModelRuntime),
	// not a wiring bug -- so this test's "everything should be wired"
	// claim only holds for the fully-configured deployment shape.
	t.Setenv(modelprovider.EnvAPIKey, "sk-test")
	request, postgres, clickhouse := investigatorBuildRequest(t)
	request.config.AnswerReuseMaxAge = time.Hour

	investigator, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)
	if err != nil {
		t.Fatalf("buildContextFabricInvestigator() = %v, want a composed investigator", err)
	}
	engine, ok := investigator.(*contextfabric.Engine)
	if !ok {
		t.Fatalf("investigator is a %T, want *contextfabric.Engine", investigator)
	}
	engineValue := reflect.ValueOf(engine).Elem()

	if v := engineValue.FieldByName("reuseProjectionVersion").String(); v == "" {
		t.Error("reuseProjectionVersion is empty; the projection-version dimension of reuse silently never matches")
	}
	if n := engineValue.FieldByName("reuseModelIdentities").Len(); n == 0 {
		t.Error("reuseModelIdentities is empty; the model-identity dimension of reuse silently never matches")
	}

	retrieval := engineValue.FieldByName("reuseRetrievalIdentity")
	if v := retrieval.FieldByName("EmbedRetrievalIdentity").String(); v == "" {
		t.Error("reuseRetrievalIdentity.EmbedRetrievalIdentity is empty (CHAOS-3833)")
	}
	if v := retrieval.FieldByName("RetrievalPolicyVersion").String(); v == "" {
		t.Error("reuseRetrievalIdentity.RetrievalPolicyVersion is empty (CHAOS-3833)")
	}

	prompts := engineValue.FieldByName("reusePromptVersions")
	if v := prompts.FieldByName("InterpretationPromptVersion").String(); v == "" {
		t.Error("reusePromptVersions.InterpretationPromptVersion is empty (CHAOS-3862): open.go's wiring silently disabled this dimension of reuse")
	}
	if v := prompts.FieldByName("SynthesisPromptVersion").String(); v == "" {
		t.Error("reusePromptVersions.SynthesisPromptVersion is empty (CHAOS-3862): open.go's wiring silently disabled this dimension of reuse")
	}

	authorities := engineValue.FieldByName("reuseVersionAuthorities")
	if v := authorities.FieldByName("QueryVersion").String(); v == "" {
		t.Error("reuseVersionAuthorities.QueryVersion is empty (CHAOS-3862 round 2): open.go's wiring silently disabled this dimension of reuse")
	}
	if v := authorities.FieldByName("CanonicalServiceVersion").String(); v == "" {
		t.Error("reuseVersionAuthorities.CanonicalServiceVersion is empty (CHAOS-3862 round 2): open.go's wiring silently disabled this dimension of reuse")
	}
	if v := authorities.FieldByName("ModelOutputSchemaVersion").String(); v == "" {
		t.Error("reuseVersionAuthorities.ModelOutputSchemaVersion is empty (CHAOS-3862 round 2): open.go's wiring silently disabled this dimension of reuse")
	}
}

// TestBuildContextFabricInvestigator_reuseInvalidatorIsNilWhenReuseIsDisabled
// is the CHAOS-3786 counterpart: with answer reuse OFF (the default --
// ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE unset), there is no reuse-capable
// investigation-result store to invalidate against, so the returned
// ReuseInvalidator must stay nil -- the model-config routes then skip
// invalidation entirely rather than calling a store that never wrote a
// reuse column in the first place.
func TestBuildContextFabricInvestigator_reuseInvalidatorIsNilWhenReuseIsDisabled(t *testing.T) {
	configureGraph(t)
	request, postgres, clickhouse := investigatorBuildRequest(t)

	investigator, _, _, reuseInvalidator, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)
	if err != nil {
		t.Fatalf("buildContextFabricInvestigator() = %v, want a composed investigator", err)
	}
	if investigator == nil {
		t.Fatal("investigator was not composed with a configured graph backend")
	}
	if reuseInvalidator != nil {
		t.Errorf("reuseInvalidator = %v, want nil with answer reuse disabled", reuseInvalidator)
	}
}

func TestBuildContextFabricInvestigator_failsOnAMisconfiguredModelProvider(t *testing.T) {
	// Given a configured graph backend and a model provider that is present
	// but invalid.
	configureGraph(t)
	t.Setenv(modelprovider.EnvAPIKey, "sk-test")
	t.Setenv(modelprovider.EnvTimeout, "10m")
	request, postgres, clickhouse := investigatorBuildRequest(t)

	// When
	investigator, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)

	// Then startup fails rather than silently serving 503s.
	if err == nil {
		t.Fatal("buildContextFabricInvestigator() = nil error for an invalid model configuration")
	}
	if investigator != nil {
		t.Fatal("investigator was composed alongside an error")
	}
}
