package falkorgraph

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// withWalkStepReads answers the entity-tree step read (UNWIND $ids ...) from
// the same edge and node rows the fake already serves for the per-node edge
// read, applying the bound validity window the way the stored read does.
func withWalkStepReads(inner *fakeConn) *fakeConn {
	base := inner.queryFunc
	inner.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if !strings.HasPrefix(cypher, "UNWIND $ids") {
			return base(ctx, graphKey, cypher, params, readOnly)
		}
		ids, _ := params["ids"].([]interface{})
		var out []row
		for _, raw := range ids {
			id, _ := raw.(string)
			edges, err := base(ctx, graphKey, "MATCH UNION", map[string]interface{}{"id": id}, readOnly)
			if err != nil {
				return nil, err
			}
			for _, r := range edges {
				e, _ := r["r"].(*edge)
				if e == nil || propStringValue(e.Properties[propRelationType]) != params["rel"] {
					continue
				}
				if start, ok := params[temporalParamStart].(int64); ok {
					if end, ended := e.Properties[propValidToNs].(int64); ended && end <= start {
						continue
					}
				}
				var neighbour, neighbourKind string
				incoming := strings.Contains(cypher, "<-[r:")
				switch {
				case incoming && propStringValue(r["dstId"]) == id && propStringValue(r["dstKind"]) == params["fromKind"]:
					neighbour, neighbourKind = propStringValue(r["srcId"]), propStringValue(r["srcKind"])
				case !incoming && propStringValue(r["srcId"]) == id && propStringValue(r["srcKind"]) == params["fromKind"]:
					neighbour, neighbourKind = propStringValue(r["dstId"]), propStringValue(r["dstKind"])
				default:
					continue
				}
				if neighbourKind != params["toKind"] {
					continue
				}
				nodes, err := base(ctx, graphKey, "MATCH NODE", map[string]interface{}{"id": neighbour}, readOnly)
				if err != nil {
					return nil, err
				}
				if len(nodes) == 0 {
					continue
				}
				out = append(out, row{"id": id, "b": nodes[0]["n"], "r": e})
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			return propStringValue(walkNode(out[i]["b"]).Properties[propCanonicalID]) < propStringValue(walkNode(out[j]["b"]).Properties[propCanonicalID])
		})
		if limit, ok := params["limit"].(int); ok && len(out) > limit {
			out = out[:limit]
		}
		return out, nil
	}
	return inner
}

func newTeamAdapter(t *testing.T, fake *fakeConn) *Adapter {
	t.Helper()
	return newFakeAdapter(t, withWalkStepReads(fake))
}
