package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func failureDetailsOf(coverage Coverage) []CoverageDetail {
	var details []CoverageDetail
	for _, detail := range coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisModelFailed {
			details = append(details, detail)
		}
	}
	return details
}

// One row per served failure class, with the expected hint written out here,
// not read from the producer.
func TestADegradedAnswerNamesTheFailureClassAndWhetherARetryMayHelp(t *testing.T) {
	cases := []struct {
		class SynthesisFailureClass
		want  contractsv1.ContextFabricRetryHint
	}{
		{SynthesisFailureModelOutputInvalid, contractsv1.ContextFabricRetryMaySucceed},
		{SynthesisFailureReceiptUnrecorded, contractsv1.ContextFabricRetryMaySucceed},
		{SynthesisFailureCallNotCompleted, contractsv1.ContextFabricRetryMaySucceed},
		{SynthesisFailureRejected, contractsv1.ContextFabricRetryUnlikelyToHelp},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.class), func(t *testing.T) {
			result, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, largeSynthesisInputFixture(3), &SynthesisFailure{Class: testCase.class, cause: errors.New("x")})
			if err != nil {
				t.Fatalf("ComposeDegraded() error = %v", err)
			}
			details := failureDetailsOf(result.Coverage)
			if len(details) != 1 || details[0].FailureClass != testCase.class || details[0].RetryHint != testCase.want || details[0].Degrading {
				t.Fatalf("failure details = %+v, want one non-degrading %s with %s", details, testCase.class, testCase.want)
			}
			if details[0].DetailID != "cov-01" {
				t.Fatalf("detail id = %q, want the served id", details[0].DetailID)
			}
			if err := details[0].Validate(); err != nil {
				t.Fatalf("detail does not validate: %v", err)
			}
			if result.Status != InvestigationDegraded || !result.Coverage.Partial || !IsSynthesisModelFailureAnswer(result) {
				t.Fatalf("status = %q partial = %v, want a degraded partial answer recognised as a model failure", result.Status, result.Coverage.Partial)
			}
		})
	}
}

// A class that has no retry hint ends the answer with an error: no default.
func TestAFailureClassWithoutARetryHintIsAnError(t *testing.T) {
	if _, err := contractsv1.ContextFabricRetryHintFor("unknown_class"); err == nil {
		t.Fatal("ContextFabricRetryHintFor accepted an unknown class")
	}
	_, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, largeSynthesisInputFixture(3), &SynthesisFailure{Class: "unknown_class", cause: errors.New("x")})
	if err == nil {
		t.Fatal("ComposeDegraded served an answer for a class with no retry hint")
	}
}

// With claims the answer carries the facts-only sentence of the failure; with
// none it carries the no-facts sentence. Never both.
func TestTheFailureLimitationMatchesWhetherFactsWereShown(t *testing.T) {
	withFacts, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, largeSynthesisInputFixture(3), &SynthesisFailure{Class: SynthesisFailureRejected, cause: errors.New("x")})
	if err != nil {
		t.Fatal(err)
	}
	if len(withFacts.ClaimedFacts) == 0 {
		t.Fatal("fixture minted no claims, want the facts-only case")
	}
	if !slices.Contains(withFacts.Limitations, contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation) || slices.Contains(withFacts.Limitations, contractsv1.ContextFabricSynthesisFailedNoFactsLimitation) {
		t.Fatalf("limitations = %q, want the facts-only sentence alone", withFacts.Limitations)
	}
	empty := largeSynthesisInputFixture(3)
	empty.Facts.Facts = nil
	withoutFacts, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, empty, &SynthesisFailure{Class: SynthesisFailureRejected, cause: errors.New("x")})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(withoutFacts.Limitations, contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation) || !slices.Contains(withoutFacts.Limitations, contractsv1.ContextFabricSynthesisFailedNoFactsLimitation) {
		t.Fatalf("limitations = %q, want the no-facts sentence alone", withoutFacts.Limitations)
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(contractsv1.ContextFabricSynthesisFailedNoFactsLimitation) {
		t.Fatal("the no-facts sentence is not service-authored: a later disclosure could displace it")
	}
}

