package v1

import (
	"fmt"
	"regexp"
	"strings"
)

// The project and incident status-filter disclosures, beside the pull request
// ones. A member set read under a status filter states the filter in one fixed
// sentence, a stored status outside the kind's set is counted in a second, and
// a question that named a state no read applies says so in a third.
type contextFabricMemberStatusNouns struct {
	plural, field, vocabulary string
}

var contextFabricMemberStatusKinds = map[string]contextFabricMemberStatusNouns{
	"project":      {plural: "projects", field: "state", vocabulary: "backlog, planned, started, paused, completed or canceled"},
	"incident":     {plural: "incidents", field: "status", vocabulary: "active, open, acknowledged, resolved, closed or suppressed"},
	"pull_request": {plural: "pull requests", field: "state", vocabulary: "open, merged, closed, awaiting review, changes requested or approved"},
}

var (
	contextFabricMemberStatusFilterPattern    = regexp.MustCompile(`^Members are the (projects|incidents) whose current (state|status) is [a-z_]{1,32}; it is read as of now, over no period, and is not completion, readiness or health\.$`)
	contextFabricMemberStatusOutsidePattern   = regexp.MustCompile(`^[1-9][0-9]{0,8} (projects have a state|incidents have a status) outside the known set and were not matched\.$`)
	contextFabricMemberStatusPoolCutPattern   = regexp.MustCompile(`^The (state|status) filter ran within the first [1-9][0-9]{0,8} (projects|incidents|pull requests) the read reached; (projects|incidents|pull requests) past that bound were not checked\.$`)
	contextFabricMemberStatusUnappliedPattern = regexp.MustCompile(`^Members are not filtered by state: the state the question names is not one the (project|incident) read applies \([a-z_ ,]{1,120}\)\.$`)
)

// ContextFabricMemberStatusFilterLimitation is the sentence for one status of a
// project or incident member set, or empty for a kind without one.
func ContextFabricMemberStatusFilterLimitation(kind, status string) string {
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok || kind == "pull_request" || status == "" {
		return ""
	}
	return fmt.Sprintf("Members are the %s whose current %s is %s; it is read as of now, over no period, and is not completion, readiness or health.", nouns.plural, nouns.field, status)
}

// ContextFabricMemberStatusOutsideSetLimitation states how many members held a
// stored status outside the known set and were not matched.
func ContextFabricMemberStatusOutsideSetLimitation(kind string, count int) string {
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok || kind == "pull_request" || count < 1 {
		return ""
	}
	verb := "a state"
	if nouns.field == "status" {
		verb = "a status"
	}
	return fmt.Sprintf("%d %s have %s outside the known set and were not matched.", count, nouns.plural, verb)
}

// ContextFabricMemberStatusPoolCutLimitation states that a status filter was
// decided within the first read members a cut read reached, or empty when
// none was read.
func ContextFabricMemberStatusPoolCutLimitation(kind string, read int) string {
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok || read < 1 {
		return ""
	}
	return fmt.Sprintf("The %s filter ran within the first %d %s the read reached; %s past that bound were not checked.", nouns.field, read, nouns.plural, nouns.plural)
}

// ContextFabricMemberStatusUnappliedLimitation is served beside members of a
// kind when the question named a state that no read applied.
func ContextFabricMemberStatusUnappliedLimitation(kind string) string {
	nouns, ok := contextFabricMemberStatusKinds[kind]
	if !ok {
		return ""
	}
	if kind == "pull_request" {
		return ContextFabricPullRequestStatusUnappliedLimitation
	}
	return fmt.Sprintf("Members are not filtered by state: the state the question names is not one the %s read applies (%s).", kind, nouns.vocabulary)
}

// IsContextFabricMemberStatusLimitation reports whether one limitation is a
// project or incident status-filter disclosure. It matches each composed
// sentence whole, so a model caveat that only starts with the same words is not
// one.
func IsContextFabricMemberStatusLimitation(limitation string) bool {
	if strings.TrimSpace(limitation) != limitation {
		return false
	}
	return contextFabricMemberStatusFilterPattern.MatchString(limitation) ||
		contextFabricMemberStatusOutsidePattern.MatchString(limitation) ||
		contextFabricMemberStatusPoolCutPattern.MatchString(limitation) ||
		contextFabricMemberStatusUnappliedPattern.MatchString(limitation)
}
