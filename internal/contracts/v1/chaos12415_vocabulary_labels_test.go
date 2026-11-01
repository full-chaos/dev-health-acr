package v1

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12415. The vocabulary label tables are total in BOTH directions: a new
// value cannot ship unlabeled, and a label cannot outlive its value. The value
// sets below come from the production vocabulary or from the published schema,
// never from a list written here, so a test that compares a table with itself
// cannot pass.

type vocabularyEntry struct{ value, label string }

// contextFabricVocabularyEntries lists one table in a stable order.
func contextFabricVocabularyEntries(name ContextFabricVocabulary) []vocabularyEntry {
	entries := make([]vocabularyEntry, 0, len(contextFabricVocabularyLabels[name]))
	for value, label := range contextFabricVocabularyLabels[name] {
		entries = append(entries, vocabularyEntry{value: value, label: label})
	}
	slices.SortFunc(entries, func(a, b vocabularyEntry) int { return strings.Compare(a.value, b.value) })
	return entries
}

func closedVocabularyStrings[T ~string, A ~[]T](values A) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

// commonSchemaDefs reads the published $defs: the source of the vocabularies
// whose only production list is the schema (the OPEN ones), and of the closed
// ones Go declares as bare constants.
func commonSchemaDefs(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "jsonschema", "v1", "context_fabric_common.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Defs) == 0 {
		t.Fatal("the common schema declares no $defs: this test would measure nothing")
	}
	return document.Defs
}

// schemaValues reads a property's enum or x-documented-values, through items
// for an array and through a local $ref for a named def. A property that
// declares neither is a failure: an empty list here would make the totality
// check below pass over nothing.
func schemaValues(t *testing.T, defs map[string]any, def, property string) []string {
	t.Helper()
	object, ok := defs[def].(map[string]any)
	if !ok {
		t.Fatalf("schema has no $defs/%s", def)
	}
	properties, _ := object["properties"].(map[string]any)
	field, ok := properties[property].(map[string]any)
	if !ok {
		t.Fatalf("schema $defs/%s has no property %q", def, property)
	}
	if items, isArray := field["items"].(map[string]any); isArray {
		field = items
	}
	if ref, isRef := field["$ref"].(string); isRef {
		target, found := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if !found {
			t.Fatalf("schema $defs/%s.%s points at %q, which is not a local def", def, property, ref)
		}
		field = target
	}
	for _, key := range []string{"enum", "x-documented-values"} {
		if values, listed := field[key].([]any); listed {
			out := make([]string, 0, len(values))
			for _, value := range values {
				if text, isText := value.(string); isText {
					out = append(out, text)
				}
			}
			if len(out) == 0 {
				t.Fatalf("schema $defs/%s.%s lists no values", def, property)
			}
			return out
		}
	}
	t.Fatalf("schema $defs/%s.%s declares no vocabulary", def, property)
	return nil
}

func vocabularySources(t *testing.T) map[ContextFabricVocabulary][][]string {
	t.Helper()
	defs := commonSchemaDefs(t)
	schema := func(def, property string) []string { return schemaValues(t, defs, def, property) }
	kinds := ContextFabricSubjectKindVocabulary()
	facts := ContextFabricFactKindVocabulary()
	outcomes := ContextFabricPlanRequirementOutcomeVocabulary()
	impacts := ContextFabricAnswerImpactKindVocabulary()
	states := ContextFabricAnswerCompletenessStateVocabulary()
	dispositions := ContextFabricStructureDispositionVocabulary()
	windows := ContextFabricRelativeWindowIDVocabulary()
	basis := make([]string, 0, len(contextFabricCohortRankingBasisLabels))
	for label := range contextFabricCohortRankingBasisLabels {
		if strings.HasPrefix(label, "investment_mix.") {
			basis = append(basis, label)
		}
	}
	signals := make([]string, 0, len(contextFabricCohortMemberDriverWeights))
	for signal := range contextFabricCohortMemberDriverWeights {
		signals = append(signals, signal)
	}
	return map[ContextFabricVocabulary][][]string{
		ContextFabricVocabularyAnswerStatus:      {schema("AnswerCompleteness", "terminal_status")},
		ContextFabricVocabularyCompletenessState: {closedVocabularyStrings(states[:]), schema("AnswerCompleteness", "state")},
		ContextFabricVocabularyRequirementOutcome: {
			closedVocabularyStrings(outcomes[:]), schema("PlanRequirementOutcomeRow", "outcome"),
		},
		ContextFabricVocabularyAnswerImpact:            {closedVocabularyStrings(impacts[:]), schema("PlanRequirementOutcomeRow", "impact")},
		ContextFabricVocabularyCandidateState:          {schema("SubjectCandidate", "state")},
		ContextFabricVocabularyPriorReceiptDisposition: {schema("PriorSubjectReceiptDispositionEntry", "disposition")},
		ContextFabricVocabularyStructureDisposition:    {closedVocabularyStrings(dispositions[:])},
		ContextFabricVocabularySubjectKind: {
			closedVocabularyStrings(kinds[:]), schema("Cohort", "kind"), schema("CoverageDetail", "kind"),
		},
		ContextFabricVocabularySubjectKindPlural:      {closedVocabularyStrings(kinds[:])},
		ContextFabricVocabularyMemberOutcome:          {schema("CohortMember", "outcome")},
		ContextFabricVocabularyMemberDataCompleteness: {schema("CohortMember", "data_completeness")},
		ContextFabricVocabularyRankingSignal: {
			signals, schema("CohortMember", "missing_signals"), schema("CohortMemberDriver", "signal"),
		},
		ContextFabricVocabularyThresholdLabel: {basis, schema("CohortMemberDriver", "threshold_labels")},
		ContextFabricVocabularyDriverWindow:   {schema("CohortMemberDriver", "window")},
		ContextFabricVocabularyDriverStanding: {schema("DriverJudgment", "standing")},
		ContextFabricVocabularyFactKind: {
			closedVocabularyStrings(facts[:]), schema("Finding", "kind"), schema("ClaimedFact", "kind"),
		},
		ContextFabricVocabularyWindowRelativeID: {closedVocabularyStrings(windows[:]), schema("EffectiveEvidenceWindow", "relative_id")},
		ContextFabricVocabularyWindowProvenance: {schema("EffectiveEvidenceWindow", "provenance")},
	}
}

