package auth

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/stretchr/testify/require"
)

const deviceFlowTestOrgID = "11111111-1111-1111-1111-111111111111"

type deviceFlowFixture struct {
	now         time.Time
	flow        *DeviceFlowService
	store       storage.DeviceAuthorizationStore
	credentials *storage.CredentialLifecycle
	audit       *memory.AuditStore
}

func newDeviceFlowFixture(t *testing.T, random io.Reader) *deviceFlowFixture {
	t.Helper()
	fixture := &deviceFlowFixture{now: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)}
	fixture.audit = memory.NewAuditStore()
	var err error
	fixture.credentials, err = memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{
		Audit: fixture.audit,
		Now:   func() time.Time { return fixture.now },
	})
	require.NoError(t, err)
	deviceStore, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{
		Credentials: fixture.credentials,
		Now:         func() time.Time { return fixture.now },
	})
	require.NoError(t, err)
	fixture.store = deviceStore
	credentialService := newTestService(t, fixture.credentials, fixture.audit, fixture.now)
	fixture.flow, err = NewDeviceFlowService(fixture.store, credentialService, DeviceFlowOptions{
		Now:    func() time.Time { return fixture.now },
		Random: random,
	})
	require.NoError(t, err)
	return fixture
}

func (f *deviceFlowFixture) start(t *testing.T) DeviceAuthorizationStart {
	t.Helper()
	started, err := f.flow.Start(context.Background(), DeviceAuthorizationHints{})
	require.NoError(t, err)
	return started
}

func deviceFlowRandom(seeds ...byte) io.Reader {
	value := make([]byte, 0, len(seeds)*(deviceCodeBytes+userCodeRandomBytes))
	for _, seed := range seeds {
		value = append(value, bytes.Repeat([]byte{seed}, deviceCodeBytes+userCodeRandomBytes)...)
	}
	return bytes.NewReader(value)
}

func deviceApprovalPrincipal(repositories ...string) storage.Principal {
	return storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodWebAssertion,
		Subject:              "user_1",
		OrgID:                deviceFlowTestOrgID,
		RepositoryScopes:     repositories,
		Permissions:          []string{WebAssertionPermissionCredentialIssue},
	}
}