// The code marks the answer, and the fixed failure warning marks it when the
// code could not be filed. A detail on an answer that is not degraded, another
// code and no marker at all are not model-failure answers.
func TestAModelFailureAnswerIsRecognisedByItsCodeOrItsWarning(t *testing.T) {
	detail, err := newSynthesisModelFailureDetail(SynthesisFailureModelOutputInvalid)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		result InvestigationResult
		want   bool
	}{
		"degraded with the code":        {InvestigationResult{Status: InvestigationDegraded, Coverage: Coverage{Details: []CoverageDetail{detail}}}, true},
		"degraded with the warning":     {InvestigationResult{Status: InvestigationDegraded, Warnings: []string{synthesisFailureWarning(SynthesisFailureModelOutputInvalid)}}, true},
		"complete with the warning":     {InvestigationResult{Status: InvestigationComplete, Warnings: []string{synthesisFailureWarning(SynthesisFailureModelOutputInvalid)}}, false},
		"degraded with another warning": {InvestigationResult{Status: InvestigationDegraded, Warnings: []string{"a coverage warning"}}, false},
		"complete with the code":        {InvestigationResult{Status: InvestigationComplete, Coverage: Coverage{Details: []CoverageDetail{detail}}}, false},
		"degraded with another code":    {InvestigationResult{Status: InvestigationDegraded, Coverage: Coverage{Details: []CoverageDetail{{Code: contractsv1.ContextFabricCoverageDetailSynthesisInputBounded}}}}, false},
		"degraded with neither":         {InvestigationResult{Status: InvestigationDegraded}, false},
	}
	for name, testCase := range cases {
		if got := IsSynthesisModelFailureAnswer(testCase.result); got != testCase.want {
			t.Errorf("%s: IsSynthesisModelFailureAnswer = %v, want %v", name, got, testCase.want)
		}
	}
}

func TestTheCoverageCapKeepsTheModelFailureDetail(t *testing.T) {
	var details []CoverageDetail
	for i := 0; i < 120; i++ {
		details = append(details, syntheticNonDegradingDetail(i))
	}
	detail, err := newSynthesisModelFailureDetail(SynthesisFailureRejected)
	if err != nil {
		t.Fatal(err)
	}
	result := &InvestigationResult{Status: InvestigationDegraded, Coverage: Coverage{Details: append(details, detail)}}

	capCoverageEntriesToWriteBound(result)

	if len(result.Coverage.Details) != contractsv1.ContextFabricCoverageEntriesMaxCount || len(failureDetailsOf(result.Coverage)) != 1 || !IsSynthesisModelFailureAnswer(*result) {
		t.Fatalf("details = %d, failure details = %d, want the cap at %d keeping the failure detail", len(result.Coverage.Details), len(failureDetailsOf(result.Coverage)), contractsv1.ContextFabricCoverageEntriesMaxCount)
	}
}

func TestTheModelFailureDetailRequiresBothFieldsAndNothingElse(t *testing.T) {
	one := 1
	valid, err := newSynthesisModelFailureDetail(SynthesisFailureRejected)
	if err != nil {
		t.Fatal(err)
	}
	valid.DetailID = "cov-01"
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid detail: %v", err)
	}
	cases := map[string]func(*CoverageDetail){
		"no class":      func(d *CoverageDetail) { d.FailureClass = "" },
		"no hint":       func(d *CoverageDetail) { d.RetryHint = "" },
		"unknown class": func(d *CoverageDetail) { d.FailureClass = "other" },
		"unknown hint":  func(d *CoverageDetail) { d.RetryHint = "other" },
		"a fact kind":   func(d *CoverageDetail) { d.FactKind = FactWork },
		"declared":      func(d *CoverageDetail) { d.Declared = &one; d.Served = &one },
		"degrading":     func(d *CoverageDetail) { d.Degrading = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			detail := valid
			mutate(&detail)
			if err := detail.Validate(); err == nil {
				t.Fatalf("detail %+v validated, want it refused", detail)
			}
		})
	}
	other := CoverageDetail{DetailID: "cov-01", Source: "s", Code: contractsv1.ContextFabricCoverageDetailGraphNoMemberFound, Kind: SubjectProject, Label: "x", FailureClass: SynthesisFailureRejected}
	if err := other.Validate(); err == nil {
		t.Fatal("another code carrying failure_class validated")
	}
}

// Coverage whose degraded_reasons have no paired degrading detail cannot take
// a server detail: the failure answer is still recognised, by its warning, and
// the miss is logged at error level with the class.
func TestAModelFailureWhoseDetailCannotBeFiledIsStillRecognisedAndLogged(t *testing.T) {
	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	input := largeSynthesisInputFixture(3)
	input.Facts.Coverage.DegradedReasons = []string{"a read failed"}

	result, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, input, &SynthesisFailure{Class: SynthesisFailureRejected, cause: errors.New("x")})

	if err != nil {
		t.Fatalf("ComposeDegraded() error = %v", err)
	}
	if len(failureDetailsOf(result.Coverage)) != 0 {
		t.Fatalf("details = %+v, want no failure detail filed over unpaired coverage", result.Coverage.Details)
	}
	if !IsSynthesisModelFailureAnswer(result) {
		t.Fatal("the failure answer without its code is not recognised: it could be reused as a good answer")
	}
	line := logs.String()
	if !strings.Contains(line, "level=ERROR") || !strings.Contains(line, "model failure coverage detail not filed") || !strings.Contains(line, "failure_class=synthesis_rejected") {
		t.Fatalf("log = %q, want one error line naming the class", line)
	}
}

