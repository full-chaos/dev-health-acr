package v1

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The project and incident status-filter disclosures, beside the pull request
// ones. A member set read under a status filter states the filter in one fixed
// sentence, a stored status outside the kind's set is counted in a second, and
// a question that named a state no read applies says so in a third. Each
// sentence is generated here from the kind's closed status set and recognised
// here by regenerating it, so a model-authored lookalike is never recognised.

// The closed status sets. A project's is the category set Linear writes for a
// project; an incident's is the canonical set ops writes into its normalized
// status. contextfabric.MemberStatusVocabulary reads them from here.
var (
	contextFabricProjectStatuses  = []string{"backlog", "planned", "started", "paused", "completed", "canceled"}
	contextFabricIncidentStatuses = []string{"active", "open", "acknowledged", "resolved", "closed", "suppressed"}
)

type contextFabricMemberStatusNouns struct {
	plural, field string
	statuses      []string
}

var contextFabricMemberStatusKinds = map[string]contextFabricMemberStatusNouns{
	"project":  {plural: "projects", field: "state", statuses: contextFabricProjectStatuses},
	"incident": {plural: "incidents", field: "status", statuses: contextFabricIncidentStatuses},
}

// ContextFabricMemberStatusVocabulary returns the closed status set of a
// project or incident member in declaration order, or nil for another kind.
func ContextFabricMemberStatusVocabulary(kind string) []string {
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok {
		return nil
	}
	return append([]string(nil), nouns.statuses...)
}

// contextFabricMemberStatusCutKinds are the kinds a status can be decided for
// over a cut read: the project and incident kinds and pull requests.
var contextFabricMemberStatusCutKinds = map[string]struct{ plural, field string }{
	"project":      {plural: "projects", field: "state"},
	"incident":     {plural: "incidents", field: "status"},
	"pull_request": {plural: "pull requests", field: "state"},
}

var contextFabricMemberStatusPoolCutPattern = regexp.MustCompile(`^The (?:state|status) filter ran (?:within the first ([1-9][0-9]{0,8})(?: of ([1-9][0-9]{0,8}))?|over a cut read that reached no) (projects|incidents|pull requests)`)

var contextFabricMemberStatusOutsideAtLeastPattern = regexp.MustCompile(`^At least ([1-9][0-9]{0,8}) (projects|incidents) have a (?:state|status) outside the known set and were not matched\.$`)

var contextFabricMemberStatusOutsidePattern = regexp.MustCompile(`^([1-9][0-9]{0,8}) (projects|incidents) have a (?:state|status) outside the known set and were not matched\.$`)

// ContextFabricMemberStatusFilterLimitation is the sentence for one status of a
// project or incident member set, or empty for a kind or status outside the
// closed sets.
func ContextFabricMemberStatusFilterLimitation(kind, status string) string {
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok || !containsString(nouns.statuses, status) {
		return ""
	}
	return fmt.Sprintf("Members are the %s whose current %s is %s; it is read as of now, over no period, and is not completion, readiness or health.", nouns.plural, nouns.field, status)
}

// ContextFabricMemberStatusOutsideSetLimitation states how many members held a
// stored status outside the known set and were not matched.
func ContextFabricMemberStatusOutsideSetLimitation(kind string, count int) string {
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok || count < 1 {
		return ""
	}
	article := "state"
	if nouns.field == "status" {
		article = "status"
	}
	return fmt.Sprintf("%d %s have a %s outside the known set and were not matched.", count, nouns.plural, article)
}

// ContextFabricMemberStatusOutsideSetAtLeastLimitation is the outside-set
// sentence for a count whose read was cut: at least count members held a
// stored status outside the known set.
func ContextFabricMemberStatusOutsideSetAtLeastLimitation(kind string, count int) string {
	exact := ContextFabricMemberStatusOutsideSetLimitation(kind, count)
	if exact == "" {
		return ""
	}
	return "At least " + exact
}

// ContextFabricMemberStatusUnappliedLimitation is served beside members of a
// kind when the question named a state that no read applied.
func ContextFabricMemberStatusUnappliedLimitation(kind string) string {
	if kind == "pull_request" {
		return ContextFabricPullRequestStatusUnappliedLimitation
	}
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok {
		return ""
	}
	last := len(nouns.statuses) - 1
	return fmt.Sprintf("Members are not filtered by state: the state the question names is not one the %s read applies (%s or %s).", kind, strings.Join(nouns.statuses[:last], ", "), nouns.statuses[last])
}

var contextFabricMemberStatusReachBasis = map[string]string{
	"team":       "the repositories and services it owns",
	"project":    "the repositories of its linked pull requests",
	"repository": "its linked pull requests and issues",
}

const contextFabricMemberStatusReachDefault = "the relationships stored for it"