func TestEveryVocabularyNameHasATableAndASource(t *testing.T) {
	sources := vocabularySources(t)
	names := ContextFabricVocabularyNames()
	if len(names) == 0 {
		t.Fatal("no vocabulary is published")
	}
	if len(contextFabricVocabularyLabels) != len(names) {
		t.Errorf("%d tables for %d vocabularies", len(contextFabricVocabularyLabels), len(names))
	}
	for _, name := range names {
		if _, ok := contextFabricVocabularyLabels[name]; !ok {
			t.Errorf("vocabulary %q has no table", name)
		}
		if _, ok := sources[name]; !ok {
			t.Errorf("vocabulary %q has no value source in this test, so nothing proves it total", name)
		}
	}
}

func TestEveryVocabularyValueHasALabel(t *testing.T) {
	for name, sources := range vocabularySources(t) {
		for _, values := range sources {
			if len(values) == 0 {
				t.Errorf("vocabulary %q: a value source is empty, so totality measured nothing", name)
			}
			for _, value := range values {
				label, ok := ContextFabricVocabularyLabel(name, value)
				if !ok {
					t.Errorf("vocabulary %q value %q has no label: add it with the value", name, value)
					continue
				}
				if strings.TrimSpace(label) != label || !stringLengthBetween(label, 1, ContextFabricCoverageDetailLabelMaxLength) {
					t.Errorf("vocabulary %q value %q: label %q is not a valid label", name, value, label)
				}
			}
		}
	}
}

// The other direction: a label whose value left the vocabulary is a word for
// nothing. Every table row must name a value of at least one source.
func TestNoVocabularyLabelOutlivesItsValue(t *testing.T) {
	sources := vocabularySources(t)
	for _, name := range ContextFabricVocabularyNames() {
		var known []string
		for _, values := range sources[name] {
			known = append(known, values...)
		}
		for _, entry := range contextFabricVocabularyEntries(name) {
			if !slices.Contains(known, entry.value) {
				t.Errorf("vocabulary %q labels %q, which no source declares", name, entry.value)
			}
		}
	}
}

func TestAnUnknownVocabularyValueHasNoLabel(t *testing.T) {
	if label, ok := ContextFabricVocabularyLabel(ContextFabricVocabularyAnswerStatus, "not_a_status"); ok {
		t.Errorf("an unknown value got label %q: a consumer takes its generic path on a miss", label)
	}
	if label, ok := ContextFabricVocabularyLabel("not_a_vocabulary", "complete"); ok {
		t.Errorf("an unknown vocabulary got label %q", label)
	}
}