// The validation-stage degrade keeps the coverage the answer already carries,
// so the failure detail the composer minted is filed onto it, and with no
// canonical value kept the composed no-facts sentence is carried too.
func TestTheValidationStageDegradeCarriesTheFailureDetailAndTheNoFactsLimitation(t *testing.T) {
	engine, _, result, basis := validationStageFixture(t)
	result.Limitations = []string{"server caveat"}
	result.Coverage.Details = nil

	out, ok := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis)

	if !ok {
		t.Fatal("not degraded")
	}
	details := failureDetailsOf(out.Coverage)
	if len(details) != 1 || details[0].FailureClass != SynthesisFailureRejected || details[0].RetryHint != contractsv1.ContextFabricRetryUnlikelyToHelp || details[0].DetailID != "cov-01" {
		t.Fatalf("failure details = %+v, want one rejected/retry_unlikely_to_help with the served id", details)
	}
	if !IsSynthesisModelFailureAnswer(out) || !slices.Contains(out.Limitations, contractsv1.ContextFabricSynthesisFailedNoFactsLimitation) {
		t.Fatalf("limitations = %q, want the no-facts sentence beside the server's own", out.Limitations)
	}
	for _, detail := range out.Coverage.Details {
		if err := detail.Validate(); err != nil {
			t.Fatalf("detail %+v does not validate: %v", detail, err)
		}
	}
}

// The degrade runs after the entry cap, so it caps again: a coverage already at
// the bound still ends at the bound with the failure detail kept.
func TestTheValidationStageDegradeKeepsTheCoverageAtTheBound(t *testing.T) {
	engine, _, result, basis := validationStageFixture(t)
	var details []CoverageDetail
	for i := 0; i < contractsv1.ContextFabricCoverageEntriesMaxCount; i++ {
		d := syntheticNonDegradingDetail(i)
		d.DetailID = fmt.Sprintf("cov-%02d", i+1)
		details = append(details, d)
	}
	result.Coverage.Details = details

	out, ok := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis)

	if !ok {
		t.Fatal("not degraded")
	}
	if len(out.Coverage.Details) != contractsv1.ContextFabricCoverageEntriesMaxCount || len(failureDetailsOf(out.Coverage)) != 1 {
		t.Fatalf("details = %d, failure details = %d, want the bound kept with the failure detail", len(out.Coverage.Details), len(failureDetailsOf(out.Coverage)))
	}
}

// 100 degrading details fill the bound and the failure detail never yields:
// the last degrading entry goes with its reason, the answer says how many were
// left out, the degrading rows still pair with the reasons, and the bound holds.
func TestTheCoverageCapNeverDropsAProtectedRowAndDisclosesTheDegradingRowsItDrops(t *testing.T) {
	var details []CoverageDetail
	var reasons []string
	for i := 0; i < contractsv1.ContextFabricCoverageEntriesMaxCount; i++ {
		detail := syntheticDegradingDetail(i)
		details = append(details, detail)
		reasons = append(reasons, detail.Raw)
	}
	failure, err := newSynthesisModelFailureDetail(SynthesisFailureRejected)
	if err != nil {
		t.Fatal(err)
	}
	result := &InvestigationResult{Status: InvestigationDegraded, Coverage: Coverage{Details: append(details, failure), DegradedReasons: reasons}}

	omitted := capCoverageEntriesToWriteBound(result)

	if len(result.Coverage.Details) != contractsv1.ContextFabricCoverageEntriesMaxCount || omitted != 1 {
		t.Fatalf("details = %d omitted = %d, want the bound with one entry left out", len(result.Coverage.Details), omitted)
	}
	if len(failureDetailsOf(result.Coverage)) != 1 || !IsSynthesisModelFailureAnswer(*result) {
		t.Fatal("the failure detail yielded: a failed answer would look good")
	}
	var raws []string
	for _, detail := range result.Coverage.Details {
		if detail.Degrading {
			raws = append(raws, detail.Raw)
		}
	}
	if len(raws) != 99 || !slices.Equal(raws, result.Coverage.DegradedReasons) || slices.Contains(raws, details[99].Raw) {
		t.Fatalf("degrading rows = %d, reasons = %d: want the last one dropped from both, the rest paired in order", len(raws), len(result.Coverage.DegradedReasons))
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricCoverageEntriesDroppedLimitation(1)) || !contractsv1.IsContextFabricServiceAuthoredLimitation(contractsv1.ContextFabricCoverageEntriesDroppedLimitation(1)) {
		t.Fatalf("limitations = %q, want the dropped-entries disclosure with the count", result.Limitations)
	}
}

