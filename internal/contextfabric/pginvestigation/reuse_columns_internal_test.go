package pginvestigation

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	contextfabric "github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// noDialDriver is a database/sql driver that is registered but never
// actually dials -- Open is only invoked lazily on the FIRST real query,
// and this file's tests call reuseColumnsFor directly (a pure function),
// never a *Store method that issues SQL. This lets the test build a real
// *Store (reuseColumnsFor reads s.reuseEnabled, an unexported field, so
// this test must live in package pginvestigation, not _test) without a
// live Postgres connection -- keeping it package-local/fake-based per the
// gate discipline for this lane (no testcontainers).
type noDialDriver struct{}

func (noDialDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("noDialDriver: this test never issues a real query")
}

func init() {
	sql.Register("pginvestigation_reuse_columns_test_driver", noDialDriver{})
}

func newNoDialStore(t *testing.T, opts ...StoreOption) *Store {
	t.Helper()
	db, err := sql.Open("pginvestigation_reuse_columns_test_driver", "test")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db, opts...)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

// TestReuseColumnsFor_PunctuationOnlyQuestionNeverPopulatesReuseColumns is
// the store-side probe for Codex round-2 finding #4, mirroring
// TestPunctuationOnlyQuestionNeverAttemptsReuseLookup's engine-side probe
// in package contextfabric. A punctuation-only question canonicalizes to
// "" -- every such question would collide on ONE hash if this row were
// ever allowed to become a reuse candidate. reuseColumnsFor must return
// all-NULL/nil, exactly as if answer reuse were disabled for this save,
// so the row can never be matched by FindReusable.
func TestReuseColumnsFor_PunctuationOnlyQuestionNeverPopulatesReuseColumns(t *testing.T) {
	t.Parallel()
	store := newNoDialStore(t, WithAnswerReuse(time.Hour))

	result := contextfabric.InvestigationResult{
		Question: "?!?",
		Versions: contextfabric.VersionSet{
			ContractVersion:   "contract-v1",
			ProjectionVersion: "projection-v1",
			ModelIdentity:     "test/model-v1",
		},
	}
	snapshot := contextfabric.SourceWatermarkSnapshot{"source-a": "watermark-1"}
	epoch := int64(3)

	questionHash, contractVersion, projectionVersion, modelIdentity, sourceWatermarks, invalidationEpoch := store.reuseColumnsFor(result, snapshot, &epoch)

	if questionHash.Valid || contractVersion.Valid || projectionVersion.Valid || modelIdentity.Valid || sourceWatermarks != nil || invalidationEpoch.Valid {
		t.Fatalf("reuseColumnsFor(punctuation-only question) = (%+v, %+v, %+v, %+v, %v, %+v), want all-NULL/nil",
			questionHash, contractVersion, projectionVersion, modelIdentity, sourceWatermarks, invalidationEpoch)
	}
}

// TestReuseColumnsFor_OrdinaryQuestionPopulatesReuseColumns is the control
// case: with the same Store and snapshot, an ordinary (non-punctuation-only)
// question DOES populate every reuse column, proving the punctuation-only
// guard above is actually a special case and not just WithAnswerReuse
// being off or the snapshot being nil.
func TestReuseColumnsFor_OrdinaryQuestionPopulatesReuseColumns(t *testing.T) {
	t.Parallel()
	store := newNoDialStore(t, WithAnswerReuse(time.Hour))

	result := contextfabric.InvestigationResult{
		Question: "What is the status of Ask Dev?",
		Versions: contextfabric.VersionSet{
			ContractVersion:   "contract-v1",
			ProjectionVersion: "projection-v1",
			ModelIdentity:     "test/model-v1",
		},
	}
	snapshot := contextfabric.SourceWatermarkSnapshot{"source-a": "watermark-1"}
	epoch := int64(3)

	questionHash, contractVersion, projectionVersion, modelIdentity, sourceWatermarks, invalidationEpoch := store.reuseColumnsFor(result, snapshot, &epoch)

	if !questionHash.Valid || !contractVersion.Valid || !projectionVersion.Valid || !modelIdentity.Valid || sourceWatermarks == nil || !invalidationEpoch.Valid {
		t.Fatalf("reuseColumnsFor(ordinary question) = (%+v, %+v, %+v, %+v, %v, %+v), want all populated",
			questionHash, contractVersion, projectionVersion, modelIdentity, sourceWatermarks, invalidationEpoch)
	}
	if want := contextfabric.QuestionHash(result.Question); questionHash.String != want {
		t.Errorf("questionHash = %q, want %q", questionHash.String, want)
	}
	if invalidationEpoch.Int64 != epoch {
		t.Errorf("invalidationEpoch = %d, want %d", invalidationEpoch.Int64, epoch)
	}
}

// TestReuseColumnsFor_NilEpochNeverPopulatesInvalidationEpoch is the
// Codex round-2 finding #7 store-side probe: a nil reuseEpoch (the
// Engine-side epoch snapshot was never captured, independent of whether
// the watermark snapshot succeeded) must leave invalidation_epoch NULL,
// exactly like a nil watermark snapshot leaves every other reuse column
// NULL -- FindReusable's invalidation_epoch IS NOT NULL guard is what
// turns this into "never reusable."
func TestReuseColumnsFor_NilEpochNeverPopulatesInvalidationEpoch(t *testing.T) {
	t.Parallel()
	store := newNoDialStore(t, WithAnswerReuse(time.Hour))

	result := contextfabric.InvestigationResult{
		Question: "What is the status of Ask Dev?",
		Versions: contextfabric.VersionSet{
			ContractVersion:   "contract-v1",
			ProjectionVersion: "projection-v1",
			ModelIdentity:     "test/model-v1",
		},
	}
	snapshot := contextfabric.SourceWatermarkSnapshot{"source-a": "watermark-1"}

	questionHash, _, _, _, sourceWatermarks, invalidationEpoch := store.reuseColumnsFor(result, snapshot, nil)

	if !questionHash.Valid || sourceWatermarks == nil {
		t.Fatalf("reuseColumnsFor(nil epoch) left the watermark-derived columns unpopulated too: questionHash=%+v sourceWatermarks=%v", questionHash, sourceWatermarks)
	}
	if invalidationEpoch.Valid {
		t.Fatalf("invalidationEpoch = %+v, want NULL when reuseEpoch is nil", invalidationEpoch)
	}
}
