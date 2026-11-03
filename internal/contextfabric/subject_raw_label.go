package contextfabric

import (
	"slices"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12619: until this change the relationship writers labelled a work item, a
// work item ref, a project or a team end with its raw source id, and the fact of an
// edge is composed from its end labels. So a stored graph holds facts such as
// "linear:CHAOS-2320 belongs to project c67b1602-…" and stub nodes labelled
// "gh:owner/repo#5059", and a reader saw them. The writers now give a work item end
// its key. A stored graph keeps the raw form until a rebuild, and changing the
// source version would stop projection for one, so the read gives such a label and
// such a fact end their reader form. The raw ids stay in the data (canonical ids,
// evidence refs) for the collapsed Details (CHAOS-4690).

// RawSubjectLabel is the raw source id a relationship writer used as this
// subject's label before CHAOS-12619, read back from its canonical id: a work
// item's or a work item ref's work_item_id, a project's source id, a team's raw
// key. ok is false for any other kind, and for a canonical id of another form.
func RawSubjectLabel(kind SubjectKind, canonicalID string) (string, bool) {
	switch kind {
	case SubjectWorkItem:
		rest, ok := strings.CutPrefix(canonicalID, identity.KindWorkItem+".v2:")
		if !ok {
			return "", false
		}
		return lastDecodedSegment(rest)
	case SubjectWorkItemRef:
		rest, ok := strings.CutPrefix(canonicalID, identity.KindWorkItemRef+":")
		if !ok || rest == "" {
			return "", false
		}
		raw, err := identity.DecodeSegment(rest)
		return raw, err == nil && raw != ""
	case SubjectProject:
		rest, ok := strings.CutPrefix(canonicalID, identity.KindProject+".v2:")
		if !ok {
			return "", false
		}
		return lastDecodedSegment(rest)
	case SubjectTeam:
		return TeamRawKey(canonicalID)
	}
	return "", false
}

func lastDecodedSegment(rest string) (string, bool) {
	cut := strings.LastIndexByte(rest, ':')
	if cut < 0 || cut == len(rest)-1 {
		return "", false
	}
	raw, err := identity.DecodeSegment(rest[cut+1:])
	return raw, err == nil && raw != ""
}

// ReadableSourceID is a raw source id as a reader names the subject. A work item
// or a work item ref reads as its key: the provider prefix is dropped, so
// linear:CHAOS-1 reads CHAOS-1, gh:owner/repo#7 reads owner/repo#7 and
// extkey:CHAOS-9 reads CHAOS-9. A team and a nameless project read as their source
// id the same way (acme/live-e2e, OPS), so a team's project list never reads
// "project, project" (lead ruling, 2026-11-02); an org-scoped source id
// ("<org UUID>:gitlab:71133891") reads as its native id, 71133891 (lead ruling,
// 2026-11-03). A UUID names nothing a reader knows, so it reads as the kind's word
// from the label registry. Any other kind keeps the raw id. The key is
// contractsv1.ContextFabricSourceKey, the evidence label's own (CHAOS-12716).
func ReadableSourceID(kind SubjectKind, raw string) string {
	switch kind {
	case SubjectWorkItem, SubjectWorkItemRef, SubjectTeam, SubjectProject:
		key := contractsv1.ContextFabricSourceKey(raw)
		if key == "" {
			return kindWord(kind, raw)
		}
		return key
	}
	return raw
}

func kindWord(kind SubjectKind, fallback string) string {
	if word, ok := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularySubjectKind, string(kind)); ok && word != "" {
		return word
	}
	return fallback
}

// SubjectReadableLabel is a subject's label for a reader: a label that is the
// subject's raw source id reads as ReadableSourceID, and any other label stands.
func SubjectReadableLabel(subject SubjectRef) string {
	if raw, ok := RawSubjectLabel(subject.Kind, subject.CanonicalID); ok && subject.Label == raw {
		return ReadableSourceID(subject.Kind, raw)
	}
	return subject.Label
}

