package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// The Ask Dev conversation contract (docs/design/ask-dev-conversation-contract.md).
//
// The goldens are encoder output of production Go values. To regenerate
// after a deliberate contract change:
//
//	ACR_WRITE_THREAD_GOLDENS=1 go test ./internal/contracts/v1 -run TestThreadGoldens
//
// and review the diff. Without the variable the test fails on any difference.

const (
	threadSchemaFile     = "context_fabric_thread.v1.schema.json"
	threadListSchemaFile = "context_fabric_thread_list.v1.schema.json"
)

var threadGoldenStart = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func goldenThread() ContextFabricThread {
	at := func(minutes int) time.Time { return threadGoldenStart.Add(time.Duration(minutes) * time.Minute) }
	last := at(9)
	return ContextFabricThread{
		SchemaVersion: ContextFabricThreadSchema,
		ThreadID:      "thread-0000-11947-ask-dev",
		RetentionDays: ContextFabricThreadRetentionMonth,
		TurnCount:     4,
		CreatedAt:     at(0),
		LastTurnAt:    last,
		ExpiresAt:     ContextFabricThreadExpiresAt(last, ContextFabricThreadRetentionMonth),
		Turns: []ContextFabricThreadTurn{
			{SubmissionID: "submission-11947-0001", State: ContextFabricSubmissionDone, Question: "Which teams own the dev-health-ops repository?", ResultID: "result_11947_thread_golden_1", AdmittedAt: at(0), UpdatedAt: at(1)},
			{SubmissionID: "submission-11947-0002", State: ContextFabricSubmissionDone, Question: "And what did they deliver last month?", ResultWithheld: true, AdmittedAt: at(3), UpdatedAt: at(4)},
			{SubmissionID: "submission-11947-0003", State: ContextFabricSubmissionDoneUnsaved, Question: "Where is review time going?", AdmittedAt: at(6), UpdatedAt: at(7)},
			{SubmissionID: "submission-11947-0004", State: ContextFabricSubmissionRunning, Question: "Compare that with the quarter before.", AdmittedAt: last, UpdatedAt: last},
		},
	}
}

func goldenThreadList() ContextFabricThreadList {
	newest := goldenThread().Summary()
	older := ContextFabricThreadSummary{
		ThreadID: "thread-0000-11947-older", RetentionDays: ContextFabricThreadRetentionDay, TurnCount: 1,
		CreatedAt: threadGoldenStart.Add(-2 * time.Hour), LastTurnAt: threadGoldenStart.Add(-2 * time.Hour),
	}
	older.ExpiresAt = ContextFabricThreadExpiresAt(older.LastTurnAt, older.RetentionDays)
	return ContextFabricThreadList{
		SchemaVersion: ContextFabricThreadListSchema,
		Threads:       []ContextFabricThreadSummary{newest, older},
		NextCursor:    "eyJsYXN0X3R1cm5fYXQiOiIyMDI2LTEwLTI5VDEwOjAwOjAwWiJ9",
	}
}

func goldenThreadClosedError() ErrorEnvelope {
	return ErrorEnvelope{
		SchemaVersion: ErrorSchema,
		RequestID:     "req_00000000000000000000000000011947",
		Error: ErrorDetail{
			Code: ErrorCodeThreadClosed, Message: ContextFabricThreadClosedMessage,
			HTTPStatus: ContextFabricThreadClosedStatus, Retryable: false,
		},
	}
}

func compareOrWriteThreadGolden(t *testing.T, name string, want []byte) {
	t.Helper()
	path := repopath.Path(t, "contracts", "examples", "v1", name)
	if os.Getenv("ACR_WRITE_THREAD_GOLDENS") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", name, len(want))
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with ACR_WRITE_THREAD_GOLDENS=1)", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden %s differs from the encoder's output (%d bytes on disk, %d encoded); regenerate with ACR_WRITE_THREAD_GOLDENS=1 and review the diff", name, len(got), len(want))
	}
}

// The goldens are the encoder's output for production values, accepted by
// the Go validator and the published schema, and they decode back strictly
// to the same values.
func TestThreadGoldens(t *testing.T) {
	for name, tc := range map[string]struct {
		value    any
		schema   string
		validate func() error
		decode   func([]byte) (any, error)
	}{
		"context_fabric_thread.v1.json": {goldenThread(), threadSchemaFile, goldenThread().Validate, func(b []byte) (any, error) {
			var v ContextFabricThread
			err := decodeContextFabricStrict(b, &v)
			return v, err
		}},
		"context_fabric_thread_list.v1.json": {goldenThreadList(), threadListSchemaFile, goldenThreadList().Validate, func(b []byte) (any, error) {
			var v ContextFabricThreadList
			err := decodeContextFabricStrict(b, &v)
			return v, err
		}},
		"error_context_fabric_thread_closed.v1.json": {goldenThreadClosedError(), "error.v1.schema.json", func() error { return nil }, func(b []byte) (any, error) {
			var v ErrorEnvelope
			err := decodeContextFabricStrict(b, &v)
			return v, err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.validate(); err != nil {
				t.Fatalf("the golden value does not validate: %v", err)
			}
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if err := contractcheck.ValidateSerializedStrict("", tc.schema, data); err != nil {
				t.Fatalf("schema rejects the encoder's output: %v", err)
			}
			decoded, err := tc.decode(data)
			if err != nil {
				t.Fatalf("strict decode: %v", err)
			}
			again, err := json.Marshal(decoded)
			if err != nil || !bytes.Equal(again, data) {
				t.Fatalf("round trip changed the value (err %v)", err)
			}
			compareOrWriteThreadGolden(t, name, append(data, '\n'))
		})
	}
}

