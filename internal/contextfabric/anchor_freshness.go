package contextfabric

// The freshness of a proven anchor: what could make the remembered SUBJECT
// wrong, and nothing else.
//
// SCOPE. An anchor binding holds an identity. Three things can make that
// identity the wrong one to carry: the node is gone or renamed under another
// id, the caller may no longer read it, or what it owns changed. The first
// two are answered by a live keyed re-read and re-authorization when a carry
// is admitted, never by a watermark. The third rides exactly one projection
// source: AnchorOwnershipSource emits the team, project, ownership and
// membership edges and nothing else. The other sources project entity rows,
// pull request and issue edges and episodes; facts are read live on every
// serve and never projected. A watermark over those would expire a carried
// anchor whenever any row in the organization moved, so they are left out.
//
// ACQUISITION. The engine reads the freshness once per turn, before the first
// graph read that can prove an anchor: before the window gate, so the
// offers-only resolution a gate discards is covered, and before the decisive
// resolution. It reads it whether or not answer reuse is enabled, and only
// while the shadow binding runs.
//
// UNMOVED IS NOT FRESH. Two equal watermarks mean ownership did not move
// between the two reads. On a projection that stopped advancing they stay
// equal forever, so the comparison is named for movement and never for
// freshness.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// AnchorOwnershipSource is the projection source whose checkpoint watermark a
// binding's freshness records. It names the source that projects ownership.
const AnchorOwnershipSource = "dev_health_teams_projects"

// AnchorFreshness is the ownership state an anchor proof stood on: the
// serving graph epoch the watermark was read at, and the ownership source's
// checkpoint watermark there. Recorded is false on the zero value, which
// means no freshness was acquired; an acquired one is recorded even when its
// epoch is 0 and its watermark empty. A value type, so two bindings compare
// equal exactly when their freshness does.
type AnchorFreshness struct {
	Recorded           bool   `json:"recorded"`
	ServingEpoch       int64  `json:"serving_epoch"`
	OwnershipWatermark string `json:"ownership_watermark"`
}

// AnchorFreshnessRead is one read of the ownership source. Recorded is false
// when the source holds no checkpoint at the serving epoch.
type AnchorFreshnessRead struct {
	ServingEpoch       int64
	OwnershipWatermark string
	Recorded           bool
}

// AnchorFreshnessReader reads the ownership source's checkpoint at the
// organization's serving epoch, in one statement so the epoch and the
// watermark describe the same moment.
type AnchorFreshnessReader interface {
	ReadAnchorFreshness(ctx context.Context, orgID string) (AnchorFreshnessRead, error)
}

// AnchorFreshnessAcquisition is the closed outcome of this turn's read.
type AnchorFreshnessAcquisition string

const (
	// AnchorFreshnessNotAttempted: the turn ended before the acquisition
	// point, or served a stored row.
	AnchorFreshnessNotAttempted AnchorFreshnessAcquisition = "not_attempted"
	// AnchorFreshnessAcquired: the ownership watermark was read at the
	// turn's own graph epoch.
	AnchorFreshnessAcquired AnchorFreshnessAcquisition = "acquired"
	// AnchorFreshnessUnwired: no reader is configured.
	AnchorFreshnessUnwired AnchorFreshnessAcquisition = "unwired"
	// AnchorFreshnessReadFailed: the read returned an error.
	AnchorFreshnessReadFailed AnchorFreshnessAcquisition = "read_failed"
	// AnchorFreshnessSourceAbsent: the ownership source holds no checkpoint
	// at the serving epoch, so ownership has no recorded state.
	AnchorFreshnessSourceAbsent AnchorFreshnessAcquisition = "source_absent"
	// AnchorFreshnessEpochMismatch: the read's serving epoch is not the
	// epoch this turn's graph reads use, so the watermark describes another
	// graph.
	AnchorFreshnessEpochMismatch AnchorFreshnessAcquisition = "epoch_mismatch"
	// AnchorFreshnessUnstorable: the watermark exceeds the snapshot's term
	// bound, so a binding carrying it would not validate.
	AnchorFreshnessUnstorable AnchorFreshnessAcquisition = "unstorable"
)

func anchorFreshnessAcquisitions() []AnchorFreshnessAcquisition {
	return []AnchorFreshnessAcquisition{AnchorFreshnessNotAttempted, AnchorFreshnessAcquired, AnchorFreshnessUnwired, AnchorFreshnessReadFailed, AnchorFreshnessSourceAbsent, AnchorFreshnessEpochMismatch, AnchorFreshnessUnstorable}
}

// AnchorOwnershipMovement compares the held binding's proven freshness with
// this turn's.
type AnchorOwnershipMovement string

