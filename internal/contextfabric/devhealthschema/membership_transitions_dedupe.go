package devhealthschema

// DedupedMembershipTransitions is the relation every membership READER of
// project_membership_transitions takes its touches from (CHAOS-7829).
//
// The table is ReplacingMergeTree(last_synced) ORDER BY (org_id, subject_kind,
// repo_id, subject_id, occurred_at, event_id): occurred_at is part of the
// sorting key, so FINAL cannot collapse one event written at TWO occurred_at
// values. A provider-native event id ("linear:<history id>", "jira:<history id>",
// an external eventId) is the event's identity; the provider may move the
// event's timestamp between two syncs (measured on prod: 3 Linear events stored
// at two occurred_at values, +145 s, +157 s, +1,993 s, the later copy from a
// later re-sync). Read raw, the two copies are two touches of one (subject,
// project) pair: the second ADD is flagged duplicate, a second REMOVE dangling,
// and both carry the same row key and the same ingest stamp, so a keyset page
// can end between them.
//
// The rule, written once: ONE row per (org_id, subject_kind, repo_id,
// subject_id, provider, event_id), the one with the latest last_synced, then
// (when the column exists) the latest ingested_at, then the latest occurred_at.
// It is a reader rule only: no rekey, no writer change. A hash event id
// (projectmembership.EventID) contains occurred_at, so two rows with one hash id
// and two occurred_at cannot exist and FINAL already collapses equal keys: the
// dedupe changes the result only for native-id duplicates.
//
// ingested bool says whether project_membership_transitions.ingested_at exists
// on the server (ops migration 100); the callers that did not read it before
// pass false.
//
// The org filter is applied here, before the dedupe, and again by the caller's
// own WHERE (the callers keep their {org_id:String} predicate).
func DedupedMembershipTransitions(ingested bool) string {
	if ingested {
		return DedupedMembershipTransitionsIngested
	}
	return DedupedMembershipTransitionsLastSynced
}

// The two spellings of the relation, as constants so the SQL constants of the
// readers can be concatenated from them at compile time.
const (
	// DedupedMembershipTransitionsLastSynced orders by last_synced, then occurred_at.
	DedupedMembershipTransitionsLastSynced = "(SELECT * FROM project_membership_transitions FINAL WHERE org_id = {org_id:String} " +
		"ORDER BY last_synced DESC, occurred_at DESC " +
		"LIMIT 1 BY org_id, subject_kind, repo_id, subject_id, provider, event_id)"
	// DedupedMembershipTransitionsIngested orders by last_synced, ingested_at, then occurred_at.
	DedupedMembershipTransitionsIngested = "(SELECT * FROM project_membership_transitions FINAL WHERE org_id = {org_id:String} " +
		"ORDER BY last_synced DESC, ingested_at DESC, occurred_at DESC " +
		"LIMIT 1 BY org_id, subject_kind, repo_id, subject_id, provider, event_id)"
)
