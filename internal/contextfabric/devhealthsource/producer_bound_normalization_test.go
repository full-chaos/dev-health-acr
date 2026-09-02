package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// End-to-end proofs for producer-side normalization, driven through the
// PRODUCTION builder (NextProjectionBatch over the fake row scanner) rather
// than through the pass directly.
//
// The pass's own properties are pinned in item_normalization_test.go. What
// only an end-to-end fixture can show is COVERAGE: that the pass is reached by
// every row-building site that can mint an out-of-bound value. The pass is
// written to cover them structurally, but "structurally" is a claim about code
// that has to be executed to be believed -- and the enumeration below is the
// list a site-by-site fix would have had to get right and would eventually
// have got wrong.

const (
	// overRunes is comfortably past both contract bounds.
	overRunes = contractsv1.ContextFabricClaimedFactValueMaxLength + 250
)

// bigText is padded with a MULTI-BYTE rune for the reason
// item_normalization_test.go's padRune is: an ASCII fixture cannot tell a rune
// bound from a byte bound.
func bigText(n int) string { return strings.Repeat("é", n) }

// normalizationObservations decodes the INFO lines normalizationLogger emits.
func normalizationObservations(t *testing.T, raw string) []map[string]any {
	t.Helper()
	return decodeLines(t, raw, "context_fabric: projection item normalized")
}

func quarantineObservations(t *testing.T, raw string) []map[string]any {
	t.Helper()
	return decodeLines(t, raw, quarantineLine)
}

func decodeLines(t *testing.T, raw, marker string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" || !strings.Contains(line, marker) {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

// projectWithBothLogs runs one page and returns the batch plus BOTH counter
// families. Both, always: a test that reads only the normalization counter
// cannot tell "the row was repaired and kept" from "the row was repaired,
// still breached something else, and was dropped anyway".
func projectWithBothLogs(t *testing.T, tables []fakeTable, cursor string) (contextfabric.ProjectionBatch, bool, []map[string]any, []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.WithLogger(logger).NextProjectionBatch(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: cursor})
	if err != nil {
		t.Fatalf("next projection batch: %v (a normalized row must PROJECT, so any error here is the change failing its own purpose)", err)
	}
	return batch, available, normalizationObservations(t, buf.String()), quarantineObservations(t, buf.String())
}

func reasonTally(entries []map[string]any, key string) map[string]int {
	tally := map[string]int{}
	for _, entry := range entries {
		tally[fmt.Sprint(entry[key])]++
	}
	return tally
}

// assertBatchIsWithinBounds is the shared postcondition: whatever the fixture
// seeded, everything that reached the batch satisfies the three bounds this
// change owns. Returns how many items it inspected, so a caller can refuse a
// vacuous pass over an empty batch.
func assertBatchIsWithinBounds(t *testing.T, batch contextfabric.ProjectionBatch) int {
	t.Helper()
	inspected := 0
	checkSubject := func(what string, s contractsv1.ContextFabricSubjectRef) {
		if strings.TrimSpace(s.Label) != s.Label {
			t.Fatalf("%s %q kept an untrimmed label %q", what, s.CanonicalID, s.Label)
		}
		if n := utf8.RuneCountInString(s.Label); n > contractsv1.ContextFabricSubjectRefLabelMaxLength {
			t.Fatalf("%s %q kept a %d-rune label, past the %d-rune bound", what, s.CanonicalID, n, contractsv1.ContextFabricSubjectRefLabelMaxLength)
		}
		inspected++
	}
	checkProperties := func(what string, properties map[string]contractsv1.ContextFabricScalarValue) {
		for name, value := range properties {
			if value.String == nil {
				continue
			}
			if n := utf8.RuneCountInString(*value.String); n > contractsv1.ContextFabricClaimedFactValueMaxLength {
				t.Fatalf("%s kept property %q at %d runes, past the %d-rune bound", what, name, n, contractsv1.ContextFabricClaimedFactValueMaxLength)
			}
			inspected++
		}
	}
	checkWindow := func(what string, validFrom, validTo *time.Time) {
		if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
			t.Fatalf("%s kept an inverted window [%s, %s)", what, validFrom, validTo)
		}
		inspected++
	}
	for _, e := range batch.Entities {
		checkSubject("entity", e.Subject)
		checkProperties("entity "+e.Subject.CanonicalID, e.Properties)
		checkWindow("entity "+e.Subject.CanonicalID, e.ValidFrom, e.ValidTo)
	}
	for _, r := range batch.Relationships {
		checkSubject("relationship "+r.RelationshipID+" from", r.From)
		checkSubject("relationship "+r.RelationshipID+" to", r.To)
		checkProperties("relationship "+r.RelationshipID, r.Properties)
		checkWindow("relationship "+r.RelationshipID, r.ValidFrom, r.ValidTo)
	}
	// The contract is the final authority, not this restatement of it.
	if err := batch.Validate(); err != nil {
		t.Fatalf("the batch fails contract validation after normalization: %v", err)
	}
	return inspected
}

