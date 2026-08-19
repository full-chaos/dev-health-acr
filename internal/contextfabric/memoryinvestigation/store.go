// Package memoryinvestigation is the in-memory test/dev twin of
// contextfabric.InvestigationResultStore (mirrors internal/storage's
// memory-vs-postgres split, internal/storage/AGENTS.md). It enforces the
// same org-scoping and immutability semantics as
// internal/contextfabric/pginvestigation.Store so behavior cannot silently
// drift between the two implementations.
package memoryinvestigation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ErrNotFound identifies a Get that found no row for the requested
// (org_id, result_id). It fires identically whether result_id is genuinely
// unknown or belongs to a different organization, matching
// pginvestigation.ErrNotFound's non-enumerating-404 behavior.
//
// It wraps contextfabric.ErrInvestigationResultNotFound (CHAOS-3746) for
// the same reason pginvestigation.ErrNotFound does: a caller holding the
// interface classifies not-found through the port, not through an
// adapter. errors.Is against either sentinel still matches.
var ErrNotFound = fmt.Errorf("memoryinvestigation: investigation result not found: %w", contextfabric.ErrInvestigationResultNotFound)

// entry holds an immutable, already-serialized snapshot. Storing the JSON
// form (rather than the Go struct) keeps Save's idempotent-replay
// comparison and Get's defensive copy on the same simple code path: encode
// once, decode fresh on every read.
type entry struct {
	orgID   string
	payload []byte
}

// Store is a mutex-protected, map-backed contextfabric.InvestigationResultStore.
type Store struct {
	mu      sync.Mutex
	results map[string]entry
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{results: make(map[string]entry)}
}

