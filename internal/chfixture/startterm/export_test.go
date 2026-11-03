package startterm

// ImageMissingAtCreate lets the live test in package startterm_test (which imports chfixture, which imports this
// package) check the matcher against a real daemon's refusal.
var ImageMissingAtCreate = imageMissingAtCreate
