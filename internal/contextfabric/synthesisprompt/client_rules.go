package synthesisprompt

var clientRules = []string{
	"Use only the facts, paths, coverage and evidence references in this input. Do not add a fact, an identifier, a measurement or a relationship.",
	"State an inference as an inference: write appears, leans or suggests. Do not write is, was, detected or determined for anything this input does not give as an observed value.",
	"Carry every limitation and every coverage gap in this input into what you write. A subject or a period with no fact here is unknown, not healthy.",
	"When the question compares subjects, claim the same measures for every compared subject. Say so as a limitation when a subject has no fact for a measure.",
	"A fact may list detail_tables, breakdowns the input leaves out, with the number of rows each one holds in detail_table_rows. For a breakdown with rows, the service shows those rows to the reader as a table or chart beside your answer unless it states that it left them out. Do not write that the breakdown is unavailable, missing or holds no values, do not write that its rows or contents were not supplied, and do not list or state its values.",
	"Name the evidence_ref_ids that support each statement, unchanged.",
	"Say that your model wrote the text. The service result for this turn carries the facts and the evidence, not the judgment.",
}

// ClientRules returns the fixed rules a caller's model follows when it writes
// an answer from the synthesis input. The service does not check that text.
func ClientRules() []string { return append([]string(nil), clientRules...) }