// Every mutation is judged the same by the Go validator and the published
// schema, where the schema can express the rule; rules only Go can hold
// (order, identities between fields) are marked goOnly and must be refused
// by Go.
func TestThreadValidatorAndSchemaAgree(t *testing.T) {
	type mutation struct {
		mutate func(*ContextFabricThread)
		valid  bool
		goOnly bool
	}
	cases := map[string]mutation{
		"golden":               {func(*ContextFabricThread) {}, true, false},
		"retention 0":          {func(th *ContextFabricThread) { setRetention(th, ContextFabricThreadRetentionDay) }, true, false},
		"retention 7":          {func(th *ContextFabricThread) { setRetention(th, 7) }, false, false},
		"wrong schema version": {func(th *ContextFabricThread) { th.SchemaVersion = ContextFabricThreadListSchema }, false, false},
		"malformed thread id":  {func(th *ContextFabricThread) { th.ThreadID = "bad thread" }, false, false},
		"short thread id":      {func(th *ContextFabricThread) { th.ThreadID = "short" }, false, false},
		"no turns":             {func(th *ContextFabricThread) { th.Turns = []ContextFabricThreadTurn{} }, false, false},
		"turn count zero":      {func(th *ContextFabricThread) { th.TurnCount = 0 }, false, false},
		"turn count at the maximum": {func(th *ContextFabricThread) {
			th.TurnCount = ContextFabricThreadMaxTurnCount
			th.EarlierTurnsCursor = "ZWFybGllcg"
		}, true, false},
		"turn count over the maximum": {func(th *ContextFabricThread) {
			th.TurnCount = ContextFabricThreadMaxTurnCount + 1
			th.EarlierTurnsCursor = "ZWFybGllcg"
		}, false, false},
		"a page before a later turn": {func(th *ContextFabricThread) { th.TurnCount = 5; shiftLast(th, time.Hour) }, true, false},
		"no question recorded":       {func(th *ContextFabricThread) { th.Turns[0].Question = "" }, true, false},
		"blank question":             {func(th *ContextFabricThread) { th.Turns[0].Question = "   " }, true, false},
		"question too long":          {func(th *ContextFabricThread) { th.Turns[0].Question = strings.Repeat("q", 8001) }, false, false},
		"question at the bound":      {func(th *ContextFabricThread) { th.Turns[0].Question = strings.Repeat("q", 8000) }, true, false},
		"cursor while all served":    {func(th *ContextFabricThread) { th.EarlierTurnsCursor = "ZWFybGllcg" }, false, true},
		"malformed earlier cursor":   {func(th *ContextFabricThread) { omitted(th, 150); th.EarlierTurnsCursor = "a+b" }, false, false},
		"earlier turns, no cursor":   {func(th *ContextFabricThread) { omitted(th, 150); th.EarlierTurnsCursor = "" }, false, true},
		"done without result":        {func(th *ContextFabricThread) { th.Turns[0].ResultID = "" }, false, false},
		"done with both":             {func(th *ContextFabricThread) { th.Turns[1].ResultID = "result_11947_both" }, false, false},
		"withheld when running":      {func(th *ContextFabricThread) { th.Turns[3].ResultWithheld = true }, false, false},
		"result when unsaved":        {func(th *ContextFabricThread) { th.Turns[2].ResultID = "result_11947_unsaved" }, false, false},
		"short result id":            {func(th *ContextFabricThread) { th.Turns[0].ResultID = "r_1" }, false, false},
		"unknown state":              {func(th *ContextFabricThread) { th.Turns[3].State = "lost" }, false, false},
		"malformed submission":       {func(th *ContextFabricThread) { th.Turns[0].SubmissionID = "short" }, false, false},
		"repeated submission":        {func(th *ContextFabricThread) { th.Turns[1].SubmissionID = th.Turns[0].SubmissionID }, false, true},
		"turns out of order":         {func(th *ContextFabricThread) { th.Turns[0], th.Turns[1] = th.Turns[1], th.Turns[0] }, false, true},
		"updated before admitted":    {func(th *ContextFabricThread) { th.Turns[0].UpdatedAt = th.Turns[0].AdmittedAt.Add(-time.Second) }, false, true},
		"expires_at not computed":    {func(th *ContextFabricThread) { th.ExpiresAt = th.ExpiresAt.Add(time.Hour) }, false, true},
		"expires_at with 0":          {func(th *ContextFabricThread) { th.RetentionDays = ContextFabricThreadRetentionDay }, false, true},
		"last_turn_at not latest":    {func(th *ContextFabricThread) { shiftLast(th, -time.Minute) }, false, true},
		"created_at not first":       {func(th *ContextFabricThread) { th.CreatedAt = th.CreatedAt.Add(-time.Minute) }, false, true},
		"turn before created_at":     {func(th *ContextFabricThread) { th.Turns[0].AdmittedAt = th.CreatedAt.Add(-time.Minute) }, false, true},
		"tie broken by submission":   {func(th *ContextFabricThread) { tieFirstTwo(th) }, true, false},
		"tie in the wrong order":     {func(th *ContextFabricThread) { tieFirstTwo(th); th.Turns[0], th.Turns[1] = th.Turns[1], th.Turns[0] }, false, true},
		"a page with earlier turns":  {func(th *ContextFabricThread) { omitted(th, 150) }, true, false},
		"omitted turns serve all":    {func(th *ContextFabricThread) { omitted(th, 150); th.TurnCount = 100 }, false, true},
		"over 100 served":            {func(th *ContextFabricThread) { omitted(th, 150); th.Turns = append(th.Turns, extraTurn(th)) }, false, false},
		"zero admitted_at":           {func(th *ContextFabricThread) { th.Turns[0].AdmittedAt = time.Time{} }, false, true},
		"one turn, created != last":  {func(th *ContextFabricThread) { oneTurn(th); th.CreatedAt = th.CreatedAt.Add(-time.Minute) }, false, true},
		"one turn, created == last":  {func(th *ContextFabricThread) { oneTurn(th) }, true, false},
		"created after last_turn_at": {func(th *ContextFabricThread) { th.CreatedAt = th.LastTurnAt.Add(time.Minute) }, false, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			thread := goldenThread()
			thread.Turns = slices.Clone(thread.Turns)
			tc.mutate(&thread)
			goErr := thread.Validate()
			if (goErr == nil) != tc.valid {
				t.Fatalf("Validate = %v, want valid %t", goErr, tc.valid)
			}
			body, err := json.Marshal(thread)
			if err != nil {
				t.Fatal(err)
			}
			if _, parseErr := ParseContextFabricThread(body); (parseErr == nil) != (goErr == nil) {
				t.Fatalf("Parse = %v on the writer's own bytes, Validate = %v", parseErr, goErr)
			}
			schemaErr := contractcheck.ValidateSerializedStrict("", threadSchemaFile, body)
			switch {
			case tc.goOnly && schemaErr != nil:
				t.Fatalf("a Go-only rule is also refused by the schema (%v): mark it schema-expressible", schemaErr)
			case !tc.goOnly && (schemaErr == nil) != tc.valid:
				t.Fatalf("schema = %v, want valid %t", schemaErr, tc.valid)
			}
		})
	}
}

