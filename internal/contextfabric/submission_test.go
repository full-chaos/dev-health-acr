package contextfabric

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-11912 (INV-1, INV-2): submission identity and the owner check.

func ownerPrincipal(subject string, scopes ...string) storage.Principal {
	return storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: subject, OrgID: "org_1", RepositoryScopes: scopes}
}

func ownerOf(t *testing.T, principal storage.Principal) *SubmissionOwner {
	t.Helper()
	owner, err := SubmissionOwnerOf(principal)
	if err != nil {
		t.Fatal(err)
	}
	return &owner
}

// A result that completed a submission is the unknown-id answer for every
// other principal on every surface, whatever the grants; its owner and every
// reader of an unbound result are decided as before.
func TestStoredResultGateServesASubmissionResultToItsOwnerOnly(t *testing.T) {
	ownerA := ownerPrincipal("user_a", "acme/tools")
	sameGrantB := ownerPrincipal("user_b", "acme/tools")
	unrestrictedB := ownerPrincipal("user_b")
	credentialA := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: "user_a", OrgID: "org_1", RepositoryScopes: []string{"acme/tools"}}
	otherGrantB := ownerPrincipal("user_b", "acme/web")

	cases := []struct {
		name     string
		owner    *SubmissionOwner
		reader   storage.Principal
		decision StoredResultDecision
		reason   StoredResultAuthorizationReason
	}{
		{"owner reads its own result", ownerOf(t, ownerA), ownerA, StoredResultAdmitted, StoredResultReasonNoSubjects},
		{"same org, same grant, other principal", ownerOf(t, ownerA), sameGrantB, StoredResultDenied, StoredResultReasonOwnerMismatch},
		{"unrestricted other principal", ownerOf(t, ownerA), unrestrictedB, StoredResultDenied, StoredResultReasonOwnerMismatch},
		{"same subject, other authentication method", ownerOf(t, ownerA), credentialA, StoredResultDenied, StoredResultReasonOwnerMismatch},
		// Owner first: a reader refused on both grounds is reported as the
		// owner refusal.
		{"other principal with another grant too", ownerOf(t, ownerA), otherGrantB, StoredResultDenied, StoredResultReasonOwnerMismatch},
		{"reader with no subject", ownerOf(t, ownerA), storage.Principal{OrgID: "org_1"}, StoredResultDenied, StoredResultReasonOwnerMismatch},
		// An unbound result keeps organization-bearer semantics.
		{"unbound result, other principal", nil, sameGrantB, StoredResultAdmitted, StoredResultReasonNoSubjects},
		{"unbound result, unrestricted other principal", nil, unrestrictedB, StoredResultAdmitted, StoredResultReasonNoSubjects},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, surface := range StoredResultSurfaceVocabulary() {
				// The stored grant digest is the reader's own wherever the
				// case is not about grants, so only the owner decides.
				digest := StoredResultGrantDigest(c.reader)
				if c.reader.Subject == otherGrantB.Subject && slices.Equal(c.reader.RepositoryScopes, otherGrantB.RepositoryScopes) {
					digest = StoredResultGrantDigest(ownerA)
				}
				decision := NewStoredResultGate(&capturingGraphReader{}).Authorize(context.Background(), c.reader,
					StoredInvestigationResult{Result: gateResult(), GrantDigest: digest, SubmissionOwner: c.owner}, surface)
				if decision.Decision != c.decision || decision.Reason != c.reason {
					t.Fatalf("%s: decision = %s/%s, want %s/%s", surface, decision.Decision, decision.Reason, c.decision, c.reason)
				}
				if c.decision == StoredResultDenied {
					if !errors.Is(decision.ServingError(), ErrInvestigationResultNotFound) {
						t.Fatalf("%s: ServingError() = %v, want the unknown-id answer", surface, decision.ServingError())
					}
					if decision.SubjectCount != 0 || len(decision.RefusedKinds) != 0 {
						t.Fatalf("%s: an owner refusal carries result content: %+v", surface, decision)
					}
				}
			}
		})
	}
}

