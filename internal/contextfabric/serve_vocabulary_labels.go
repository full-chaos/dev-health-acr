package contextfabric

import (
	"encoding/json"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ServeVocabularyLabels returns the served copy of a result: the words a reader
// sees for its closed vocabularies (CHAOS-12416, CHAOS-4690). It is the only
// writer of vocabulary_labels on a result.
//
// Two doors serve a result, and both call it after the document's last writer:
// serveContextFabricInvestigationResult (every engine exit, fresh, reused or
// fitted, on the JSON response and the event stream) and
// serveStoredInvestigationResultOn (the read by id, the replay of a done
// submission and the attach terminal). The engine saves its result before
// either door, so the stored document never carries a word and a stored row
// gets its words on read with no migration.
//
//   - The map holds only the values in this result, read from its served
//     fields. A value with no row has no key, and the consumer takes its
//     generic path.
//   - coverage.sources[].label and .state_label are stamped from the structured
//     source and state, so a stored row reads today's words. They are existing
//     fields that every pin accepts, so enabled does not gate them.
//   - enabled off serves no map (ACR_CONTEXT_FABRIC_VOCABULARY_LABELS_ENABLED).
//
// A word never costs an answer, and it never costs a row: the words are added
// after the engine fits the document. maxBytes is the bound the door enforces,
// and the shed is tiered, so the result is the richest document within it: the
// full map; then only answer_status:* and the correction note, because with no
// map a cut answer reads a neutral pill and loses its note; then no map; then no
// stamped words. When none is within maxBytes the input stands, and the door
// refuses or projects it exactly as it would with no words. A non-positive
// maxBytes measures nothing: the caller bounds what it builds from the copy. The
// tier is the one this copy reached, and off when enabled is off.
func ServeVocabularyLabels(result InvestigationResult, enabled bool, maxBytes int64) (InvestigationResult, contractsv1.ContextFabricVocabularyLabelsTier) {
	result.VocabularyLabels = nil
	result.RenderShapes = withDriverSeriesWords(result.RenderShapes)
	stamped := withStampedSourceWords(result)
	tier := contractsv1.ContextFabricVocabularyLabelsOff
	if enabled {
		words := composeVocabularyLabels(stamped)
		labeled := stamped
		labeled.VocabularyLabels = words
		if withinServedBound(labeled, maxBytes) {
			return labeled, contractsv1.ContextFabricVocabularyLabelsFull
		}
		labeled.VocabularyLabels = contractsv1.ContextFabricStatusVocabularyLabels(words)
		if labeled.VocabularyLabels != nil && withinServedBound(labeled, maxBytes) {
			return labeled, contractsv1.ContextFabricVocabularyLabelsStatusOnly
		}
		tier = contractsv1.ContextFabricVocabularyLabelsNone
	}
	if withinServedBound(stamped, maxBytes) {
		return stamped, tier
	}
	return result, tier
}

// withinServedBound measures with the encoder the routes use. A document that
// cannot be encoded does not fit, and the door reports that failure itself.
func withinServedBound(result InvestigationResult, maxBytes int64) bool {
	if maxBytes <= 0 {
		return true
	}
	encoded, err := json.Marshal(result)
	return err == nil && int64(len(encoded)) <= maxBytes
}

// withStampedSourceWords reads each word from its source's structured fields.
// The sources are copied, so the stored document is not rewritten. Clone keeps
// an empty list non-nil, as the contract requires.
func withStampedSourceWords(result InvestigationResult) InvestigationResult {
	result.Coverage.Sources = slices.Clone(result.Coverage.Sources)
	for index := range result.Coverage.Sources {
		source := &result.Coverage.Sources[index]
		source.Label = contractsv1.ContextFabricSourceObservationLabel(source.Source)
		source.StateLabel = contractsv1.ContextFabricSourceStateLabel(source.State)
	}
	return result
}

// withDriverSeriesWords restates a driver series' legend at serve. render_shapes
// is written before save, so a stored result keeps the label it was made with
// (CHAOS-12473). Like the source words it needs no flag: series.label is on
// every pin. It runs before the bound checks, so no fallback brings the token
// back, and a word is never longer than the token it replaces. The saved
// document stays as it was.
func withDriverSeriesWords(shapes []contractsv1.ContextFabricRenderShape) []contractsv1.ContextFabricRenderShape {
	restated := slices.Clone(shapes)
	for index := range restated {
		if restated[index].SelectedBy != contractsv1.ContextFabricRenderRuleCohortDriverContribution {
			continue
		}
		restated[index].Series = slices.Clone(restated[index].Series)
		for row := range restated[index].Series {
			series := &restated[index].Series[row]
			series.Label = cohortDriverSeriesLabel(series.Key)
		}
	}
	return restated
}

// composeVocabularyLabels walks the served fields of the design's table
// (docs/design/context-fabric-result-semantics.md §5). It reads only structured
// fields and returns nil when no value has a word.
func composeVocabularyLabels(result InvestigationResult) map[string]string {
	var words vocabularyWords
	correction := result.StatusCorrection
	if correction == nil {
		correction = result.Completeness.StatusCorrection
	}

	words.answerStatus(result, result.Status)
	words.answerStatus(result, result.Completeness.TerminalStatus)
	for _, row := range result.Completeness.Outcomes {
		words.add(contractsv1.ContextFabricVocabularyRequirementOutcome, string(row.Outcome))
	}
	if correction != nil {
		words.answerStatus(result, correction.ReplacedStatus)
		words.add(contractsv1.ContextFabricVocabularyRequirementOutcome, string(correction.DecidingOutcome))
		if note, ok := contractsv1.ContextFabricStatusCorrectionNote(*correction, result.Status, result.Completeness.State); ok {
			words.set(contractsv1.ContextFabricStatusCorrectionNoteKey, note)
		}
	}

	for _, candidate := range result.SubjectResolution.Candidates {
		words.add(contractsv1.ContextFabricVocabularyCandidateState, string(candidate.State))
	}
	for _, entry := range result.SubjectResolution.PriorSubjectReceiptDispositions {
		words.add(contractsv1.ContextFabricVocabularyPriorReceiptDisposition, string(entry.Disposition))
	}
	for _, entry := range result.ConfirmedStructure {
		words.add(contractsv1.ContextFabricVocabularyStructureDisposition, string(entry.Disposition))
	}

	for _, detail := range result.Coverage.Details {
		words.subjectKind(detail.Kind)
	}
	if result.Cohort != nil {
		words.subjectKind(result.Cohort.Kind)
		for _, member := range result.Cohort.Members {
			words.add(contractsv1.ContextFabricVocabularyMemberOutcome, string(member.Outcome))
			for _, signal := range member.MissingSignals {
				words.add(contractsv1.ContextFabricVocabularyRankingSignal, signal)
			}
			for _, driver := range member.Drivers {
				words.add(contractsv1.ContextFabricVocabularyRankingSignal, driver.Signal)
				words.add(contractsv1.ContextFabricVocabularyDriverWindow, string(driver.Window))
				for _, threshold := range driver.ThresholdLabels {
					words.add(contractsv1.ContextFabricVocabularyThresholdLabel, threshold)
				}
			}
		}
	}

	for _, driver := range result.Drivers {
		words.add(contractsv1.ContextFabricVocabularyDriverStanding, string(driver.Standing))
	}
	for _, findings := range [][]contractsv1.ContextFabricFinding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			words.add(contractsv1.ContextFabricVocabularyFactKind, finding.Kind)
		}
	}
	for _, fact := range result.ClaimedFacts {
		words.add(contractsv1.ContextFabricVocabularyFactKind, string(fact.Kind))
		// A derivation's field is free text, even when it matches a declared name.
		if len(fact.DerivedFrom) == 0 {
			words.add(contractsv1.ContextFabricVocabularyFactField, fact.Field)
		}
		words.factRows(fact.Rows)
		words.factRows(fact.TimeSeriesRows)
	}

	if window := result.EffectiveEvidenceWindow; window != nil {
		words.add(contractsv1.ContextFabricVocabularyWindowRelativeID, string(window.RelativeID))
		words.add(contractsv1.ContextFabricVocabularyWindowProvenance, string(window.Provenance))
	}
	return words
}