func setRetention(th *ContextFabricThread, retention ContextFabricThreadRetentionDays) {
	th.RetentionDays = retention
	th.ExpiresAt = ContextFabricThreadExpiresAt(th.LastTurnAt, retention)
}

func shiftLast(th *ContextFabricThread, by time.Duration) {
	th.LastTurnAt = th.LastTurnAt.Add(by)
	th.ExpiresAt = ContextFabricThreadExpiresAt(th.LastTurnAt, th.RetentionDays)
}

func tieFirstTwo(th *ContextFabricThread) {
	th.Turns[1].AdmittedAt = th.Turns[0].AdmittedAt
	th.Turns[1].UpdatedAt = th.Turns[0].UpdatedAt
}

func oneTurn(th *ContextFabricThread) {
	th.Turns = th.Turns[:1]
	th.TurnCount = 1
	th.LastTurnAt = th.Turns[0].AdmittedAt
	th.CreatedAt = th.LastTurnAt
	th.ExpiresAt = ContextFabricThreadExpiresAt(th.LastTurnAt, th.RetentionDays)
}

// omitted makes a thread of total turns serving its latest 100 with a cursor
// to the earlier ones: created_at is the first turn's admission, before
// every served turn.
func omitted(th *ContextFabricThread, total int) {
	turns := make([]ContextFabricThreadTurn, 0, ContextFabricThreadMaxServedTurns)
	for i := range ContextFabricThreadMaxServedTurns {
		at := threadGoldenStart.Add(time.Duration(i) * time.Minute)
		turns = append(turns, ContextFabricThreadTurn{
			SubmissionID: fmt.Sprintf("submission-11947-%04d", i), State: ContextFabricSubmissionAbandoned, AdmittedAt: at, UpdatedAt: at,
		})
	}
	th.Turns = turns
	th.TurnCount = total
	th.CreatedAt = threadGoldenStart.Add(-time.Hour)
	th.LastTurnAt = turns[len(turns)-1].AdmittedAt
	th.ExpiresAt = ContextFabricThreadExpiresAt(th.LastTurnAt, th.RetentionDays)
	th.EarlierTurnsCursor = "eyJiZWZvcmUiOjB9"
}

func extraTurn(th *ContextFabricThread) ContextFabricThreadTurn {
	return ContextFabricThreadTurn{SubmissionID: "submission-11947-extra", State: ContextFabricSubmissionAbandoned, AdmittedAt: th.LastTurnAt, UpdatedAt: th.LastTurnAt}
}