// The engine's every prior-result read (parent chain, receipts, carries) goes
// through authorizedResultStore, so a submission result of A is the unknown-id
// answer for B there too.
func TestEngineResultStoreDeniesAnotherPrincipalsSubmissionResult(t *testing.T) {
	a := ownerPrincipal("user_a")
	b := ownerPrincipal("user_b")
	store := authorizedResultStore{
		InvestigationResultStore: digestResultStore{stored: StoredInvestigationResult{Result: gateResult(), GrantDigest: StoredResultGrantDigest(a), SubmissionOwner: ownerOf(t, a)}},
		gate:                     NewStoredResultGate(&capturingGraphReader{}),
	}
	if _, err := store.Get(context.Background(), a, "result_submitted_by_a"); err != nil {
		t.Fatalf("owner: %v", err)
	}
	_, err := store.Get(context.Background(), b, "result_submitted_by_a")
	var denied *storedResultDeniedError
	if !errors.Is(err, ErrInvestigationResultNotFound) || !errors.As(err, &denied) || denied.reason != StoredResultReasonOwnerMismatch {
		t.Fatalf("principal B read A's submission result: err = %v", err)
	}
}

// ownedResultStore is staticResultStore whose every row completed the
// submission of owner.
type ownedResultStore struct {
	*staticResultStore
	owner SubmissionOwner
}

func (s ownedResultStore) Get(ctx context.Context, reader storage.Principal, resultID string) (StoredInvestigationResult, error) {
	stored, err := s.staticResultStore.Get(ctx, reader, resultID)
	if err != nil {
		return stored, err
	}
	owner := s.owner
	stored.SubmissionOwner = &owner
	return stored, nil
}

// End to end through Investigate: a prior subject receipt naming another
// principal's submission result binds exactly like a receipt naming a missing
// result, and the decision on the trace is owner_mismatch on prior_result.
func TestAPriorSubmissionResultOfAnotherPrincipalBindsLikeAMissingOne(t *testing.T) {
	t.Parallel()
	b := ownerPrincipal("user_b")
	run := func(t *testing.T, stored bool, owner SubmissionOwner) (InvestigationResult, *recordingTelemetry) {
		t.Helper()
		project, prior := priorReceiptFixture()
		results := map[string]InvestigationResult{}
		if stored {
			results[prior.ResultID] = prior
		}
		graph := &storedSubjectGraph{
			capturingGraphReader: &capturingGraphReader{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
				context: GraphContext{
					Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
					EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				},
			},
			outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted},
		}
		telemetry := &recordingTelemetry{}
		engine := mustEngineForPriorReceiptTest(t, graph, ownedResultStore{staticResultStore: &staticResultStore{results: results}, owner: owner}, telemetry)
		request := validInvestigationRequest()
		request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "receipt_abc12345"}}
		request.ParentResultID = prior.ResultID
		result, err := engine.Investigate(context.Background(), b, request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		return result, telemetry
	}
	missing, _ := run(t, false, SubmissionOwner{})
	refused, telemetry := run(t, true, *ownerOf(t, ownerPrincipal("user_a")))
	if !reflect.DeepEqual(refused.SubjectResolution.PriorSubjectReceiptDispositions, missing.SubjectResolution.PriorSubjectReceiptDispositions) {
		t.Fatalf("refused dispositions = %#v, missing = %#v", refused.SubjectResolution.PriorSubjectReceiptDispositions, missing.SubjectResolution.PriorSubjectReceiptDispositions)
	}
	var prior []StoredResultAuthorization
	for _, decision := range telemetry.storedResultAuthorizations {
		if decision.Surface == StoredResultSurfacePriorResult {
			prior = append(prior, decision)
		}
	}
	if len(prior) == 0 {
		t.Fatal("no prior_result decision reached the trace")
	}
	for _, decision := range prior {
		if decision.Decision != StoredResultDenied || decision.Reason != StoredResultReasonOwnerMismatch {
			t.Fatalf("prior_result decision = %+v, want denied/owner_mismatch", decision)
		}
	}
	// Control: the owner's own turn binds the receipt.
	admitted, _ := run(t, true, *ownerOf(t, b))
	if reflect.DeepEqual(admitted.SubjectResolution.PriorSubjectReceiptDispositions, missing.SubjectResolution.PriorSubjectReceiptDispositions) {
		t.Fatal("control: the owner's receipt must bind differently from a missing one")
	}
}

