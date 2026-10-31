package pginvestigation

// SetAfterThreadSummaryRead sets the hook GetThread runs between its thread
// row read and its turns read. Test builds only.
func SetAfterThreadSummaryRead(s *Store, hook func()) {
	s.afterThreadSummaryRead = hook
}

// SetRetentionStepHook sets the hook SweepRetention runs after each statement
// of a unit's transaction (CHAOS-11952). Test builds only.
func SetRetentionStepHook(s *Store, hook func(step string)) {
	s.retentionStepHook = hook
}
