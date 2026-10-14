package devhealthfacts

import (
	"context"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	// fileHotspotTopPerRepo is the most files one repository's fact lists.
	// hotspot_file_count carries the size of the run so the cut is visible.
	fileHotspotTopPerRepo = 10

	fileHotspotBasisLatestRun = "latest_run_in_window"
	fileHotspotBasisNoRows    = "no_hotspot_rows_in_window"

	fileComplexityStored   = "complexity_snapshot_stored"
	fileComplexityNotStore = "no_complexity_snapshot_stored"

	fileHotspotRankBasis = "Files of the latest stored hotspot run inside the window, ordered by the stored risk score and then by path, at most 10 per repository. The risk score is a stored score, not a verdict, and it ranks files, never people."

	fileHotspotChurnOnlyNote = " No complexity snapshot is stored for this repository, so the stored risk score reflects churn only (the daily hotspot computation sums a churn z-score and a complexity z-score); the cyclomatic fields are not served."
)

// fileHotspotSettings keeps the statements single-threaded: the peak is then
// the aggregate state and one block per column, not one read buffer per
// thread of a large run. A variable only so the memory row can measure the
// default-threads form beside it.
var fileHotspotSettings = "\nSETTINGS max_threads = 1"

// FileHotspotsProvider implements contextfabric.FactProvider for
// FactFileHotspots from file_hotspot_daily: the stored rows of a repository's
// latest hotspot run inside the window. It recomputes no score, and it serves
// no author, e-mail or commit.
type FileHotspotsProvider struct{ facts clickhouseFacts }

func newFileHotspotsProvider(client contextpacket.ClickHouseQueryClient) *FileHotspotsProvider {
	return &FileHotspotsProvider{facts: clickhouseFacts{client: client}}
}

func (p *FileHotspotsProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactFileHotspots, "devhealthfacts.file_hotspots", []contextfabric.SubjectKind{
		contextfabric.SubjectRepository,
	})
}

type fileHotspotRow struct {
	path                     string
	risk                     float64
	churnLOC                 uint64
	churnCommits, cyclomatic uint32
	cyclomaticAvg            float64
	blame                    *float64
	day                      time.Time
	computedAt               time.Time
	files                    int64
}

type fileHotspotRepo struct {
	rows []fileHotspotRow
}

func (p *FileHotspotsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	timeBound, unsupported, degraded := resolveTimeBound(query)
	if degraded {
		return unsupported, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	rejectedCount := 0
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.file_hotspots", contextfabric.FactFileHotspots, rejectedCount)
		}
	}()

	ids, bySubject, rejected := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), repositoryPrefix)
	rejectedCount += rejected
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	truncated := false
	if len(ids) > 0 {
		stored, rowCount, readErr := p.readLatestRuns(ctx, orgID, ids, timeBound)
		if readErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query file hotspots", readErr)
		}
		snapshots, readErr := p.readComplexitySnapshots(ctx, orgID, ids)
		if readErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query file complexity snapshots", readErr)
		}
		truncated = rowCount > fileHotspotTopPerRepo*maxFactRowsPerQuery
		budget := newFactBudget()
		for _, id := range ids {
			subject, ok := bySubject[id]
			if !ok || !budget.admit() {
				continue
			}
			fields, rowsCut := p.hotspotFields(stored[id], snapshots[id])
			truncated = truncated || rowsCut
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactFileHotspots, Subject: subject, Fields: fields,
				EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, id)},
			})
		}
		truncated = truncated || budget.truncated()
	}
	state, reason := timeBound.retentionState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: reason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated}, nil
}

// readLatestRuns reads, per repository, the rows of its latest run inside the
// window: the greatest (day, computed_at) pair, found over the compact run
// projection. The top files are kept inside the aggregate (a bounded sorted
// array per repository) so the statement's memory follows the repository
// count and not the size of a run.
func (p *FileHotspotsProvider) readLatestRuns(ctx context.Context, orgID string, repoIDs []string, timeBound factTimeBound) (map[string]*fileHotspotRepo, int, error) {
	out := map[string]*fileHotspotRepo{}
	if len(repoIDs) == 0 {
		return out, 0, nil
	}
	top := strconv.Itoa(fileHotspotTopPerRepo)
	// runs names each repository's latest (day, computed_at) run in the
	// window, read through the compact run projection. The membership test
	// prunes the read to those runs; measured against the forms with extra
	// key-prefix memberships, it prunes as well and peaks lower.
	runs := `SELECT repo_id, day, computed_at
			FROM file_hotspot_daily
			WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
			GROUP BY repo_id, day, computed_at
			ORDER BY repo_id, day DESC, computed_at DESC
			LIMIT 1 BY repo_id`
	statement := withRowLimitOf(`SELECT toString(repo_id), run_day, run_at, toInt64(files),
	tupleElement(entry, 2), tupleElement(entry, 1), tupleElement(entry, 3), tupleElement(entry, 4),
	tupleElement(entry, 5), tupleElement(entry, 6), tupleElement(entry, 7)
FROM (
	SELECT repo_id, max(day) AS run_day, max(computed_at) AS run_at, count() AS files,
		groupArraySorted(`+top+`)(tuple(-risk_score, file_path, churn_loc_30d, churn_commits_30d, cyclomatic_total, cyclomatic_avg, blame_concentration)) AS top_files
	FROM file_hotspot_daily
	WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}
		AND (repo_id, day, computed_at) IN (`+runs+`)
	GROUP BY repo_id
)
ARRAY JOIN top_files AS entry
ORDER BY repo_id, tupleElement(entry, 1), tupleElement(entry, 2)`, fileHotspotTopPerRepo*maxFactRowsPerQuery+1) + fileHotspotSettings
	rowCount := 0
	err := p.facts.query(ctx, statement, orgID, repoIDs, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID string
		var r fileHotspotRow
		var negRisk float64
		if err := row.Scan(&repoID, &r.day, &r.computedAt, &r.files, &r.path, &negRisk, &r.churnLOC, &r.churnCommits, &r.cyclomatic, &r.cyclomaticAvg, &r.blame); err != nil {
			return err
		}
		if rowCount > fileHotspotTopPerRepo*maxFactRowsPerQuery {
			return nil
		}
		r.risk = -negRisk
		repo := out[repoID]
		if repo == nil {
			repo = &fileHotspotRepo{}
			out[repoID] = repo
		}
		repo.rows = append(repo.rows, r)
		return nil
	}, timeBound.bindings()...)
	return out, rowCount, err
}

