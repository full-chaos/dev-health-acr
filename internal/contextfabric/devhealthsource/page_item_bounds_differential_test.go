package devhealthsource

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// mainTruncateToCompleteRows is the page cut before the batch item bounds: the
// row cap and the truncated-table bound only. It is the reference the head cut
// must equal on every page that reference publishes.
func mainTruncateToCompleteRows(all []candidate, maxRows int, bound pageBound) []candidate {
	rows := 0
	for i, c := range all {
		if i == 0 || !c.position().Equal(all[i-1].position()) || c.sortKey != all[i-1].sortKey {
			if rows == maxRows {
				return all[:i]
			}
			if bound.passedBy(c) {
				return all[:i]
			}
			rows++
		}
	}
	return all
}

// differentialPage builds one random page from seed, in one of three modes:
// light (far inside the bounds), near (raw items around the relationship bound,
// with incidents read twice, edge rows of their own whose endpoint is an
// incident of the page, now and then an incident past the entity
// authorization bound, and untrimmed titles), and heavy (rows past a bound
// alone). Each call builds fresh items.
func differentialPage(seed int64) []candidate {
	rng := rand.New(rand.NewSource(seed))
	at := fanOutStamp
	var stats incidentPageStats
	var all []candidate
	var made []incidentRow
	mode := rng.Intn(10)
	fanOut := func() (active, ended int) {
		switch {
		case mode < 4:
			return rng.Intn(30), rng.Intn(10)
		case mode < 8:
			if rng.Intn(100) < 2 {
				return 5001 + rng.Intn(20), 0
			}
			return 20 + rng.Intn(10), rng.Intn(3)
		default:
			if rng.Intn(100) < 2 {
				return 2600 + rng.Intn(2600), 2600 + rng.Intn(2600)
			}
			return rng.Intn(60), rng.Intn(40)
		}
	}
	rows := 120 + rng.Intn(110)
	for n := 0; n < rows; n++ {
		at = at.Add(time.Duration(1+rng.Intn(3)) * time.Millisecond)
		key := fmt.Sprintf("row-%04d", n)
		kind := rng.Intn(100)
		switch {
		case kind < 8 && len(made) > 0:
			row := made[rng.Intn(len(made))]
			row.observedAt, row.cursorAt = at, at
			for _, c := range incidentRowCandidates(row, &stats) {
				c.cursorAt, c.sortKey = at, key
				all = append(all, c)
			}
		case kind < 16 && len(made) > 0:
			row := made[rng.Intn(len(made))]
			for _, c := range incidentRowCandidates(row, &stats) {
				if c.relationship != nil {
					edge := *c.relationship
					all = append(all, candidate{observedAt: at, cursorAt: at, sortKey: key, relationship: &edge})
					break
				}
			}
		default:
			active, ended := fanOut()
			repos := make([]string, 0, active)
			for r := 0; r < active; r++ {
				repos = append(repos, fmt.Sprintf("%s|acme/r%05d", fanOutActiveRepo(r), r))
			}
			endedRepos := make([]string, 0, ended)
			for r := 0; r < ended; r++ {
				endedRepos = append(endedRepos, fanOutEndedRepo(r))
			}
			title := fmt.Sprintf("incident %d", n)
			if rng.Intn(100) < 10 {
				title = " " + title
			}
			row := incidentRow{
				id: fanOutIncidentID(seedRowID(seed, n)), title: title, activeRepos: parseIncidentRepos(repos), endedRepos: endedRepos,
				observedAt: at, cursorAt: at,
			}
			made = append(made, row)
			for _, c := range incidentRowCandidates(row, &stats) {
				c.cursorAt, c.sortKey = at, key
				all = append(all, c)
			}
		}
	}
	sortCandidates(all)
	return all
}

func seedRowID(seed int64, n int) int { return int(seed%1000)*1000 + n }

// differentialBatch builds the batch for one page the way pagedBatch does:
// normalize, judge, build. It reports the batch JSON, or the build error.
func differentialBatch(page []candidate, judge func([]candidate, func(quarantineObservation)) []candidate) ([]byte, []string, error) {
	var observed []string
	normalizeCandidates(page, func(o normalizationObservation) { observed = append(observed, "n:"+o.Kind+"/"+o.Reason) })
	items := judge(page, func(o quarantineObservation) { observed = append(observed, "q:"+o.Kind+"/"+o.Reason) })
	if !carriesPayload(items) {
		return nil, observed, errNoPayload
	}
	batch, err := buildBatch("org", "page_item_bounds_test", "v1", "", page, items, false, false, fanOutStamp)
	if err != nil {
		return nil, observed, err
	}
	out, err := json.Marshal(batch)
	return out, observed, err
}

var errNoPayload = fmt.Errorf("page carries no item")

// Every page the cut before the item bounds publishes, the head cut publishes
// byte for byte: the same rows, the same batch JSON and the same repair and
// quarantine observations. A page that reference cannot publish (it passes a
// bound) publishes a valid batch at head.
func TestPageCutIsByteEqualOnEveryPageThatPublishedBefore(t *testing.T) {
	// Under the race detector the sweep is the first pages of the same seed:
	// it checks no concurrency, and the whole sweep runs in every unit leg.
	const seed = 9826
	pages, minPublished, minGuarded, minWedged := int64(80), 40, 20, 6
	if raceDetectorEnabled {
		pages, minPublished, minGuarded, minWedged = 12, 6, 3, 1
	}
	t.Logf("seed=%d pages=%d", seed, pages)
	published, wedged, guarded := 0, 0, 0
	for i := int64(0); i < pages; i++ {
		pageSeed := seed*1000 + i
		reference := mainTruncateToCompleteRows(differentialPage(pageSeed), incrementalBatchCap, pageBound{})
		want, wantObserved, wantErr := differentialBatch(reference, func(all []candidate, observe func(quarantineObservation)) []candidate {
			return judgeCandidates(all, observe, nil)
		})
		headAll := differentialPage(pageSeed)
		head, _ := truncateToCompleteRows(headAll, incrementalBatchCap, pageBound{}, nil)
		got, gotObserved, gotErr := differentialBatch(head, partitionProjectableCandidates)
		if wantErr == errNoPayload {
			continue
		}
		if wantErr != nil {
			wedged++
			if gotErr != nil || len(head) == 0 {
				t.Fatalf("page seed %d: the reference wedges (%v) and head builds no batch: %v", pageSeed, wantErr, gotErr)
			}
			continue
		}
		published++
		var raw batchItems
		for _, c := range reference {
			raw.add(c)
		}
		if !raw.fit() {
			guarded++
		}
		if len(head) != len(reference) {
			t.Fatalf("page seed %d: head kept %d candidates, the reference %d", pageSeed, len(head), len(reference))
		}
		if gotErr != nil || string(got) != string(want) {
			t.Fatalf("page seed %d: head batch differs from the reference (err %v)", pageSeed, gotErr)
		}
		if fmt.Sprint(gotObserved) != fmt.Sprint(wantObserved) {
			t.Fatalf("page seed %d: observations differ: head %d, reference %d", pageSeed, len(gotObserved), len(wantObserved))
		}
	}
	t.Logf("pages the reference publishes: %d (byte-equal at head; %d of them pass a bound in raw items); pages it wedges on: %d (valid at head)", published, guarded, wedged)
	if published < minPublished || guarded < minGuarded || wedged < minWedged {
		t.Fatalf("the sweep is vacuous: %d published, %d past a bound in raw items, %d wedged; want at least %d, %d and %d", published, guarded, wedged, minPublished, minGuarded, minWedged)
	}
}