// TestNormalizationKeepsEveryRowTheThreeBoundsUsedToCost is the headline
// red-first proof, through the production builder.
//
// Each vehicle is a real work_items row that the parent commit quarantines --
// the ticket's own bound map, verbatim. After this change every one of them
// PROJECTS, under a normalization token, and NONE of the three quarantine
// tokens for these bounds is reported at all.
func TestNormalizationKeepsEveryRowTheThreeBoundsUsedToCost(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ended := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) // BEFORE created: inverted

	workItem := func(id, title, projectName string, createdAt time.Time, hasEnded uint8, endedAt time.Time) []any {
		return []any{id, "repo-1", "example-org/widget-service", title, "in_progress", "", at, createdAt, hasEnded, endedAt, "", "", projectName, []string{}}
	}

	cases := []struct {
		name      string
		row       []any
		wantToken string
		wantLabel string
	}{
		// The title reaches the pass RAW. An earlier revision trimmed it at
		// the row for readability, which repaired the row and made the repair
		// invisible -- the three title sites are the ones an operator most
		// wants counted. So this case asserts BOTH: the token fires, and the
		// label that reaches the batch is the trimmed one.
		{"untrimmed title", workItem("WI-TRIM", "  Investigate the checkout flake  ", "", created, 0, zeroTime), "label_trimmed", "Investigate the checkout flake"},
		{"oversize title", workItem("WI-CAP", bigText(contractsv1.ContextFabricSubjectRefLabelMaxLength+40), "", created, 0, zeroTime), "label_capped", ""},
		{"oversize free-text property", workItem("WI-SCALAR", "ok", bigText(overRunes), created, 0, zeroTime), "scalar_capped", ""},
		{"inverted validity window", workItem("WI-WINDOW", "ok", "", created, 1, ended), "window_collapsed", ""},
	}

	checked := 0
	for _, tc := range cases {
		tables := baseTables(at)
		for i, tb := range tables {
			if tb.match == "FROM work_items AS w" {
				tables[i].rows = [][]any{tc.row}
				tables[i].cursorOf = workItemCursorOf
				continue
			}
			tables[i].rows = nil
		}

		batch, available, normalizations, quarantines := projectWithBothLogs(t, tables, testCursor(t, created.Add(-time.Hour), ""))
		if !available {
			t.Fatalf("%s: no batch -- the row was consumed and emitted nothing, which is the loss this change exists to end", tc.name)
		}
		if len(batch.Entities) != 1 {
			t.Fatalf("%s: entities = %d, want the one normalized work item: %+v", tc.name, len(batch.Entities), batch.Entities)
		}
		if assertBatchIsWithinBounds(t, batch) == 0 {
			t.Fatalf("%s: the postcondition inspected nothing", tc.name)
		}

		tally := reasonTally(normalizations, "normalization_reason")
		if tc.wantToken != "" && tally[tc.wantToken] == 0 {
			t.Fatalf("%s: %s was never reported -- a repair that is not counted is a silent rewrite: %v", tc.name, tc.wantToken, tally)
		}
		if tc.wantLabel != "" {
			if got := batch.Entities[0].Subject.Label; got != tc.wantLabel {
				t.Fatalf("%s: label = %q, want %q", tc.name, got, tc.wantLabel)
			}
		}
		// The decisive negative: the bound that used to cost this row must no
		// longer appear in the QUARANTINE counter at all.
		for _, gone := range []string{"untrimmed_label", "oversize_scalar", "inverted_window"} {
			if n := reasonTally(quarantines, "quarantine_reason")[gone]; n != 0 {
				t.Fatalf("%s: quarantine still reports %s %d times -- the row is still being lost: %+v", tc.name, gone, n, quarantines)
			}
		}
		checked++
	}
	if checked != len(cases) {
		t.Fatalf("only %d of %d vehicles reached their assertions", checked, len(cases))
	}
}

