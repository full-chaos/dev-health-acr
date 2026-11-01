package hosted

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/authprojection"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

type fakeOutboxTicker struct {
	calls  atomic.Int32
	report authprojection.Report
	err    error
	limits chan int
}

func (f *fakeOutboxTicker) Tick(_ context.Context, limit int) (authprojection.Report, error) {
	f.calls.Add(1)
	select {
	case f.limits <- limit:
	default:
	}
	return f.report, f.err
}

func TestTheOutboxPublisherMakesNoCallAtStartAndTicksEveryInterval(t *testing.T) {
	ticker := &fakeOutboxTicker{limits: make(chan int, 8)}
	stop := startCredentialOutboxPublisher(context.Background(), ticker, nil, nil, time.Hour)
	if got := ticker.calls.Load(); got != 0 {
		t.Fatalf("the publisher ticked at start (%d): start must make no call", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	ticker = &fakeOutboxTicker{limits: make(chan int, 8)}
	stop = startCredentialOutboxPublisher(context.Background(), ticker, nil, nil, 10*time.Millisecond)
	select {
	case limit := <-ticker.limits:
		if limit != 100 {
			t.Errorf("batch limit = %d, want 100", limit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no tick")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	calls := ticker.calls.Load()
	time.Sleep(50 * time.Millisecond)
	if ticker.calls.Load() != calls {
		t.Fatal("a tick ran after stop returned")
	}
}

func TestAFailingPublisherTickIsReportedWithTheFixedMessageAndNeverStops(t *testing.T) {
	ticker := &fakeOutboxTicker{limits: make(chan int, 8), err: errors.New("dial tcp 10.0.0.1:5432: password=hunter2")}
	messages := make(chan string, 8)
	stop := startCredentialOutboxPublisher(context.Background(), ticker, nil, func(_ context.Context, message string, _ ...slog.Attr) {
		select {
		case messages <- message:
		default:
		}
	}, 10*time.Millisecond)
	defer func() { _ = stop() }()
	for i := 0; i < 2; i++ {
		select {
		case message := <-messages:
			if message != credentialOutboxPublishFailureMessage || strings.Contains(message, "hunter2") {
				t.Fatalf("message = %q", message)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the failure was not reported")
		}
	}
}

// syncBuffer is a log sink the publisher goroutine writes while the test polls it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestThePublisherLogsCountsWithFixedTextAndNoIdentifierOrSecret(t *testing.T) {
	buffer := &syncBuffer{}
	logger := slog.New(telemetryscrub.Handler(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	ticker := &fakeOutboxTicker{limits: make(chan int, 8), report: authprojection.Report{Applied: 4, Retry: 1, Schema: 2, Conflict: 5, FinalRefused: 6, Held: 3, Stopped: authprojection.OutcomeBusy, Full: true}}
	stop := startCredentialOutboxPublisher(context.Background(), ticker, logger, nil, 10*time.Millisecond)
	<-ticker.limits
	deadline := time.After(5 * time.Second)
	for !strings.Contains(buffer.String(), credentialOutboxPublishFullMessage) {
		select {
		case <-deadline:
			t.Fatalf("lines missing: %s", buffer.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	out := buffer.String()
	for _, want := range []string{"credential outbox publish", "applied=4", "projection_retry=1", "schema=2", "projection_conflict=5", "projection_refused=6", credentialOutboxPublishHeldMessage, "held=3", credentialOutboxPublishStoppedMessage, "stopped_by=busy", "source=publisher"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q: %s", want, out)
		}
	}
	for _, banned := range []string{"Bearer", "token", "cred_", "acr_credential"} {
		if strings.Contains(out, banned) {
			t.Errorf("log carries %q: %s", banned, out)
		}
	}
}

func TestAnIdleTickLogsNothing(t *testing.T) {
	logger, buffer := tickLogger()
	ticker := &fakeOutboxTicker{limits: make(chan int, 8)}
	stop := startCredentialOutboxPublisher(context.Background(), ticker, logger, nil, 10*time.Millisecond)
	<-ticker.limits
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("an idle tick logged: %s", buffer.String())
	}
}

func TestThePublisherIsOffUnlessTheProjectionURLIsSet(t *testing.T) {
	publisher, err := buildCredentialOutboxPublisher(config.Config{}, nil, nil)
	if err != nil || publisher != nil {
		t.Fatalf("publisher = %v, err = %v", publisher, err)
	}
	workloadOnly := config.Config{AuthWorkload: config.AuthWorkloadConfig{ExchangeURL: "https://auth.example.test", Issuer: "https://auth.example.test", SubjectFile: "/nonexistent/subject"}}
	if publisher, err = buildCredentialOutboxPublisher(workloadOnly, nil, nil); err != nil || publisher != nil {
		t.Fatalf("a workload client alone must not start the publisher: %v, %v", publisher, err)
	}
}

func TestTheProjectionURLWithNoWorkloadClientRefusesWithoutEchoingTheValue(t *testing.T) {
	cfg := config.Config{AuthProjection: config.AuthProjectionConfig{URL: "https://secret-host.example.test"}}
	publisher, err := buildCredentialOutboxPublisher(cfg, nil, nil)
	if err == nil || publisher != nil {
		t.Fatalf("publisher = %v, err = %v", publisher, err)
	}
	if strings.Contains(err.Error(), "secret-host") {
		t.Fatalf("the error echoes the value: %v", err)
	}
}

func TestTheProjectionURLWithAWorkloadClientBuildsWithoutANetworkCallOrASubjectRead(t *testing.T) {
	cfg := config.Config{
		AuthWorkload:   config.AuthWorkloadConfig{ExchangeURL: "https://auth.example.test", Issuer: "https://auth.example.test", SubjectFile: "/nonexistent/subject"},
		AuthProjection: config.AuthProjectionConfig{URL: "https://auth.example.test"},
	}
	publisher, err := buildCredentialOutboxPublisher(cfg, nil, nil)
	if err != nil || publisher == nil {
		t.Fatalf("publisher = %v, err = %v", publisher, err)
	}
}