// A kind reads differently one and many, and the plural is served, so no
// consumer adds an "s" ("Ranked repositorys").
func TestEverySubjectKindServesItsPlural(t *testing.T) {
	for _, kind := range ContextFabricSubjectKindVocabulary() {
		one, ok := ContextFabricVocabularyLabel(ContextFabricVocabularySubjectKind, string(kind))
		if !ok {
			t.Fatalf("kind %q has no singular", kind)
		}
		many, ok := ContextFabricVocabularyLabel(ContextFabricVocabularySubjectKindPlural, string(kind))
		if !ok {
			t.Fatalf("kind %q has no plural", kind)
		}
		if one == many {
			t.Errorf("kind %q reads %q in both forms", kind, one)
		}
		if strings.HasSuffix(many, "ys") {
			t.Errorf("kind %q plural %q is a consumer's pluralizer, not a word", kind, many)
		}
	}
	if got, _ := ContextFabricVocabularyLabel(ContextFabricVocabularySubjectKindPlural, string(ContextFabricSubjectRepository)); got != "repositories" {
		t.Errorf("repository plural = %q, want repositories", got)
	}
}

// Fact kinds are not a second table: a chip and a coverage sentence say the
// same word for the same kind. Likewise a ranking signal and the noun the
// answer prose already uses for it.
func TestSharedVocabulariesReadTheExistingRegistryWord(t *testing.T) {
	for _, kind := range ContextFabricFactKindVocabulary() {
		got, ok := ContextFabricVocabularyLabel(ContextFabricVocabularyFactKind, string(kind))
		if !ok || got != ContextFabricFactKindLabel(kind) {
			t.Errorf("fact kind %q reads %q, the registry says %q", kind, got, ContextFabricFactKindLabel(kind))
		}
	}
	for signal, noun := range contextFabricCohortSignalNouns {
		got, ok := ContextFabricVocabularyLabel(ContextFabricVocabularyRankingSignal, signal)
		if !ok || got != noun {
			t.Errorf("signal %q reads %q, the prose noun is %q", signal, got, noun)
		}
	}
}

// Two values a reader must tell apart may not share a word (North Star check
// 12: unknown, stale, no data, not applicable and zero stay distinct; check 3:
// status is not completion). The tables that merge values on purpose are named.
func TestNoVocabularyFoldsTwoValuesIntoOneWord(t *testing.T) {
	for _, name := range ContextFabricVocabularyNames() {
		seen := map[string]string{}
		for _, entry := range contextFabricVocabularyEntries(name) {
			if other, duplicate := seen[entry.label]; duplicate {
				if name == ContextFabricVocabularyFactKind && sameFindingAndFactKind(entry.value, other) {
					continue
				}
				t.Errorf("vocabulary %q: %q and %q both read %q", name, entry.value, other, entry.label)
			}
			seen[entry.label] = entry.value
		}
	}
}

// operational_deficiency (a finding) and operational_deficiencies (a claimed
// fact) are one thing in two published spellings.
func sameFindingAndFactKind(a, b string) bool {
	return strings.HasPrefix(a, "operational_deficienc") && strings.HasPrefix(b, "operational_deficienc")
}

var vocabularyKeyShape = regexp.MustCompile(`^[a-z][a-z_]*:[^\s:]+$`)

func TestEveryVocabularyLabelKeyHasTheServedShape(t *testing.T) {
	for _, name := range ContextFabricVocabularyNames() {
		for _, entry := range contextFabricVocabularyEntries(name) {
			key := ContextFabricVocabularyLabelKey(name, entry.value)
			if !vocabularyKeyShape.MatchString(key) {
				t.Errorf("key %q does not have the shape <vocabulary>:<value>", key)
			}
			if len([]rune(key)) > ContextFabricVocabularyLabelKeyMaxLength {
				t.Errorf("key %q is over the key bound", key)
			}
		}
	}
}

// The map holds only values in one result, so the whole registry is its
// ceiling. Stated as a measurement against the serving budget: the registry
// must stay a small share of the 65,536 bytes an answer gets.
func TestTheWholeRegistryFitsInASmallShareOfTheAnswerBudget(t *testing.T) {
	const budget = 65536
	all := map[string]string{}
	for _, name := range ContextFabricVocabularyNames() {
		for _, entry := range contextFabricVocabularyEntries(name) {
			all[ContextFabricVocabularyLabelKey(name, entry.value)] = entry.label
		}
	}
	if len(all) == 0 {
		t.Fatal("the registry is empty: this measured nothing")
	}
	encoded, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > budget/8 {
		t.Errorf("the whole registry is %d bytes (%d entries), over an eighth of the %d-byte answer budget", len(encoded), len(all), budget)
	}
	t.Logf("whole registry: %d entries, %d bytes, %.1f%% of %d", len(all), len(encoded), float64(len(encoded))*100/budget, budget)
}

