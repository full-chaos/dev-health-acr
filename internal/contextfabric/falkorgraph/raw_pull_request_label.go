package falkorgraph

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const (
	pullRequestCanonicalPrefix = "pull_request:"
	propFact                   = "fact"
)

// rawPullRequestID is a pull request's canonical id inside an edge's fact.
var rawPullRequestID = regexp.MustCompile(`\bpull_request:[^\s:]+:(\d+)\b`)

// nodeProperties is the read of a node's properties. Until CHAOS-12468 two
// edges labelled their pull request end with its canonical id, so a node that
// an edge created and no entity write reached holds that id as its label. The
// writers are fixed and changing the source version would stop projection for
// a rebuild, so the graph keeps such nodes and the read gives them the label a
// pull request has when no title is known.
//
// The next ClickHouseSourceVersion bump rebuilds every graph, and with it no
// such node or fact is left: that bump deletes this file and its two callers.
func nodeProperties(properties map[string]interface{}) map[string]interface{} {
	out := normalizeProperties(properties)
	label, _ := out[propLabel].(string)
	if label == "" || label != out[propCanonicalID] || out[propKind] != string(contractsv1.ContextFabricSubjectPullRequest) {
		return out
	}
	if number, ok := pullRequestNumber(label); ok {
		out[propLabel] = identity.PullRequestLabel(number)
	}
	return out
}

// edgeProperties is the read of an edge's properties. An edge's fact was
// composed from its end labels, so one written before CHAOS-12468 names a pull
// request by its canonical id.
func edgeProperties(properties map[string]interface{}) map[string]interface{} {
	out := normalizeProperties(properties)
	fact, _ := out[propFact].(string)
	if !strings.Contains(fact, pullRequestCanonicalPrefix) {
		return out
	}
	out[propFact] = rawPullRequestID.ReplaceAllStringFunc(fact, func(id string) string {
		if number, ok := pullRequestNumber(id); ok {
			return identity.PullRequestLabel(number)
		}
		return id
	})
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
