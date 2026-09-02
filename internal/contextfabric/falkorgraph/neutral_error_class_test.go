package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// CHAOS-4874 family A. Every FalkorDB failure used to reach
// projectionrun.classifyOutcomeError as failure_class=unclassified, because
// that classifier matches ONLY contextfabric.Err*/context.* and this package's
// sentinels are its own. These tests drive REAL adapter classification output
// (classifyFalkorError / safeDependencyError, never a hand-built error shaped
// like theirs) into projectionrun's own exported classifier, so what is
// measured is the actual seam an operator reads.
//
// neutralUniverse is every neutral class this package may attach. Tests below
// quantify over it in BOTH directions: a sentinel must satisfy the one class
// neutralClass names for it, and must satisfy NONE of the others -- an
// assertion in one direction alone would pass a sentinel wrapped in two
// classes, or in the wrong one out of a compound group.
var neutralUniverse = []error{
	contextfabric.ErrUnavailable,
	contextfabric.ErrInvalidResult,
	contextfabric.ErrRateLimited,
}

// falkorOrigin is one real FalkorDB response text, the sentinel this package
// classifies it into, and the class projectionrun must then log. rawMessage is
// the driver's own text, so classifyFalkorError does the work under test.
type falkorOrigin struct {
	name       string
	rawMessage string
	sentinel   error
	wantClass  string
}

// falkorOrigins covers every arm of classifyFalkorError including its
// residual default -- the arm that eats a connection refused, a
// mid-handshake EOF and a TLS alert, and the one that carried no sentinel at
// all before CHAOS-4874.
var falkorOrigins = []falkorOrigin{
	{
		name:       "unique constraint violation is a rejected write, not an outage",
		rawMessage: "unique constraint violation on node of type Subject",
		sentinel:   ErrConstraintViolation,
		wantClass:  "invalid_result",
	},
	{
		name:       "already indexed",
		rawMessage: "Attribute 'embedding' is already indexed",
		sentinel:   errAlreadyExists,
		wantClass:  "invalid_result",
	},
	{
		name:       "no such index",
		rawMessage: "Unable to drop index on :Subject(embedding): no such index",
		sentinel:   errIndexNotFound,
		wantClass:  "invalid_result",
	},
	{
		name:       "WRONGPASS is ACR's own credential, reported as a dependency outage",
		rawMessage: "WRONGPASS invalid username-password pair or user is disabled.",
		sentinel:   ErrUnauthorized,
		wantClass:  "dependency_unavailable",
	},
	{
		name:       "NOAUTH",
		rawMessage: "NOAUTH Authentication required.",
		sentinel:   ErrUnauthorized,
		wantClass:  "dependency_unavailable",
	},
	{
		name:       "query timed out keeps its pre-existing cancellation class",
		rawMessage: "Query timed out",
		sentinel:   context.DeadlineExceeded,
		wantClass:  "canceled",
	},
	{
		name:       "an unrecognised driver error is a dependency outage, not a vocabulary gap",
		rawMessage: "dial tcp 10.0.0.4:6379: connect: connection refused",
		sentinel:   nil, // the residual: no package sentinel, only the neutral class
		wantClass:  "dependency_unavailable",
	},
	{
		name:       "a TLS alert reaches the same residual",
		rawMessage: "remote error: tls: handshake failure",
		sentinel:   nil,
		wantClass:  "dependency_unavailable",
	},
}

// TestFalkorErrorOriginsClassifyForProjectionRun is the red-first test: on
// origin/main every row below returns "unclassified" except the timeout.
func TestFalkorErrorOriginsClassifyForProjectionRun(t *testing.T) {
	if len(falkorOrigins) == 0 {
		t.Fatal("falkorOrigins is empty; this test would be vacuous")
	}
	reached := 0
	for _, origin := range falkorOrigins {
		t.Run(origin.name, func(t *testing.T) {
			classified := classifyFalkorError("apply projection batch", errors.New(origin.rawMessage))
			if classified == nil {
				t.Fatal("classifyFalkorError returned nil for a non-nil error")
			}
			if origin.sentinel != nil && !errors.Is(classified, origin.sentinel) {
				t.Fatalf("classified error does not satisfy %v: %v", origin.sentinel, classified)
			}
			if got := projectionrun.ClassifyFailure(classified); got != origin.wantClass {
				t.Fatalf("projectionrun.ClassifyFailure(%v) = %q, want %q", classified, got, origin.wantClass)
			}
			reached++
		})
	}
	if reached != len(falkorOrigins) {
		t.Fatalf("assertion reach: %d of %d origins reached their assertions", reached, len(falkorOrigins))
	}
}

// TestSecondPassThroughSafeDependencyErrorPreservesTheClass pins the defeat a
// declaration-site wrap exists to prevent. safeDependencyError is documented
// as commonly running a SECOND time over an already-classified error, and it
// REBUILDS that error from the bare sentinel. A neutral class attached in
// classifyFalkorError's arms instead of on the sentinel is erased right here.
func TestSecondPassThroughSafeDependencyErrorPreservesTheClass(t *testing.T) {
	reached := 0
	for _, origin := range falkorOrigins {
		t.Run(origin.name, func(t *testing.T) {
			once := classifyFalkorError("apply projection batch", errors.New(origin.rawMessage))
			twice := safeDependencyError("write watermark", once)
			if got := projectionrun.ClassifyFailure(twice); got != origin.wantClass {
				t.Fatalf("after a second safeDependencyError pass the class became %q, want %q: %v",
					got, origin.wantClass, twice)
			}
			reached++
		})
	}
	if reached != len(falkorOrigins) {
		t.Fatalf("assertion reach: %d of %d origins reached their assertions", reached, len(falkorOrigins))
	}
}