// TestNormalizationCoversEverySingleRowWindowSite executes the five window
// sites the ticket enumerates, one row each, in ONE page.
//
// Every one of them mints TWO items from a single row -- the entity, and the
// BELONGS_TO_REPOSITORY edge that is handed the very same window pointers --
// which is why the ticket calls each unguarded site a two-item loss. Both are
// counted here, so a repair that fixed only the entity and left the edge
// inverted would fail on the arithmetic rather than merely on a spot check.
func TestNormalizationCoversEverySingleRowWindowSite(t *testing.T) {
	t.Parallel()
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	early := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) // strictly before late: inverted
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	slug := "example-org/widget-service"

	// Each row inverts its OWN site's window. The site names are the ticket's.
	tables := []fakeTable{
		repoRow("repo-1", slug, "synthetic", at),
		// work_items: requiredTime(created_at) vs optionalTime(ended_at)
		{match: "FROM work_items AS w", rows: [][]any{
			{"WI-1", "repo-1", slug, "work item", "open", "", at, late, uint8(1), early, "", "", "", []string{}}}},
		// git_pull_requests: requiredTime(created_at) vs optionalTime(closed/merged)
		{match: "FROM git_pull_requests AS p", rows: [][]any{
			{"repo-1", slug, uint32(1042), "pull request", "open", at, late, uint8(1), early, "main", ""}}},
		// deployments: optionalTime(started_at) vs optionalTime(finished_at)
		{match: "FROM deployments AS d", rows: [][]any{
			{"repo-1", slug, "deploy-1", "success", "production", at, uint8(1), late, uint8(1), early, "v1"}}},
		// operational_incidents: optionalTime(started_at) vs optionalTime(ended_at)
		{match: "FROM operational_incidents AS i", rows: [][]any{
			{"incident-1", "repo-1", slug, "incident", "open", "low", at, uint8(0), uint8(1), late, uint8(1), early, ""}}},
		// ci_pipeline_runs: requiredTime(started_at) vs optionalTime(finished_at)
		{match: "FROM ci_pipeline_runs AS c", rows: [][]any{
			{"run-1", "repo-1", "main", "success", slug, at, late, uint8(1), early, "pipeline"}}},
	}

	batch, available, normalizations, quarantines := projectWithBothLogs(t, tables, "")
	if !available {
		t.Fatal("no batch: every seeded row was lost")
	}
	if assertBatchIsWithinBounds(t, batch) == 0 {
		t.Fatal("the postcondition inspected nothing")
	}

	const sites = 5
	const itemsPerSite = 2 // the entity and its BELONGS_TO_REPOSITORY edge
	tally := reasonTally(normalizations, "normalization_reason")
	if tally["window_collapsed"] != sites*itemsPerSite {
		t.Fatalf("window_collapsed = %d, want %d (%d single-row sites x {entity, BELONGS_TO_REPOSITORY edge}): %v",
			tally["window_collapsed"], sites*itemsPerSite, sites, tally)
	}
	// Counted per item KIND too, so a repair that hit only entities is
	// distinguishable from one that hit both.
	kinds := map[string]int{}
	for _, entry := range normalizations {
		if entry["normalization_reason"] == "window_collapsed" {
			kinds[fmt.Sprint(entry["item_kind"])]++
		}
	}
	if kinds["entity"] != sites || kinds["relationship"] != sites {
		t.Fatalf("window_collapsed by kind = %v, want %d entities and %d relationships -- an unguarded site poisons BOTH items it mints", kinds, sites, sites)
	}
	if n := reasonTally(quarantines, "quarantine_reason")["inverted_window"]; n != 0 {
		t.Fatalf("inverted_window quarantines = %d, want 0: %+v", n, quarantines)
	}
	// Every collapsed window is the degenerate one, never a widened interval.
	degenerate := 0
	for _, e := range batch.Entities {
		if e.ValidFrom != nil && e.ValidTo != nil && e.ValidFrom.Equal(*e.ValidTo) {
			degenerate++
		}
	}
	if degenerate != sites {
		t.Fatalf("degenerate [t, t) windows = %d, want %d: a collapse that produced anything else invented an interval the source never asserted", degenerate, sites)
	}
}

