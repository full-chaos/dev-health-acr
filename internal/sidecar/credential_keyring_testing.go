package sidecar

import (
	"context"
	"errors"
	"maps"
	"sync"
	"testing"
)

// KeyringAddress identifies one OS keyring record. Both halves are
// operator-supplied, so they are kept as separate fields rather than joined
// into a single identifier that could collide (see credentialPurgeKey).
type KeyringAddress struct {
	Service string
	Account string
}

// ErrKeyringTestSeamUnavailable is returned when a memory keyring is requested
// outside a test binary.
var ErrKeyringTestSeamUnavailable = errors.New("acr: the in-memory keyring is available only under go test")

// MemoryKeyring is an in-process stand-in for the OS secret store.
//
// It exists because the packages that own login and logout (cmd/acr-mcp,
// internal/mcp) are not this package, so they cannot reach the unexported
// keyring seams the sidecar's own tests use. Without an injectable store their
// only options were to disable the keyring -- leaving every keyring code path
// in those commands untested -- or to run against the host's real login
// keychain, which prompts, depends on the developer's machine, and can leave
// real entries behind.
//
// Nothing here touches a host keychain, and installation is refused outside a
// test binary so a production process can never substitute a fake secret store
// for the real one.
type MemoryKeyring struct {
	mu             sync.Mutex
	entries        map[KeyringAddress]string
	commitThenFail map[KeyringAddress]error
	lookupErr      map[KeyringAddress]error
	deleteErr      map[KeyringAddress]error
	deletes        []KeyringAddress
}

// ProbeKeyringSeamForTesting calls the installed lookup seam with a fixed
// address, so a test package outside this one can assert what its own default
// seam does -- in particular that it panics rather than quietly answering.
//
// A guarantee that lives only in a TestMain is invisible at every call site
// that depends on it, so it has to be assertable from a test.
func ProbeKeyringSeamForTesting() error {
	if !testing.Testing() {
		return ErrKeyringTestSeamUnavailable
	}
	_, _, err := currentKeyringLookup(context.Background(), "acr-keyring-seam-probe", "acr-keyring-seam-probe")
	return err
}

// SetKeyringSeamsForTesting replaces the three OS keyring seams outright and
// returns a restore function.
//
// It exists so a test package outside this one can install a seam that is not a
// working store at all -- most usefully one that panics, which makes any
// unintended keyring access a loud failure rather than a silent "no entry" that
// reads as a pass. InstallMemoryKeyringForTesting below is the opt-in
// counterpart, for tests that genuinely need keyring contents.
//
// Refused outside a test binary, so a production process can never replace its
// secret store.
func SetKeyringSeamsForTesting(lookup KeyringLookup, writer KeyringWriter, deleter KeyringDeleter) (func(), error) {
	if !testing.Testing() {
		return nil, ErrKeyringTestSeamUnavailable
	}
	originalLookup, originalWriter, originalDeleter := currentKeyringLookup, currentKeyringWriter, currentKeyringDeleter
	currentKeyringLookup, currentKeyringWriter, currentKeyringDeleter = lookup, writer, deleter
	return func() {
		currentKeyringLookup, currentKeyringWriter, currentKeyringDeleter = originalLookup, originalWriter, originalDeleter
	}, nil
}

// InstallMemoryKeyringForTesting replaces the OS keyring seam with an
// in-memory store seeded with entries, and returns the store together with a
// restore function the caller must defer.
func InstallMemoryKeyringForTesting(entries map[KeyringAddress]string) (*MemoryKeyring, func(), error) {
	if !testing.Testing() {
		return nil, nil, ErrKeyringTestSeamUnavailable
	}
	keyring := &MemoryKeyring{
		entries:        map[KeyringAddress]string{},
		commitThenFail: map[KeyringAddress]error{},
		lookupErr:      map[KeyringAddress]error{},
		deleteErr:      map[KeyringAddress]error{},
	}
	maps.Copy(keyring.entries, entries)
	originalLookup, originalWriter, originalDeleter := currentKeyringLookup, currentKeyringWriter, currentKeyringDeleter
	currentKeyringLookup = keyring.lookup
	currentKeyringWriter = keyring.store
	currentKeyringDeleter = keyring.delete
	restore := func() {
		currentKeyringLookup, currentKeyringWriter, currentKeyringDeleter = originalLookup, originalWriter, originalDeleter
	}
	return keyring, restore, nil
}

// FailStoreAfterCommit makes a store at address publish the token and then
// report failure, reproducing a backend whose mutation succeeds while
// something after it -- the collection write-out, the reply, the process exit
// -- does not. This is the case whose on-disk outcome is genuinely ambiguous.
func (k *MemoryKeyring) FailStoreAfterCommit(address KeyringAddress, cause error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.commitThenFail[address] = cause
}

// FailLookup makes a lookup at address report an operational failure, which
// must fail closed rather than fall through to a lower-precedence source.
func (k *MemoryKeyring) FailLookup(address KeyringAddress, cause error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lookupErr[address] = cause
}

// FailDelete makes a delete at address report failure.
func (k *MemoryKeyring) FailDelete(address KeyringAddress, cause error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deleteErr[address] = cause
}

// Entries returns a copy of the store's current contents, so a test asserts
// what is actually held rather than that a seam was called.
func (k *MemoryKeyring) Entries() map[KeyringAddress]string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return maps.Clone(k.entries)
}

// Deletes returns every address a delete was attempted at, in order.
func (k *MemoryKeyring) Deletes() []KeyringAddress {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]KeyringAddress(nil), k.deletes...)
}

func (k *MemoryKeyring) lookup(_ context.Context, service, account string) (string, bool, error) {
	address := KeyringAddress{Service: service, Account: account}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.lookupErr[address]; err != nil {
		return "", false, err
	}
	token, ok := k.entries[address]
	return token, ok, nil
}

func (k *MemoryKeyring) store(_ context.Context, service, account, token string) error {
	address := KeyringAddress{Service: service, Account: account}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.entries[address] = token
	if err := k.commitThenFail[address]; err != nil {
		return err
	}
	return nil
}

func (k *MemoryKeyring) delete(_ context.Context, service, account string) error {
	address := KeyringAddress{Service: service, Account: account}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deletes = append(k.deletes, address)
	if err := k.deleteErr[address]; err != nil {
		return err
	}
	delete(k.entries, address)
	return nil
}