// TestClassifiedErrorsNeverCarryDriverText is the content-safety guarantee
// this change had to keep: only sentinels are wrapped, never the dependency's
// own message. The marker is embedded in every raw message so a leak on ANY
// arm fails, including the residual.
func TestClassifiedErrorsNeverCarryDriverText(t *testing.T) {
	const marker = "s3cret-host.internal:6379/tenant-42"
	reached := 0
	for _, origin := range falkorOrigins {
		t.Run(origin.name, func(t *testing.T) {
			raw := errors.New(origin.rawMessage + " " + marker)
			once := classifyFalkorError("apply projection batch", raw)
			twice := safeDependencyError("write watermark", once)
			for _, got := range []error{once, twice} {
				if strings.Contains(got.Error(), marker) {
					t.Fatalf("dependency text leaked into %q", got.Error())
				}
			}
			reached++
		})
	}
	if reached != len(falkorOrigins) {
		t.Fatalf("assertion reach: %d of %d origins reached their assertions", reached, len(falkorOrigins))
	}
}

// TestConfirmedAbsenceIsNotADependencyOutage pins the one deliberate nil in
// neutralClass. ErrNotFound must keep resolving to the two neutral classes
// that say "absent", and must NOT become ErrUnavailable -- that would make a
// never-projected organization, which Engine degrades to a clean empty
// answer, classify as an outage and answer 503.
func TestConfirmedAbsenceIsNotADependencyOutage(t *testing.T) {
	classified := classifyFalkorError("read watermark", errors.New("Invalid graph operation on empty key"))
	if !errors.Is(classified, ErrNotFound) {
		t.Fatalf("empty-key error no longer classifies as ErrNotFound: %v", classified)
	}
	for _, forbidden := range neutralUniverse {
		if errors.Is(classified, forbidden) {
			t.Fatalf("a confirmed absence must not satisfy %v: %v", forbidden, classified)
		}
	}
	// The two translations that DO carry its neutral meaning still hold.
	if !errors.Is(notFoundWatermarkErr(), contextfabric.ErrProjectionWatermarkNotFound) {
		t.Fatal("notFoundWatermarkErr no longer satisfies ErrProjectionWatermarkNotFound")
	}
	if !errors.Is(graphNotProjectedError(classified), contextfabric.ErrGraphNotProjected) {
		t.Fatal("graphNotProjectedError no longer satisfies ErrGraphNotProjected")
	}
}

// TestRateLimitedSentinelReachesTheRateLimitedClass covers the one sentinel
// with no producer in this package (see its declaration): the MAPPING is what
// is under test, so the sentinel is supplied the way a caller would supply it
// and driven through the same safeDependencyError seam.
func TestRateLimitedSentinelReachesTheRateLimitedClass(t *testing.T) {
	supplied := fmt.Errorf("resolve subjects: %w", ErrRateLimited)
	if got := projectionrun.ClassifyFailure(supplied); got != "dependency_rate_limited" {
		t.Fatalf("ClassifyFailure = %q, want %q", got, "dependency_rate_limited")
	}
	if got := projectionrun.ClassifyFailure(safeDependencyError("resolve subjects", supplied)); got != "dependency_rate_limited" {
		t.Fatalf("after safeDependencyError ClassifyFailure = %q, want %q", got, "dependency_rate_limited")
	}
}

// TestVectorIndexNotReadyClassifiesAsRetryable covers the round-1 finding. The
// vector index existing but not yet OPERATIONAL is a REPLAYABLE condition --
// the batch fails, the checkpoint holds, the next tick retries once the index
// settles (see the sentinel's own declaration). It is returned straight from a
// projection batch, so it reaches projectionrun.classifyOutcomeError directly,
// and before this it logged failure_class=unclassified: an operator watching an
// org stall during a vector-index build was told the vocabulary was missing,
// not that the backend was still catching up.
func TestVectorIndexNotReadyClassifiesAsRetryable(t *testing.T) {
	// Both real construction sites: the bare return and the wrapped one.
	cases := map[string]error{
		"bare":    errVectorIndexNotReady,
		"wrapped": fmt.Errorf("%w: vector index did not become operational for key %q", errVectorIndexNotReady, "acr-cf-org"),
	}
	reached := 0
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			// The batch path wraps once more before the coordinator sees it.
			asTickSees := fmt.Errorf("apply projection batch: %w", err)
			if !errors.Is(asTickSees, errVectorIndexNotReady) {
				t.Fatal("the sentinel no longer survives its own wrapping; the case is vacuous")
			}
			if got := projectionrun.ClassifyFailure(asTickSees); got != "dependency_unavailable" {
				t.Fatalf("ClassifyFailure = %q, want %q", got, "dependency_unavailable")
			}
			reached++
		})
	}
	if reached != len(cases) {
		t.Fatalf("assertion reach: %d of %d cases reached their assertions", reached, len(cases))
	}
}