func TestThreadListValidatorAndSchemaAgree(t *testing.T) {
	type mutation struct {
		mutate func(*ContextFabricThreadList)
		valid  bool
		goOnly bool
	}
	cases := map[string]mutation{
		"golden":                   {func(*ContextFabricThreadList) {}, true, false},
		"empty last page":          {func(l *ContextFabricThreadList) { l.Threads, l.NextCursor = []ContextFabricThreadSummary{}, "" }, true, false},
		"absent threads":           {func(l *ContextFabricThreadList) { l.Threads, l.NextCursor = nil, "" }, false, false},
		"cursor after empty page":  {func(l *ContextFabricThreadList) { l.Threads = []ContextFabricThreadSummary{} }, false, true},
		"malformed cursor":         {func(l *ContextFabricThreadList) { l.NextCursor = "a+b/c=" }, false, false},
		"cursor too long":          {func(l *ContextFabricThreadList) { l.NextCursor = strings.Repeat("a", 513) }, false, false},
		"wrong schema version":     {func(l *ContextFabricThreadList) { l.SchemaVersion = ContextFabricThreadSchema }, false, false},
		"out of order":             {func(l *ContextFabricThreadList) { l.Threads[0], l.Threads[1] = l.Threads[1], l.Threads[0] }, false, true},
		"repeated thread":          {func(l *ContextFabricThreadList) { l.Threads[1] = l.Threads[0] }, false, true},
		"retention 1":              {func(l *ContextFabricThreadList) { l.Threads[1].RetentionDays = 1 }, false, false},
		"expires_at not computed":  {func(l *ContextFabricThreadList) { l.Threads[1].ExpiresAt = l.Threads[1].LastTurnAt }, false, true},
		"turn count zero":          {func(l *ContextFabricThreadList) { l.Threads[0].TurnCount = 0 }, false, false},
		"over the page limit":      {func(l *ContextFabricThreadList) { l.Threads = manySummaries(51) }, false, false},
		"at the page limit":        {func(l *ContextFabricThreadList) { l.Threads = manySummaries(50) }, true, false},
		"same last turn, id order": {func(l *ContextFabricThreadList) { l.Threads = manySummaries(2) }, true, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			list := goldenThreadList()
			list.Threads = slices.Clone(list.Threads)
			tc.mutate(&list)
			goErr := list.Validate()
			if (goErr == nil) != tc.valid {
				t.Fatalf("Validate = %v, want valid %t", goErr, tc.valid)
			}
			body, err := json.Marshal(list)
			if err != nil {
				t.Fatal(err)
			}
			if _, parseErr := ParseContextFabricThreadList(body); (parseErr == nil) != (goErr == nil) {
				t.Fatalf("Parse = %v on the writer's own bytes, Validate = %v", parseErr, goErr)
			}
			schemaErr := contractcheck.ValidateSerializedStrict("", threadListSchemaFile, body)
			switch {
			case tc.goOnly && schemaErr != nil:
				t.Fatalf("a Go-only rule is also refused by the schema (%v)", schemaErr)
			case !tc.goOnly && (schemaErr == nil) != tc.valid:
				t.Fatalf("schema = %v, want valid %t", schemaErr, tc.valid)
			}
		})
	}
}

// manySummaries is n threads sharing one last turn, in thread_id order.
func manySummaries(n int) []ContextFabricThreadSummary {
	out := make([]ContextFabricThreadSummary, 0, n)
	for i := range n {
		s := ContextFabricThreadSummary{
			ThreadID: fmt.Sprintf("thread-11947-%04d", i), RetentionDays: ContextFabricThreadRetentionMonth, TurnCount: 1,
			CreatedAt: threadGoldenStart, LastTurnAt: threadGoldenStart,
		}
		s.ExpiresAt = ContextFabricThreadExpiresAt(s.LastTurnAt, s.RetentionDays)
		out = append(out, s)
	}
	return out
}

// The retention header has one grammar: exactly "0" or "30".
func TestThreadRetentionHeaderGrammar(t *testing.T) {
	for value, want := range map[string]ContextFabricThreadRetentionDays{"0": ContextFabricThreadRetentionDay, "30": ContextFabricThreadRetentionMonth} {
		got, err := ParseContextFabricThreadRetention(value)
		if err != nil || got != want {
			t.Fatalf("%q = %d, %v", value, got, err)
		}
	}
	for _, value := range []string{"", " 0", "0 ", "00", "030", "+30", "-0", "1", "7", "29", "31", "30d", "thirty", "0x1e"} {
		if _, err := ParseContextFabricThreadRetention(value); !errors.Is(err, ErrContextFabricThreadRetentionInvalid) {
			t.Fatalf("%q is accepted (err %v)", value, err)
		}
	}
	if ContextFabricThreadDefaultRetention != ContextFabricThreadRetentionDay {
		t.Fatal("the default retention is not the shortest")
	}
}

// 0 keeps a thread 24 hours after its last turn, 30 keeps it 30 days
// (chris 2026-10-29: "0-day" means deleted within 24 hours). Every member
// has a window and a value outside the set has none.
func TestThreadRetentionWindows(t *testing.T) {
	last := threadGoldenStart
	if got := ContextFabricThreadExpiresAt(last, ContextFabricThreadRetentionDay); !got.Equal(last.Add(24 * time.Hour)) {
		t.Fatalf("retention 0 expires at %s", got)
	}
	if got := ContextFabricThreadExpiresAt(last, ContextFabricThreadRetentionMonth); !got.Equal(last.AddDate(0, 0, 30)) {
		t.Fatalf("retention 30 expires at %s", got)
	}
	for _, member := range ContextFabricThreadRetentionVocabulary() {
		if !member.Valid() || member.Window() <= 0 {
			t.Fatalf("retention %d has no window", member)
		}
	}
	for _, outside := range []ContextFabricThreadRetentionDays{-1, 1, 7, 29, 31, 90} {
		if outside.Valid() || outside.Window() != 0 {
			t.Fatalf("retention %d is accepted", outside)
		}
	}
}

// The published retention enum, in both schemas, is the Go vocabulary.
func TestThreadRetentionEnumMatchesTheSchemas(t *testing.T) {
	want := ContextFabricThreadRetentionVocabulary()
	for _, file := range []struct{ name, pointer string }{
		{threadSchemaFile, "properties"},
		{threadListSchemaFile, "$defs.ThreadSummary.properties"},
	} {
		raw, err := os.ReadFile(repopath.Path(t, "contracts", "jsonschema", "v1", file.name))
		if err != nil {
			t.Fatal(err)
		}
		var node any
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatal(err)
		}
		for _, key := range strings.Split(file.pointer, ".") {
			node = node.(map[string]any)[key]
		}
		enum := node.(map[string]any)["retention_days"].(map[string]any)["enum"].([]any)
		if len(enum) != len(want) {
			t.Fatalf("%s retention enum = %v, want %v", file.name, enum, want)
		}
		for i, member := range enum {
			if int(member.(float64)) != int(want[i]) {
				t.Fatalf("%s retention enum = %v, want %v", file.name, enum, want)
			}
		}
	}
}

