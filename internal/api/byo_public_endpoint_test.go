package api

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func loopbackModelServer(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := &atomic.Int64{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	return listener.Addr().String(), accepted
}

// A saved BYO configuration whose base_url names a loopback server is served by
// ops, resolved by the real per-organization resolver and built by the real
// modelprovider.Build: the organization's turn never connects to that server
// and never reaches the platform model, and neither the address nor the key
// reaches the response or the log. With the deployment's allowance on (the
// control) the same turn does connect, so the counter tells a refusal from
// silence.
func TestAServedBaseURLOnALoopbackServerIsNeverDialledByTheOrgModel(t *testing.T) {
	address, accepted := loopbackModelServer(t)
	ops := opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "saved_with_base_url", baseURL: "https://" + address + "/v1"}
	defaults := modelprovider.Config{Timeout: modelprovider.DefaultTimeout, MaxAttempts: 1, MaxTransportRetries: modelprovider.DefaultMaxTransportRetries}
	run := func(allowNonPublic bool) (int, string, string) {
		defaults.AllowNonPublicBYOEndpoint = allowNonPublic
		rig := opsBackedRouteRigWith(t, ops, func(model contextfabric.ModelRuntime) (contextfabric.ModelRuntime, modelruntimeresolver.Build) {
			return model, modelruntimeresolver.NewModelProviderBuild(defaults)
		})
		recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
		return recorder.Code, recorder.Body.String(), rig.logs.String()
	}

	_, controlBody, _ := run(true)
	if accepted.Load() == 0 {
		t.Fatalf("with the allowance on the turn never reached the loopback server: the counter cannot tell a refusal from silence\nbody=%s", controlBody)
	}

	before := accepted.Load()
	status, body, logs := run(false)
	if got := accepted.Load() - before; got != 0 {
		t.Fatalf("the org model connected to the loopback server %d times, want 0\nbody=%s", got, body)
	}
	if status < 500 {
		t.Fatalf("status = %d body=%s, want a server-side refusal of the turn", status, body)
	}
	for _, text := range []string{body, logs} {
		for _, secret := range []string{"sk-org-standin", address, "127.0.0.1"} {
			if strings.Contains(text, secret) {
				t.Fatalf("%q reached the response or the log:\n%s", secret, text)
			}
		}
	}
}
