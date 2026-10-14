package devhealthsource

import (
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-go/readers"
)

// The project-team ownership statement exactly as it was before the ownership
// history was collapsed ahead of the arms: the differential oracle the new
// statement is compared against on real ClickHouse. Do not edit it to follow
// the production statement.

func legacyProjectTeamsOwnershipRowsSQL(ingest bool) string {
	return `(
		SELECT provider, ` + readers.ProjectOwnershipJoinColumn + ` AS project_ref, ifNull(project_key, '') AS project_key, team_id, toString(source) AS source_name, valid_from, valid_to, updated_at` + ingestStampSQL(ingest, "last_synced AS ingest_at") + `
		FROM team_project_ownership FINAL
		WHERE org_id = {org_id:String}
	)`
}

func legacyProjectTeamsAssertingArm(resolved string, ingest bool) string {
	ownership := `(
		SELECT provider, project_ref, project_key, team_id, source_name, valid_from, valid_to, updated_at` + ingestStampSQL(ingest, "ingest_at") + `,
		       project_ref AS scope_value, '' AS required_scope_kind
		FROM ` + legacyProjectTeamsOwnershipRowsSQL(ingest) + `

		UNION ALL

		SELECT provider, project_ref, project_key, team_id, source_name, valid_from, valid_to, updated_at` + ingestStampSQL(ingest, "ingest_at") + `,
		       project_key AS scope_value, 'key' AS required_scope_kind
		FROM ` + legacyProjectTeamsOwnershipRowsSQL(ingest) + `
	) AS o`
	return `
		SELECT p.id AS project_id, p.provider AS provider,
		       o.project_ref AS ownership_ref, o.project_key AS ownership_key,
		       o.team_id AS team_id, o.source_name AS source_name, o.valid_from AS valid_from, o.valid_to AS valid_to, o.updated_at AS updated_at` + ingestStampSQL(ingest, "o.ingest_at AS ingest_at") + `,
		       p.project_updated_at AS project_updated_at, p.provider_updated_at AS provider_updated_at` + ingestStampSQL(ingest, "p.project_ingest_at AS project_ingest_at, p.provider_ingest_at AS provider_ingest_at") + `, toUInt8(0) AS retraction_only
		FROM ` + resolved + `
		INNER JOIN ` + ownership + ` ON o.provider = p.provider AND ` + readers.ProjectIdentityMatchSQL("o", "scope_value") + `
		INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND ` + devhealthschema.ActiveTeamPredicate("") + `) AS t ON t.id = o.team_id
		WHERE o.required_scope_kind = '' OR p.scope_kind = o.required_scope_kind`
}

func legacyProjectTeamsRetractionArm(ambiguous string, ingest bool) string {
	ownership := `(
		SELECT provider, project_ref, project_key, team_id, source_name, valid_from, valid_to, updated_at` + ingestStampSQL(ingest, "ingest_at") + `,
		       project_key AS match_value
		FROM ` + legacyProjectTeamsOwnershipRowsSQL(ingest) + `
		WHERE project_key != ''

		UNION ALL

		SELECT provider, project_ref, project_key, team_id, source_name, valid_from, valid_to, updated_at` + ingestStampSQL(ingest, "ingest_at") + `,
		       project_ref AS match_value
		FROM ` + legacyProjectTeamsOwnershipRowsSQL(ingest) + `
		WHERE project_key = ''
	) AS o`
	return `
		SELECT p.id AS project_id, p.provider AS provider,
		       o.project_ref AS ownership_ref, o.project_key AS ownership_key,
		       o.team_id AS team_id, o.source_name AS source_name, o.valid_from AS valid_from, o.valid_to AS valid_to, o.updated_at AS updated_at` + ingestStampSQL(ingest, "o.ingest_at AS ingest_at") + `,
		       p.project_updated_at AS project_updated_at, p.provider_updated_at AS provider_updated_at` + ingestStampSQL(ingest, "p.project_ingest_at AS project_ingest_at, p.provider_ingest_at AS provider_ingest_at") + `, toUInt8(1) AS retraction_only
		FROM ` + ambiguous + `
		INNER JOIN ` + ownership + ` ON o.provider = p.provider AND o.match_value = p.project_key
		INNER JOIN (SELECT id FROM teams FINAL WHERE org_id = {org_id:String} AND ` + devhealthschema.ActiveTeamPredicate("") + `) AS t ON t.id = o.team_id
		WHERE p.key_project_count > 1`
}