func TestVocabularyLabelsValidateKeyShapeAndLabelLength(t *testing.T) {
	long := strings.Repeat("a", ContextFabricCoverageDetailLabelMaxLength)
	cases := map[string]struct {
		labels map[string]string
		ok     bool
	}{
		"absent":                     {nil, true},
		"empty":                      {map[string]string{}, true},
		"a served key":               {map[string]string{"answer_status:partial": "partial answer"}, true},
		"a dotted value":             {map[string]string{"ranking_signal:health.compounding_risk": "health risk"}, true},
		"a label at the bound":       {map[string]string{"answer_status:partial": long}, true},
		"a vocabulary not yet known": {map[string]string{"a_new_vocabulary:a_new_value": "words"}, true},
		"no separator":               {map[string]string{"answer_status": "words"}, false},
		"no vocabulary":              {map[string]string{":partial": "words"}, false},
		"no value":                   {map[string]string{"answer_status:": "words"}, false},
		"a vocabulary opening with an underscore":    {map[string]string{"_answer_status:partial": "words"}, false},
		"an upper-case letter inside the vocabulary": {map[string]string{"answer_Status:partial": "words"}, false},
		"whitespace in the vocabulary":               {map[string]string{"answer status:partial": "words"}, false},
		"an upper-case vocabulary":                   {map[string]string{"Answer_status:partial": "words"}, false},
		"whitespace in the value":                    {map[string]string{"answer_status:par tial": "words"}, false},
		"a second separator":                         {map[string]string{"answer_status:par:tial": "words"}, false},
		"a key over the bound":                       {map[string]string{"answer_status:" + strings.Repeat("v", ContextFabricVocabularyLabelKeyMaxLength): "words"}, false},
		"an empty label":                             {map[string]string{"answer_status:partial": ""}, false},
		"an untrimmed label":                         {map[string]string{"answer_status:partial": " words"}, false},
		"a label over the bound":                     {map[string]string{"answer_status:partial": long + "a"}, false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateVocabularyLabels(test.labels)
			if test.ok && err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if !test.ok && err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestTheMapHoldsAtMostItsEntryBound(t *testing.T) {
	labels := make(map[string]string, ContextFabricVocabularyLabelsMaxEntries+1)
	for index := 0; index <= ContextFabricVocabularyLabelsMaxEntries; index++ {
		labels["fact_kind:value_"+strconv.Itoa(index)] = "words"
	}
	if validateVocabularyLabels(labels) == nil {
		t.Fatal("a map over the entry bound validated")
	}
	delete(labels, "fact_kind:value_0")
	if err := validateVocabularyLabels(labels); err != nil {
		t.Fatalf("a map at the entry bound was rejected: %v", err)
	}
}

// A bad map reaches a reader through either document, so both real validators
// refuse it, the write bounds and the read bounds alike.
func TestTheRealValidatorsRefuseABadMap(t *testing.T) {
	bad := map[string]string{"no_separator": "words"}
	good := map[string]string{"answer_status:partial": "partial answer"}

	projection := validAnswerProjection()
	if err := projection.Validate(); err != nil {
		t.Fatalf("the fixture must validate first, or this proves nothing: %v", err)
	}
	projection.VocabularyLabels = good
	if err := projection.Validate(); err != nil {
		t.Fatalf("a projection with a good map was refused: %v", err)
	}
	projection.VocabularyLabels = bad
	if err := projection.Validate(); err == nil || !strings.Contains(err.Error(), "vocabulary_labels") {
		t.Errorf("Validate on a projection with a bad map = %v", err)
	}
	if err := projection.validateRead(); err == nil || !strings.Contains(err.Error(), "vocabulary_labels") {
		t.Errorf("validateRead on a projection with a bad map = %v", err)
	}

	result := chaos5405CensusBase(t)
	if err := result.Validate(); err != nil {
		t.Fatalf("the fixture must validate first, or this proves nothing: %v", err)
	}
	result.VocabularyLabels = good
	if err := result.Validate(); err != nil {
		t.Fatalf("a result with a good map was refused: %v", err)
	}
	result.VocabularyLabels = bad
	if err := result.Validate(); err == nil || !strings.Contains(err.Error(), "vocabulary_labels") {
		t.Errorf("Validate on a result with a bad map = %v", err)
	}
	if err := result.ValidateStored(); err == nil || !strings.Contains(err.Error(), "vocabulary_labels") {
		t.Errorf("ValidateStored on a result with a bad map = %v", err)
	}
}

// Absent stays absent: a nil map marshals to no key at all, which is what an
// old pin needs while nothing emits it.
func TestAnAbsentMapIsNotOnTheWire(t *testing.T) {
	encoded, err := json.Marshal(chaos5405CensusBase(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "vocabulary_labels") {
		t.Fatal("an answer with no map still carries the key: every old pin would refuse it")
	}
}
