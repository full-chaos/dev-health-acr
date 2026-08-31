package v1

import (
	"fmt"
	"strings"
)

// CHAOS-4690 item 4: deterministic display labels, shipped WITH the
// vocabulary. These registries are the contract-owned source of every
// terse label the engine stamps onto chips and coverage details, so a new
// vocabulary value cannot ship unlabeled ("unmapped" is unrepresentable):
// totality tests in context_fabric_display_labels_test.go fail CI when any
// member of a closed vocabulary here lacks a label.
//
// These are NOT the banned phrasing-table shape: they live in the model
// layer's own repo, beside the vocabulary they label, are
// totality-enforced, and carry terse identity/quantity phrases — the
// deterministic fail-closed floor. Sentences remain synthesis's job
// (ContextFabricCoverageDetail.Phrasing). Ticket item 4 names this
// mechanism verbatim; the settled design's sol review cleared it against
// the binding language principle (r2 dimension-1: "deterministic registry
// labels satisfy it").

// contextFabricFactKindLabels labels every member of the closed fact-kind
// vocabulary (contextFabricFactKinds).
var contextFabricFactKindLabels = map[ContextFabricFactKind]string{
	ContextFabricFactIdentity:                "identity",
	ContextFabricFactMembership:              "membership",
	ContextFabricFactStatus:                  "status",
	ContextFabricFactActualCompletion:        "completion",
	ContextFabricFactWork:                    "work",
	ContextFabricFactBlockers:                "blockers",
	ContextFabricFactRequiredChildren:        "required children",
	ContextFabricFactPullRequests:            "pull request",
	ContextFabricFactReviews:                 "review",
	ContextFabricFactContinuousIntegration:   "CI",
	ContextFabricFactDeployments:             "deployment",
	ContextFabricFactIncidents:               "incident",
	ContextFabricFactMetrics:                 "metrics",
	ContextFabricFactHealth:                  "health",
	ContextFabricFactWorkload:                "workload",
	ContextFabricFactInvestment:              "investment",
	ContextFabricFactReadiness:               "readiness",
	ContextFabricFactOperationalDeficiencies: "operational deficiency",
	ContextFabricFactSourceHealth:            "source health",
	ContextFabricFactEvidence:                "evidence",
	ContextFabricFactFlow:                    "flow",
	ContextFabricFactLandscape:               "landscape",
}

// contextFabricSourceStateLabels labels every member of the closed source
// state vocabulary.
var contextFabricSourceStateLabels = map[ContextFabricSourceState]string{
	ContextFabricSourceAvailable:     "available",
	ContextFabricSourceStale:         "may be out of date",
	ContextFabricSourceUnavailable:   "unavailable",
	ContextFabricSourceUnconfigured:  "not configured",
	ContextFabricSourceUnauthorized:  "not authorized",
	ContextFabricSourceNoData:        "no data",
	ContextFabricSourceTruncated:     "partially included",
	ContextFabricSourceConflicted:    "conflicting data",
	ContextFabricSourceNotApplicable: "not applicable",
	ContextFabricSourcePruned:        "not needed",
}

// contextFabricEvidenceEntityLabels labels every KNOWN acr:v1:<entity-type>
// segment (the producers in internal/contextpacket/source_queries.go and
// internal/contextfabric). The segment vocabulary is NOT closed at the
// producer signature today (evidenceRefID takes an arbitrary string), so an
// unknown segment falls back to the generic "Evidence" label — countable
// via RecordEvidenceLabelFallback (count only, never the segment) and
// visible beside its raw ref in evidence_ref_labels. The follow-up that
// closes the taxonomy into a typed enum is filed at CHAOS-4690 close-out.
var contextFabricEvidenceEntityLabels = map[string]string{
	"commit":               "Commit",
	"repository":           "Repository",
	"work-item":            "Work item",
	"work-item-dependency": "Work item dependency",
	"commit-file":          "Commit file",
	"pull-request":         "Pull request",
	"review":               "Review",
	"ci":                   "CI run",
	"graph":                "Relationship",
	"ai-run":               "AI workflow run",
	"ai-artifact":          "AI artifact",
	"review-outcome":       "Review outcome",
	"deployment":           "Deployment",
	"incident":             "Incident",
	"deployment-incident":  "Deployment/incident link",
	"hotspot":              "File hotspot",
	"complexity":           "File complexity",
	"team":                 "Team",
	"project":              "Project",
	"organization":         "Organization",
}

// humanizeVocabularyToken is the deterministic transform for a token from
// one of acr's OWN vocabularies whose display needs no bespoke label
// (underscores to spaces). Never applied to free text.
func humanizeVocabularyToken(token string) string {
	return strings.ReplaceAll(strings.TrimSpace(token), "_", " ")
}

// ContextFabricFactKindLabel returns the display label for a fact kind,
// falling back to the deterministic token transform for a value outside the
// registry (unreachable for a validated result; the totality test keeps the
// registry complete).
func ContextFabricFactKindLabel(kind ContextFabricFactKind) string {
	if label, ok := contextFabricFactKindLabels[kind]; ok {
		return label
	}
	return humanizeVocabularyToken(string(kind))
}

// ContextFabricSourceStateLabel returns the display label for a source
// state.
func ContextFabricSourceStateLabel(state ContextFabricSourceState) string {
	if label, ok := contextFabricSourceStateLabels[state]; ok {
		return label
	}
	return humanizeVocabularyToken(string(state))
}

