package falkorgraph

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	pullRequestCanonicalPrefix = "pull_request:"
	propFact                   = "fact"
)

// rawPullRequestEnd is the end of a fact whose last words are the pull request
// end's label. Only that end: the fact's first label is a title, and a title may
// hold anything.
var rawPullRequestEnd = regexp.MustCompile(` pull request pull_request:[^\s:]+:(\d+)$`)

// nodeProperties is the read of a node's properties. Until CHAOS-12468 two
// edges labelled their pull request end with its canonical id, so a node that
// an edge created and no entity write reached holds that id as its label. The
// writers are fixed and changing the source version would stop projection for
// a rebuild, so the graph keeps such nodes and the read gives them the label a
// pull request has when no title is known.
//
// The next ClickHouseSourceVersion bump rebuilds every graph, and with it no
// such node or fact is left: that bump deletes this file and its two callers.
//
// CHAOS-12619: a work item, work item ref, project or team node that an edge
// created holds its raw source id as its label in the same way. The read gives it
// the reader form (contextfabric.SubjectReadableLabel); any other label stands.
func nodeProperties(properties map[string]interface{}) map[string]interface{} {
	out := normalizeProperties(properties)
	label, _ := out[propLabel].(string)
	if label == "" {
		return out
	}
	kind, _ := out[propKind].(string)
	canonicalID, _ := out[propCanonicalID].(string)
	if kind != string(contractsv1.ContextFabricSubjectPullRequest) {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(kind), CanonicalID: canonicalID, Label: label}
		if readable := contextfabric.SubjectReadableLabel(subject); readable != label {
			out[propLabel] = readable
		}
		return out
	}
	if label != canonicalID {
		return out
	}
	if number, ok := pullRequestNumber(label); ok {
		out[propLabel] = identity.PullRequestLabel(number)
	}
	return out
}

// edgeProperties is the read of an edge's properties. An edge's fact was
// composed from its end labels, so one written before CHAOS-12468 ends with a
// pull request's canonical id.
func edgeProperties(properties map[string]interface{}) map[string]interface{} {
	out := normalizeProperties(properties)
	fact, _ := out[propFact].(string)
	match := rawPullRequestEnd.FindStringSubmatchIndex(fact)
	if match == nil {
		return out
	}
	if number, err := strconv.ParseInt(fact[match[2]:match[3]], 10, 64); err == nil {
		out[propFact] = fact[:match[0]] + " pull request " + identity.PullRequestLabel(number)
	}
	return out
}

func pullRequestNumber(canonicalID string) (int64, bool) {
	rest, ok := strings.CutPrefix(canonicalID, pullRequestCanonicalPrefix)
	if !ok {
		return 0, false
	}
	cut := strings.LastIndexByte(rest, ':')
	if cut <= 0 {
		return 0, false
	}
	number, err := strconv.ParseInt(rest[cut+1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return number, true
}