// ownedReuseGate offers one candidate that completed owner's submission.
type ownedReuseGate struct {
	candidate InvestigationResult
	owner     SubmissionOwner
}

func (g ownedReuseGate) FindReusable(_ context.Context, principal storage.Principal, _ ReuseKey) (StoredInvestigationResult, bool, ReuseMissReason, error) {
	owner := g.owner
	return StoredInvestigationResult{Result: g.candidate, GrantDigest: StoredResultGrantDigest(principal), SemanticStateRead: SemanticStateReadAbsent, SubmissionOwner: &owner}, true, "", nil
}

// Reuse never crosses principals: another principal's submission result
// offered as a reuse candidate is a miss (owner_mismatch on answer_reuse); the
// owner's own is served.
func TestAReuseCandidateOfAnotherPrincipalsSubmissionIsAMiss(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		reader storage.Principal
		reused bool
	}{
		{"owner", ownerPrincipal("user_a", "acme/tools"), true},
		{"other principal", ownerPrincipal("user_b", "acme/tools"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, candidate := reusableCandidate()
			telemetry := &recordingTelemetry{}
			freshPath := errors.New("fresh investigation reached")
			engine := mustReuseTestEngine(t, EngineDependencies{
				Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
					return InterpretedQuestion{}, freshPath
				}),
				Graph:     reuseSubjectGraph{graphReaderStub: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}}, outcome: StoredSubjectAdmitted},
				Results:   &resultStoreStub{},
				Telemetry: telemetry,
				ReuseGate: ownedReuseGate{candidate: candidate, owner: *ownerOf(t, ownerPrincipal("user_a"))},
			})
			result, err := engine.Investigate(context.Background(), tc.reader, validInvestigationRequest())
			if tc.reused != (err == nil && result.Reused) {
				t.Fatalf("Investigate() = reused %t, error %v; want reused %t", result.Reused, err, tc.reused)
			}
			var reuse []StoredResultAuthorization
			for _, decision := range telemetry.storedResultAuthorizations {
				if decision.Surface == StoredResultSurfaceAnswerReuse {
					reuse = append(reuse, decision)
				}
			}
			if len(reuse) != 1 || reuse[0].Admitted() != tc.reused {
				t.Fatalf("answer_reuse decisions = %+v", reuse)
			}
			if !tc.reused && reuse[0].Reason != StoredResultReasonOwnerMismatch {
				t.Fatalf("reason = %s, want owner_mismatch", reuse[0].Reason)
			}
		})
	}
}

