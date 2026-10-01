package contextfabric

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireRefusedFactResult asserts the whole of what a refused provider
// result leaves behind: no error, the kind disclosed as unavailable with the
// fixed sentence, no fact of the kind admitted, and the ledger naming the
// branch.
func requireRefusedFactResult(t *testing.T, bundle CanonicalFactBundle, err error, kind FactKind) {
	t.Helper()
	if err != nil {
		t.Fatalf("ReadFacts() error = %v, want nil: a refused provider result degrades its own kind", err)
	}
	source, ok := coverageBySource(bundle)["canonical_fact:"+string(kind)]
	if !ok {
		t.Fatalf("coverage has no %s source: %+v", kind, bundle.Coverage.Sources)
	}
	if source.State != SourceUnavailable {
		t.Fatalf("%s state = %q, want %q", kind, source.State, SourceUnavailable)
	}
	if !strings.Contains(source.Reason, factResultRejectedReason) {
		t.Fatalf("%s reason = %q, want it to carry %q", kind, source.Reason, factResultRejectedReason)
	}
	if !bundle.Coverage.Partial {
		t.Fatalf("Coverage.Partial = false after %s was refused", kind)
	}
	for _, fact := range bundle.Facts {
		if fact.Kind == kind {
			t.Fatalf("a %s fact from a refused result was admitted: %+v", kind, fact)
		}
	}
	if branch := bundle.Outcomes[kind].Branch; branch != string(factReadRejected) {
		t.Fatalf("%s ledger branch = %q, want %q", kind, branch, factReadRejected)
	}
}

// declaredFactResultRejectionCauses reads the cause vocabulary out of the
// production source, so a cause added there without a row below fails.
func declaredFactResultRejectionCauses(t *testing.T) map[factResultRejectionCause]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "fact_registry.go", nil, 0)
	if err != nil {
		t.Fatalf("parse fact_registry.go: %v", err)
	}
	causes := map[factResultRejectionCause]bool{}
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typed, ok := value.Type.(*ast.Ident)
			if !ok || typed.Name != "factResultRejectionCause" {
				continue
			}
			for _, expression := range value.Values {
				literal, ok := expression.(*ast.BasicLit)
				if !ok {
					t.Fatalf("a factResultRejectionCause constant is not a string literal")
				}
				text, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				causes[factResultRejectionCause(text)] = true
			}
		}
	}
	if len(causes) == 0 {
		t.Fatal("found no factResultRejectionCause constant in fact_registry.go: the measurement did not happen")
	}
	return causes
}

