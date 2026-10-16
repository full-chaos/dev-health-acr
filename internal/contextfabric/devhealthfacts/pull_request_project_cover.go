package devhealthfacts

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// pullRequestProjectLinkRow is one (pull request, project) row of the project
// links statement, before the fallback-only rule: the strongest tier among the
// links that reach the project, and, when that tier is below native, every link
// below native as "rank|provenance|issue" so a covered one can be dropped.
type pullRequestProjectLinkRow struct {
	key, provider, id, name, tier string
	rank                          uint8
	fallbacks                     []string
	fallbackCount                 uint64
}

// dropCoveredProjectLinks applies the fallback-only rule (ops
// issueprlinks.NativeLinks.Outranks) to rows whose strongest tier is below
// native: a fallback link whose issue, or whose pull request, a native link
// covers is dropped, the row takes the strongest remaining tier, and a row left
// with no link is dropped. A row at native is untouched. The native cover is one
// organization-wide statement (readNativeCover), sent only when a fallback link
// exists.
func dropCoveredProjectLinks(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, rows []pullRequestProjectLinkRow) ([]pullRequestProjectLinkRow, error) {
	type fallback struct {
		row      int
		rank     uint8
		tier     string
		issue    string
		repo     string
		number   string
		coverKey string
	}
	var all []fallback
	var links []linkedSetRow
	overflow := make([]bool, len(rows))
	overflowRows := 0
	for i, r := range rows {
		if r.rank >= nativeRank {
			continue
		}
		if r.fallbackCount > uint64(len(r.fallbacks)) {
			// The statement carried only the first links of this row: cover cannot
			// be proved for all of them, so the row keeps the tier it was read at.
			overflow[i] = true
			overflowRows++
			continue
		}
		at := strings.LastIndex(r.key, ":")
		if at < 0 {
			return nil, fmt.Errorf("pull request project row key %q has no repository", r.key)
		}
		repo, number := r.key[:at], r.key[at+1:]
		for _, entry := range r.fallbacks {
			parts := strings.SplitN(entry, "|", 3)
			if len(parts) != 3 {
				return nil, fmt.Errorf("pull request project fallback link %q is malformed", entry)
			}
			rank, err := strconv.ParseUint(parts[0], 10, 8)
			if err != nil {
				return nil, fmt.Errorf("pull request project fallback link %q has no rank: %w", entry, err)
			}
			all = append(all, fallback{row: i, rank: uint8(rank), tier: parts[1], issue: parts[2], repo: repo, number: number, coverKey: coverPullRequestKey(repo, number)})
			links = append(links, linkedSetRow{repo: repo, number: number, issue: parts[2], rank: uint8(rank)})
		}
	}
	if len(all) == 0 {
		slog.InfoContext(ctx, "devhealthfacts.pull_request_project_native_first", "outcome", contextfabric.SanitizeLogAttr("no_fallback_rows"), "project_rows", len(rows), "fallback_links", 0, "dropped_links", 0, "dropped_rows", 0, "overflow_rows", overflowRows)
		return rows, nil
	}
	cover, err := readNativeCover(ctx, client, orgID, links)
	if err != nil {
		return nil, err
	}
	best := make([]*fallback, len(rows))
	droppedLinks := 0
	for i := range all {
		f := &all[i]
		_, issueCovered := cover.issues[f.issue]
		_, prCovered := cover.pullRequests[f.coverKey]
		if issueCovered || prCovered {
			droppedLinks++
			continue
		}
		if best[f.row] == nil || f.rank > best[f.row].rank {
			best[f.row] = f
		}
	}
	out := make([]pullRequestProjectLinkRow, 0, len(rows))
	droppedRows := 0
	for i, r := range rows {
		if r.rank < nativeRank && !overflow[i] {
			if best[i] == nil {
				droppedRows++
				continue
			}
			r.rank, r.tier = best[i].rank, best[i].tier
		}
		out = append(out, r)
	}
	outcome := "no_fallback_link_covered"
	if droppedLinks > 0 {
		outcome = "covered_fallback_links_dropped"
	}
	slog.InfoContext(ctx, "devhealthfacts.pull_request_project_native_first", "outcome", contextfabric.SanitizeLogAttr(outcome), "project_rows", len(rows), "fallback_links", len(all), "dropped_links", droppedLinks, "dropped_rows", droppedRows, "overflow_rows", overflowRows, "native_cover_issues", len(cover.issues), "native_cover_pull_requests", len(cover.pullRequests))
	return out, nil
}
