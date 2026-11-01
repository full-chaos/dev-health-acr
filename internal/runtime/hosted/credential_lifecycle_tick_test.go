package hosted

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

type fakeLifecycleTicker struct {
	result storagepostgres.LifecycleTickResult
	err    error
	limits []int
	nows   []time.Time
}

func (f *fakeLifecycleTicker) Tick(_ context.Context, now time.Time, limit int) (storagepostgres.LifecycleTickResult, error) {
	f.limits = append(f.limits, limit)
	f.nows = append(f.nows, now)
	return f.result, f.err
}

func tickLogger() (*slog.Logger, *bytes.Buffer) {
	var buffer bytes.Buffer
	return slog.New(telemetryscrub.Handler(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))), &buffer
}

func TestTheLifecycleTickReportsCountsInTheAckSweepFormAndAFixedLineOnAFullBatch(t *testing.T) {
	logger, buffer := tickLogger()
	ticker := &fakeLifecycleTicker{result: storagepostgres.LifecycleTickResult{Expired: 3, FirstTouch: 2}}
	stop, err := startCredentialLifecycleTick(context.Background(), ticker, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	out := buffer.String()
	if !strings.Contains(out, "credential lifecycle tick") || !strings.Contains(out, "expired=3") || !strings.Contains(out, "first_touch=2") || !strings.Contains(out, "source=tick") {
		t.Errorf("counts line missing: %s", out)
	}
	if strings.Contains(out, credentialLifecycleTickFullMessage) {
		t.Errorf("a batch under the limit logs no full-batch line: %s", out)
	}
	if len(ticker.limits) == 0 || ticker.limits[0] != defaultCredentialLifecycleTickBatchLimit {
		t.Errorf("limits = %v, want the batch limit %d", ticker.limits, defaultCredentialLifecycleTickBatchLimit)
	}

	logger, buffer = tickLogger()
	full := &fakeLifecycleTicker{result: storagepostgres.LifecycleTickResult{Expired: defaultCredentialLifecycleTickBatchLimit, Full: true}}
	stop, err = startCredentialLifecycleTick(context.Background(), full, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = stop()
	if !strings.Contains(buffer.String(), credentialLifecycleTickFullMessage) {
		t.Errorf("a full batch must log the fixed line: %s", buffer.String())
	}

	logger, buffer = tickLogger()
	idle := &fakeLifecycleTicker{}
	stop, err = startCredentialLifecycleTick(context.Background(), idle, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = stop()
	if buffer.Len() != 0 {
		t.Errorf("a tick that did nothing logs nothing: %s", buffer.String())
	}
}

func TestTheLifecycleTickInitialFailureFailsStartup(t *testing.T) {
	logger, _ := tickLogger()
	boom := errors.New("grant missing")
	if _, err := startCredentialLifecycleTick(context.Background(), &fakeLifecycleTicker{err: boom}, logger, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v: the first tick proves the privileges and fails startup", err)
	}
}

func TestTheLifecycleTickNamesEverySkippedCredentialOnEveryTick(t *testing.T) {
	logger, buffer := tickLogger()
	ticker := &fakeLifecycleTicker{result: storagepostgres.LifecycleTickResult{Expired: 1, Skipped: 2, SkippedIDs: []string{"cred_bad_one", "cred_bad_two"}}}
	stop, err := startCredentialLifecycleTick(context.Background(), ticker, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = stop()
	out := buffer.String()
	for _, want := range []string{credentialLifecycleTickSkippedMessage, "aggregate_id=cred_bad_one", "aggregate_id=cred_bad_two", "skipped=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}

	logger, buffer = tickLogger()
	onlySkipped := &fakeLifecycleTicker{result: storagepostgres.LifecycleTickResult{Skipped: 1, SkippedIDs: []string{"cred_bad_one"}}}
	stop, err = startCredentialLifecycleTick(context.Background(), onlySkipped, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = stop()
	if !strings.Contains(buffer.String(), "aggregate_id=cred_bad_one") {
		t.Errorf("a tick that only skipped still names the credential: %s", buffer.String())
	}
}
