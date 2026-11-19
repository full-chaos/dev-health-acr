package api

import (
	"errors"
	"regexp"

	"github.com/full-chaos/dev-health-acr/internal/logsanitize"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var dependencyIdentifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// dependencyErrorClassAttrs returns the log attributes for the
// storage.DependencyErrorClass a storage adapter attached to err, or nil when
// none is attached. A table or constraint name is logged only when it is a
// plain lowercase identifier.
func dependencyErrorClassAttrs(err error) []any {
	var class *storage.DependencyErrorClass
	if !errors.As(err, &class) || class == nil {
		return nil
	}
	attrs := []any{
		"db_error_class", logsanitize.SanitizeLogAttr(class.Class),
		"db_sqlstate", logsanitize.SanitizeLogAttr(class.SQLState),
	}
	if dependencyIdentifierPattern.MatchString(class.Constraint) {
		attrs = append(attrs, "db_constraint", class.Constraint)
	}
	if dependencyIdentifierPattern.MatchString(class.Table) {
		attrs = append(attrs, "db_table", class.Table)
	}
	return attrs
}