// The turn state enum is the submission state vocabulary, in order.
func TestThreadTurnStateEnumIsTheSubmissionVocabulary(t *testing.T) {
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "jsonschema", "v1", threadSchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			ThreadTurn struct {
				Properties struct {
					State struct {
						Enum []string `json:"enum"`
					} `json:"state"`
				} `json:"properties"`
			} `json:"ThreadTurn"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	want := ContextFabricSubmissionStateVocabulary()
	got := schema.Defs.ThreadTurn.Properties.State.Enum
	if len(got) != len(want) {
		t.Fatalf("turn state enum = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != string(want[i]) {
			t.Fatalf("turn state enum = %v, want %v", got, want)
		}
	}
}

// The thread_closed refusal is a member of the published error code enum
// and its golden is the Go envelope.
func TestThreadClosedIsAPublishedErrorCode(t *testing.T) {
	if !validErrorCode(ErrorCodeThreadClosed) {
		t.Fatal("thread_closed is not in the Go error code vocabulary")
	}
	golden := contextFabricGolden(t, "error_context_fabric_thread_closed.v1.json")
	var envelope ErrorEnvelope
	if err := decodeContextFabricStrict(golden, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != ErrorCodeThreadClosed || envelope.Error.HTTPStatus != 409 || envelope.Error.Retryable {
		t.Fatalf("thread_closed golden = %+v", envelope.Error)
	}
}

// The OpenAPI document publishes the Go constants: the retention header's
// enum, the list's limit bounds and default, and the cursor grammar.
func TestThreadOpenAPIPublishesTheGoConstants(t *testing.T) {
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Enum      []string `json:"enum"`
		Minimum   *int     `json:"minimum"`
		Maximum   *int     `json:"maximum"`
		Default   *int     `json:"default"`
		MaxLength *int     `json:"maxLength"`
		Pattern   string   `json:"pattern"`
	}
	type parameter struct {
		Ref    string `json:"$ref"`
		Name   string `json:"name"`
		In     string `json:"in"`
		Schema schema `json:"schema"`
	}
	var document struct {
		Paths map[string]map[string]struct {
			Parameters []parameter `json:"parameters"`
		} `json:"paths"`
		Components struct {
			Parameters map[string]parameter `json:"parameters"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	header := document.Components.Parameters["ThreadRetentionDays"]
	var wantEnum []string
	for _, member := range ContextFabricThreadRetentionVocabulary() {
		wantEnum = append(wantEnum, fmt.Sprint(int(member)))
	}
	if header.Name != ContextFabricThreadRetentionHeader || header.In != "header" || !slices.Equal(header.Schema.Enum, wantEnum) {
		t.Fatalf("retention header = %+v, want enum %v", header, wantEnum)
	}
	for _, value := range header.Schema.Enum {
		if _, err := ParseContextFabricThreadRetention(value); err != nil {
			t.Fatalf("published retention %q is refused by the parser", value)
		}
	}
	// The create operation names the header (confirmation-pass review: the
	// component alone was checked, so dropping the operation's reference
	// passed every test).
	attached := false
	for _, p := range document.Paths["/api/v1/context-fabric/investigations"]["post"].Parameters {
		attached = attached || p.Ref == "#/components/parameters/ThreadRetentionDays"
	}
	if !attached {
		t.Fatal("POST /api/v1/context-fabric/investigations does not name the ThreadRetentionDays parameter")
	}
	var limit, cursor *parameter
	for i, p := range document.Paths["/api/v1/context-fabric/threads"]["get"].Parameters {
		switch p.Name {
		case "limit":
			limit = &document.Paths["/api/v1/context-fabric/threads"]["get"].Parameters[i]
		case "cursor":
			cursor = &document.Paths["/api/v1/context-fabric/threads"]["get"].Parameters[i]
		}
	}
	if limit == nil || limit.Schema.Minimum == nil || *limit.Schema.Minimum != 1 || limit.Schema.Maximum == nil || *limit.Schema.Maximum != ContextFabricThreadListMaxLimit ||
		limit.Schema.Default == nil || *limit.Schema.Default != ContextFabricThreadListDefaultLimit {
		t.Fatalf("list limit = %+v", limit)
	}
	if cursor == nil || cursor.Schema.MaxLength == nil || *cursor.Schema.MaxLength != ContextFabricThreadCursorMaxLength || cursor.Schema.Pattern != "^[A-Za-z0-9_-]{1,512}$" {
		t.Fatalf("list cursor = %+v", cursor)
	}
	var before *parameter
	for i, p := range document.Paths["/api/v1/context-fabric/threads/{thread_id}"]["get"].Parameters {
		if p.Name == "before" && p.In == "query" {
			before = &document.Paths["/api/v1/context-fabric/threads/{thread_id}"]["get"].Parameters[i]
		}
	}
	if before == nil || before.Schema.MaxLength == nil || *before.Schema.MaxLength != ContextFabricThreadCursorMaxLength || before.Schema.Pattern != cursor.Schema.Pattern {
		t.Fatalf("thread before = %+v", before)
	}
	for _, accepted := range []string{"a", strings.Repeat("Z", ContextFabricThreadCursorMaxLength), "eyJiZWZvcmUiOjB9", "a-b_c"} {
		if !ValidContextFabricThreadCursor(accepted) {
			t.Fatalf("cursor %q is refused", accepted)
		}
	}
	for _, refused := range []string{"", strings.Repeat("Z", ContextFabricThreadCursorMaxLength+1), "a+b", "a/b", "a=", "a b", "é"} {
		if ValidContextFabricThreadCursor(refused) {
			t.Fatalf("cursor %q is accepted", refused)
		}
	}
}