// TestNormalizationCoversEveryUncappedPropertyWriteSite executes every
// property write site in this source that a source value can push past the
// scalar bound, and separately proves that every site left out is bounded by
// something other than this pass.
//
// THE TICKET'S ENUMERATION IS SHORT TWICE OVER, and this corpus is what
// showed it. It names "nine setStringProperty(..., 0) call sites"; the merged
// tip carries fourteen. More importantly, fourteen FURTHER properties are
// written with `properties[name] = stringScalar(value)` directly -- status,
// state, severity, environment, description, url, a CI run's branch -- which
// never touch setStringProperty and so carry no cap at all, not even a
// zero one. A repair keyed on `setStringProperty(..., 0)` would have fixed
// nine sites, looked complete, and left twenty-three uncapped.
//
// That is the case for a pass rather than an edit per site, stated as an
// executed test instead of an argument.
func TestNormalizationCoversEveryUncappedPropertyWriteSite(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	big := bigText(overRunes)
	// The repo slug stays LEGAL on purpose -- see
	// TestAnOversizeRepositorySlugIsNotThisPassesToRepair.
	slug := "example-org/widget-service"

	// Distinct oversize values per site, so one site's repair cannot stand in
	// for another's.
	tables := []fakeTable{
		repoRow("repo-1", slug, "synthetic", at),
		// work_items: type, native_team_key, project_name (setStringProperty)
		//             + status (direct stringScalar)
		{match: "FROM work_items AS w", rows: [][]any{
			{"WI-1", "repo-1", slug, "work item", big + "status", "", at, created, uint8(0), zeroTime, big + "type", big + "team", big + "project", []string{}}}},
		// git_pull_requests: branch (setStringProperty) + state (direct)
		{match: "FROM git_pull_requests AS p", rows: [][]any{
			{"repo-1", slug, uint32(1042), "pull request", big + "state", at, created, uint8(0), zeroTime, big + "branch", ""}}},
		// deployments: release_ref (setStringProperty) + status, environment (direct)
		{match: "FROM deployments AS d", rows: [][]any{
			{"repo-1", slug, "deploy-1", big + "status", big + "env", at, uint8(1), created, uint8(0), zeroTime, big + "release"}}},
		// operational_incidents: status, severity (both direct)
		{match: "FROM operational_incidents AS i", rows: [][]any{
			{"incident-1", "repo-1", slug, "incident", big + "status", big + "severity", at, uint8(0), uint8(1), created, uint8(0), zeroTime, ""}}},
		// git_pull_request_reviews: pr_title (setStringProperty) + state (direct)
		{match: "FROM git_pull_request_reviews AS r", rows: [][]any{
			{"review-1", "repo-1", uint32(1042), big + "state", at, slug, created, uint8(0), zeroTime, big + "prtitle"}}},
		// ci_pipeline_runs: pipeline_name (setStringProperty) + status, branch (direct)
		{match: "FROM ci_pipeline_runs AS c", rows: [][]any{
			{"run-1", "repo-1", big + "branch", big + "status", slug, at, created, uint8(0), zeroTime, big + "pipeline"}}},
	}

	batch, available, normalizations, quarantines := projectWithBothLogs(t, tables, "")
	if !available {
		t.Fatal("no batch: every seeded row was lost")
	}
	if assertBatchIsWithinBounds(t, batch) == 0 {
		t.Fatal("the postcondition inspected nothing")
	}
	if n := reasonTally(quarantines, "quarantine_reason")["oversize_scalar"]; n != 0 {
		t.Fatalf("oversize_scalar quarantines = %d, want 0: %+v", n, quarantines)
	}

	// ONCE PER ITEM, not once per field: six entities carry oversize
	// properties and one of them carries four, so a per-field counter would
	// read sixteen. The operational question is how many ITEMS this producer
	// had to repair.
	const entitiesWithOversizeProperties = 6
	tally := reasonTally(normalizations, "normalization_reason")
	if tally["scalar_capped"] != entitiesWithOversizeProperties {
		t.Fatalf("scalar_capped = %d, want %d (one per ITEM; the work item alone carries four oversize properties and must still count once): %v",
			tally["scalar_capped"], entitiesWithOversizeProperties, tally)
	}

	// The site-by-site ledger, keyed on (subject kind, property name) -- the
	// write SITE's own identity in the batch. A bare count could be satisfied
	// by one site firing sixteen times; a bare property name could be
	// satisfied by `status` on one kind standing in for `status` on four.
	type site struct {
		kind     contractsv1.ContextFabricSubjectKind
		property string
	}
	wantSites := []site{
		// setStringProperty(..., 0) -- the family the ticket enumerates
		{contractsv1.ContextFabricSubjectWorkItem, "type"},
		{contractsv1.ContextFabricSubjectWorkItem, "native_team_key"},
		{contractsv1.ContextFabricSubjectWorkItem, "project_name"},
		{contractsv1.ContextFabricSubjectPullRequest, "branch"},
		{contractsv1.ContextFabricSubjectDeployment, "release_ref"},
		{contractsv1.ContextFabricSubjectPullRequestReview, "pr_title"},
		{contractsv1.ContextFabricSubjectCIRun, "pipeline_name"},
		// properties[name] = stringScalar(value) -- the family it does not
		{contractsv1.ContextFabricSubjectWorkItem, "status"},
		{contractsv1.ContextFabricSubjectPullRequest, "state"},
		{contractsv1.ContextFabricSubjectDeployment, "status"},
		{contractsv1.ContextFabricSubjectDeployment, "environment"},
		{contractsv1.ContextFabricSubjectIncident, "status"},
		{contractsv1.ContextFabricSubjectIncident, "severity"},
		{contractsv1.ContextFabricSubjectPullRequestReview, "state"},
		{contractsv1.ContextFabricSubjectCIRun, "status"},
		{contractsv1.ContextFabricSubjectCIRun, "branch"},
	}
	capped := map[site]int{}
	for _, e := range batch.Entities {
		for name, value := range e.Properties {
			if value.String != nil && utf8.RuneCountInString(*value.String) == contractsv1.ContextFabricClaimedFactValueMaxLength {
				capped[site{e.Subject.Kind, name}]++
			}
		}
	}
	for _, want := range wantSites {
		if capped[want] == 0 {
			t.Fatalf("%s.%s was never observed capped at the bound: that write site is unproven by this corpus (saw %v)", want.kind, want.property, capped)
		}
	}
	if len(capped) != len(wantSites) {
		t.Fatalf("capped %d (kind, property) pairs, enumerated %d -- a site appeared that this ledger does not name, so the enumeration is stale: %v", len(capped), len(wantSites), capped)
	}
}

