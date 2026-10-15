package devhealthschema

// The two operational ordering-contract tables keep every source revision of
// an id as its own sorting key, so FINAL leaves several rows per id. Readers
// take rows only from these relations: one row per (org_id, id), picked as ops
// storage/operational_current.py picks it. Every other predicate (is_deleted,
// is_active, validity, cursor) goes in the caller's WHERE, after the pick.
const (
	currentOperationalPick = "ORDER BY org_id, id, source_revision DESC, source_conflict_key DESC, ingest_revision DESC LIMIT 1 BY org_id, id"

	CurrentOperationalIncidents      = "(SELECT * FROM operational_incidents WHERE org_id = {org_id:String} " + currentOperationalPick + ")"
	CurrentServiceRepositoryMappings = "(SELECT * FROM operational_service_repository_mappings WHERE org_id = {org_id:String} " + currentOperationalPick + ")"
)

// OperationalRowVersion is the pick's order as one tuple, for an argMax over
// the rows of one id.
func OperationalRowVersion(alias string) string {
	return "(" + alias + ".source_revision, " + alias + ".source_conflict_key, " + alias + ".ingest_revision)"
}