// Parse and the published schema judge raw documents the same: the keys a
// Go value cannot see (present as empty, false or null; missing; unknown;
// given twice) are refused by both, so a consumer decoding with Parse and one
// validating with the schema accept the same documents (round-1 review: Go
// accepted an explicit empty question and result_withheld false).
func TestThreadParseAndSchemaAgreeOnRawDocuments(t *testing.T) {
	type document = map[string]any
	golden := func(name string) document {
		var value document
		decoder := json.NewDecoder(bytes.NewReader(contextFabricGolden(t, name)))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	turn := func(d document, index int) document { return d["turns"].([]any)[index].(document) }
	summary := func(d document, index int) document { return d["threads"].([]any)[index].(document) }
	encode := func(d document) []byte {
		body, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	type rawCase struct {
		list   bool
		body   func() []byte
		valid  bool
		goOnly bool
	}
	thread := func(mutate func(document)) func() []byte {
		return func() []byte { d := golden("context_fabric_thread.v1.json"); mutate(d); return encode(d) }
	}
	list := func(mutate func(document)) func() []byte {
		return func() []byte { d := golden("context_fabric_thread_list.v1.json"); mutate(d); return encode(d) }
	}
	cases := map[string]rawCase{
		"thread golden":                  {false, thread(func(document) {}), true, false},
		"explicit empty question":        {false, thread(func(d document) { turn(d, 0)["question"] = "" }), false, false},
		"null question":                  {false, thread(func(d document) { turn(d, 0)["question"] = nil }), false, false},
		"whitespace question":            {false, thread(func(d document) { turn(d, 0)["question"] = "   " }), true, false},
		"withheld false beside a result": {false, thread(func(d document) { turn(d, 0)["result_withheld"] = false }), false, false},
		"withheld false when running":    {false, thread(func(d document) { turn(d, 3)["result_withheld"] = false }), false, false},
		"withheld null":                  {false, thread(func(d document) { turn(d, 3)["result_withheld"] = nil }), false, false},
		"empty result_id when unsaved":   {false, thread(func(d document) { turn(d, 2)["result_id"] = "" }), false, false},
		"empty earlier cursor":           {false, thread(func(d document) { d["earlier_turns_cursor"] = "" }), false, false},
		"null earlier cursor":            {false, thread(func(d document) { d["earlier_turns_cursor"] = nil }), false, false},
		"missing retention_days":         {false, thread(func(d document) { delete(d, "retention_days") }), false, false},
		"null retention_days":            {false, thread(func(d document) { d["retention_days"] = nil }), false, false},
		"missing turn_count":             {false, thread(func(d document) { delete(d, "turn_count") }), false, false},
		"missing turn state":             {false, thread(func(d document) { delete(turn(d, 3), "state") }), false, false},
		"null turns":                     {false, thread(func(d document) { d["turns"] = nil }), false, false},
		"unknown top-level key":          {false, thread(func(d document) { d["title"] = "x" }), false, false},
		"unknown turn key":               {false, thread(func(d document) { turn(d, 0)["answer"] = "x" }), false, false},
		"retention as a string":          {false, thread(func(d document) { d["retention_days"] = "30" }), false, false},
		"duplicate key": {false, func() []byte {
			return bytes.Replace(contextFabricGolden(t, "context_fabric_thread.v1.json"), []byte(`{"schema_version"`), []byte(`{"thread_id":"thread-0000-11947-other","schema_version"`), 1)
		}, false, true},
		"duplicate turn key": {false, func() []byte {
			return bytes.Replace(contextFabricGolden(t, "context_fabric_thread.v1.json"), []byte(`"state":"running"`), []byte(`"state":"done","state":"running"`), 1)
		}, false, true},
		"list golden":               {true, list(func(document) {}), true, false},
		"empty next cursor":         {true, list(func(d document) { d["next_cursor"] = "" }), false, false},
		"null next cursor":          {true, list(func(d document) { d["next_cursor"] = nil }), false, false},
		"missing threads":           {true, list(func(d document) { delete(d, "threads"); delete(d, "next_cursor") }), false, false},
		"null threads":              {true, list(func(d document) { d["threads"] = nil; delete(d, "next_cursor") }), false, false},
		"summary missing retention": {true, list(func(d document) { delete(summary(d, 1), "retention_days") }), false, false},
		"summary unknown key":       {true, list(func(d document) { summary(d, 0)["title"] = "x" }), false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := tc.body()
			var parseErr error
			schemaFile := threadSchemaFile
			if tc.list {
				_, parseErr = ParseContextFabricThreadList(body)
				schemaFile = threadListSchemaFile
			} else {
				_, parseErr = ParseContextFabricThread(body)
			}
			if (parseErr == nil) != tc.valid {
				t.Fatalf("Parse = %v, want valid %t", parseErr, tc.valid)
			}
			// An unknown key is refused as unknown, not misreported as an
			// empty value.
			if strings.Contains(name, "unknown") && (parseErr == nil || !strings.Contains(parseErr.Error(), "unknown field")) {
				t.Fatalf("Parse = %v, want an unknown field refusal", parseErr)
			}
			schemaErr := contractcheck.ValidateSerializedStrict("", schemaFile, body)
			standardErr := standardThreadSchemaErr(t, schemaFile, body)
			switch {
			case tc.goOnly && (schemaErr != nil || standardErr != nil):
				t.Fatalf("a Go-only rule is also refused by a schema validator (local %v, standard %v)", schemaErr, standardErr)
			case !tc.goOnly && ((schemaErr == nil) != tc.valid || (standardErr == nil) != tc.valid):
				t.Fatalf("schema = %v, standard = %v, want valid %t", schemaErr, standardErr, tc.valid)
			}
		})
	}
	raw := contextFabricGolden(t, "context_fabric_thread.v1.json")
	for name, body := range map[string][]byte{
		"trailing content": append(bytes.Clone(raw), []byte(`{}`)...),
		"invalid UTF-8":    bytes.Replace(raw, []byte("dev-health-ops"), []byte{'d', 0xff}, 1),
		"not an object":    []byte(`[]`),
	} {
		if _, err := ParseContextFabricThread(body); err == nil {
			t.Fatalf("%s: Parse accepted it", name)
		}
	}
}

// No key of either document may be null: encoding/json reads a null into a
// non-pointer field as no value, so a null retention_days on a retention-0
// thread read as the valid 0 (round-2 review). Every key of every object is
// swept, on a retention-0 thread and list, so the sweep holds the case the
// 30-day golden hid (there a null retention failed expires_at instead).
func TestThreadParseRefusesNullAtEveryKey(t *testing.T) {
	zeroThread := goldenThread()
	zeroThread.Turns = slices.Clone(zeroThread.Turns)
	setRetention(&zeroThread, ContextFabricThreadRetentionDay)
	zeroThread.EarlierTurnsCursor = ""
	zeroList := goldenThreadList()
	threadBody, err := json.Marshal(zeroThread)
	if err != nil {
		t.Fatal(err)
	}
	listBody, err := json.Marshal(zeroList)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseContextFabricThread(threadBody); err != nil {
		t.Fatalf("the retention-0 thread does not parse: %v", err)
	}
	if _, err := ParseContextFabricThreadList(listBody); err != nil {
		t.Fatalf("the list does not parse: %v", err)
	}
	type target struct {
		list   bool
		object func(map[string]any) map[string]any
	}
	targets := map[string]target{
		"thread":        {false, func(d map[string]any) map[string]any { return d }},
		"thread turn 0": {false, func(d map[string]any) map[string]any { return d["turns"].([]any)[0].(map[string]any) }},
		"thread turn 1": {false, func(d map[string]any) map[string]any { return d["turns"].([]any)[1].(map[string]any) }},
		"list":          {true, func(d map[string]any) map[string]any { return d }},
		"list entry 1":  {true, func(d map[string]any) map[string]any { return d["threads"].([]any)[1].(map[string]any) }},
	}
	swept := 0
	for name, tg := range targets {
		base := threadBody
		schemaFile := threadSchemaFile
		if tg.list {
			base, schemaFile = listBody, threadListSchemaFile
		}
		var probe map[string]any
		if err := json.Unmarshal(base, &probe); err != nil {
			t.Fatal(err)
		}
		for key := range tg.object(probe) {
			var doc map[string]any
			decoder := json.NewDecoder(bytes.NewReader(base))
			decoder.UseNumber()
			if err := decoder.Decode(&doc); err != nil {
				t.Fatal(err)
			}
			tg.object(doc)[key] = nil
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			var parseErr error
			if tg.list {
				_, parseErr = ParseContextFabricThreadList(body)
			} else {
				_, parseErr = ParseContextFabricThread(body)
			}
			if parseErr == nil {
				t.Errorf("%s: Parse accepts %s: null", name, key)
			}
			if err := contractcheck.ValidateSerializedStrict("", schemaFile, body); err == nil {
				t.Errorf("%s: the schema accepts %s: null", name, key)
			}
			if err := standardThreadSchemaErr(t, schemaFile, body); err == nil {
				t.Errorf("%s: the standard validator accepts %s: null", name, key)
			}
			swept++
		}
	}
	if swept < 20 {
		t.Fatalf("swept %d keys; the sweep lost its targets", swept)
	}
	// A key spelled in another case: encoding/json matches it, the schema
	// does not.
	cased := bytes.Replace(threadBody, []byte(`"retention_days"`), []byte(`"Retention_Days"`), 1)
	if _, err := ParseContextFabricThread(cased); err == nil {
		t.Fatal("Parse accepts Retention_Days")
	}
	if err := contractcheck.ValidateSerializedStrict("", threadSchemaFile, cased); err == nil {
		t.Fatal("the schema accepts Retention_Days")
	}
	// Integer spellings: Parse judges them as a standard validator does.
	for _, spelling := range []string{"0.0", "0e0", "-0", "0.5"} {
		number := bytes.Replace(threadBody, []byte(`"retention_days":0`), []byte(`"retention_days":`+spelling), 1)
		_, parseErr := ParseContextFabricThread(number)
		standardErr := standardThreadSchemaErr(t, threadSchemaFile, number)
		if (parseErr == nil) != (standardErr == nil) {
			t.Fatalf("retention_days %s: Parse = %v, standard schema = %v", spelling, parseErr, standardErr)
		}
	}
}

// standardThreadSchemaErr validates body against a published thread schema
// with an independent Draft 2020-12 validator, which reads a number by value
// as the standard does (contractcheck reads an integer lexically, so it
// refuses 30.0 where a standard validator accepts it).
func standardThreadSchemaErr(t *testing.T, file string, body []byte) error {
	t.Helper()
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "jsonschema", "v1", file))
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return err
	}
	return resolved.Validate(document)
}