func TestEveryMergeRefusalIsATypedFailureThatWritesNothing(t *testing.T) {
	t.Parallel()

	repository := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:api", Label: "api"}
	labelLess := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:label-less"}
	stranger := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:stranger", Label: "stranger"}
	capability := FactCapability{
		Kind: FactMetrics, Name: "metrics", Version: "v1",
		SupportedSubjectKinds: []SubjectKind{SubjectRepository}, RequiresEvidence: true,
		Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject},
	}
	validFact := func() CanonicalFact {
		return CanonicalFact{
			Kind: FactMetrics, Subject: repository,
			Fields:         map[string]FactValue{"commits": IntegerFactValue(7)},
			EvidenceRefIDs: []string{"evidence_metrics_0001"},
		}
	}
	// Every result opens with a fact the merge accepts and an evaluation it
	// accepts, so a refusal that left either behind is visible.
	withBadFact := func(mutate func(*CanonicalFact)) FactProviderResult {
		bad := validFact()
		mutate(&bad)
		return FactProviderResult{State: SourceAvailable, EvaluatedSubjects: []SubjectRef{repository}, Facts: []CanonicalFact{validFact(), bad}}
	}
	withBadResult := func(mutate func(*FactProviderResult)) FactProviderResult {
		result := FactProviderResult{State: SourceAvailable, EvaluatedSubjects: []SubjectRef{repository}, Facts: []CanonicalFact{validFact()}}
		mutate(&result)
		return result
	}
	nonNumericMeasure := TableFactValue(FactTable{
		Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"risk"},
		Rows: []FactValueRow{{Fields: map[string]FactValue{"day": StringFactValue("2026-08-15"), "risk": StringFactValue("high")}}},
	})

	cases := []struct {
		name           string
		result         FactProviderResult
		want           factResultRejectionCause
		wantNonNumeric bool
	}{
		{name: "result state outside the vocabulary", want: factResultRejectedSourceState,
			result: withBadResult(func(r *FactProviderResult) { r.State = SourceState("invented") })},
		{name: "result state a provider must not mint", want: factResultRejectedSourceState,
			result: withBadResult(func(r *FactProviderResult) { r.State = SourcePruned })},
		{name: "zero observed timestamp", want: factResultRejectedObservedAt,
			result: withBadResult(func(r *FactProviderResult) { r.ObservedAt = &time.Time{} })},
		{name: "untrimmed version", want: factResultRejectedVersion,
			result: withBadResult(func(r *FactProviderResult) { r.Version = " v1" })},
		{name: "result state that cannot carry facts", want: factResultRejectedStateCarriesFacts,
			result: withBadResult(func(r *FactProviderResult) { r.State = SourceNoData })},
		{name: "evaluation of a subject outside the set", want: factResultRejectedEvaluatedSubject,
			result: withBadResult(func(r *FactProviderResult) { r.EvaluatedSubjects = []SubjectRef{repository, stranger} })},
		{name: "fact of another kind", want: factResultRejectedFactKind,
			result: withBadFact(func(f *CanonicalFact) { f.Kind = FactHealth })},
		{name: "fact subject outside the set", want: factResultRejectedFactSubjectOutsideSet,
			result: withBadFact(func(f *CanonicalFact) { f.Subject = stranger })},
		{name: "fact subject without a label", want: factResultRejectedFactSubject,
			result: withBadFact(func(f *CanonicalFact) { f.Subject = labelLess })},
		{name: "fact state outside the vocabulary", want: factResultRejectedFactSourceState,
			result: withBadFact(func(f *CanonicalFact) { f.SourceState = SourceState("invented") })},
		{name: "fact state that cannot carry facts", want: factResultRejectedFactStateCarriesFacts,
			result: withBadFact(func(f *CanonicalFact) { f.SourceState = SourceNoData })},
		{name: "fact without required evidence", want: factResultRejectedFact,
			result: withBadFact(func(f *CanonicalFact) { f.EvidenceRefIDs = nil })},
		{name: "fact with a non-numeric measure", want: factResultRejectedFact, wantNonNumeric: true,
			result: withBadFact(func(f *CanonicalFact) { f.Fields = map[string]FactValue{"daily": nonNumericMeasure} })},
	}

	covered := map[factResultRejectionCause]bool{}
	for _, testCase := range cases {
		covered[testCase.want] = true
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			bundle := CanonicalFactBundle{
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}
			allowed := map[string]SubjectRef{
				canonicalFactSubjectKey(repository): repository,
				canonicalFactSubjectKey(labelLess):  labelLess,
			}
			query := FactQuery{Kind: FactMetrics, Subjects: []SubjectRef{repository, labelLess}}

			rejection := mergeFactProviderResult(&bundle, capability, query, testCase.result, allowed, coverageDetailSpec{})

			if rejection == nil {
				t.Fatal("mergeFactProviderResult accepted the result")
			}
			if rejection.cause != testCase.want {
				t.Fatalf("cause = %q, want %q (refusal: %v)", rejection.cause, testCase.want, rejection)
			}
			var failure *FactReadFailure
			if !errors.As(rejection, &failure) {
				t.Fatalf("the refusal carries no FactReadFailure: %v", rejection)
			}
			if failure.State != SourceUnavailable || failure.Reason != factResultRejectedReason {
				t.Fatalf("failure = %+v, want state %q and the fixed sentence", failure, SourceUnavailable)
			}
			if state, reason := classifyFactReadError(rejection); state != SourceUnavailable || reason != factResultRejectedReason {
				t.Fatalf("classifyFactReadError = (%q, %q), want (%q, %q)", state, reason, SourceUnavailable, factResultRejectedReason)
			}
			if got := errors.Is(rejection, ErrFactTableMeasureNotNumeric); got != testCase.wantNonNumeric {
				t.Fatalf("errors.Is(ErrFactTableMeasureNotNumeric) = %v, want %v", got, testCase.wantNonNumeric)
			}
			if len(bundle.Facts) != 0 {
				t.Fatalf("a refused result left %d facts in the bundle", len(bundle.Facts))
			}
			if len(bundle.EvaluatedSubjects) != 0 {
				t.Fatalf("a refused result credited evaluations: %v", bundle.EvaluatedSubjects)
			}
			if len(bundle.Coverage.Sources) != 0 || len(bundle.Versions) != 0 || len(bundle.Watermarks) != 0 {
				t.Fatalf("a refused result wrote coverage or versions: %+v %v %v", bundle.Coverage.Sources, bundle.Versions, bundle.Watermarks)
			}
		})
	}

	for cause := range declaredFactResultRejectionCauses(t) {
		if !covered[cause] {
			t.Errorf("rejection cause %q is declared in fact_registry.go and has no case here", cause)
		}
	}
	for cause := range covered {
		if !declaredFactResultRejectionCauses(t)[cause] {
			t.Errorf("rejection cause %q is asserted here and not declared in fact_registry.go", cause)
		}
	}
}

func TestAnAcceptedResultStillCommitsFactsAndEvaluations(t *testing.T) {
	t.Parallel()

	repository := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:api", Label: "api"}
	capability := planCapability(FactMetrics, "metrics", SubjectRepository)
	bundle := CanonicalFactBundle{
		Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
	}
	result := FactProviderResult{
		State: SourceAvailable, Watermark: "wm", EvaluatedSubjects: []SubjectRef{repository},
		Facts: []CanonicalFact{{Kind: FactMetrics, Subject: repository, Fields: map[string]FactValue{"commits": IntegerFactValue(7)}}},
	}

	rejection := mergeFactProviderResult(&bundle, capability, FactQuery{Kind: FactMetrics, Subjects: []SubjectRef{repository}}, result,
		map[string]SubjectRef{canonicalFactSubjectKey(repository): repository}, coverageDetailSpec{})

	if rejection != nil {
		t.Fatalf("mergeFactProviderResult refused a valid result: %v", rejection)
	}
	if len(bundle.Facts) != 1 || !bundle.EvaluatedSubjects.covers(FactMetrics, repository) {
		t.Fatalf("facts = %d, evaluated = %v, want the fact and the evaluation committed", len(bundle.Facts), bundle.EvaluatedSubjects)
	}
	if bundle.Versions[FactMetrics] != "v1" || bundle.Watermarks[FactMetrics] != "wm" || len(bundle.Coverage.Sources) != 1 {
		t.Fatalf("versions = %v, watermarks = %v, coverage = %+v", bundle.Versions, bundle.Watermarks, bundle.Coverage.Sources)
	}
}