// TestPropertySitesLeftOutOfTheCorpusAreBoundedBySomethingElse is the other
// half of the enumeration: a site absent from the corpus above is either
// covered by a different bound or it is a hole, and the two look identical
// from a passing test.
//
// Three sites pass through joinedSortedList, which caps element COUNT and
// element WIDTH, so no source value can push them near the scalar bound. Four
// more carry the joined repository slug, which the AUTHORIZATION bound already
// holds at 512 runes -- a row whose slug is longer never projects at all, so
// its `repo` property cannot reach 4,000 either.
func TestPropertySitesLeftOutOfTheCorpusAreBoundedBySomethingElse(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, joined := range []struct {
		property             string
		elements, width, sep int
	}{
		{"tags", 10, 40, 1},         // tables.go, parsedRepoTags -> joinedSortedList(10, 40, " ")
		{"labels", 10, 40, 2},       // tables.go, joinedSortedList(10, 40, ", ")
		{"project_keys", 10, 80, 2}, // teams_projects.go, joinedSortedList(10, 80, ", ")
	} {
		ceiling := joined.elements*joined.width + (joined.elements-1)*joined.sep
		if ceiling >= contractsv1.ContextFabricClaimedFactValueMaxLength {
			t.Fatalf("property %q can reach %d runes, which is NOT under the %d-rune bound -- it belongs in the executed corpus, not in this exemption list",
				joined.property, ceiling, contractsv1.ContextFabricClaimedFactValueMaxLength)
		}
		checked++
	}
	// The `repo` property is the repository slug, and the slug is an
	// AUTHORIZATION value before it is a property. Proven, not asserted: a
	// scope carrying a slug one rune past its bound is refused.
	scope := contractsv1.ContextFabricAuthorizationScope{RepositorySlugs: []string{bigText(contractsv1.ContextFabricSubjectRefLabelMaxLength + 1)}}
	err := scope.Validate()
	if err == nil {
		t.Fatal("an oversize repository slug must be refused by the authorization bound; if it is not, the four `repo` property sites are genuinely uncapped and belong in the executed corpus")
	}
	if !strings.Contains(err.Error(), "authorization scope violates v1 bounds") {
		t.Fatalf("the slug must be refused by the AUTHORIZATION rule specifically, got %q", err)
	}
	checked++
	if contractsv1.ContextFabricSubjectRefLabelMaxLength >= contractsv1.ContextFabricClaimedFactValueMaxLength {
		t.Fatal("the authorization slug bound is not tighter than the scalar bound, so it does not cover the `repo` property sites")
	}
	checked++
	if checked != 5 {
		t.Fatalf("only %d of 5 exemption proofs ran", checked)
	}
}

// TestAnOversizeRepositorySlugIsNotThisPassesToRepair marks the boundary of
// this change, and it is a security boundary rather than a scoping
// convenience.
//
// A repository slug is the entity's LABEL, its BELONGS_TO_REPOSITORY endpoint
// label, AND the single value in its authorization scope
// (repoAuthorization, clickhouse.go). Capping the label is harmless; capping
// the AUTHORIZATION value would change which principals can see the node --
// two different long slugs sharing a 512-rune prefix would collapse onto one
// scope. So the label repair cannot save such a row, and it must not: the row
// stays quarantined, under the authorization bound, exactly as before this
// change.
func TestAnOversizeRepositorySlugIsNotThisPassesToRepair(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	huge := bigText(contractsv1.ContextFabricSubjectRefLabelMaxLength + 40)

	tables := []fakeTable{
		{match: "FROM repos", rows: [][]any{{"repo-1", huge, "synthetic", at, created, ""}}, cursorOf: repoCursorOf},
	}
	batch, _, _, quarantines := projectWithBothLogs(t, tables, "")
	for _, e := range batch.Entities {
		if e.Subject.Kind == contractsv1.ContextFabricSubjectRepository {
			t.Fatalf("the repository projected with label %q -- normalization repaired a value that is also an authorization scope, which silently widens who can read the node", e.Subject.Label)
		}
	}
	if len(quarantines) == 0 {
		t.Fatal("the row was neither projected nor quarantined: it vanished uncounted")
	}
}