// The digest covers every request field but request_id and the consumer's
// name and version; consumer.surface is behaviour and is covered. The field
// set is read from the production type, so a field added to the request is
// covered (or this test fails) the day it lands.
func TestSubmissionRequestDigestCoversEveryRequestFieldButCorrelation(t *testing.T) {
	base := validInvestigationRequest()
	baseDigest, err := SubmissionRequestDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(baseDigest, "sha256:") || len(baseDigest) != len("sha256:")+64 {
		t.Fatalf("digest = %q", baseDigest)
	}
	excluded := map[string]bool{"RequestID": true, "Consumer": true} // Consumer: perturbs Name, its first field
	requestType := reflect.TypeOf(base)
	seen := 0
	for index := 0; index < requestType.NumField(); index++ {
		field := requestType.Field(index)
		if !field.IsExported() {
			continue
		}
		seen++
		changed := base
		value := reflect.ValueOf(&changed).Elem().Field(index)
		if !perturbForDigest(value) {
			t.Fatalf("field %s: no perturbation for type %s", field.Name, field.Type)
		}
		digest, err := SubmissionRequestDigest(changed)
		if err != nil {
			t.Fatalf("field %s: %v", field.Name, err)
		}
		if excluded[field.Name] {
			if digest != baseDigest {
				t.Errorf("field %s is correlation, not meaning, and must not change the digest", field.Name)
			}
			continue
		}
		if digest == baseDigest {
			t.Errorf("field %s does not change the digest", field.Name)
		}
	}
	if seen < 20 {
		t.Fatalf("walked %d request fields; the request type has more", seen)
	}
	// consumer.surface changes the digest (trimmed, as the engine reads
	// it); name and version do not.
	surface := base
	surface.Consumer.Surface = "mcp"
	if digest, _ := SubmissionRequestDigest(surface); digest == baseDigest {
		t.Error("consumer surface does not change the digest")
	}
	for name, change := range map[string]func(*InvestigationRequest){
		"name":            func(r *InvestigationRequest) { r.Consumer.Name = "another-client" },
		"version":         func(r *InvestigationRequest) { r.Consumer.Version = "9.9.9" },
		"surface spacing": func(r *InvestigationRequest) { r.Consumer.Surface = "  " + r.Consumer.Surface + " " },
	} {
		changed := base
		change(&changed)
		if digest, _ := SubmissionRequestDigest(changed); digest != baseDigest {
			t.Errorf("consumer %s changes the digest", name)
		}
	}
	// Deterministic: the same request has the same digest.
	again, _ := SubmissionRequestDigest(validInvestigationRequest())
	if again != baseDigest {
		t.Fatal("digest is not deterministic")
	}
}

var timeType = reflect.TypeOf(time.Time{})

// perturbForDigest changes value to a different value of its type.
func perturbForDigest(value reflect.Value) bool {
	if value.Type() == timeType {
		current := value.Interface().(time.Time)
		value.Set(reflect.ValueOf(current.Add(time.Hour)))
		return true
	}
	switch value.Kind() {
	case reflect.String:
		value.SetString(value.String() + "_perturbed")
		return true
	case reflect.Bool:
		value.SetBool(!value.Bool())
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(value.Int() + 1)
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(value.Uint() + 1)
		return true
	case reflect.Float32, reflect.Float64:
		value.SetFloat(value.Float() + 1)
		return true
	case reflect.Slice:
		value.Set(reflect.Append(value, reflect.Zero(value.Type().Elem())))
		return true
	case reflect.Map:
		if value.IsNil() {
			value.Set(reflect.MakeMap(value.Type()))
		}
		key := reflect.New(value.Type().Key()).Elem()
		perturbForDigest(key)
		value.SetMapIndex(key, reflect.Zero(value.Type().Elem()))
		return true
	case reflect.Pointer:
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
			return true
		}
		return perturbForDigest(value.Elem())
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).IsExported() && perturbForDigest(value.Field(index)) {
				return true
			}
		}
	}
	return false
}