// Parse reads a number as the published schema does, by value (round-3
// review: Parse refused 30.0 and 4e0, which a standard validator accepts,
// and accepted no turn_count bound while Go's int has one). Every case is
// judged by Parse and by an independent standard validator; contractcheck's
// lexical integer rule is recorded, not used as the reference.
func TestThreadParseReadsNumbersAsTheSchemaDoes(t *testing.T) {
	thread := contextFabricGolden(t, "context_fabric_thread.v1.json")
	list := contextFabricGolden(t, "context_fabric_thread_list.v1.json")
	type numberCase struct {
		list          bool
		from, to      string
		valid         bool
		localStricter bool
		// floatMasked: the literal is not an integer, but a float64 reads it
		// as one, so the float-based reference validator cannot judge it;
		// Parse is held to the exact value alone (confirmation-pass review:
		// 30.0000000000000001 was rounded to 30 and accepted).
		floatMasked bool
	}
	cases := []numberCase{
		{false, `"retention_days":30`, `"retention_days":30.0`, true, true, false},
		{false, `"retention_days":30`, `"retention_days":3e1`, true, true, false},
		{false, `"retention_days":30`, `"retention_days":30.5`, false, false, false},
		{false, `"retention_days":30`, `"retention_days":30.000`, true, true, false},
		{false, `"turn_count":4`, `"turn_count":4.0`, true, true, false},
		{false, `"turn_count":4`, `"turn_count":4e0`, true, true, false},
		{false, `"turn_count":4`, `"turn_count":4.5`, false, false, false},
		{false, `"turn_count":4`, `"turn_count":1000000`, true, false, false},
		{false, `"turn_count":4`, `"turn_count":1000001`, false, false, false},
		{false, `"turn_count":4`, `"turn_count":1e6`, true, true, false},
		{false, `"turn_count":4`, `"turn_count":9223372036854775808`, false, false, false},
		{false, `"turn_count":4`, `"turn_count":1e400`, false, false, false},
		{true, `"retention_days":0`, `"retention_days":0.0`, true, true, false},
		{true, `"turn_count":1,`, `"turn_count":1e0,`, true, true, false},
		{true, `"turn_count":4`, `"turn_count":1000001`, false, false, false},
		{false, `"retention_days":30`, `"retention_days":30.0000000000000001`, false, false, true},
		{false, `"turn_count":4`, `"turn_count":4.0000000000000000001e0`, false, false, true},
		{false, `"retention_days":30`, `"retention_days":3.0e1`, true, true, false},
		{false, `"retention_days":30`, `"retention_days":300e-1`, true, true, false},
		{false, `"retention_days":30`, `"retention_days":0.3e2`, true, true, false},
		{false, `"retention_days":30`, `"retention_days":3e-1`, false, false, false},
		{false, `"turn_count":4`, `"turn_count":400E-2`, true, true, false},
	}
	for _, tc := range cases {
		base, file := thread, threadSchemaFile
		if tc.list {
			base, file = list, threadListSchemaFile
		}
		if !bytes.Contains(base, []byte(tc.from)) {
			t.Fatalf("%s: the golden has no %s", tc.to, tc.from)
		}
		body := bytes.Replace(base, []byte(tc.from), []byte(tc.to), 1)
		var parseErr error
		if tc.list {
			_, parseErr = ParseContextFabricThreadList(body)
		} else {
			_, parseErr = ParseContextFabricThread(body)
		}
		if (parseErr == nil) != tc.valid {
			t.Errorf("%s: Parse = %v, want valid %t", tc.to, parseErr, tc.valid)
		}
		if tc.floatMasked {
			continue
		}
		standardErr := standardThreadSchemaErr(t, file, body)
		if (standardErr == nil) != tc.valid {
			t.Errorf("%s: standard schema = %v, want valid %t", tc.to, standardErr, tc.valid)
		}
		localErr := contractcheck.ValidateSerializedStrict("", file, body)
		if tc.localStricter != (tc.valid && localErr != nil) {
			t.Errorf("%s: contractcheck = %v, want its lexical-integer refusal %t", tc.to, localErr, tc.localStricter)
		}
	}
}