func legacyProjectTeamsArmsFor(ingest bool) []string {
	resolved := projectIdentityWithWatermarkSQL(ingest)
	ambiguous := ambiguousProjectIdentitySQL(ingest)
	return []string{
		legacyProjectTeamsAssertingArm(resolved, ingest),
		legacyProjectTeamsRetractionArm(ambiguous, ingest),
	}
}

func legacyProjectTeamsGroupSQL(ingest bool) string {
	const identityPartition = " OVER (PARTITION BY provider, ownership_ref, ownership_key, retraction_only)"
	// The provider-wide project stamp widens WHICH groups a cursor revisits. It
	// belongs in the cursor position only: observed_at is the edge's own
	// ObservedAt and a tombstone's EffectiveAt, which an unrelated project's
	// write must not move. On the ingest cursor the position is row_ingest, so
	// observed_at stays free of it; the legacy cursor's position IS observed_at,
	// so there it carries the term.
	legacyProviderTerm := ""
	if !ingest {
		legacyProviderTerm = ", max(provider_updated_at)" + identityPartition
	}
	return `SELECT o.project_id, o.team_id, o.source_name,
       minIf(o.valid_from, o.unassertable = 0) AS first_valid_from,
       argMaxIf(tuple(o.valid_to), ` + ownershipFactOrder + `, o.unassertable = 0).1 IS NULL AS latest_is_open,
       ifNull(argMaxIf(tuple(o.valid_to), ` + ownershipFactOrder + `, o.unassertable = 0).1, toDateTime64(0, 3, 'UTC')) AS latest_valid_to,
       ` + projectTeamsWatermark + ` AS observed_at, o.provider,
       toUInt8(countIf(o.unassertable = 0) = 0) AS edge_suppressed,
       groupUniqArrayIf(concat(o.ownership_ref, '\0', o.ownership_key, '\0', o.team_id, '\0', o.source_name), o.unassertable = 1 AND o.retraction_only = 0) AS conflict_identities,
       toUInt8(countIf(o.unassertable = 1 AND o.retraction_only = 0) > 0) AS conflicting_identity_present` + ingestStampSQL(ingest, projectTeamsIngestWatermark+" AS ingest_at") + `
FROM (
	SELECT project_id, provider, ownership_ref, ownership_key, team_id, source_name, valid_from, valid_to, retraction_only,
	       greatest(updated_at, max(project_updated_at)` + identityPartition + legacyProviderTerm + `) AS row_watermark,` + ingestStampSQL(ingest, " greatest(ingest_at, max(project_ingest_at)"+identityPartition+", max(provider_ingest_at)"+identityPartition+") AS row_ingest,") + `
	       toUInt8(retraction_only = 1 OR min(project_id)` + identityPartition + ` != max(project_id)` + identityPartition + `) AS unassertable
	FROM (` + strings.Join(legacyProjectTeamsArmsFor(ingest), "\n\n\t\tUNION ALL\n") + `
	)
) AS o
GROUP BY o.project_id, o.provider, o.team_id, o.source_name`
}

func legacyProjectTeamsStatementFor(cursor cursorState, ingest bool) string {
	cursorWatermark := projectTeamsWatermark
	if ingest {
		cursorWatermark = projectTeamsIngestWatermark
	}
	return legacyProjectTeamsGroupSQL(ingest) + havingSincePredicate(cursor, cursorWatermark, projectTeamsRowKey) + orderBy(cursorWatermark, projectTeamsRowKey)
}