// Save persists an immutable InvestigationResult snapshot. It never
// overwrites an existing entry: an identical replay under the same
// result_id succeeds idempotently, a divergent one errors.
//
// reuseSnapshot, reuseEpoch, the CHAOS-3833 retrieval identity, the
// CHAOS-3862 prompt versions, the CHAOS-3862 round-2 version authorities,
// and the CHAOS-3898 graph epoch are accepted to satisfy
// contextfabric.InvestigationResultStore but otherwise ignored: this
// test/dev store does not implement CHAOS-3782 answer reuse
// (contextfabric.AnswerReuseGate), so there is no reuse-key bookkeeping to
// populate; Get correspondingly always returns a nil GraphEpoch on its
// StoredInvestigationResult carrier.
func (s *Store) Save(ctx context.Context, principal storage.Principal, result contextfabric.InvestigationResult, reuseSnapshot contextfabric.SourceWatermarkSnapshot, reuseEpoch contextfabric.RebuildEpoch, timeAxisKey string, _ contextfabric.ReuseRetrievalIdentity, _ contextfabric.ReusePromptVersions, _ contextfabric.ReuseVersionAuthorities, _ int64) error {
	if s == nil {
		return errors.New("memoryinvestigation: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	resultID := strings.TrimSpace(result.ResultID)
	if orgID == "" || resultID == "" {
		return errors.New("memoryinvestigation: organization and result id are required")
	}
	// M2 (Codex adversarial review, CHAOS-3755): reject a semantically
	// invalid result before it is ever persisted -- an immutable row that
	// fails the same contract the public API enforces on every returned
	// result can never be corrected later.
	if err := result.Validate(); err != nil {
		return fmt.Errorf("memoryinvestigation: invalid investigation result: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("memoryinvestigation: marshal investigation result: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, found := s.results[resultID]; found {
		// P2 (Codex delta review, CHAOS-3755): the EXISTING stored row may
		// have reached storage some other way (see the matching comment
		// in Get) and could carry an explicit null where the schema only
		// ever allows an omitted or real-array value. Reject that before
		// trusting it as a valid idempotent-replay target.
		if err := rejectExplicitNullDegradedReasons(existing.payload); err != nil {
			return fmt.Errorf("memoryinvestigation: stored investigation result %q is invalid: %w", resultID, err)
		}
		// M1 (Codex adversarial review, CHAOS-3755): the conflict check
		// is org-scoped FIRST, independent of content equality.
		// InvestigationResult carries no organization discriminator of
		// its own, so a byte-identical replay from a DIFFERENT org would
		// otherwise pass the content-equality check below and be treated
		// as a successful idempotent replay, while the row still belongs
		// to whichever org wrote it first.
		if existing.orgID != orgID {
			return fmt.Errorf("memoryinvestigation: investigation result %q already exists under a different organization", resultID)
		}
		if bytes.Equal(existing.payload, payload) {
			return nil
		}
		return fmt.Errorf("memoryinvestigation: investigation result %q already exists with different content", resultID)
	}
	s.results[resultID] = entry{orgID: orgID, payload: payload}
	return nil
}

// Get returns the InvestigationResult for resultID, scoped to
// principal.OrgID. A result stored under a different organization is
// reported identically to an unknown result_id (ErrNotFound); this
// package never allows result_id alone to satisfy a lookup.
func (s *Store) Get(ctx context.Context, principal storage.Principal, resultID string) (contextfabric.StoredInvestigationResult, error) {
	if s == nil {
		return contextfabric.StoredInvestigationResult{}, errors.New("memoryinvestigation: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.StoredInvestigationResult{}, err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	resultID = strings.TrimSpace(resultID)
	if orgID == "" || resultID == "" {
		return contextfabric.StoredInvestigationResult{}, ErrNotFound
	}

	s.mu.Lock()
	stored, found := s.results[resultID]
	s.mu.Unlock()
	if !found || stored.orgID != orgID {
		return contextfabric.StoredInvestigationResult{}, ErrNotFound
	}

	// P2 (Codex delta review, CHAOS-3755): an explicit `"degraded_reasons":
	// null` collapses to the identical Go nil slice an OMITTED field would
	// decode to, so Validate()'s relaxed nil-check (correct for the
	// omitted case) cannot tell them apart after decoding into the
	// struct. The JSON Schema only allows degraded_reasons to be an array
	// WHEN PRESENT -- never null -- so this must be caught on the raw
	// bytes, before or independent of the struct decode.
	if err := rejectExplicitNullDegradedReasons(stored.payload); err != nil {
		return contextfabric.StoredInvestigationResult{}, fmt.Errorf("memoryinvestigation: stored investigation result is invalid: %w", err)
	}
	var result contextfabric.InvestigationResult
	if err := json.Unmarshal(stored.payload, &result); err != nil {
		return contextfabric.StoredInvestigationResult{}, fmt.Errorf("memoryinvestigation: decode investigation result: %w", err)
	}
	// M2 (Codex adversarial review, CHAOS-3755): validate on read too, not
	// just on write. Save already rejects an invalid result before it is
	// stored, but Get defends independently against any row that reached
	// storage some other way (e.g. written directly, or by a future/older
	// binary with different validation) -- a caller must never receive a
	// result this package cannot vouch for.
	if err := result.ValidateStored(); err != nil {
		return contextfabric.StoredInvestigationResult{}, fmt.Errorf("memoryinvestigation: stored investigation result is invalid: %w", err)
	}
	// CHAOS-3898 §2.4: this store never persists a graph epoch (it does not
	// implement answer reuse at all), so GraphEpoch is always nil -- every
	// consumer (starting with the §2.2 ingress taint gate) must already
	// treat that as "cannot prove", never a silent pass.
	return contextfabric.StoredInvestigationResult{Result: result}, nil
}

// rejectExplicitNullDegradedReasons reports whether payload contains a
// literal `"coverage":{"degraded_reasons":null,...}` (P2, Codex delta
// review, CHAOS-3755). coverage.degraded_reasons is `omitempty` in Go and
// not in the Coverage schema's required set, so "absent" is the only
// schema-conformant way to skip it -- explicit null is not a valid array
// and violates the schema even though, once decoded into
// ContextFabricCoverage.DegradedReasons ([]string), it is indistinguishable
// from the omitted case (both become a nil slice). This check runs on the
// raw bytes specifically because that distinction stops existing the
// moment json.Unmarshal returns.
func rejectExplicitNullDegradedReasons(payload []byte) error {
	var probe struct {
		Coverage struct {
			DegradedReasons json.RawMessage `json:"degraded_reasons"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return fmt.Errorf("decode for explicit-null check: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(probe.Coverage.DegradedReasons), []byte("null")) {
		return errors.New("coverage.degraded_reasons must be omitted or an array, not explicit null")
	}
	return nil
}