// TestNormalizationCoversEveryLabelFamily seeds an untrimmed AND oversize
// label into every subject family this source mints from source text.
//
// This is the enumeration the ticket named three members of. The sweep for
// this change found nine more label write sites across four files, which is
// why the repair is a pass and not an edit at three call sites -- and why this
// corpus is keyed on the SUBJECT KIND reaching the batch rather than on call
// sites: a new site minting an existing kind is covered by construction, while
// a new KIND is a visible gap.
//
// EVERY family reaches the counter, including the three the ticket names: no
// site repairs its own label first, so nothing is repaired upstream of the
// count. The one family that is not repairable at all is the repository, whose
// label is also its authorization scope (see
// TestAnOversizeRepositorySlugIsNotThisPassesToRepair).
func TestNormalizationCoversEveryLabelFamily(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Untrimmed AND oversize together, so both tokens can fire on one item.
	dirty := func(seed string) string {
		return "  " + seed + bigText(contractsv1.ContextFabricSubjectRefLabelMaxLength+40) + "  "
	}
	slug := "example-org/widget-service" // legal: it is an authorization value

	tables := []fakeTable{
		repoRow("repo-1", slug, "synthetic", at),
		// The three ticket-named title families. Both tokens fire on each.
		{match: "FROM work_items AS w", rows: [][]any{
			{"WI-1", "repo-1", slug, dirty("wi"), "open", "", at, created, uint8(0), zeroTime, "", "", "", []string{}}}},
		{match: "FROM git_pull_requests AS p", rows: [][]any{
			{"repo-1", slug, uint32(1042), dirty("pr"), "open", at, created, uint8(0), zeroTime, "main", ""}}},
		{match: "FROM operational_incidents AS i", rows: [][]any{
			{"incident-1", "repo-1", slug, dirty("inc"), "open", "low", at, uint8(0), uint8(1), created, uint8(0), zeroTime, ""}}},
		// The families the ticket does NOT name: the deployment label is built
		// from the environment column, and the ref stub's label is the raw
		// target id. These are the sites a three-site fix would have missed.
		{match: "FROM deployments AS d", rows: [][]any{
			{"repo-1", slug, "deploy-1", "success", dirty("env"), at, uint8(1), created, uint8(0), zeroTime, "v1"}}},
		// A CLEAN unresolved target, so the work_item_ref family is present
		// and demonstrably untouched. It cannot carry a dirty label here:
		// identity.DeriveWorkItemRef builds the canonical id from that same
		// raw target id, so a dirty target is an IDENTITY breach, not a label
		// one -- see TestAnUntrimmedDependencyTargetIsAnIdentityBreachNotALabelOne.
		{match: dependencyTable, rows: [][]any{
			{"WI-1", "T-UNRESOLVED", "BLOCKS", "repo-1", slug, at, created, uint8(0), zeroTime, uint8(0), zeroTime, uint8(0), zeroTime, ""}}},
		// A clean CI run, so its label family is present and demonstrably
		// untouched rather than absent.
		{match: "FROM ci_pipeline_runs AS c", rows: [][]any{
			{"run-1", "repo-1", "main", "success", slug, at, created, uint8(0), zeroTime, "pipeline"}}},
	}

	batch, available, normalizations, quarantines := projectWithBothLogs(t, tables, "")
	if !available {
		t.Fatal("no batch: every seeded row was lost")
	}
	if assertBatchIsWithinBounds(t, batch) == 0 {
		t.Fatal("the postcondition inspected nothing")
	}
	for _, gone := range []string{"untrimmed_label", "oversize_scalar"} {
		if n := reasonTally(quarantines, "quarantine_reason")[gone]; n != 0 {
			t.Fatalf("%s quarantines = %d, want 0: %+v", gone, n, quarantines)
		}
	}
	tally := reasonTally(normalizations, "normalization_reason")
	if tally["label_capped"] == 0 {
		t.Fatalf("label_capped never fired on a corpus of five oversize labels: %v", tally)
	}
	// EVERY dirty label family must reach BOTH counters -- five of them here,
	// each untrimmed AND oversize. This is the positive control that no site
	// has quietly started repairing its own label upstream of the count: such
	// a site would still project a clean label and this assertion is the only
	// thing that would notice the counter had gone quiet.
	const dirtyLabelFamilies = 5 // work item, pull request, incident, deployment, ref stub
	if tally["label_trimmed"] < dirtyLabelFamilies {
		t.Fatalf("label_trimmed = %d, want at least %d (one per dirty label family) -- a family whose label is repaired before the pass runs is repaired SILENTLY: %v",
			tally["label_trimmed"], dirtyLabelFamilies, tally)
	}
	if tally["label_capped"] < dirtyLabelFamilies {
		t.Fatalf("label_capped = %d, want at least %d: %v", tally["label_capped"], dirtyLabelFamilies, tally)
	}

	wantKinds := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectRepository,
		contractsv1.ContextFabricSubjectWorkItem,
		contractsv1.ContextFabricSubjectPullRequest,
		contractsv1.ContextFabricSubjectDeployment,
		contractsv1.ContextFabricSubjectIncident,
		contractsv1.ContextFabricSubjectCIRun,
		contractsv1.ContextFabricSubjectWorkItemRef,
	}
	seen := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, e := range batch.Entities {
		seen[e.Subject.Kind] = true
	}
	for _, kind := range wantKinds {
		if !seen[kind] {
			t.Fatalf("subject kind %q never reached the batch: this label family is unproven (saw %v)", kind, seen)
		}
	}
	// The BELONGS_TO_REPOSITORY edge labels its endpoints from clickhouse.go
	// rather than tables.go -- a separate write site, and one a three-site fix
	// would have missed.
	edges := 0
	for _, r := range batch.Relationships {
		if r.Type == contractsv1.ContextFabricRelationshipBelongsToRepository {
			edges++
		}
	}
	if edges == 0 {
		t.Fatal("no BELONGS_TO_REPOSITORY edge reached the batch, so the endpoint-label site in clickhouse.go is unproven here")
	}
}