// readComplexitySnapshots returns, per repository, the newest stored
// complexity snapshot day. It answers existence only.
func (p *FileHotspotsProvider) readComplexitySnapshots(ctx context.Context, orgID string, repoIDs []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	statement := withRowLimitOf(`SELECT toString(repo_id), max(as_of_day)
FROM file_complexity_snapshots
WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}
GROUP BY repo_id
ORDER BY repo_id`, maxFactRowsProbe) + fileHotspotSettings
	err := p.facts.query(ctx, statement, orgID, repoIDs, func(row contextpacket.ClickHouseRowScanner) error {
		var repoID string
		var day time.Time
		if err := row.Scan(&repoID, &day); err != nil {
			return err
		}
		out[repoID] = day
		return nil
	})
	return out, err
}

func (p *FileHotspotsProvider) hotspotFields(stored *fileHotspotRepo, snapshotDay time.Time) (map[string]contextfabric.FactValue, bool) {
	hasSnapshot := !snapshotDay.IsZero()
	complexityBasis := fileComplexityNotStore
	rankBasis := fileHotspotRankBasis
	if hasSnapshot {
		complexityBasis = fileComplexityStored
	} else {
		rankBasis += fileHotspotChurnOnlyNote
	}
	fields := map[string]contextfabric.FactValue{
		"rank_basis":       contextfabric.StringFactValue(rankBasis),
		"complexity_basis": contextfabric.StringFactValue(complexityBasis),
	}
	if hasSnapshot {
		fields["complexity_snapshot_day"] = contextfabric.StringFactValue(snapshotDay.UTC().Format("2006-01-02"))
	}
	if stored == nil || len(stored.rows) == 0 {
		fields["hotspot_basis"] = contextfabric.StringFactValue(fileHotspotBasisNoRows)
		return fields, false
	}
	first := stored.rows[0]
	fields["hotspot_basis"] = contextfabric.StringFactValue(fileHotspotBasisLatestRun)
	fields["hotspot_file_count"] = contextfabric.IntegerFactValue(first.files)
	fields["latest_run_day"] = contextfabric.StringFactValue(first.day.UTC().Format("2006-01-02"))
	fields["latest_computed_at"] = contextfabric.StringFactValue(first.computedAt.UTC().Format(time.RFC3339))
	rows := make([]contextfabric.FactValueRow, 0, len(stored.rows))
	for _, r := range stored.rows {
		cells := map[string]contextfabric.FactValue{
			"file_path":         contextfabric.StringFactValue(r.path),
			"risk_score":        contextfabric.NumberFactValue(r.risk),
			"churn_loc_30d":     contextfabric.IntegerFactValue(int64(minUint64(r.churnLOC, 1<<62))),
			"churn_commits_30d": contextfabric.IntegerFactValue(int64(r.churnCommits)),
			"cyclomatic_total":  contextfabric.NullFactValue(),
			"cyclomatic_avg":    contextfabric.NullFactValue(),
		}
		if hasSnapshot {
			cells["cyclomatic_total"] = contextfabric.IntegerFactValue(int64(r.cyclomatic))
			cells["cyclomatic_avg"] = contextfabric.NumberFactValue(r.cyclomaticAvg)
		}
		if r.blame != nil {
			cells["blame_concentration"] = contextfabric.NumberFactValue(*r.blame)
		} else {
			cells["blame_concentration"] = contextfabric.NullFactValue()
		}
		rows = append(rows, contextfabric.FactValueRow{Fields: cells})
	}
	rows, omitted := capFactValueRows(rows)
	fields["hotspot_files"] = contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:    contextfabric.FactTableBreakdown,
		Key:      []string{"file_path"},
		Measures: []string{"risk_score", "churn_loc_30d", "churn_commits_30d", "cyclomatic_total", "cyclomatic_avg", "blame_concentration"},
		Grain:    contextfabric.GrainDay,
		Rows:     rows,
	})
	return fields, omitted > 0
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