// Every result id the engine mints goes through mintResultID, so a reserved
// id cannot be bypassed by a mint site that calls the generator directly.
func TestEveryResultIDMintHonoursTheReservation(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	direct := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			declaration, ok := node.(*ast.FuncDecl)
			if ok && declaration.Name.Name == "mintResultID" {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "newResultID" {
				direct++
				t.Errorf("%s: a result id is minted with newResultID directly; use mintResultID(ctx)", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if direct != 0 {
		return
	}
	// And the mint itself returns the reservation, on every mint of the run.
	engine := &Engine{newResultID: func() string { return "result_fresh_00001" }}
	ctx := WithReservedResultID(context.Background(), "result_reserved_01")
	for mint := 0; mint < 3; mint++ {
		if got := engine.mintResultID(ctx); got != "result_reserved_01" {
			t.Fatalf("mint %d = %q, want the reserved id", mint, got)
		}
	}
	if got := engine.mintResultID(context.Background()); got != "result_fresh_00001" {
		t.Fatalf("mint without a reservation = %q, want the generator's id", got)
	}
}

// unsavedMarkerStore is staticResultStore that is also the submission
// marker, recording every done_unsaved write.
type unsavedMarkerStore struct {
	*staticResultStore
	marks []string
}

func (s *unsavedMarkerStore) MarkSubmissionUnsaved(_ context.Context, principal storage.Principal, resultID string, _ time.Time) (bool, error) {
	s.marks = append(s.marks, principal.Subject+"/"+resultID)
	return true, nil
}

// The payload-rejected terminal branch serves the answer without storing it,
// and writes done_unsaved for the submission it answers, under the reserved
// id, where the save is skipped. A run without a reservation writes nothing.
func TestTheNotStoredTerminalMarksItsSubmissionUnsaved(t *testing.T) {
	for _, reserved := range []bool{true, false} {
		t.Run(map[bool]string{true: "reserved", false: "no reservation"}[reserved], func(t *testing.T) {
			frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
			prior := validInvestigationResult()
			prior.ResultID = "result_prior_window_axis"
			prior.WindowClarification = &WindowClarification{Options: []WindowOption{{ReceiptID: "winr_axis", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd}}}
			store := &unsavedMarkerStore{staticResultStore: &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}}
			asOf := time.Unix(100, 0).UTC()
			telemetry := &recordingTelemetry{}
			engine := mustReuseTestEngine(t, EngineDependencies{
				Results: store, Telemetry: telemetry,
				Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &asOf}}, outcome: tupleFamilyOutcomeForTest(t)},
			})
			request := validInvestigationRequest()
			request.Consumer.Surface = "mcp"
			request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "winr_axis"}}
			request.Question += " Include the drivers."
			ctx := context.Background()
			if reserved {
				ctx = WithReservedResultID(ctx, "result_reserved_unsaved")
			}
			principal := storage.Principal{OrgID: "org-1", Subject: "user_a", AuthenticationMethod: storage.AuthenticationMethodWebAssertion}
			result, err := engine.Investigate(ctx, principal, request)
			if err != nil {
				t.Fatalf("a window veto must be served, got %v", err)
			}
			assertTerminalNotSaved(t, result, telemetry, BudgetAssertWindowVeto)
			if !reserved {
				if len(store.marks) != 0 {
					t.Fatalf("a run without a submission wrote %v", store.marks)
				}
				return
			}
			if result.ResultID != "result_reserved_unsaved" {
				t.Fatalf("served result id = %q, want the reserved id", result.ResultID)
			}
			if !slices.Equal(store.marks, []string{"user_a/result_reserved_unsaved"}) {
				t.Fatalf("done_unsaved writes = %v, want one for the reserved id", store.marks)
			}
		})
	}
}

// The submission vocabularies are closed and the owner identity is the
// audit actor.
func TestSubmissionOwnerIsTheAuditActor(t *testing.T) {
	if _, err := SubmissionOwnerOf(storage.Principal{OrgID: "org_1"}); !errors.Is(err, ErrSubmissionOwnerUnknown) {
		t.Fatalf("a principal with no subject has an owner: %v", err)
	}
	web := ownerPrincipal("user_a")
	owner, err := SubmissionOwnerOf(web)
	if err != nil || owner.AuthenticationMethod != string(storage.AuthenticationMethodWebAssertion) || owner.Subject != "user_a" || !owner.Is(web) {
		t.Fatalf("owner = %+v, %v", owner, err)
	}
	if !ValidSubmissionID("6f1c1e4a-6a37-4a4d-9d9b-1b2a3c4d5e6f") || ValidSubmissionID("short") || ValidSubmissionID("has space in it") {
		t.Fatal("submission id grammar")
	}
	codes := contractsv1.ContextFabricSubmissionErrorCodes()
	if !slices.Equal(codes[:], []string{"submission_mismatch", "submission_in_progress", "submission_unsaved", "submission_abandoned"}) {
		t.Fatal("submission error codes")
	}
}