// ContextFabricSourceObservationLabel returns the display label for a
// coverage source name ("canonical_fact:blockers" → "Canonical facts —
// blockers"). Unknown shapes get the generic "Source" — the deterministic
// fail-readable floor, shipped with the vocabulary instead of guessed at a
// consumer.
func ContextFabricSourceObservationLabel(source string) string {
	switch source {
	case "context-fabric:graph":
		return "Relationship graph"
	case "context-fabric:graph-validity-windows":
		return "Relationship graph — undated elements"
	}
	if kind, ok := strings.CutPrefix(source, "canonical_fact:"); ok {
		if validFactKind(ContextFabricFactKind(kind)) {
			return "Canonical facts — " + ContextFabricFactKindLabel(ContextFabricFactKind(kind))
		}
		return "Canonical facts"
	}
	if capability, ok := strings.CutPrefix(source, "dev-health-ops:"); ok {
		token := humanizeVocabularyToken(capability)
		if token == "" {
			return "Dev Health"
		}
		return "Dev Health — " + token
	}
	return "Source"
}

// ContextFabricEvidenceRefLabel returns the display label for one
// acr:v1:<entity-type>:<id...> evidence ref, plus whether the entity-type
// segment was in the known registry (false = the generic floor was used;
// the caller counts that via telemetry, never the segment itself).
func ContextFabricEvidenceRefLabel(refID string) (string, bool) {
	parts := strings.Split(refID, ":")
	if len(parts) < 4 || parts[0] != "acr" || parts[1] != "v1" {
		return "Evidence", false
	}
	entityType := parts[2]
	id := strings.Join(parts[3:], ":")
	label, known := contextFabricEvidenceEntityLabels[entityType]
	if !known {
		if id == "" {
			return "Evidence", false
		}
		return "Evidence: " + id, false
	}
	if id == "" {
		return label, true
	}
	return label + ": " + id, true
}

// countPhrase renders a detail's structured Count for a label — the ONLY
// place a quantity is put into words on this surface (the settled design's
// digit rule: synthesis Phrasing may carry no digits; the deterministic
// Label states the number).
func countPhrase(count *int, singular, plural string) string {
	if count == nil {
		return "some " + plural
	}
	if *count == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", *count, plural)
}

// ComposeCoverageDetailLabel composes the deterministic terse label for one
// coverage detail from its own structured fields. Pure; the engine stamps
// its result into ContextFabricCoverageDetail.Label before validation.
func ComposeCoverageDetailLabel(d ContextFabricCoverageDetail) string {
	kind := ContextFabricFactKindLabel(d.FactKind)
	var label string
	switch d.Code {
	case ContextFabricCoverageDetailFactUnconfigured:
		label = "No source is configured for " + kind + " facts"
	case ContextFabricCoverageDetailFactScopeUnexpanded:
		label = kindClause(kind) + " facts were not reachable from this question's subject"
	case ContextFabricCoverageDetailFactReadFailed:
		label = kindClause(kind) + " facts could not be read"
	case ContextFabricCoverageDetailFactProviderReported:
		label = providerReportedLabel(kind, d.SourceState)
	case ContextFabricCoverageDetailFactPruned:
		label = kindClause(kind) + " facts do not apply to what was asked"
	case ContextFabricCoverageDetailFactNarrowed:
		label = countPhrase(d.Count, "subject was", "subjects were") + " skipped for " + kind + " facts"
	case ContextFabricCoverageDetailGraphEndpointLookupFailed:
		label = countPhrase(d.Count, "relationship link", "relationship links") + " could not be resolved"
	case ContextFabricCoverageDetailGraphExactNameCandidatesTruncated:
		label = "More exact-name matches existed than could be shown"
	case ContextFabricCoverageDetailGraphCohortDeniedByAuthorization:
		label = countPhrase(d.Count, "group member", "group members") + " excluded by authorization"
	case ContextFabricCoverageDetailGraphUnknownRelationshipType:
		label = countPhrase(d.Count, "relationship edge", "relationship edges") + " of an unrecognized type dropped"
	case ContextFabricCoverageDetailGraphValidityUnbounded:
		label = countPhrase(d.Count, "undated element", "undated elements") + " included at the requested time"
	default:
		label = "Coverage was limited"
	}
	if d.Narrowed && d.Code != ContextFabricCoverageDetailFactNarrowed {
		label += "; some subjects were skipped"
	}
	return clampLabel(label)
}

func kindClause(kindLabel string) string {
	if kindLabel == "" {
		return "Some"
	}
	return strings.ToUpper(kindLabel[:1]) + kindLabel[1:]
}

func providerReportedLabel(kind string, state ContextFabricSourceState) string {
	switch state {
	case ContextFabricSourceStale:
		return kindClause(kind) + " facts may be out of date"
	case ContextFabricSourceTruncated:
		return "Only part of the " + kind + " facts could be included"
	case ContextFabricSourceNoData:
		return "No " + kind + " facts were found"
	case ContextFabricSourceNotApplicable:
		return kindClause(kind) + " facts do not apply here"
	case ContextFabricSourceUnauthorized:
		return kindClause(kind) + " facts are not authorized for this account"
	case ContextFabricSourceConflicted:
		return kindClause(kind) + " facts held conflicting data"
	default:
		return kindClause(kind) + " facts were limited"
	}
}

func clampLabel(label string) string {
	runes := []rune(label)
	if len(runes) <= ContextFabricCoverageDetailLabelMaxLength {
		return label
	}
	return strings.TrimSpace(string(runes[:ContextFabricCoverageDetailLabelMaxLength]))
}
