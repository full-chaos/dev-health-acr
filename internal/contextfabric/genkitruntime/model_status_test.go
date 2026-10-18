package genkitruntime

import "fmt"

func newModelStatusError(status modelStatus, format string, args ...any) *modelStatusError {
	return &modelStatusError{Status: status, Message: fmt.Sprintf(format, args...)}
}