func TestTheDroppedEntriesDisclosureIsOnlyTheComposedSentence(t *testing.T) {
	if !contractsv1.IsContextFabricCoverageEntriesDroppedLimitation(contractsv1.ContextFabricCoverageEntriesDroppedLimitation(7)) {
		t.Fatal("the composed sentence is not recognised")
	}
	for _, other := range []string{contractsv1.ContextFabricCoverageEntriesDroppedLimitation(0), "Coverage reported more entries than an answer can carry, so the last x degraded coverage entries and their reasons were left out.", "Coverage reported more entries than an answer can carry, so the last 07 degraded coverage entries and their reasons were left out.", "a model caveat"} {
		if contractsv1.IsContextFabricCoverageEntriesDroppedLimitation(other) {
			t.Fatalf("%q recognised as the dropped-entries disclosure", other)
		}
	}
}

// A nonempty fact bundle with no room for a claim reads values and carries
// none: the sentence must not say none were read.
func TestTheNoFactsSentenceDoesNotClaimNothingWasReadWhenNoClaimFits(t *testing.T) {
	input := largeSynthesisInputFixture(3)
	input.Allocation.MaxItems = 1
	result, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, input, &SynthesisFailure{Class: SynthesisFailureRejected, cause: errors.New("x")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ClaimedFacts) != 0 || len(input.Facts.Facts) == 0 {
		t.Fatalf("claims = %d facts read = %d, want a nonempty bundle with no claim carried", len(result.ClaimedFacts), len(input.Facts.Facts))
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisFailedNoFactsLimitation) {
		t.Fatalf("limitations = %q, want the sentence for a failure that carries no value", result.Limitations)
	}
	if strings.Contains(contractsv1.ContextFabricSynthesisFailedNoFactsLimitation, "read") {
		t.Fatalf("the sentence %q says nothing was read, which is false when values were read and none fit", contractsv1.ContextFabricSynthesisFailedNoFactsLimitation)
	}
}

// The cap's own limitation can displace a caveat when the list is full, and
// the displaced count is kept.
func TestTheCapKeepsTheLimitationDisplacementCountWhenItsDisclosureDisplacesACaveat(t *testing.T) {
	var details []CoverageDetail
	var reasons []string
	for i := 0; i < contractsv1.ContextFabricCoverageEntriesMaxCount+3; i++ {
		detail := syntheticDegradingDetail(i)
		details = append(details, detail)
		reasons = append(reasons, detail.Raw)
	}
	var limitations []string
	for i := 0; i < contractsv1.ContextFabricLimitationsMaxCount; i++ {
		limitations = append(limitations, fmt.Sprintf("model caveat %02d", i))
	}
	result := &InvestigationResult{Limitations: limitations, Coverage: Coverage{Details: details, DegradedReasons: reasons}}

	capCoverageEntriesToWriteBound(result)

	if len(result.Limitations) != contractsv1.ContextFabricLimitationsMaxCount || !slices.Contains(result.Limitations, contractsv1.ContextFabricCoverageEntriesDroppedLimitation(3)) {
		t.Fatalf("limitations = %d, want the full list carrying the dropped-entries sentence", len(result.Limitations))
	}
	if result.LimitationsDisplaced != 1 {
		t.Fatalf("limitations_displaced = %d, want the one caveat the sentence displaced", result.LimitationsDisplaced)
	}
}

// The second cap pass of the validation-stage degrade reports what it omits.
func TestTheValidationStageDegradeReportsTheEntriesItsCapOmits(t *testing.T) {
	engine, telemetry, result, basis := validationStageFixture(t)
	var details []CoverageDetail
	for i := 0; i < contractsv1.ContextFabricCoverageEntriesMaxCount; i++ {
		d := syntheticNonDegradingDetail(i)
		d.DetailID = fmt.Sprintf("cov-%02d", i+1)
		details = append(details, d)
	}
	result.Coverage.Details = details

	if _, ok := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis); !ok {
		t.Fatal("not degraded")
	}

	if len(telemetry.coverageEntriesCapped) != 1 || telemetry.coverageEntriesCapped[0].omitted != 1 {
		t.Fatalf("cap events = %+v, want one reporting the single omitted entry", telemetry.coverageEntriesCapped)
	}
}
