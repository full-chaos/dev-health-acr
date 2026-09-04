package contextfabric

import (
	"encoding/json"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The wire mirrors of the requirement vocabularies must equal the domain's
// own, IN BOTH DIRECTIONS.
//
// One direction alone is the failure this guards against: checking only that
// every domain member reaches the wire lets a mirror entry outlive the member
// it mirrors, and checking only the reverse lets a new domain member ship with
// no wire representation and a validator that rejects every row carrying it.
//
// Table-driven over every mirror the plan requirement carries, rather than one
// test per vocabulary, because the failure mode is the same for all of them
// and a per-vocabulary test is one somebody forgets to add. The count check at
// the end is what makes THAT true: a mirror added to the wire without a row
// here fails, so this table cannot silently cover seven of eight.
func TestTheWireRequirementVocabulariesMirrorTheDomainInBothDirections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		domain []string
		wire   []string
	}{
		{"SubjectRole", vocabularyStrings(SubjectRoleVocabulary()), vocabularyStrings(contractsv1.ContextFabricSubjectRoleVocabulary())},
		{"AnswerObligationKind", vocabularyStrings(AnswerObligationKindVocabulary()), vocabularyStrings(contractsv1.ContextFabricAnswerObligationKindVocabulary())},
		{"ComputedObligationStep", vocabularyStrings(ComputedObligationStepVocabulary()), vocabularyStrings(contractsv1.ContextFabricComputedObligationStepVocabulary())},
		{"ComputedStepInputClass", vocabularyStrings(ComputedStepInputClassVocabulary()), vocabularyStrings(contractsv1.ContextFabricComputedStepInputClassVocabulary())},
		{"ComputedStepExecution", vocabularyStrings(ComputedStepExecutionVocabulary()), vocabularyStrings(contractsv1.ContextFabricComputedStepExecutionVocabulary())},
		{"CompletionScope", vocabularyStrings(CompletionScopeVocabulary()), vocabularyStrings(contractsv1.ContextFabricCompletionScopeVocabulary())},
		{"CompletionQuantifier", vocabularyStrings(CompletionQuantifierVocabulary()), vocabularyStrings(contractsv1.ContextFabricCompletionQuantifierVocabulary())},
		{"RequirementUnavailableReason", vocabularyStrings(RequirementUnavailableReasonVocabulary()), vocabularyStrings(contractsv1.ContextFabricRequirementUnavailableReasonVocabulary())},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			domain := map[string]bool{}
			for _, member := range testCase.domain {
				domain[member] = true
			}
			wire := map[string]bool{}
			for _, member := range testCase.wire {
				wire[member] = true
			}
			if len(domain) == 0 || len(wire) == 0 {
				t.Fatalf("%s: an empty vocabulary on either side makes both loops below vacuous", testCase.name)
			}
			for member := range domain {
				if !wire[member] {
					t.Errorf("%s %q exists in the domain and not on the wire; a requirement row naming it would be rejected by its own validator", testCase.name, member)
				}
			}
			for member := range wire {
				if !domain[member] {
					t.Errorf("%s wire mirror carries %q, which is not a domain member; a mirror entry has outlived the member it mirrors", testCase.name, member)
				}
			}
			if len(domain) != len(wire) {
				t.Fatalf("%s: domain has %d members, the wire mirror has %d", testCase.name, len(domain), len(wire))
			}
		})
	}
	// Every mirrored vocabulary the wire row carries must appear above. The
	// count is derived from the published surface rather than written down,
	// so adding a mirror without a row here fails instead of being covered
	// by a table that merely looks complete.
	if got, want := len(cases), contractsv1.ContextFabricPlanRequirementMirroredVocabularyCount; got != want {
		t.Fatalf("this table covers %d mirrored vocabularies but the wire row carries %d; add the missing row", got, want)
	}
}

// vocabularyStrings flattens any fixed-length vocabulary array to strings.
//
// One reflect-based helper rather than eight typed ones: the vocabularies
// return arrays of eight different named string types at five different
// lengths, and a typed helper per shape is seven more places to make the same
// mistake. It PANICS on a non-array or a non-string element rather than
// returning empty, because an empty result here would make both directions of
// the parity loop vacuous -- the exact failure this file exists to prevent.
func vocabularyStrings(array any) []string {
	value := reflect.ValueOf(array)
	if value.Kind() != reflect.Array {
		panic("vocabularyStrings needs a fixed-length array, got " + value.Kind().String())
	}
	out := make([]string, 0, value.Len())
	for i := 0; i < value.Len(); i++ {
		if value.Index(i).Kind() != reflect.String {
			panic("vocabularyStrings needs string members, got " + value.Index(i).Kind().String())
		}
		out = append(out, value.Index(i).String())
	}
	return out
}