// TestAnUntrimmedDependencyTargetIsAnIdentityBreachNotALabelOne records the
// second boundary this change deliberately does not cross, found by building
// the label-family corpus above rather than by reading the code.
//
// A work_item_ref stub labels itself with the RAW target_work_item_id, so
// "a work_item_ref with an untrimmed label" looks like exactly the defect this
// pass repairs. It is not. identity.DeriveWorkItemRef mints the canonical id
// as "work_item_ref:" + EncodeSegment(raw) from that same string, so the
// whitespace is in the IDENTITY too, and SubjectRef.Validate refuses an
// untrimmed canonical id under the same rule it refuses an untrimmed label.
// Repairing only the label leaves the row rejected under the other half of
// that rule; repairing both would re-point the node.
//
// So the row still quarantines, and the fix -- trimming the target id BEFORE
// deriving -- changes the canonical id of anything already projected under the
// untrimmed spelling. That is a rebuild decision, reported rather than taken.
func TestAnUntrimmedDependencyTargetIsAnIdentityBreachNotALabelOne(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tables := dependencyTablesOnly(t, at, [][]any{
		unresolvedDependencyRow("WI-1", "  T-DIRTY  ", "BLOCKS", at, created),
	})
	_, _, normalizations, quarantines := projectWithBothLogs(t, tables, testCursor(t, created.Add(-time.Hour), ""))

	tally := reasonTally(quarantines, "quarantine_reason")
	if tally["untrimmed_label"] == 0 {
		t.Fatalf("an untrimmed dependency target must STILL quarantine: its canonical id carries the same whitespace, and normalization must not rewrite identity to rescue it: %v", tally)
	}
	// And it must not be reported as repaired: a normalization counter that
	// fires on a row which was dropped anyway is worse than no counter.
	for _, entry := range normalizations {
		if fmt.Sprint(entry["normalization_reason"]) == "label_trimmed" {
			t.Fatalf("the pass claimed to have trimmed a label on a row it could not save: %+v", entry)
		}
	}
}