const (
	// AnchorOwnershipNotApplicable: the turn started from no held binding.
	AnchorOwnershipNotApplicable AnchorOwnershipMovement = "not_applicable"
	// AnchorOwnershipProvenUnrecorded: the held binding carries no proven
	// freshness to compare.
	AnchorOwnershipProvenUnrecorded AnchorOwnershipMovement = "proven_unrecorded"
	// AnchorOwnershipNotAcquired: this turn holds no freshness to compare.
	AnchorOwnershipNotAcquired AnchorOwnershipMovement = "not_acquired"
	// AnchorOwnershipUnmoved: the same epoch and the same watermark. Not a
	// statement that the data is fresh.
	AnchorOwnershipUnmoved AnchorOwnershipMovement = "unmoved"
	// AnchorOwnershipMoved: the epoch or the watermark differs.
	AnchorOwnershipMoved AnchorOwnershipMovement = "moved"
)

func anchorOwnershipMovements() []AnchorOwnershipMovement {
	return []AnchorOwnershipMovement{AnchorOwnershipNotApplicable, AnchorOwnershipProvenUnrecorded, AnchorOwnershipNotAcquired, AnchorOwnershipUnmoved, AnchorOwnershipMoved}
}

// anchorOwnershipMovement compares from's proven freshness with now.
func anchorOwnershipMovement(from AnchorBinding, now AnchorFreshness) AnchorOwnershipMovement {
	switch {
	case !from.active():
		return AnchorOwnershipNotApplicable
	case !from.ProvenFreshness.Recorded:
		return AnchorOwnershipProvenUnrecorded
	case !now.Recorded:
		return AnchorOwnershipNotAcquired
	case from.ProvenFreshness == now:
		return AnchorOwnershipUnmoved
	default:
		return AnchorOwnershipMoved
	}
}

// validateAnchorFreshness bounds a persisted freshness.
func validateAnchorFreshness(f AnchorFreshness) error {
	if !f.Recorded {
		if f != (AnchorFreshness{}) {
			return fmt.Errorf("%w: an unrecorded proven_freshness carries an epoch or a watermark", errAnchorBindingInvalid)
		}
		return nil
	}
	if f.ServingEpoch < 0 {
		return fmt.Errorf("%w: proven_freshness serving_epoch %d is negative", errAnchorBindingInvalid, f.ServingEpoch)
	}
	if len(f.OwnershipWatermark) > SemanticStateMaxTermBytes {
		return fmt.Errorf("%w: proven_freshness ownership_watermark is %d bytes, exceeds %d", errAnchorBindingInvalid, len(f.OwnershipWatermark), SemanticStateMaxTermBytes)
	}
	return nil
}

// acquireAnchorFreshness reads the ownership freshness for the tracker. A nil
// tracker is the shadow switched off: nothing is read.
func (e *Engine) acquireAnchorFreshness(ctx context.Context, orgID string, t *anchorBindingTracker) {
	if t == nil {
		return
	}
	if e.anchorFreshness == nil {
		t.freshnessAcquisition = AnchorFreshnessUnwired
		return
	}
	read, err := e.anchorFreshness.ReadAnchorFreshness(ctx, orgID)
	switch {
	case err != nil:
		t.freshnessAcquisition = AnchorFreshnessReadFailed
	case read.ServingEpoch != t.epoch:
		t.freshnessAcquisition = AnchorFreshnessEpochMismatch
	case !read.Recorded:
		t.freshnessAcquisition = AnchorFreshnessSourceAbsent
	case len(read.OwnershipWatermark) > SemanticStateMaxTermBytes:
		t.freshnessAcquisition = AnchorFreshnessUnstorable
	default:
		t.freshnessAcquisition = AnchorFreshnessAcquired
		t.freshness = AnchorFreshness{Recorded: true, ServingEpoch: read.ServingEpoch, OwnershipWatermark: read.OwnershipWatermark}
	}
}

// anchorFreshnessEpoch is a freshness's serving epoch for the line, -1 when
// there is none.
func anchorFreshnessEpoch(f AnchorFreshness) int64 {
	if !f.Recorded {
		return -1
	}
	return f.ServingEpoch
}

// anchorFreshnessDigest is the line's stand-in for a watermark: the first 16
// hex digits of its SHA-256, empty when there is none. A checkpoint cursor is
// never written to a log line, and two lines compare equal exactly when the
// watermarks they stand for do.
func anchorFreshnessDigest(f AnchorFreshness) string {
	if !f.Recorded {
		return ""
	}
	sum := sha256.Sum256([]byte(f.OwnershipWatermark))
	return hex.EncodeToString(sum[:8])
}
