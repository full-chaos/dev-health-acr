package v1

import (
	"strings"
	"testing"
)

// CHAOS-5442: EVERY REFUSAL-BASIS RULE THE VALIDATOR ENFORCES, EXERCISED FROM
// THE REJECTING SIDE.
//
// Three deletion arms of the 34123455192 battery removed three different
// rules from validateCompleteness -- the completeness/result mirror, the
// vocabulary-membership test, and the refusal-cannot-be-a-served-answer test
// -- and the whole suite stayed green for all three. That is ONE gap, not
// three: the bound table carries a single PastMax per field, so the field's
// only negative arm sets a REAL member on BOTH surfaces of a SERVED answer,
// which satisfies the mirror, satisfies membership, and is caught by two
// overlapping clauses at once. A rule family needs one negative per rule, and
// each negative must be attributable to the rule it names.
//
// Attribution here is by MESSAGE, the same oracle TestEveryBoundIsBreachable
// uses, and the messages are quoted narrowly enough to tell the clauses apart
// -- "cannot accompany" alone matches both the served-answer clause and the
// claimed-facts clause beside it, which is exactly how the served-answer arm
// survived.
func TestEveryRefusalBasisRuleRejectsFromItsOwnSide(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		breach func(*ContextFabricInvestigationResult)
		want   string
		why    string
	}{
		{
			name: "the completeness block disagrees with the result",
			breach: func(r *ContextFabricInvestigationResult) {
				r.RefusalBasis = ""
				r.Completeness.RefusalBasis = ContextFabricRefusalBasisMemberKindUnservable
			},
			want: "must equal the result's refusal_basis",
			why:  "two surfaces carry one fact, and a consumer reading only the self-contained disclosure block would otherwise be told a different story than one reading the result",
		},
		{
			name: "the basis is not a vocabulary member",
			breach: func(r *ContextFabricInvestigationResult) {
				r.RefusalBasis = ContextFabricRefusalBasis("member_kind_unservable_ish")
				r.Completeness.RefusalBasis = r.RefusalBasis
			},
			want: "is not a vocabulary member",
			why:  "the field is a closed vocabulary on the wire; an open one lets a consumer's switch fall through on a value no contract ever named",
		},
		{
			name: "a refusal claims to have served a complete answer",
			breach: func(r *ContextFabricInvestigationResult) {
				r.RefusalBasis = ContextFabricRefusalBasisMemberKindUnservable
				r.Completeness.RefusalBasis = r.RefusalBasis
			},
			want: "cannot accompany status",
			why:  "the gate refuses above retrieval, so a refused turn read no canonical fact; admitting this would let the field become decoration on an ordinary answer rather than a statement about the turn",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			r := buildFromTable(t, func(x answerBound) func(*ContextFabricInvestigationResult) { return x.Max })
			// The control, first and every time: a rejection means
			// nothing about THIS rule unless the document it was applied
			// to was accepted a line earlier.
			if err := r.Validate(); err != nil {
				t.Fatalf("the maximal fixture must validate before a breach is applied to it, got: %v", err)
			}
			testCase.breach(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("Validate accepted a document that %s -- %s", testCase.name, testCase.why)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("rejected for the WRONG reason:\n  got:  %v\n  want a message containing: %q\n  (%s)", err, testCase.want, testCase.why)
			}
		})
	}
}

// THE RECOGNISER'S TWO INTERPOLATED SEGMENTS ARE BOTH CHECKED, and a battery
// arm that replaced the whole membership test with `len(x) >= 0` -- constant
// true for any string -- survived, because every existing caller composes the
// sentence with real members and then asks whether it is recognised. Nothing
// asked the question from the other side.
//
// This is not a formatting nit. Everything that consults the service-authored
// registry is deciding whether a string may be DISPLACED, so a recogniser
// that accepts any kind and any basis lets a model-authored caveat that
// merely borrows this wording become undisplaceable and take a real caveat's
// place.
func TestTheRefusalBasisRecogniserRejectsNonMemberSegments(t *testing.T) {
	t.Parallel()
	realKind := ContextFabricSubjectRepository
	realBasis := ContextFabricRefusalBasisMemberKindUnservable
	genuine := ContextFabricRefusalBasisLimitation(realKind, realBasis)
	if !IsContextFabricRefusalBasisLimitation(genuine) {
		t.Fatalf("the sentence this package itself composes is not recognised: %q -- every arm below is meaningless if the positive control fails", genuine)
	}
	for _, testCase := range []struct {
		name     string
		sentence string
	}{
		{"a kind the registry does not name", ContextFabricRefusalBasisLimitation(ContextFabricSubjectKind("a_kind_no_vocabulary_names"), realBasis)},
		{"a basis the vocabulary does not name", ContextFabricRefusalBasisLimitation(realKind, ContextFabricRefusalBasis("a_basis_nobody_declared"))},
		{"neither segment is a member", ContextFabricRefusalBasisLimitation(ContextFabricSubjectKind("a_kind_no_vocabulary_names"), ContextFabricRefusalBasis("a_basis_nobody_declared"))},
		{"an empty kind", ContextFabricRefusalBasisLimitation("", realBasis)},
		{"an empty basis", ContextFabricRefusalBasisLimitation(realKind, "")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if IsContextFabricRefusalBasisLimitation(testCase.sentence) {
				t.Fatalf("recognised %q as a sentence this service composes -- it is not, and treating it as one makes a model-authored caveat undisplaceable", testCase.sentence)
			}
			if IsContextFabricServiceAuthoredLimitation(testCase.sentence) {
				t.Fatalf("the registry reports %q as service-authored, so the engine's displacement rule would protect a string this service never wrote", testCase.sentence)
			}
		})
	}
}