// TestProdShapedPageStillReportsZeroForTheNormalizedBounds re-runs the merged
// tip's prod-shaped page -- 105 illegal + 96 legal dependency rows, the exact
// shape that stalled the affected organization -- with the three normalizable
// defects added on top, and asserts the claim this ticket is FOR: those three
// quarantine counters read zero, while the unknown-vocabulary counter is
// untouched.
//
// The negative has a POSITIVE CONTROL in the same run: unknown_relationship_type
// must still be reported 105 times. A run where every quarantine counter read
// zero because nothing was quarantined at all would satisfy the negative and
// prove nothing.
func TestProdShapedPageStillReportsZeroForTheNormalizedBounds(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const illegal, legal = 105, 96
	rows := make([][]any, 0, illegal+legal)
	for i := 0; i < illegal; i++ {
		rows = append(rows, unresolvedDependencyRow(fmt.Sprintf("WI-X%03d", i), fmt.Sprintf("EXT-%03d", i), "EXTERNAL_ISSUE_KEY", at.Add(time.Duration(i)*time.Second), created))
	}
	for i := 0; i < legal; i++ {
		rows = append(rows, dependencyRow(fmt.Sprintf("WI-G%03d", i), fmt.Sprintf("WI-H%03d", i), "RELATES_TO", at.Add(time.Duration(illegal+i)*time.Second), created))
	}

	tables := dependencyTablesOnly(t, at, rows)
	// Layer the three normalizable defects onto the SAME page, so the counters
	// are read under the prod-shaped load rather than in isolation.
	// dependencyTablesOnly KEEPS every base table entry and merely nils its
	// rows, so these must REPLACE the existing entries: appending a second
	// fakeTable with the same match is shadowed by the first and the fixture
	// silently seeds nothing -- which reads exactly like a passing negative.
	for index, table := range tables {
		switch table.match {
		case "FROM work_items AS w":
			tables[index].cursorOf = workItemCursorOf
			tables[index].rows = [][]any{
				// An oversize free-text property, and an inverted window on a second row.
				{"WI-SCALAR", "repo-1", "example-org/widget-service", "scalar", "open", "", at, created, uint8(0), zeroTime, "", "", bigText(overRunes), []string{}},
				{"WI-INVERTED", "repo-1", "example-org/widget-service", "inverted", "open", "", at,
					time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), uint8(1), time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), "", "", "", []string{}},
			}
		case "FROM deployments AS d":
			// The untrimmed label arrives through the deployment environment
			// rather than a work item title: the three ticket-named title sites
			// trim at the row, so a work_items row cannot exercise the pass's
			// trim at all.
			tables[index].rows = [][]any{
				{"repo-1", "example-org/widget-service", "deploy-1", "success", "  staging  ", at, uint8(1), created, uint8(0), zeroTime, "v1"}}
		}
	}

	_, available, normalizations, quarantines := projectWithBothLogs(t, tables, testCursor(t, created.Add(-time.Hour), ""))
	if !available {
		t.Fatal("the prod-shaped page produced no batch")
	}

	quarantineTally := reasonTally(quarantines, "quarantine_reason")
	// POSITIVE CONTROL first: the counter that must NOT go to zero.
	if quarantineTally["unknown_relationship_type"] == 0 {
		t.Fatalf("unknown_relationship_type = 0, so the zero-assertions below are vacuous -- nothing was quarantined at all: %v", quarantineTally)
	}
	for _, gone := range []string{"untrimmed_label", "inverted_window", "oversize_scalar"} {
		if n := quarantineTally[gone]; n != 0 {
			t.Fatalf("%s = %d on the prod-shaped page, want 0 -- these three bounds are supposed to cost no rows any more: %v", gone, n, quarantineTally)
		}
	}
	normalizationTally := reasonTally(normalizations, "normalization_reason")
	for _, want := range []string{"label_trimmed", "scalar_capped", "window_collapsed"} {
		if normalizationTally[want] == 0 {
			t.Fatalf("%s was never reported: the defects moved out of the quarantine counter without arriving in the normalization counter, which would mean they were repaired SILENTLY: %v", want, normalizationTally)
		}
	}
}

// TestTeamsProjectsSourceNormalizesThroughTheSamePass proves the second source
// on the shared assembly path reaches the pass too.
//
// This is the gap quarantineLogger's own comment records: when per-item
// quarantine shipped, the ClickHouse source got the telemetry hook and
// TeamsProjectsSource did not, so every item that source dropped was dropped
// silently. The same wiring mistake is available here, one field over, and
// only a fixture through THIS source can catch it.
func TestTeamsProjectsSourceNormalizesThroughTheSamePass(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 6, 30, 10, 47, 54, 0, time.UTC)
	dirty := "  " + bigText(contractsv1.ContextFabricSubjectRefLabelMaxLength+40) + "  "

	client := liveShapedTeamsProjectsClient()
	for i, table := range client.tables {
		switch {
		case strings.Contains(table.match, "FROM teams"):
			client.tables[i].rows = [][]any{teamRow("team-1", dirty, "", "github", "TEAM", uint8(1), at, nil, nil)}
		case strings.Contains(table.match, "FROM projects"):
			client.tables[i].rows = [][]any{projectRow("project-1", dirty, "PROJ", "github", "active", "", uint8(1), at)}
		default:
			client.tables[i].rows = nil
		}
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	batch, available, err := source.WithLogger(logger).NextProjectionBatch(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("no batch from the teams/projects source")
	}
	if assertBatchIsWithinBounds(t, batch) == 0 {
		t.Fatal("the postcondition inspected nothing")
	}
	tally := reasonTally(normalizationObservations(t, buf.String()), "normalization_reason")
	if tally["label_trimmed"] == 0 || tally["label_capped"] == 0 {
		t.Fatalf("the teams/projects source reported no label normalization (%v) -- it is on the shared assembly path but its observeNormalization hook is not wired, exactly the gap quarantine telemetry had on this source", tally)
	}
	// The source name on the line must be this source's, not the other's: a
	// hook wired to the wrong constant would still produce lines.
	for _, entry := range normalizationObservations(t, buf.String()) {
		if got := fmt.Sprint(entry["source"]); got != devhealthsource.TeamsProjectsSourceName {
			t.Fatalf("normalization line reports source %q, want %q", got, devhealthsource.TeamsProjectsSourceName)
		}
	}
}