// ContextFabricMemberStatusNoneReachedLimitation is served when the status
// filter ran and the anchor reached no member of the kind at all, so an empty
// set is not read as every member holding another status. The reach basis
// follows the anchor kind.
func ContextFabricMemberStatusNoneReachedLimitation(kind, anchorKind string) string {
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok {
		return ""
	}
	basis, known := contextFabricMemberStatusReachBasis[anchorKind]
	subject := anchorKind
	if !known {
		basis, subject = contextFabricMemberStatusReachDefault, "named subject"
	}
	return fmt.Sprintf("No %s were reached from the %s through %s, so the status was matched against none.", nouns.plural, subject, basis)
}

// ContextFabricMemberStatusNotComputedLimitation is served when a status
// question named a kind and no read applied the status, so whether any member
// holds it is unknown. reason is a fixed coverage label.
func ContextFabricMemberStatusNotComputedLimitation(kind, reason string) string {
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok || !containsReason(reason) {
		return ""
	}
	return fmt.Sprintf("The %s were not read by status, so whether any holds it is unknown: %s.", nouns.plural, reason)
}

// ContextFabricMemberStatusPoolCutLimitation states that a status filter was
// decided within a cut read: within the first read members it reached, of
// pool when pool is known (counted under the caller's authorization), or over
// none when the cut read reached no member of the kind. Empty for a kind with
// no status filter.
func ContextFabricMemberStatusPoolCutLimitation(kind string, read, pool int) string {
	nouns, ok := contextFabricMemberStatusCutKinds[kind]
	switch {
	case !ok:
		return ""
	case read < 1:
		return fmt.Sprintf("The %s filter ran over a cut read that reached no %s; %s past that bound were not checked.", nouns.field, nouns.plural, nouns.plural)
	case pool >= read:
		return fmt.Sprintf("The %s filter ran within the first %d of %d %s the read reached; %s past that bound were not checked.", nouns.field, read, pool, nouns.plural, nouns.plural)
	}
	return fmt.Sprintf("The %s filter ran within the first %d %s the read reached; %s past that bound were not checked.", nouns.field, read, nouns.plural, nouns.plural)
}

// isContextFabricMemberStatusPoolCutLimitation reports whether one limitation
// is exactly a pool cut sentence ContextFabricMemberStatusPoolCutLimitation
// generates.
func isContextFabricMemberStatusPoolCutLimitation(limitation string) bool {
	match := contextFabricMemberStatusPoolCutPattern.FindStringSubmatch(limitation)
	if match == nil {
		return false
	}
	read, pool := 0, 0
	if match[1] != "" {
		read, _ = strconv.Atoi(match[1])
	}
	if match[2] != "" {
		pool, _ = strconv.Atoi(match[2])
	}
	for kind, nouns := range contextFabricMemberStatusCutKinds {
		if nouns.plural == match[3] && limitation == ContextFabricMemberStatusPoolCutLimitation(kind, read, pool) {
			return true
		}
	}
	return false
}

// IsContextFabricMemberStatusLimitation reports whether one limitation is
// exactly a sentence this file generates for a kind and status of its closed
// set, or a count of at least one.
func IsContextFabricMemberStatusLimitation(limitation string) bool {
	if isContextFabricMemberStatusPoolCutLimitation(limitation) || isContextFabricMemberStatusAbsenceLimitation(limitation) {
		return true
	}
	for kind, nouns := range contextFabricMemberStatusKinds {
		if limitation == ContextFabricMemberStatusUnappliedLimitation(kind) {
			return true
		}
		for _, status := range nouns.statuses {
			if limitation == ContextFabricMemberStatusFilterLimitation(kind, status) {
				return true
			}
		}
		if match := contextFabricMemberStatusOutsideAtLeastPattern.FindStringSubmatch(limitation); match != nil && match[2] == nouns.plural {
			if count, err := strconv.Atoi(match[1]); err == nil && limitation == ContextFabricMemberStatusOutsideSetAtLeastLimitation(kind, count) {
				return true
			}
		}
		if match := contextFabricMemberStatusOutsidePattern.FindStringSubmatch(limitation); match != nil && match[2] == nouns.plural {
			if count, err := strconv.Atoi(match[1]); err == nil && limitation == ContextFabricMemberStatusOutsideSetLimitation(kind, count) {
				return true
			}
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func isContextFabricMemberStatusAbsenceLimitation(limitation string) bool {
	for kind := range contextFabricMemberStatusKinds {
		for _, anchor := range []string{"team", "project", "repository", ""} {
			if limitation == ContextFabricMemberStatusNoneReachedLimitation(kind, anchor) {
				return true
			}
		}
		for _, reason := range contextFabricCoverageCauseReasons() {
			if limitation == ContextFabricMemberStatusNotComputedLimitation(kind, reason) {
				return true
			}
		}
	}
	return false
}
