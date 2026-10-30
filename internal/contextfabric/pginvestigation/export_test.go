package pginvestigation

// SetAfterThreadSummaryRead sets the hook GetThread runs between its thread
// row read and its turns read. Test builds only.
func SetAfterThreadSummaryRead(s *Store, hook func()) {
	s.afterThreadSummaryRead = hook
}