// EVERY FIELD OF DerivedRequirement IS EITHER PROJECTED ONTO THE WIRE OR
// NAMED HERE AS DELIBERATELY OMITTED.
//
// The projection is a hand-written struct literal, so a field added to the
// derivation tomorrow is silently dropped: the code compiles, the tests pass,
// and the artifact quietly stops describing part of what was planned. This
// walks the domain type by reflection and fails on any field that is neither
// projected nor disclosed, which is the same discipline the answer bound table
// holds the result struct to.
//
// The omissions are stated with their reasons rather than merely listed,
// because an omission whose reason nobody wrote down is indistinguishable from
// an oversight the next reader will "fix".
func TestEveryDerivedRequirementFieldIsProjectedOrNamedOmitted(t *testing.T) {
	t.Parallel()
	omitted := map[string]string{
		"Dimensions": "what the planned EVIDENCE covers, derived from the serving fact kinds' own capability declarations. " +
			"It is a function of FactKinds, which the row already publishes, so publishing it too would put a derived value " +
			"beside its own input and let the two disagree on the wire.",
	}
	// The coordinate is embedded, and its three fields are projected
	// individually as obligation/role/subject. Naming it here rather than
	// walking into it keeps the two levels from being confused.
	projected := map[string]bool{
		"RequirementCoordinate": true,
		"Kind":                  true,
		"FactKinds":             true,
		"Step":                  true,
		"InputClass":            true,
		"InputFactKinds":        true,
		"StepExecution":         true,
		"Scope":                 true,
		"Quantifier":            true,
		"Unavailable":           true,
	}
	rowType := reflect.TypeOf(DerivedRequirement{})
	seen := map[string]bool{}
	for i := 0; i < rowType.NumField(); i++ {
		name := rowType.Field(i).Name
		seen[name] = true
		if projected[name] {
			continue
		}
		if reason, disclosed := omitted[name]; disclosed {
			t.Logf("deliberately not on the wire: %s -- %s", name, reason)
			continue
		}
		t.Errorf("DerivedRequirement.%s is neither projected onto the wire nor named as a deliberate omission; "+
			"the artifact would silently stop describing it", name)
	}
	// The other direction, which keeps both lists honest: an entry naming a
	// field that no longer exists is stale and hides the next real gap.
	for name := range projected {
		if !seen[name] {
			t.Errorf("projected list names %s, which DerivedRequirement no longer has", name)
		}
	}
	for name := range omitted {
		if !seen[name] {
			t.Errorf("omission list names %s, which DerivedRequirement no longer has", name)
		}
	}
}