// exactJSONInteger decides integrality from the literal's digits, never
// through a float.
func TestExactJSONInteger(t *testing.T) {
	for literal, want := range map[string]string{
		"30.0": "30", "3e1": "30", "3.0e1": "30", "300e-1": "30", "0.3e2": "30", "30.000": "30",
		"-0.0": "0", "0e-5000": "0", "1e+2": "100", "100e-2": "1", "-4.0": "-4",
		"9223372036854775807.0": "9223372036854775807", "-9223372036854775808.0": "-9223372036854775808",
	} {
		got, ok := exactJSONInteger(literal)
		if !ok || got != want {
			t.Errorf("%s = %q %t, want %q", literal, got, ok, want)
		}
	}
	for _, literal := range []string{
		"30.5", "30.0000000000000001", "3e-1", "1.5e0", "1e-5000", "1e5000", "1e99999999999999999999",
		"9223372036854775808.0", "-9223372036854775809.0", "4.0000000000000000001e0",
	} {
		if got, ok := exactJSONInteger(literal); ok {
			t.Errorf("%s = %q, want not an int64 integer", literal, got)
		}
	}
}

// Trailing content is refused before a number is rewritten: the rewrite
// re-encodes the first value only (confirmation-pass review: 30.0 followed
// by {} was accepted).
func TestThreadParseRefusesTrailingContentAfterARewrite(t *testing.T) {
	body := bytes.Replace(contextFabricGolden(t, "context_fabric_thread.v1.json"), []byte(`"retention_days":30`), []byte(`"retention_days":30.0`), 1)
	if _, err := ParseContextFabricThread(body); err != nil {
		t.Fatalf("the rewritten document itself must parse: %v", err)
	}
	for _, trailing := range []string{`{}`, ` {}`, `[]`, `1`, `"x"`, `null`} {
		if _, err := ParseContextFabricThread(append(bytes.Clone(body), trailing...)); err == nil {
			t.Errorf("trailing %s after a rewritten number is accepted", trailing)
		}
	}
}
