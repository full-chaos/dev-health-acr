package eventspec

// Teams and projects source: the read walks one shared cursor over six tables.
// Every table but the membership producer states its read per page and when
// the read drains; a page that moves the cursor past rows that are not new
// outside a replay is an error line.

const (
	// DevhealthsourceTableReadPageLogMessage is the message of the per-page line.
	DevhealthsourceTableReadPageLogMessage = "devhealthsource table read page"
	// DevhealthsourceTableReadDrainedLogMessage is the message of the drain summary.
	DevhealthsourceTableReadDrainedLogMessage = "devhealthsource table read drained"
	// DevhealthsourceTableReadNotNewLogMessage is the message of the error line.
	DevhealthsourceTableReadNotNewLogMessage = "devhealthsource table read page consumed rows that are not new and is not a replay"
	// DevhealthsourceTeamsProjectsSource is the source name the lines carry.
	DevhealthsourceTeamsProjectsSource = "dev_health_teams_projects"
)

// DevhealthsourceTableReadTables lists the tables that state their read this
// way: every table of the teams and projects cursor except the membership
// producer. A test holds it equal to the producer registry.
// devhealthschema:not-a-production-replica closed vocabulary of a log field; it spells table names and declares no column, type or engine
func DevhealthsourceTableReadTables() []string {
	return []string{"teams", "projects", "work_item_team_attributions", "team_project_ownership", "team_repo_ownership"}
}

func devhealthsourceTableReadSourceVocabulary() []string {
	return []string{DevhealthsourceTeamsProjectsSource}
}

const devhealthsourceTableReadUnbounded = "one line per page of the cursor walk for each table that has a row in the page, so the count follows the rows the organization holds in these tables divided by the page size."

// DevhealthsourceTableReadPage is the Info line per table and page: the rows
// the table's statement returned inside its limit, whether rows lay beyond
// it, and the rows the shared cursor moved past (the page cut can leave
// statement rows past the cursor). consumed_new counts rows this run had not
// moved past before; replay is a page that consumed rows and no new one.
var DevhealthsourceTableReadPage = Event{
	ID:                 "devhealthsource.table_read_page",
	Msg:                DevhealthsourceTableReadPageLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrManyPerRequest,
	UnboundedReason:    devhealthsourceTableReadUnbounded,
	Attribution:        []string{"org_id", "table", "run", "page_n"},
	BoundedAggregation: "at most one line per table per page, and only for a table with a statement row or a consumed row in the page; at most five tables. No row id: the last key is a digest.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: devhealthsourceTableReadSourceVocabulary()},
		{Key: "table", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: DevhealthsourceTableReadTables()},
		{Key: "run", Type: FieldInt, Presence: PresenceRequired},
		{Key: "page_n", Type: FieldInt, Presence: PresenceRequired},
		{Key: "statement_rows", Type: FieldInt, Presence: PresenceRequired},
		{Key: "statement_more", Type: FieldBool, Presence: PresenceRequired},
		{Key: "consumed_rows", Type: FieldInt, Presence: PresenceRequired},
		{Key: "consumed_new", Type: FieldInt, Presence: PresenceRequired},
		{Key: "consumed_total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "replay", Type: FieldBool, Presence: PresenceRequired},
		{Key: "last_stamp", Type: FieldString, Presence: PresenceRequired},
		{Key: "last_key_digest", Type: FieldString, Presence: PresenceRequired},
	},
}

// DevhealthsourceTableReadDrained is the Info line per table when the read
// finds every table drained: after rows were consumed, or at the first empty
// read of a run that started from an empty cursor.
var DevhealthsourceTableReadDrained = Event{
	ID:                 "devhealthsource.table_read_drained",
	Msg:                DevhealthsourceTableReadDrainedLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrManyPerRequest,
	UnboundedReason:    "one line per table each time the read drains, and a run drains again whenever new rows arrive after it, so the count follows how often the organization's tables change.",
	Attribution:        []string{"org_id", "table", "run"},
	BoundedAggregation: "at most five lines (one per table) each time a read drains.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: devhealthsourceTableReadSourceVocabulary()},
		{Key: "table", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: DevhealthsourceTableReadTables()},
		{Key: "run", Type: FieldInt, Presence: PresenceRequired},
		{Key: "rows_total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "pages", Type: FieldInt, Presence: PresenceRequired},
		{Key: "statements", Type: FieldInt, Presence: PresenceRequired},
	},
}

// DevhealthsourceTableReadNotNew is the Error line for a page that moved the
// cursor past rows of a table that this run had already moved past, on a page
// that is not a replay: two rows on one cursor position, or a page that
// changed between two attempts.
var DevhealthsourceTableReadNotNew = Event{
	ID:                 "devhealthsource.table_read_not_new",
	Msg:                DevhealthsourceTableReadNotNewLogMessage,
	Level:              LevelError,
	Multiplicity:       MultiplicityZeroOrManyPerRequest,
	UnboundedReason:    devhealthsourceTableReadUnbounded,
	Attribution:        []string{"org_id", "table", "run", "page_n"},
	BoundedAggregation: "at most one line per table per page, only for a page that consumed rows that are not new and is not a replay.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: devhealthsourceTableReadSourceVocabulary()},
		{Key: "table", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: DevhealthsourceTableReadTables()},
		{Key: "run", Type: FieldInt, Presence: PresenceRequired},
		{Key: "page_n", Type: FieldInt, Presence: PresenceRequired},
		{Key: "consumed_rows", Type: FieldInt, Presence: PresenceRequired},
		{Key: "consumed_new", Type: FieldInt, Presence: PresenceRequired},
		{Key: "shared_position_rows", Type: FieldInt, Presence: PresenceRequired},
		{Key: "last_stamp", Type: FieldString, Presence: PresenceRequired},
		{Key: "last_key_digest", Type: FieldString, Presence: PresenceRequired},
	},
}