// The projection must copy the derivation's values, not merely produce a
// well-formed row.
//
// A projection test that only asserts validity passes on a function that
// returns a constant. This drives the real derivation over a frame that
// produces both a read row and a computed row, then asserts field by field
// that what the wire carries is what the derivation decided.
func TestPlanRequirementsCarryTheDerivationsOwnValues(t *testing.T) {
	t.Parallel()
	rows := []DerivedRequirement{
		{
			RequirementCoordinate: RequirementCoordinate{
				Obligation: ObligationState, Role: SubjectRoleSubject, Subject: SubjectProject,
			},
			Kind:       ObligationKindRead,
			FactKinds:  []FactKind{FactHealth, FactStatus},
			Scope:      CompletionScopeSingleSubject,
			Quantifier: CompletionQuantifierAtLeastOne,
		},
		{
			RequirementCoordinate: RequirementCoordinate{
				Obligation: ObligationCount, Role: SubjectRoleMember, Subject: SubjectRepository,
			},
			Kind:           ObligationKindComputed,
			Step:           ComputedStepMembershipCardinality,
			InputClass:     ComputedInputResolvedMemberSet,
			StepExecution:  ComputedStepDeclaredOnly,
			Scope:          CompletionScopeEachMember,
			Quantifier:     CompletionQuantifierExact,
			InputFactKinds: nil,
		},
	}
	projected := PlanRequirementsFromDerived(rows)
	if len(projected) != len(rows) {
		t.Fatalf("projected %d rows from %d derived rows", len(projected), len(rows))
	}

	// ASSERT THE TWO ROWS DIFFER BEFORE ASSERTING EITHER IS RIGHT. A
	// projection that returned its first row twice would satisfy every
	// per-field check below against row 0 and nothing would notice.
	if projected[0].Requirement == projected[1].Requirement {
		t.Fatalf("both projected rows carry identity %q; the fixture cannot discriminate", projected[0].Requirement)
	}
	if projected[0].Kind == projected[1].Kind {
		t.Fatalf("both projected rows carry kind %q; the read/computed distinction is untested", projected[0].Kind)
	}

	read := projected[0]
	if read.Requirement != "state/subject/project" {
		t.Errorf("read row identity = %q, want state/subject/project", read.Requirement)
	}
	if read.Obligation != string(ObligationState) || read.Role != string(SubjectRoleSubject) || read.Subject != SubjectProject {
		t.Errorf("read row coordinate = %q/%q/%q, want state/subject/project", read.Obligation, read.Role, read.Subject)
	}
	if read.Kind != string(ObligationKindRead) {
		t.Errorf("read row kind = %q, want read", read.Kind)
	}
	if !reflect.DeepEqual(read.FactKinds, []FactKind{FactHealth, FactStatus}) {
		t.Errorf("read row fact kinds = %v, want [health status]", read.FactKinds)
	}
	if read.Step != "" || read.InputClass != "" || read.StepExecution != "" || read.InputFactKinds != nil {
		t.Errorf("read row carries computation fields: step=%q class=%q execution=%q inputs=%v",
			read.Step, read.InputClass, read.StepExecution, read.InputFactKinds)
	}
	if read.Scope != string(CompletionScopeSingleSubject) || read.Quantifier != string(CompletionQuantifierAtLeastOne) {
		t.Errorf("read row scope/quantifier = %q/%q", read.Scope, read.Quantifier)
	}

	computed := projected[1]
	if computed.Step != string(ComputedStepMembershipCardinality) {
		t.Errorf("computed row step = %q", computed.Step)
	}
	if computed.InputClass != string(ComputedInputResolvedMemberSet) {
		t.Errorf("computed row input class = %q", computed.InputClass)
	}
	if computed.StepExecution != string(ComputedStepDeclaredOnly) {
		t.Errorf("computed row step execution = %q", computed.StepExecution)
	}
	if len(computed.FactKinds) != 0 {
		t.Errorf("computed row carries serving fact kinds %v; a computation reads none of its own", computed.FactKinds)
	}
	for index, row := range projected {
		if err := row.Validate(); err != nil {
			t.Errorf("projected row %d does not validate: %v", index, err)
		}
	}
}

// The projection must not ALIAS the derivation's slices.
//
// A shared backing array lets a later mutation of either side reach through
// into the other, and the document is supposed to be a record of what was
// decided, not a live view of a mutable structure.
func TestPlanRequirementProjectionCopiesItsSlices(t *testing.T) {
	t.Parallel()
	kinds := []FactKind{FactHealth, FactStatus}
	row := DerivedRequirement{
		RequirementCoordinate: RequirementCoordinate{
			Obligation: ObligationState, Role: SubjectRoleSubject, Subject: SubjectProject,
		},
		Kind: ObligationKindRead, FactKinds: kinds,
		Scope: CompletionScopeSingleSubject, Quantifier: CompletionQuantifierAtLeastOne,
	}
	projected := PlanRequirementsFromDerived([]DerivedRequirement{row})
	kinds[0] = FactWorkload
	if projected[0].FactKinds[0] != FactHealth {
		t.Fatalf("mutating the derivation's slice changed the projected row to %v; the projection aliased it",
			projected[0].FactKinds)
	}
}

// nil and empty are DIFFERENT on the wire, and the difference carries meaning:
// a read row is told from a computed row by which kind list is PRESENT.
func TestPlanRequirementProjectionPreservesNilFactKinds(t *testing.T) {
	t.Parallel()
	row := DerivedRequirement{
		RequirementCoordinate: RequirementCoordinate{
			Obligation: ObligationCount, Role: SubjectRoleMember, Subject: SubjectRepository,
		},
		Kind: ObligationKindComputed, Step: ComputedStepMembershipCardinality,
		InputClass: ComputedInputResolvedMemberSet, StepExecution: ComputedStepServerExecuted,
		Scope: CompletionScopeEachMember, Quantifier: CompletionQuantifierExact,
	}
	encoded, err := json.Marshal(PlanRequirementsFromDerived([]DerivedRequirement{row})[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// A positive control in the same assertion: the key that SHOULD be
	// present proves the encoder ran and the probe can see keys at all.
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := decoded["input_class"]; !present {
		t.Fatal("input_class is absent from a computed row; the probe cannot detect key presence")
	}
	if _, present := decoded["fact_kinds"]; present {
		t.Errorf("fact_kinds is present on a computed row as %v; nil must be omitted, not emitted as []", decoded["fact_kinds"])
	}
	if _, present := decoded["input_fact_kinds"]; present {
		t.Errorf("input_fact_kinds is present on a resolved_member_set row; the step reads no fact")
	}
}
