package authprojection

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type truncatedBody struct {
	prefix string
	read   bool
}

func (b *truncatedBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.ErrUnexpectedEOF
	}
	b.read = true
	return copy(p, b.prefix), nil
}

func (b *truncatedBody) Close() error { return nil }

type answerTransport func(*http.Request) (*http.Response, error)

func (f answerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func truncatedClient(t *testing.T, status int, body string) *Client {
	t.Helper()
	client, err := New("https://auth.example.test", &fakeTokens{})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = answerTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: &truncatedBody{prefix: body}}, nil
	})
	return client
}

func reasonBody(reason string) string {
	return `{"schema_version":"error.v1","status":409,"reason_code":"` + reason + `"}`
}

// A body that ends early is no answer, whatever the status or the part of the reason that arrived: a truncated
// final 409 must not close the event.
func TestABodyReadFailureIsNoAnswerForEveryStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"409 projection_conflict", 409, reasonBody("projection_conflict")},
		{"409 projection_refused", 409, reasonBody("projection_refused")},
		{"409 projection_retry", 409, reasonBody("projection_retry")},
		{"409 no body read at all", 409, ""},
		{"400", 400, reasonBody("x")},
		{"401", 401, ""},
		{"429 with a Retry-After body", 429, `{"retry_after_seconds":30}`},
		{"503", 503, ""},
		{"204", 204, ""},
		{"500", 500, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncatedClient(t, tc.status, tc.body).Post(context.Background(), []byte("{}")); got.Outcome != OutcomeNoAnswer || got.Acked() {
				t.Fatalf("result = %+v, want no_answer and not acked", got)
			}
		})
	}
}

// Through the publisher: the row stays unpublished, the aggregate is held, the tick stops and the count is no_answer.
func TestATruncatedFinalAnswerLeavesTheRowUnpublishedAndCountsNoAnswer(t *testing.T) {
	for _, reason := range []string{"projection_conflict", "projection_refused"} {
		store := &fakeStore{}
		store.add("a", 1)
		client := truncatedClient(t, 409, reasonBody(reason))
		publisher, _ := newTestPublisher(store, client)
		report, err := publisher.Tick(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		if store.unpublished() != 1 || report.NoAnswer != 1 || report.Conflict != 0 || report.FinalRefused != 0 || report.Stopped != OutcomeNoAnswer || report.Held != 1 {
			t.Errorf("%s: unpublished = %d, report = %+v", reason, store.unpublished(), report)
		}
	}
}

func TestAFullBodyStillClassifiesWhenNothingFailed(t *testing.T) {
	client, err := New("https://auth.example.test", &fakeTokens{})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = answerTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 409, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reasonBody("projection_conflict")))}, nil
	})
	if got := client.Post(context.Background(), []byte("{}")); got.Outcome != OutcomeConflict {
		t.Fatalf("result = %+v", got)
	}
}

func httpDate(offset time.Duration) string {
	return time.Now().UTC().Truncate(time.Second).Add(offset).Format(http.TimeFormat)
}

// Retry-After also comes as an HTTP-date; the wait is the distance to that date, capped at five minutes, a date in
// the past is no wait.
func TestRetryAfterHTTPDateIsHonouredForBusyAnswers(t *testing.T) {
	cases := []struct {
		name     string
		date     string
		min, max time.Duration
	}{
		{"90 s ahead", httpDate(90 * time.Second), 85 * time.Second, 90 * time.Second},
		{"within the cap", httpDate(200 * time.Second), 195 * time.Second, 200 * time.Second},
		{"a day ahead is capped", httpDate(24 * time.Hour), MaxRetryAfter, MaxRetryAfter},
		{"just past the cap is capped", httpDate(MaxRetryAfter + 30*time.Second), MaxRetryAfter, MaxRetryAfter},
		{"a past date is no wait", httpDate(-time.Hour), 0, 0},
		{"now is no wait", httpDate(0), 0, 0},
		{"not a date is no wait", "yesterday-ish", 0, 0},
	}
	for _, status := range []int{429, 503} {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				server, _ := newServer(t, status, http.Header{"Retry-After": {tc.date}}, "")
				got := newClient(t, server.URL, &fakeTokens{}).Post(context.Background(), []byte("{}"))
				if got.Outcome != OutcomeBusy || got.RetryAfter < tc.min || got.RetryAfter > tc.max {
					t.Fatalf("status %d: result = %+v, want busy in [%v, %v]", status, got, tc.min, tc.max)
				}
			})
		}
	}
}

func TestRetryAfterSecondsAndTheEnvelopeStillWinOverNoDate(t *testing.T) {
	server, _ := newServer(t, 503, http.Header{"Retry-After": {"42"}}, "")
	if got := newClient(t, server.URL, &fakeTokens{}).Post(context.Background(), []byte("{}")); got.RetryAfter != 42*time.Second {
		t.Fatalf("result = %+v", got)
	}
}