// RepairRawEdgeFact gives a stored edge fact's raw ends their reader form. The
// writer composes a fact as its From label, the relationship's words and its To
// label (falkorgraph.relationshipFact), so a fact is repaired only when it is that
// whole composition: an end the writer can have used for each endpoint (its own
// raw id, read back from its canonical id, that id's reader form, or its label),
// the relationship's words between them, and nothing else. Only an end that is the
// endpoint's raw id changes. A custom fact, even one that starts or ends like the
// composition, a title, or text that merely looks like an id is never touched.
// relation is the relationship type the edge carries. A work item end reads as its
// key; a project or a team end reads as the endpoint's own label when the graph
// carries one, and else as ReadableSourceID.
func RepairRawEdgeFact(fact, relation string, from, to SubjectRef) string {
	words := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(relation), "_", " "))
	if words == "" {
		return fact
	}
	fromRaw, fromOK := factEndRawID(from)
	toRaw, toOK := factEndRawID(to)
	if !fromOK && !toOK {
		return fact
	}
	for _, head := range factEndForms(from, fromRaw, fromOK) {
		tail, found := strings.CutPrefix(fact, head+" "+words+" ")
		if !found || !slices.Contains(factEndForms(to, toRaw, toOK), tail) {
			continue
		}
		fromRepair, toRepair := fromOK && head == fromRaw, toOK && tail == toRaw
		if !fromRepair && !toRepair {
			return fact
		}
		if fromRepair {
			head = factEnd(from, fromRaw)
		}
		if !toRepair {
			return head + " " + words + " " + tail
		}
		// A To end with no reader name is the kind's word, and the
		// relationship's words already end with it ("belongs to project"), so
		// the end is left out rather than said twice.
		if end := factEnd(to, toRaw); end != kindWord(to.Kind, "") {
			return head + " " + words + " " + end
		}
		return head + " " + words
	}
	return fact
}

// factEndForms are the texts a writer can have named an endpoint by in a fact:
// its raw id, that id's reader form (the writer after CHAOS-12619), and its label.
// The raw id comes first, so a fact whose end is the raw id is read as such.
func factEndForms(end SubjectRef, raw string, ok bool) []string {
	forms := make([]string, 0, 3)
	if ok {
		forms = append(forms, raw, factEndReaderForm(end, raw))
	}
	if end.Label != "" {
		forms = append(forms, end.Label)
	}
	return forms
}

func factEnd(end SubjectRef, raw string) string {
	switch end.Kind {
	case SubjectProject, SubjectTeam:
		if label := strings.TrimSpace(end.Label); label != "" && end.Label != raw {
			return end.Label
		}
	}
	return factEndReaderForm(end, raw)
}

// factEndRawID is the raw id a writer can have named an endpoint by in a fact:
// RawSubjectLabel for a work item, work item ref, project or team, and for a pull
// request its canonical id, which the writers used until CHAOS-12468.
func factEndRawID(end SubjectRef) (string, bool) {
	if raw, ok := RawSubjectLabel(end.Kind, end.CanonicalID); ok {
		return raw, true
	}
	if _, ok := pullRequestFactLabel(end); ok {
		return end.CanonicalID, true
	}
	return "", false
}

// factEndReaderForm is the reader form of an endpoint's raw id in a fact. A pull
// request's is the label a graph read gives its canonical id
// (identity.PullRequestLabel, falkorgraph's edgeProperties), and that read runs
// before this repair: a stored "linear:CHAOS-1 links pull request pull_request:r:7"
// reaches RepairRawEdgeFact as "linear:CHAOS-1 links pull request PR #7". Both
// texts must match the pull request end, or its work item end is never repaired
// (CHAOS-12715). Every other kind's is ReadableSourceID.
func factEndReaderForm(end SubjectRef, raw string) string {
	if label, ok := pullRequestFactLabel(end); ok {
		return label
	}
	return ReadableSourceID(end.Kind, raw)
}

// pullRequestFactLabel is identity.PullRequestLabel of a pull request endpoint's
// number, read from its canonical id (pull_request:<repository>:<number>) the
// way falkorgraph's read reads it. ok is false for a canonical id of any other
// form, a review's (pull_request_review:…) among them.
func pullRequestFactLabel(end SubjectRef) (string, bool) {
	rest, ok := strings.CutPrefix(end.CanonicalID, "pull_request:")
	if !ok {
		return "", false
	}
	cut := strings.LastIndexByte(rest, ':')
	if cut <= 0 {
		return "", false
	}
	number, err := strconv.ParseInt(rest[cut+1:], 10, 64)
	if err != nil {
		return "", false
	}
	return identity.PullRequestLabel(number), true
}
