package directread

const (
	RelationshipsAxisCurrent = "current"
	RelationshipsAxisAsOf    = "as_of"
)

// RelationshipsAxisVocabulary is the set of effective.axis values read_relationships serves.
func RelationshipsAxisVocabulary() []string {
	return []string{RelationshipsAxisCurrent, RelationshipsAxisAsOf}
}