// vocabularyWords collects the map. A nil map stays nil until a word lands, so
// a result with no known value carries no key at all.
type vocabularyWords map[string]string

func (w *vocabularyWords) set(key, label string) {
	if *w == nil {
		*w = vocabularyWords{}
	}
	(*w)[key] = label
}

func (w *vocabularyWords) add(vocabulary contractsv1.ContextFabricVocabulary, value string) {
	if label, ok := contractsv1.ContextFabricVocabularyLabel(vocabulary, value); ok {
		w.set(contractsv1.ContextFabricVocabularyLabelKey(vocabulary, value), label)
	}
}

func (w *vocabularyWords) subjectKind(kind contractsv1.ContextFabricSubjectKind) {
	w.add(contractsv1.ContextFabricVocabularySubjectKind, string(kind))
	w.add(contractsv1.ContextFabricVocabularySubjectKindPlural, string(kind))
}

// factRows words the columns of a claimed fact's table: its rows' keys are the
// declared column names.
func (w *vocabularyWords) factRows(rows []contractsv1.ContextFabricClaimedFactRow) {
	for _, row := range rows {
		for column := range row.Fields {
			w.add(contractsv1.ContextFabricVocabularyFactField, column)
		}
	}
}

// answerStatus reads the one status rule with this result's completeness.
func (w *vocabularyWords) answerStatus(result InvestigationResult, status contractsv1.ContextFabricInvestigationStatus) {
	if label, ok := contractsv1.ContextFabricAnswerStatusLabel(status, result.Completeness.State); ok {
		w.set(contractsv1.ContextFabricVocabularyLabelKey(contractsv1.ContextFabricVocabularyAnswerStatus, string(status)), label)
	}
}
