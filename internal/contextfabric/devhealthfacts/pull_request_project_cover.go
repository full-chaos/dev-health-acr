package devhealthfacts

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// pullRequestProjectLinkRow is one (pull request, project) row of the project
// links statement, before the fallback-only rule: the strongest tier among the
// links that reach the project, whether a native row covers the pull request,
// the link rows below native and those of them whose issue a native row covers,
// and the strongest tier below native that no native row covers (rank 0 and an
// empty tier when there is none).
type pullRequestProjectLinkRow struct {
	key, provider, id, name, tier string
	rank                          uint8
	pullRequestCovered            uint8
	fallbackLinks                 uint64
	issueCoveredLinks             uint64
	remainingRank                 uint8
	remainingTier                 string
}

// dropCoveredProjectLinks applies the fallback-only rule (ops
// issueprlinks.NativeLinks.Outranks) to rows whose strongest tier is below
// native: a fallback link whose issue, or whose pull request, a native link
// covers is dropped, the row takes the strongest remaining tier, and a row left
// with no link is dropped. A row at native is untouched. The statement already
// decided each link (pullRequestProjectLinksSQL); this applies the decision and
// writes its counts. The link counts are link rows of the group, so an issue id
// held under several repositories counts once per work item row.
func dropCoveredProjectLinks(ctx context.Context, rows []pullRequestProjectLinkRow) []pullRequestProjectLinkRow {
	out := make([]pullRequestProjectLinkRow, 0, len(rows))
	fallbackLinks, droppedLinks, droppedRows, pullRequestCoveredRows := 0, 0, 0, 0
	for _, r := range rows {
		if r.rank >= nativeRank {
			out = append(out, r)
			continue
		}
		fallbackLinks += int(r.fallbackLinks)
		if r.pullRequestCovered == 1 {
			droppedLinks += int(r.fallbackLinks)
			droppedRows++
			pullRequestCoveredRows++
			continue
		}
		droppedLinks += int(r.issueCoveredLinks)
		if r.remainingRank == 0 {
			droppedRows++
			continue
		}
		r.rank, r.tier = r.remainingRank, r.remainingTier
		out = append(out, r)
	}
	outcome := "no_fallback_rows"
	switch {
	case droppedLinks > 0:
		outcome = "covered_fallback_links_dropped"
	case fallbackLinks > 0:
		outcome = "no_fallback_link_covered"
	}
	slog.InfoContext(ctx, "devhealthfacts.pull_request_project_native_first", append([]any{"outcome", contextfabric.SanitizeLogAttr(outcome), "project_rows", len(rows), "fallback_links", fallbackLinks, "dropped_links", droppedLinks, "dropped_rows", droppedRows, "pull_request_covered_rows", pullRequestCoveredRows}, requestIDLogAttrs(ctx)...)...)
	return out
}
